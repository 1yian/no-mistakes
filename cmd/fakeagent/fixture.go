package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func fixtureDir(agent string) string {
	root := os.Getenv("FAKEAGENT_FIXTURE")
	if root == "" {
		return ""
	}
	return filepath.Join(root, agent)
}

func readFixtureFile(dir, flavour, name string) ([]byte, error) {
	if dir == "" {
		return nil, nil
	}
	// Try <dir>/<flavour>/<name> first (opencode layout), then
	// <dir>/<flavour>.<ext> (claude/codex layout where flavour is
	// the file basename and name carries the extension).
	candidates := []string{
		filepath.Join(dir, flavour, name),
		filepath.Join(dir, flavour+filepath.Ext(name)),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			return data, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read fixture %s: %w", p, err)
		}
	}
	return nil, fmt.Errorf("missing fixture for %s/%s (%s)", filepath.Base(dir), flavour, strings.Join(candidates, ", "))
}
