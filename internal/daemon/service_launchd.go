package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

var (
	launchctlBootstrapRetryTimeout  = 10 * time.Second
	launchctlBootstrapRetryInterval = 200 * time.Millisecond
)

func installLaunchAgent(p *paths.Paths, exe string) error {
	path := launchAgentPath(p)
	home, err := serviceUserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create launch agents directory: %w", err)
	}
	// writeServiceFile resolves the forwarded environment once and feeds it to the
	// renderer, so the plist content and its permission mode stay in sync
	// (see serviceEnv / writeServiceFile).
	render := func(proxyEnv [][2]string) string {
		return renderLaunchAgentWithProxyEnv(exe, p, home, proxyEnv)
	}
	if err := writeServiceFile(path, launchAgentProxyEnv, render); err != nil {
		return fmt.Errorf("write launch agent: %w", err)
	}
	cleanupLegacyLaunchAgent(p)
	return nil
}

func cleanupLegacyLaunchAgent(p *paths.Paths) {
	path := legacyLaunchAgentPath()
	data, err := os.ReadFile(path)
	if err != nil || !serviceDefinitionMatchesRoot(data, p) {
		return
	}
	if domain, err := launchdDomainTarget(); err == nil {
		_, _ = serviceCommandRunner("launchctl", "bootout", domain+"/"+legacyLaunchdServiceLabel)
	}
	_ = os.Remove(path)
}

func startLaunchAgent(p *paths.Paths) error {
	domain, err := launchdDomainTarget()
	if err != nil {
		return err
	}
	serviceTarget := domain + "/" + launchdServiceLabel(p)
	path := launchAgentPath(p)
	_, _ = serviceCommandRunner("launchctl", "bootout", serviceTarget)
	bootstrapErr := launchctlBootstrapWithRetry(domain, path)
	_, kickstartErr := serviceCommandRunner("launchctl", "kickstart", "-k", serviceTarget)
	if kickstartErr != nil {
		if bootstrapErr != nil {
			return fmt.Errorf("launchctl bootstrap: %v; kickstart: %w", bootstrapErr, kickstartErr)
		}
		return fmt.Errorf("launchctl kickstart: %w", kickstartErr)
	}
	return nil
}

func launchctlBootstrapWithRetry(domain, path string) error {
	deadline := time.Now().Add(launchctlBootstrapRetryTimeout)
	var lastErr error
	for {
		_, err := serviceCommandRunner("launchctl", "bootstrap", domain, path)
		if err == nil {
			return nil
		}
		if !launchctlBootstrapBusy(err) {
			return err
		}
		lastErr = err
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(launchctlBootstrapRetryInterval)
	}
}

func launchctlBootstrapBusy(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "operation already in progress") ||
		strings.Contains(text, "exit status 37")
}

func stopLaunchAgent(p *paths.Paths) error {
	domain, err := launchdDomainTarget()
	if err != nil {
		return err
	}
	output, err := serviceCommandRunner("launchctl", "bootout", domain+"/"+launchdServiceLabel(p))
	if err != nil {
		if launchctlBootoutServiceNotLoaded(err, output) {
			return nil
		}
		return fmt.Errorf("launchctl bootout: %w", err)
	}
	return nil
}

func removeLaunchAgent(p *paths.Paths) error {
	err := os.Remove(launchAgentPath(p))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func launchctlBootoutServiceNotLoaded(err error, output []byte) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(string(output) + " " + err.Error())
	return strings.Contains(combined, "no such process")
}

func launchAgentPath(p *paths.Paths) string {
	home, err := serviceUserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdServiceLabel(p)+".plist")
}

func legacyLaunchAgentPath() string {
	home, _ := serviceUserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", legacyLaunchdServiceLabel+".plist")
}

func launchdDomainTarget() (string, error) {
	u, err := serviceCurrentUser()
	if err != nil {
		return "", fmt.Errorf("resolve current user: %w", err)
	}
	if u == nil || u.Uid == "" {
		return "", fmt.Errorf("resolve current user: empty uid")
	}
	return "gui/" + u.Uid, nil
}

func renderLaunchAgent(exe string, p *paths.Paths, home string) string {
	return renderLaunchAgentWithProxyEnv(exe, p, home, serviceEnv(nil))
}

func renderLaunchAgentWithProxyEnv(exe string, p *paths.Paths, home string, proxyEnv [][2]string) string {
	values := []string{exe, "daemon", "run", "--root", p.Root()}
	var args strings.Builder
	for _, value := range values {
		args.WriteString("    <string>")
		args.WriteString(xmlEscaped(value))
		args.WriteString("</string>\n")
	}
	var envDict strings.Builder
	envDict.WriteString("  <dict>\n")
	envDict.WriteString("    <key>HOME</key>\n    <string>")
	envDict.WriteString(xmlEscaped(home))
	envDict.WriteString("</string>\n")
	envDict.WriteString("    <key>PATH</key>\n    <string>")
	envDict.WriteString(xmlEscaped(managedServicePath(home)))
	envDict.WriteString("</string>\n")
	for _, kv := range proxyEnv {
		envDict.WriteString("    <key>")
		envDict.WriteString(xmlEscaped(kv[0]))
		envDict.WriteString("</key>\n    <string>")
		envDict.WriteString(xmlEscaped(kv[1]))
		envDict.WriteString("</string>\n")
	}
	envDict.WriteString("  </dict>")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>WorkingDirectory</key>
  <string>%s</string>
  <key>EnvironmentVariables</key>
%s
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
`, xmlEscaped(launchdServiceLabel(p)), args.String(), xmlEscaped(p.Root()), envDict.String(), xmlEscaped(p.DaemonBootstrapLog()), xmlEscaped(p.DaemonBootstrapLog()))
}

func managedServicePath(home string) string {
	return strings.Join(shellenv.WellKnownBinDirsForHome(home), string(os.PathListSeparator))
}
