package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorktreeDefaultsWithoutSetup(t *testing.T) {
	cfg, err := LoadGlobal(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worktree.Retention != DefaultWorktreeRetention || cfg.Worktree.MaxRuns != DefaultWorktreeMaxRuns {
		t.Fatalf("worktree defaults = %#v, want retention %s and max_runs %d", cfg.Worktree, DefaultWorktreeRetention, DefaultWorktreeMaxRuns)
	}
	merged := Merge(cfg, &RepoConfig{})
	if merged.Worktree != cfg.Worktree {
		t.Fatalf("merged worktree = %#v, want the global values %#v", merged.Worktree, cfg.Worktree)
	}
}

func TestWorktreeSettingsAreConfigurable(t *testing.T) {
	cfg, err := LoadGlobalFromBytes([]byte("worktree:\n  retention: 48h\n  max_runs: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worktree.Retention != 48*time.Hour || cfg.Worktree.MaxRuns != 5 {
		t.Fatalf("worktree config = %#v, want retention 48h and max_runs 5", cfg.Worktree)
	}
}

func TestWorktreeRetentionUnlimitedDisablesAging(t *testing.T) {
	cfg, err := LoadGlobalFromBytes([]byte("worktree:\n  retention: unlimited\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worktree.Retention != 0 {
		t.Fatalf("worktree.retention = %s, want 0 (disabled) for \"unlimited\"", cfg.Worktree.Retention)
	}
}

func TestWorktreeMaxRunsZeroKeepsEveryLeftover(t *testing.T) {
	cfg, err := LoadGlobalFromBytes([]byte("worktree:\n  max_runs: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worktree.MaxRuns != 0 {
		t.Fatalf("worktree.max_runs = %d, want 0 (keep every leftover)", cfg.Worktree.MaxRuns)
	}
}

func TestLoadGlobalRejectsNegativeWorktreeMaxRuns(t *testing.T) {
	_, err := LoadGlobalFromBytes([]byte("worktree:\n  max_runs: -1\n"))
	if err == nil || !strings.Contains(err.Error(), "worktree.max_runs") {
		t.Fatalf("error = %v, want a rejected negative worktree.max_runs", err)
	}
}

func TestLoadGlobalRejectsUnparseableWorktreeRetention(t *testing.T) {
	_, err := LoadGlobalFromBytes([]byte("worktree:\n  retention: not-a-duration\n"))
	if err == nil || !strings.Contains(err.Error(), "worktree.retention") {
		t.Fatalf("error = %v, want a rejected unparseable worktree.retention", err)
	}
}

func TestRepoConfigCannotChangeWorktreeRetention(t *testing.T) {
	global, err := LoadGlobalFromBytes([]byte("worktree:\n  retention: 12h\n  max_runs: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := LoadRepoFromBytes([]byte("worktree:\n  retention: unlimited\n  max_runs: 9999\n"))
	if err != nil {
		t.Fatalf("repo config with a worktree block must load, ignoring the key: %v", err)
	}
	merged := Merge(global, repo)
	if merged.Worktree.Retention != 12*time.Hour || merged.Worktree.MaxRuns != 3 {
		t.Fatalf("merged worktree = %#v, want the operator's global values untouched by the repository", merged.Worktree)
	}
}

func TestDefaultConfigYAMLDocumentsWorktreeRetention(t *testing.T) {
	if !strings.Contains(defaultConfigYAML, "# worktree:") {
		t.Fatal("shipped default config template does not document the worktree key")
	}
	if !strings.Contains(defaultConfigYAML, "retention: 24h") || !strings.Contains(defaultConfigYAML, "max_runs: 20") {
		t.Fatal("shipped default config template's worktree example does not match the Go defaults")
	}
	if worktreeDefaults().Retention != 24*time.Hour || worktreeDefaults().MaxRuns != 20 {
		t.Fatalf("worktreeDefaults() = %#v, drifted from the documented example", worktreeDefaults())
	}
	if _, err := LoadGlobalFromBytes([]byte(defaultConfigYAML)); err != nil {
		t.Fatalf("shipped default config does not load: %v", err)
	}
}
