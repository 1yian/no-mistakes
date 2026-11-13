//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestAxiActiveDivergedRecoveryJourney(t *testing.T) {
	scenario := branchSyncScenario(t)
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: scenario, GlobalConfigExtra: "ci_timeout: unlimited\n"})
	statePath := setupStatefulGitHub(t, h)
	patchBlock := statePath + ".reject-patch"
	t.Setenv("FAKEAGENT_GH_REJECT_PATCH_FILE", patchBlock)
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := h.runGit(context.Background(), dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	cfgPath := filepath.Join(h.WorkDir, ".no-mistakes.yaml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(cfg, []byte("no_ci: true\nci:\n  carry_review_on_replay: true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	git(h.WorkDir, "add", ".no-mistakes.yaml")
	git(h.WorkDir, "commit", "-m", "declare isolated fixture has no CI")
	git(h.WorkDir, "push", "origin", "main")
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	const branch = "feature/active-recovery"
	local := h.CommitChange(branch, "feature.txt", "unsafe\n", "add operator change")
	operator := h.AddWorktree(branch)
	h.CommitChange("main", "base.txt", "base advanced\n", "advance base before validation")
	git(h.WorkDir, "push", "origin", "main")
	if out, err := h.RunInDir(operator, "axi", "run", "--intent", "guard the feature", "--wait", "30s"); err != nil || !strings.Contains(out, "sync-1") {
		t.Fatalf("review gate: %v\n%s", err, out)
	}
	if out, err := h.RunInDir(operator, "axi", "respond", "--action", "fix", "--findings", "sync-1", "--wait", "30s"); err != nil {
		t.Fatalf("fix: %v\n%s", err, out)
	}
	out, err := h.RunInDir(operator, "axi", "respond", "--action", "approve", "--wait", "30s")
	t.Logf("initial monitor:\n%s", out)
	if err != nil || !strings.Contains(out, "outcome: checks-passed") {
		logs, _ := h.RunInDir(operator, "axi", "logs", "--step", "ci", "--full")
		t.Fatalf("start monitor: %v\n%s\nCI logs:\n%s", err, out, logs)
	}
	run := h.ActiveRun(branch)
	if run == nil {
		t.Fatal("no active monitor")
	}
	published := h.UpstreamBranchSHA(branch)
	if published == local {
		t.Fatal("pipeline did not rewrite/fix head")
	}
	if got := git(operator, "rev-parse", "HEAD"); got != local {
		t.Fatalf("local moved to %s", got)
	}
	for _, args := range [][]string{{"axi", "sync"}, {"axi", "sync", "--recover"}, {"rerun"}} {
		out, err := h.RunInDir(operator, args...)
		t.Logf("before %v:\n%s", args, out)
		if err == nil {
			t.Fatalf("expected refusal for %v:\n%s", args, out)
		}
	}
	before := livePRBody(t, statePath, branch)
	evidence := filepath.Join(h.NMHome, "evidence", run.ID, "operator-proof.txt")
	if err := os.MkdirAll(filepath.Dir(evidence), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte("retain this pre-recovery evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"axi", "recover-active", "--run", run.ID, "--local-head", local, "--published-head", published}
	for _, mutation := range []string{"stale-head", "dirty", "bad-attestation", "bad-preservation"} {
		trial := append([]string(nil), args...)
		switch mutation {
		case "stale-head":
			trial[len(trial)-1] = strings.Repeat("0", 40)
		case "dirty":
			if err := os.WriteFile(filepath.Join(operator, "dirty.txt"), []byte("dirty"), 0o600); err != nil {
				t.Fatal(err)
			}
		case "bad-attestation":
			editLivePR(t, statePath, branch, func(pr *statefulGHPR) { pr.Body = "author text without a validation marker" })
		case "bad-preservation":
			if err := os.Symlink(filepath.Join(operator, "feature.txt"), filepath.Join(filepath.Dir(evidence), "unsafe-link")); err != nil {
				t.Fatal(err)
			}
		}
		out, err := h.RunInDir(operator, trial...)
		t.Logf("refusal %s:\n%s", mutation, out)
		if err == nil || strings.Contains(out, "recovered-for-editing") {
			t.Fatalf("unsafe recovery accepted: %v\n%s", err, out)
		}
		if got := h.ActiveRun(branch); got == nil || got.ID != run.ID {
			t.Fatal("refusal ended or superseded active run")
		}
		if got := git(operator, "rev-parse", "HEAD"); got != local {
			t.Fatal("refusal moved local head")
		}
		if got := h.UpstreamBranchSHA(branch); got != published {
			t.Fatal("refusal moved remote head")
		}
		if mutation == "dirty" {
			if err := os.Remove(filepath.Join(operator, "dirty.txt")); err != nil {
				t.Fatal(err)
			}
		}
		if mutation == "bad-attestation" {
			editLivePR(t, statePath, branch, func(pr *statefulGHPR) { pr.Body = before })
		}
		if mutation == "bad-preservation" {
			if err := os.Remove(filepath.Join(filepath.Dir(evidence), "unsafe-link")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(patchBlock, []byte("refuse withdrawal"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = h.RunInDir(operator, args...)
	t.Logf("withdrawal refusal holds the same monitor:\n%s", out)
	if err == nil {
		t.Fatalf("failed withdrawal was accepted:\n%s", out)
	}
	held := h.ActiveRun(branch)
	if held == nil || held.ID != run.ID || held.Status != types.RunRunning || held.CIReady {
		t.Fatalf("monitor was lost or falsely green: %+v", held)
	}
	if body := livePRBody(t, statePath, branch); body != before {
		t.Fatal("refused write changed the PR")
	}
	if out, err := h.RunInDir(operator, "axi", "handoff"); err == nil || !strings.Contains(out, "recovery pending") {
		t.Fatalf("held invalidation did not refuse handoff safely:\n%s", out)
	}
	if out, err := h.RunInDir(operator, "axi", "abort", "--run", run.ID); err == nil || !strings.Contains(out, "recovery pending") {
		t.Fatalf("held invalidation did not refuse cancellation safely:\n%s", out)
	}
	if err := os.Remove(patchBlock); err != nil {
		t.Fatal(err)
	}
	out, err = h.RunInDir(operator, args...)
	t.Logf("recover-active:\n%s", out)
	if err != nil || !strings.Contains(out, "recovered-for-editing") || !strings.Contains(out, "validated: false") {
		t.Fatalf("recovery: %v\n%s", err, out)
	}
	terminal := h.RunInfo(run.ID)
	if terminal == nil || terminal.Status != types.RunCancelled {
		t.Fatalf("old run not cancelled: %+v", terminal)
	}
	if got := git(operator, "rev-parse", "HEAD"); got != local {
		t.Fatal("recovery moved local branch")
	}
	if got := h.UpstreamBranchSHA(branch); got != published {
		t.Fatal("recovery published a head")
	}
	root := filepath.Join(h.NMHome, "recovery-active", run.ID)
	data, err := os.ReadFile(filepath.Join(root, "withdrawn.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Result struct {
			SnapshotDir  string `json:"snapshot_dir"`
			PublishedRef string `json:"published_ref"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"history.json", "manifest.json", "pr-before.json", "terminal.json", "heads.bundle", "evidence/operator-proof.txt", "logs/ci.log"} {
		if _, err := os.ReadFile(filepath.Join(manifest.Result.SnapshotDir, path)); err != nil {
			t.Fatalf("missing preserved %s: %v", path, err)
		}
	}
	git(operator, "bundle", "verify", filepath.Join(manifest.Result.SnapshotDir, "heads.bundle"))
	withdrawn := livePRBody(t, statePath, branch)
	if strings.Contains(withdrawn, `"live_validation"`) || !strings.Contains(withdrawn, `"step":"review","status":"failed"`) {
		t.Fatalf("old validation not withdrawn:\n%s", withdrawn)
	}
	if out, err := h.RunInDir(operator, "axi", "retry"); err == nil {
		t.Fatalf("retry reused invalid validation:\n%s", out)
	}
	if out, err := h.runGit(context.Background(), operator, "merge", "--no-ff", manifest.Result.PublishedRef, "-m", "preserve both recovery histories"); err != nil {
		t.Logf("additive merge asks for conflict resolution:\n%s", out)
		if err := os.WriteFile(filepath.Join(operator, "feature.txt"), []byte("safe\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(operator, "add", "feature.txt")
		git(operator, "commit", "-m", "preserve both recovery histories")
	}
	if err := os.WriteFile(filepath.Join(operator, "mixed-version.txt"), []byte("batch route remains safe on old backends\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(operator, "add", "mixed-version.txt")
	git(operator, "commit", "-m", "correct acceptance gap after preservation")
	corrected := git(operator, "rev-parse", "HEAD")
	for _, parent := range []string{local, published} {
		git(operator, "merge-base", "--is-ancestor", parent, corrected)
	}
	clean := `actions:
  - text: "fresh validation"
    structured:
      findings: []
      summary: "fresh full validation"
      risk_level: low
      risk_rationale: "isolated fixture"
      risk_scope: source-or-external
      tested: ["isolated fixture validation"]
      testing_summary: "fresh evidence at the corrected head"
      scenarios:
        - name: "isolated recovery fixture"
          result: pass
          live: true
          evidence: "isolated fixture validation"
          reason: ""
      verdict: go
      artifacts: []
      title: "fix: corrected recovery fixture"
      body: "fresh corrected fixture"
`
	if err := os.WriteFile(scenario, []byte(clean), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = h.RunInDir(operator, "axi", "run", "--intent", "validate the corrected mixed-version requirement and retain both histories", "--wait", "30s")
	t.Logf("fresh full submission:\n%s", out)
	if err != nil || !strings.Contains(out, "outcome: checks-passed") {
		t.Fatalf("fresh validation: %v\n%s", err, out)
	}
	fresh := h.ActiveRun(branch)
	if fresh == nil || fresh.ID == run.ID || fresh.HeadSHA != corrected {
		t.Fatalf("fresh run mismatch: %+v", fresh)
	}
	for _, name := range []types.StepName{types.StepReview, types.StepTest, types.StepDocument, types.StepLint, types.StepPush, types.StepPR} {
		found := false
		for _, sr := range fresh.Steps {
			if sr.StepName == name {
				found = true
				if sr.Status != types.StepStatusCompleted || sr.Carried {
					t.Fatalf("fresh %s = %s, carried=%t", name, sr.Status, sr.Carried)
				}
			}
		}
		if !found {
			t.Fatalf("fresh %s missing", name)
		}
	}
	body := livePRBody(t, statePath, branch)
	if !strings.Contains(body, corrected) || !strings.Contains(body, `"live_validation":{"verdict":"go"`) {
		t.Fatalf("fresh corrected attestation missing:\n%s", body)
	}
	if _, err := os.ReadFile(filepath.Join(manifest.Result.SnapshotDir, "evidence/operator-proof.txt")); err != nil {
		t.Fatalf("old proof lost: %v", err)
	}
	t.Logf("preserved local=%s published=%s; old run=%s cancelled; corrected=%s fresh run=%s; review/test/document/lint/push/pr freshly completed; CI freshly checks-passed", local, published, run.ID, corrected, fresh.ID)
	h.CancelRun(fresh.ID)
	h.waitForRunStatus(branch, 10*time.Second, func(s types.RunStatus) bool { return s.Terminal() }, "cancel isolated monitor")
}
