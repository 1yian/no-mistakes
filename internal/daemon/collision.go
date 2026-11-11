package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

type daemonProcessInfo struct {
	PID  int
	Root string // raw --root value exactly as it appeared on the command line
}

var daemonListDaemonProcesses = listDaemonProcesses

var errDaemonCollisionHealthy = errors.New("daemon already running")

type daemonProcessLineSplitter func(line string) (pid int, command string, ok bool)

func parseDaemonProcessOutput(output string, split daemonProcessLineSplitter) []daemonProcessInfo {
	var infos []daemonProcessInfo
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		pid, command, ok := split(line)
		if !ok {
			continue
		}
		if !looksLikeDaemonRunCommand(command) {
			continue
		}
		root, hasRoot := extractRootFromCommand(command)
		if !hasRoot {
			continue
		}
		infos = append(infos, daemonProcessInfo{PID: pid, Root: root})
	}
	return infos
}

func looksLikeDaemonRunCommand(command string) bool {
	tokens := splitCommandLineTokens(command)
	hasDaemon, hasRun := false, false
	for _, token := range tokens {
		switch strings.ToLower(token) {
		case "daemon":
			hasDaemon = true
		case "run":
			hasRun = true
		}
	}
	return hasDaemon && hasRun
}

func extractRootFromCommand(command string) (string, bool) {
	tokens := splitCommandLineTokens(command)
	for i := 0; i < len(tokens); i++ {
		switch {
		case tokens[i] == "--root":
			if i+1 < len(tokens) {
				return tokens[i+1], true
			}
			return "", false
		case strings.HasPrefix(tokens[i], "--root="):
			return strings.TrimPrefix(tokens[i], "--root="), true
		}
	}
	return "", false
}

func splitCommandLineTokens(command string) []string {
	var tokens []string
	var current strings.Builder
	inQuotes := false
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for i := 0; i < len(command); i++ {
		ch := command[i]
		switch {
		case escaped:
			current.WriteByte(ch)
			escaped = false
		case ch == '\\':
			escaped = true
		case ch == '"':
			inQuotes = !inQuotes
		case (ch == ' ' || ch == '\t') && !inQuotes:
			flush()
		default:
			current.WriteByte(ch)
		}
	}
	flush()
	return tokens
}

func reconcileCollidingDaemons(p *paths.Paths) error {
	want := canonicalRoot(p.Root())
	if want == "" {
		return nil
	}
	procs, err := daemonListDaemonProcesses()
	if err != nil {
		slog.Warn("daemon collision check skipped: process enumeration failed", "error", err)
		return nil
	}

	self := os.Getpid()
	type candidate struct {
		pid  int
		root string
	}
	var matches []candidate
	for _, info := range procs {
		if info.PID == self {
			continue
		}
		if info.Root == "" || canonicalRoot(info.Root) != want {
			continue
		}
		matches = append(matches, candidate{pid: info.PID, root: info.Root})
	}
	if len(matches) == 0 {
		return nil
	}

	for _, m := range matches {
		if alive, _ := daemonHealthCheck(paths.WithRoot(m.root)); alive {
			slog.Info("daemon already running under alternate root path", "pid", m.pid, "root", m.root)
			return fmt.Errorf("%w (pid %d)", errDaemonCollisionHealthy, m.pid)
		}
	}

	for _, m := range matches {
		if killErr := daemonKillPID(m.pid); killErr != nil {
			slog.Warn("kill stale colliding daemon", "pid", m.pid, "error", killErr)
		}
	}
	// Stop the managed unit so Restart=always cannot revive a dead binary while
	// Start re-installs and re-starts it. Best-effort: install/start below will
	// reinstall the unit even if this returns an error.
	if _, err := stopManagedService(p); err != nil {
		slog.Warn("stop managed service before fresh daemon start", "error", err)
	}
	resetFailedManagedService(p)
	for _, m := range matches {
		cleanupDaemonArtifacts(paths.WithRoot(m.root))
	}
	cleanupDaemonArtifacts(p)
	return nil
}
