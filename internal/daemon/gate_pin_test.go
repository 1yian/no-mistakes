package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	oneGateYAML = `auto_fix:
  lint: 0
  test: 0
  review: 0
gates:
  - name: arch-fitness
    after: review
    command: "true"
`
	twoGatesYAML = `auto_fix:
  lint: 0
  test: 0
  review: 0
gates:
  - name: arch-fitness
    after: review
    command: "true"
  - name: mutation-budget
    after: test
    command: "true"
`
)

func gatePinFixture(t *testing.T, defaultBranchYAML string, pinnedGates []config.Gate) (*RunManager, *db.Run, []types.StepName) {
	t.Helper()
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	mockClaude := writeMockClaude(t, t.TempDir())
	if err := os.WriteFile(p.ConfigFile(), []byte("agent: claude\nagent_path_override:\n  claude: "+mockClaude+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	repo, _ := setupTestGitRepo(t, p, d, "repo1")
	headSHA := commitDefaultBranchConfig(t, repo.WorkingPath, defaultBranchYAML)

	run, err := d.InsertRun(repo.ID, "feature", headSHA, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	bindFixtureCommitIdentity(t, d, run.ID)
	if err := d.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, p.RepoDir(repo.ID), "worktree", "add", "--detach", p.WorktreeDir(repo.ID, run.ID), headSHA)

	payload, err := config.MarshalGates(pinnedGates)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunGates(run.ID, payload); err != nil {
		t.Fatal(err)
	}

	recorded := parkRunAtReviewGate(t, d, run.ID, steps.WithCustomGates(steps.AllSteps(), pinnedGates))
	stored, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return NewRunManager(d, p, nil), stored, recorded
}

func commitDefaultBranchConfig(t *testing.T, workDir, yaml string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workDir, ".no-mistakes.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, workDir, "add", ".")
	gitCmd(t, workDir, "commit", "-m", "configure gates")
	gitCmd(t, workDir, "push", "gate", "HEAD:refs/heads/main")
	return gitOutput(t, workDir, "rev-parse", "HEAD")
}

func parkRunAtReviewGate(t *testing.T, d *db.DB, runID string, sequence []pipeline.Step) []types.StepName {
	t.Helper()
	findings := `{"findings":[{"id":"review-1","severity":"warning","description":"needs a decision","action":"ask-user"}],"summary":"needs a decision"}`
	names := make([]types.StepName, 0, len(sequence))
	parked := false
	for _, step := range sequence {
		name := step.Name()
		names = append(names, name)
		result, err := d.InsertStepResult(runID, name)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case name == types.StepReview:
			if err := d.StartStep(result.ID); err != nil {
				t.Fatal(err)
			}
			if err := d.ParkStepForApproval(runID, result.ID, types.StepStatusAwaitingApproval, 0, 5, &findings); err != nil {
				t.Fatal(err)
			}
			if _, err := d.InsertStepRound(result.ID, 1, "initial", &findings, nil, 5); err != nil {
				t.Fatal(err)
			}
			parked = true
		case !parked:
			if err := d.CompleteStepWithStatus(result.ID, types.StepStatusCompleted, 0, 1, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	return names
}

func planStepNames(plan *recoveredRunPlan) []types.StepName {
	names := make([]types.StepName, 0, len(plan.steps))
	for _, step := range plan.steps {
		names = append(names, step.Name())
	}
	return names
}

func TestPrepareRecoveredRun_ResumesWithTheGatesTheRunPinned(t *testing.T) {
	pinned := []config.Gate{{Name: "arch-fitness", After: types.StepReview, Command: "true"}}
	m, run, recorded := gatePinFixture(t, oneGateYAML, pinned)

	repo, err := m.db.GetRepo(run.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	// The maintainer merges a second gate onto the default branch after the run
	// parked. The run's own head, and its recorded steps, are untouched.
	commitDefaultBranchConfig(t, repo.WorkingPath, twoGatesYAML)

	plan, err := m.prepareRecoveredRun(context.Background(), run)
	if err != nil {
		t.Fatalf("parked run must still recover after the default branch changed its gates: %v", err)
	}
	if got := planStepNames(plan); !stepNamesEqual(got, recorded) {
		t.Errorf("recovered step sequence = %v, want the sequence the run recorded %v", got, recorded)
	}
	if len(plan.cfg.Gates) != 1 || plan.cfg.Gates[0].Name != "arch-fitness" {
		t.Errorf("recovered gates = %v, want only the pinned arch-fitness gate", plan.cfg.Gates)
	}
}

func TestPrepareRecoveredRun_UnpinnedRunRecoversAsTheCorePipeline(t *testing.T) {
	m, run, recorded := gatePinFixture(t, oneGateYAML, nil)
	if payload, err := m.db.GetRunGates(run.ID); err != nil || payload != "" {
		t.Fatalf("fixture pinned %q (err %v), want the pre-upgrade empty pin", payload, err)
	}

	plan, err := m.prepareRecoveredRun(context.Background(), run)
	if err != nil {
		t.Fatalf("unpinned parked run must recover: %v", err)
	}
	if got := planStepNames(plan); !stepNamesEqual(got, recorded) {
		t.Errorf("recovered step sequence = %v, want the core sequence the run recorded %v", got, recorded)
	}
	for _, name := range planStepNames(plan) {
		if name.IsCustomGate() {
			t.Errorf("unpinned run recovered with gate step %q from the live default branch", name)
		}
	}
}

func TestPrepareRecoveredRun_UnusableGatePinFailsClosed(t *testing.T) {
	pinned := []config.Gate{{Name: "arch-fitness", After: types.StepReview, Command: "true"}}
	m, run, _ := gatePinFixture(t, oneGateYAML, pinned)
	if err := m.db.SetRunGates(run.ID, `[{"name":"arch-fitness","after":"push","command":"true"}]`); err != nil {
		t.Fatal(err)
	}

	_, err := m.prepareRecoveredRun(context.Background(), run)
	if err == nil {
		t.Fatal("an unusable gate pin must not resume the run")
	}
	if !strings.Contains(err.Error(), "pinned gates") {
		t.Errorf("recovery error = %q, want it to name the pinned gates", err)
	}
}

func stepNamesEqual(a, b []types.StepName) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
