package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestExecutor_RecoveredAnswerRoundIsTriggeredAsAnAnswer(t *testing.T) {
	database, p, run, repo := setupTest(t)
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	stepResult, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.StartStep(stepResult.ID); err != nil {
		t.Fatal(err)
	}

	// The gate the reviewer left behind: one open question, parked as the
	// ask-user warning the review step emits for it.
	findings := `{"findings":[{"id":"question-q1","severity":"warning","description":"Review question awaiting an answer: keep the legacy route?","action":"ask-user","category":"review-question"}],"summary":"one open question"}`
	if err := database.SetStepFindings(stepResult.ID, findings); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertReviewStepRound(stepResult.ID, 1, "initial", &findings, nil, "", 25); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateStepStatusWithDuration(stepResult.ID, types.StepStatusAwaitingApproval, 25); err != nil {
		t.Fatal(err)
	}
	if err := database.SetRunAwaitingAgent(run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	finalized := make(chan struct{})
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			if sctx.Fixing {
				return nil, fmt.Errorf("an answer is not a fix round; Fixing must stay false")
			}
			if !sctx.FinalizingAnswers {
				return nil, fmt.Errorf("recovered answer round did not reach the step as a finalize turn")
			}
			close(finalized)
			return &StepOutcome{}, nil
		},
	}
	exec := NewExecutor(database, p, &config.Config{AutoFix: config.AutoFix{Review: 2}}, newFakeSessionAgent(), []Step{step}, nil)

	done := make(chan error, 1)
	go func() { done <- exec.Resume(context.Background(), run, repo, t.TempDir()) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := exec.Respond(types.StepReview, types.ActionAnswer, nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered gate never accepted an answer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-finalized:
	case <-time.After(5 * time.Second):
		t.Fatal("recovered finalize turn never ran")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovered executor timed out")
	}

	rounds, err := database.GetRoundsByStep(stepResult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 2 {
		t.Fatalf("got %d review rounds, want the parked one plus the finalize round", len(rounds))
	}
	if rounds[1].Trigger != "answer" {
		t.Fatalf("recovered finalize round trigger = %q, want \"answer\"; %q would claim the run had two initial review rounds", rounds[1].Trigger, rounds[1].Trigger)
	}
	// An answer is not a fix round, and nothing else may reclassify it as one.
	if rounds[1].IsFixRound() {
		t.Fatal("the recovered answer round reads as a fix round")
	}
}

func TestExecutor_AnswerActionIsRefusedForAnyStepButReview(t *testing.T) {
	database, p, run, repo := setupTest(t)
	step := newApprovalStep(types.StepDocument, `{"findings":[{"id":"d-1","severity":"warning","description":"waiting","action":"ask-user"}],"summary":"1 issue"}`)
	exec := NewExecutor(database, p, &config.Config{}, newFakeSessionAgent(), []Step{step}, nil)

	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, t.TempDir()) }()
	waitForStepStatus(t, database, run.ID, types.StepDocument, types.StepStatusAwaitingApproval)

	if err := exec.Respond(types.StepDocument, types.ActionAnswer, nil); err != nil {
		t.Fatalf("Respond() error = %v", err)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "only a review response") {
			t.Fatalf("Execute() error = %v, want the answer action refused for a non-review step", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run neither failed nor completed after an out-of-scope answer")
	}

	got, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.RunFailed {
		t.Fatalf("run status = %s, want %s: an out-of-scope answer must fail closed", got.Status, types.RunFailed)
	}
	// It failed instead of re-executing the step with review semantics.
	if n := step.callCount(); n != 1 {
		t.Fatalf("step executed %d times, want 1: the refused answer must not re-run it", n)
	}
}

func TestExecutor_RecoveredAnswerRoundInheritsAFixReviewGatesContext(t *testing.T) {
	database, p, run, repo := setupTest(t)
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	stepResult, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.StartStep(stepResult.ID); err != nil {
		t.Fatal(err)
	}

	// Round 1 parked on code findings and was auto-fixed; round 2 is the fix
	// round whose rereview asked a question, so the gate is a fix_review.
	first := `{"findings":[{"id":"f-1","severity":"error","description":"bug","action":"auto-fix"}],"summary":"1 issue"}`
	round1, err := database.InsertReviewStepRound(stepResult.ID, 1, "initial", &first, nil, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	selected := `["f-1"]`
	if err := database.SetStepRoundSelection(round1.ID, &selected, db.RoundSelectionSourceAutoFix); err != nil {
		t.Fatal(err)
	}
	findings := `{"findings":[{"id":"question-q1","severity":"warning","description":"Review question awaiting an answer: was the fix meant to change this?","action":"ask-user","category":"review-question"}],"summary":"one open question"}`
	if err := database.SetStepFindings(stepResult.ID, findings); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertReviewStepRound(stepResult.ID, 2, "auto_fix", &findings, nil, "", 30); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateStepStatusWithDuration(stepResult.ID, types.StepStatusFixReview, 30); err != nil {
		t.Fatal(err)
	}
	if err := database.SetRunAwaitingAgent(run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	finalized := make(chan struct{})
	var sawFixing, sawSkipFix bool
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			if !sctx.FinalizingAnswers {
				return nil, fmt.Errorf("recovered answer round did not reach the step as a finalize turn")
			}
			sawFixing = sctx.Fixing
			sawSkipFix = sctx.SkipFixExecution
			close(finalized)
			return &StepOutcome{}, nil
		},
	}
	exec := NewExecutor(database, p, &config.Config{AutoFix: config.AutoFix{Review: 2}}, newFakeSessionAgent(), []Step{step}, nil)

	done := make(chan error, 1)
	go func() { done <- exec.Resume(context.Background(), run, repo, t.TempDir()) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := exec.Respond(types.StepReview, types.ActionAnswer, nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered fix_review gate never accepted an answer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-finalized:
	case <-time.After(5 * time.Second):
		t.Fatal("recovered finalize turn never ran")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovered executor timed out")
	}

	// The context the live path would have handed in.
	if !sawFixing {
		t.Fatal("the recovered answer round dropped the fix_review gate's fixing context, so it would re-park as awaiting_approval")
	}
	// ...and the half that stops it re-running the fixer on already-fixed code.
	if !sawSkipFix {
		t.Fatal("the recovered answer round inherited fixing without SkipFixExecution, so the fixer would re-run over already-fixed code")
	}

	rounds, err := database.GetRoundsByStep(stepResult.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := rounds[len(rounds)-1]
	if last.Trigger != "answer" {
		t.Fatalf("recovered finalize round trigger = %q, want \"answer\"; the inherited fix context must not relabel a human's answer", last.Trigger)
	}
	if last.IsFixRound() {
		t.Fatal("the recovered answer round reads as a fix round")
	}
}

func TestExecutor_RecoveredAnswerRoundKeepsTheOutstandingFindings(t *testing.T) {
	database, p, run, repo := setupTest(t)
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	stepResult, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.StartStep(stepResult.ID); err != nil {
		t.Fatal(err)
	}

	first := `{"findings":[{"id":"f-1","severity":"error","file":"feature.txt","description":"bug","action":"auto-fix"}],"summary":"1 issue"}`
	round1, err := database.InsertReviewStepRound(stepResult.ID, 1, "initial", &first, nil, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	selected := `["f-1"]`
	if err := database.SetStepRoundSelection(round1.ID, &selected, db.RoundSelectionSourceAutoFix); err != nil {
		t.Fatal(err)
	}
	// The parked gate carries both: the unverified code finding and the
	// question the rereview asked.
	findings := `{"findings":[{"id":"f-1","severity":"error","file":"feature.txt","description":"bug","action":"auto-fix"},{"id":"question-q1","severity":"warning","description":"Review question awaiting an answer: was the fix meant to change this?","action":"ask-user","category":"review-question"}],"summary":"one open question"}`
	if err := database.SetStepFindings(stepResult.ID, findings); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertReviewStepRound(stepResult.ID, 2, "auto_fix", &findings, &selected, "", 30); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateStepStatusWithDuration(stepResult.ID, types.StepStatusFixReview, 30); err != nil {
		t.Fatal(err)
	}
	if err := database.SetRunAwaitingAgent(run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	finalized := make(chan struct{})
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			select {
			case <-finalized:
			default:
				close(finalized)
			}
			// A clean turn that reviewed the file: it reports nothing, which is
			// silence about f-1 rather than verification of it.
			return &StepOutcome{}, nil
		},
	}
	exec := NewExecutor(database, p, &config.Config{AutoFix: config.AutoFix{Review: 0}}, newFakeSessionAgent(), []Step{step}, nil)

	done := make(chan error, 1)
	go func() { done <- exec.Resume(context.Background(), run, repo, t.TempDir()) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := exec.Respond(types.StepReview, types.ActionAnswer, nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered fix_review gate never accepted an answer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-finalized:
	case <-time.After(5 * time.Second):
		t.Fatal("recovered finalize turn never ran")
	}

	// The step must NOT complete: f-1 was selected for a fix and never
	// positively verified, so it is still outstanding and still parks.
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusFixReview)
	got, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].FindingsJSON == nil || !strings.Contains(*got[0].FindingsJSON, `"f-1"`) {
		t.Fatalf("the recovered answer round dropped the outstanding findings: %+v", got)
	}

	if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("recovered executor timed out")
	}
}
