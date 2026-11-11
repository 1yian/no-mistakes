package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

const (
	launchdServiceLabelBase = "com.kunchenguid.no-mistakes.daemon"
	systemdServiceNameBase  = "no-mistakes-daemon"
	windowsTaskNameBase     = "no-mistakes-daemon"
)

const (
	legacyLaunchdServiceLabel = "com.kunchenguid.no-mistakes.daemon"
	legacySystemdServiceName  = "no-mistakes-daemon.service"
	legacyWindowsTaskName     = "no-mistakes-daemon"
)

var runtimeGOOS = runtime.GOOS
var serviceUserHomeDir = os.UserHomeDir
var serviceCurrentUser = user.Current
var serviceExecutablePath = os.Executable
var serviceCommandRunner = runServiceCommand
var serviceManagerBypassed = defaultServiceManagerBypassed
var prepareManagedDaemonLaunch = managedDaemonLaunch
var inspectManagedDaemonService = managedDaemonServiceState

type managedServiceState int

type managedServiceLaunch struct {
	windowsRunGeneration string
}

const (
	managedServiceUnknown managedServiceState = iota
	managedServiceRunning
	managedServiceExited
)

func managedDaemonLaunch(p *paths.Paths) (managedServiceLaunch, error) {
	if serviceManagerBypassed() || runtimeGOOS != "windows" {
		return managedServiceLaunch{}, nil
	}
	observation, err := inspectWindowsManagedDaemon(p)
	if err != nil {
		return managedServiceLaunch{}, err
	}
	return managedServiceLaunch{windowsRunGeneration: observation.runGeneration}, nil
}

func managedDaemonServiceState(p *paths.Paths, launch managedServiceLaunch) (managedServiceState, error) {
	if serviceManagerBypassed() {
		return managedServiceUnknown, nil
	}
	switch runtimeGOOS {
	case "darwin":
		domain, err := launchdDomainTarget()
		if err != nil {
			return managedServiceUnknown, err
		}
		output, err := serviceCommandRunner("launchctl", "print", domain+"/"+launchdServiceLabel(p))
		if err != nil {
			return managedServiceExited, nil
		}
		text := strings.ToLower(string(output))
		if strings.Contains(text, "state = running") || strings.Contains(text, "pid =") {
			return managedServiceRunning, nil
		}
		if strings.Contains(text, "last exit code =") || strings.Contains(text, "state = exited") {
			return managedServiceExited, nil
		}
	case "linux":
		output, err := serviceCommandRunner(
			"systemctl", "--user", "show", systemdServiceName(p),
			"--property=ActiveState", "--property=SubState", "--property=MainPID",
			"--property=ExecMainCode", "--property=ExecMainStatus", "--property=Result",
		)
		if err != nil {
			return managedServiceUnknown, err
		}
		values := make(map[string]string)
		for _, line := range strings.Split(string(output), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[key] = value
			}
		}
		if values["ActiveState"] == "active" && values["MainPID"] != "" && values["MainPID"] != "0" {
			return managedServiceRunning, nil
		}
		if values["ActiveState"] == "failed" || values["ActiveState"] == "inactive" ||
			values["Result"] == "exit-code" || values["Result"] == "signal" {
			return managedServiceExited, nil
		}
	case "windows":
		return windowsManagedDaemonState(p, launch)
	}
	return managedServiceUnknown, nil
}

var proxyEnvKeys = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
}

func serviceProxyEnv() [][2]string {
	// Only Windows treats environment-variable names case-insensitively; macOS
	// and Linux keep HTTP_PROXY and http_proxy as distinct variables.
	caseInsensitiveEnv := runtimeGOOS == "windows"
	var out [][2]string
	seen := make(map[string]bool, len(proxyEnvKeys))
	for _, key := range proxyEnvKeys {
		if caseInsensitiveEnv && seen[strings.ToUpper(key)] {
			continue
		}
		value, ok := os.LookupEnv(key)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		out = append(out, [2]string{key, value})
		if caseInsensitiveEnv {
			seen[strings.ToUpper(key)] = true
		}
	}
	return out
}

func serviceEnv(inherited [][2]string) [][2]string {
	out := serviceProxyEnv()
	if len(out) == 0 {
		for _, kv := range inherited {
			if isProxyEnvKey(kv[0]) {
				out = append(out, kv)
			}
		}
	}
	const telemetryKey = "NO_MISTAKES_TELEMETRY"
	if value := os.Getenv(telemetryKey); strings.TrimSpace(value) != "" {
		return append(out, [2]string{telemetryKey, value})
	}
	for _, kv := range inherited {
		if kv[0] == telemetryKey {
			out = append(out, kv)
		}
	}
	return out
}

func writeServiceFile(path string, parseExistingProxyEnv func([]byte) [][2]string, render func(proxyEnv [][2]string) string) error {
	var inherited [][2]string
	if parseExistingProxyEnv != nil {
		if existing, err := os.ReadFile(path); err == nil {
			inherited = parseExistingProxyEnv(existing)
		}
	}
	proxyEnv := serviceEnv(inherited)
	content := []byte(render(proxyEnv))
	if len(proxyEnv) == 0 {
		mode := os.FileMode(0o644)
		if err := os.WriteFile(path, content, mode); err != nil {
			return err
		}
		return os.Chmod(path, mode)
	}
	return writeFileAtomic(path, content, 0o600)
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	renamed = true
	return nil
}

func defaultServiceManagerBypassed() bool {
	if os.Getenv("NM_TEST_START_DAEMON") == "1" {
		return true
	}
	return testing.Testing()
}

func serviceInstanceSuffix(p *paths.Paths) string {
	root := ""
	if p != nil {
		root = p.Root()
	}
	sum := sha256.Sum256([]byte(canonicalRoot(root)))
	return hex.EncodeToString(sum[:4])
}

func canonicalRoot(root string) string {
	if root == "" {
		return ""
	}
	if !filepath.IsAbs(root) {
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	root = filepath.Clean(root)
	if runtimeGOOS == "windows" {
		root = strings.ToLower(root)
	}
	return root
}

func launchdServiceLabel(p *paths.Paths) string {
	return launchdServiceLabelBase + "." + serviceInstanceSuffix(p)
}

func systemdServiceName(p *paths.Paths) string {
	return systemdServiceNameBase + "-" + serviceInstanceSuffix(p) + ".service"
}

func windowsTaskName(p *paths.Paths) string {
	return windowsTaskNameBase + "-" + serviceInstanceSuffix(p)
}

func installManagedService(p *paths.Paths) (bool, error) {
	if serviceManagerBypassed() {
		return false, nil
	}
	exe, err := serviceExecutablePath()
	if err != nil {
		return false, fmt.Errorf("resolve executable: %w", err)
	}
	return installManagedServiceWithExecutable(p, exe)
}

func installManagedServiceWithExecutable(p *paths.Paths, exe string) (bool, error) {
	switch runtimeGOOS {
	case "darwin":
		return true, installLaunchAgent(p, exe)
	case "linux":
		return true, installSystemdUserService(p, exe)
	case "windows":
		return true, installWindowsTask(p, exe)
	default:
		return false, nil
	}
}

func startManagedService(p *paths.Paths) (bool, error) {
	if serviceManagerBypassed() {
		return false, nil
	}
	switch runtimeGOOS {
	case "darwin":
		return true, startLaunchAgent(p)
	case "linux":
		return true, startSystemdUserService(p)
	case "windows":
		return true, startWindowsTask(p)
	default:
		return false, nil
	}
}

func restartManagedService(p *paths.Paths) (bool, error) {
	if serviceManagerBypassed() {
		return false, nil
	}
	switch runtimeGOOS {
	case "darwin":
		return true, startLaunchAgent(p)
	case "linux":
		return true, restartSystemdUserService(p)
	default:
		return startManagedService(p)
	}
}

func reloadManagedServiceDefinition(p *paths.Paths) error {
	if serviceManagerBypassed() {
		return nil
	}
	switch runtimeGOOS {
	case "linux":
		_, err := serviceCommandRunner("systemctl", "--user", "daemon-reload")
		if err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w", err)
		}
	}
	return nil
}

func stopManagedService(p *paths.Paths) (bool, error) {
	if serviceManagerBypassed() || !managedServiceInstalled(p) {
		return false, nil
	}
	switch runtimeGOOS {
	case "darwin":
		return true, stopLaunchAgent(p)
	case "linux":
		return true, stopSystemdUserService(p)
	case "windows":
		return true, stopWindowsTask(p)
	default:
		return false, nil
	}
}

func resetFailedManagedService(p *paths.Paths) {
	if serviceManagerBypassed() {
		return
	}
	switch runtimeGOOS {
	case "linux":
		_, _ = serviceCommandRunner("systemctl", "--user", "reset-failed", systemdServiceName(p))
	}
}

func managedServiceInstalled(p *paths.Paths) bool {
	if serviceManagerBypassed() {
		return false
	}
	switch runtimeGOOS {
	case "darwin":
		_, err := os.Stat(launchAgentPath(p))
		return err == nil
	case "linux":
		_, err := os.Stat(systemdUserServicePath(p))
		return err == nil
	case "windows":
		if p == nil {
			return false
		}
		_, err := serviceCommandRunner("schtasks", "/Query", "/TN", windowsTaskName(p))
		return err == nil
	default:
		return false
	}
}
