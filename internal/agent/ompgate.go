package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const ompACPTarget = "omp"

const ompGateOverlayYAML = `# Written by no-mistakes for a gate run under disable_project_settings: true.
# Neutralizes the target repository's project instructions by disabling every
# omp discovery provider that contributes a context file (AGENTS.md/CLAUDE.md/
# GEMINI.md, .omp/AGENTS.md, and the other supported conventions) and by turning
# off mnemopi memory so repo content the gate reads cannot be retained and later
# recalled around that suppression. A CLI --config overlay is the highest
# settings layer, so the target repo's own .omp/config.yml cannot override it.
disabledProviders:
  - native
  - claude
  - codex
  - gemini
  - opencode
  - github
  - agents
  - agents-md
  - claude-md
memory:
  backend: off
`

var ompGateSuppressionFlags = []string{"--no-rules", "--no-skills", "--no-extensions"}

func neutralizesOMPGate(target, rawCommand string, disableProjectSettings bool) bool {
	return disableProjectSettings &&
		target == ompACPTarget &&
		strings.TrimSpace(rawCommand) == ""
}

func ompNeutralizedACPCommand(overlayPath string) string {
	parts := make([]string, 0, 4+len(ompGateSuppressionFlags))
	parts = append(parts, "omp", "acp", "--config", overlayPath)
	parts = append(parts, ompGateSuppressionFlags...)
	return strings.Join(parts, " ")
}

func writeOMPGateOverlay() (string, error) {
	f, err := os.CreateTemp("", "nm-omp-gate-*.yml")
	if err != nil {
		return "", fmt.Errorf("create omp gate overlay: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(ompGateOverlayYAML); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write omp gate overlay: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close omp gate overlay: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("resolve omp gate overlay path: %w", err)
	}
	if !overlayPathShellSafe(abs, runtime.GOOS) {
		_ = os.Remove(abs)
		return "", fmt.Errorf("omp gate overlay path is not shell-safe: %q", abs)
	}
	return abs, nil
}

func overlayPathShellSafe(path, goos string) bool {
	unsafe := " \t\r\n\"'"
	if goos != "windows" {
		unsafe += "\\"
	}
	return !strings.ContainsAny(path, unsafe)
}
