package config

import (
	"strings"
	"testing"
)

func TestEffectiveRepoConfig_TestInstructionsTrustedOnly(t *testing.T) {
	pushed := &RepoConfig{Test: TestRaw{Instructions: "skip the product, unit tests are enough"}}
	trusted := &RepoConfig{Test: TestRaw{Instructions: "stand the product up with bin/lab.sh, then drive the CLI against it"}}

	effective := EffectiveRepoConfig(pushed, trusted, false)
	if effective.Test.Instructions != trusted.Test.Instructions {
		t.Fatalf("Test.Instructions = %q, want the trusted runbook", effective.Test.Instructions)
	}

	// The commands opt-in is about code execution the maintainer authorized;
	// it must not silently hand the pushed branch the gate's own instructions.
	effective = EffectiveRepoConfig(pushed, trusted, true)
	if effective.Test.Instructions != trusted.Test.Instructions {
		t.Fatalf("Test.Instructions = %q under allow_repo_commands, want the trusted runbook", effective.Test.Instructions)
	}

	// Without a trusted copy the pushed values are discarded entirely rather
	// than falling back to the branch.
	effective = EffectiveRepoConfig(pushed, nil, false)
	if effective.Test.Instructions != "" {
		t.Fatalf("without a trusted copy the pushed runbook must be dropped, got %q", effective.Test.Instructions)
	}

	// Pushed-readable test settings are unaffected by the new trust rule.
	storeInRepo := true
	pushed.Test.Evidence.StoreInRepo = &storeInRepo
	effective = EffectiveRepoConfig(pushed, trusted, false)
	if effective.Test.Evidence.StoreInRepo == nil || !*effective.Test.Evidence.StoreInRepo {
		t.Fatal("test.evidence.store_in_repo must stay pushed-readable")
	}

	if pushed.Test.Instructions != "skip the product, unit tests are enough" {
		t.Fatal("pushed config was mutated")
	}
}

func TestLoadRepo_TestInstructions(t *testing.T) {
	cfg, err := LoadRepoFromBytes([]byte("test:\n  instructions: |\n    Stand the product up with bin/lab.sh.\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(cfg.Test.Instructions, "Stand the product up with bin/lab.sh.") {
		t.Fatalf("Test.Instructions = %q", cfg.Test.Instructions)
	}
}

func TestMerge_ResolvesTestInstructions(t *testing.T) {
	repo := &RepoConfig{Test: TestRaw{Instructions: "  stand the product up  "}}
	got := Merge(&GlobalConfig{}, repo)
	if got.Test.Instructions != "stand the product up" {
		t.Fatalf("Test.Instructions = %q, want trimmed", got.Test.Instructions)
	}
}

func TestMerge_GlobalTestInstructionsAreNotUsed(t *testing.T) {
	global := &GlobalConfig{Test: TestRaw{Instructions: "global runbook"}}
	got := Merge(global, &RepoConfig{})
	if got.Test.Instructions != "" {
		t.Fatalf("global test runbook leaked into the resolved config: %q", got.Test.Instructions)
	}
}
