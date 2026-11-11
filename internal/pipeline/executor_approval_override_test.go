package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type overrideVerifyingStep struct {
	*mockStep
	verify func(sctx *StepContext) (string, error)
}

func (o *overrideVerifyingStep) VerifyApprovalOverride(sctx *StepContext) (string, error) {
	return o.verify(sctx)
}

func newOverrideVerifyingApprovalStep(name types.StepName, findings string, verify func(sctx *StepContext) (string, error)) *overrideVerifyingStep {
	return &overrideVerifyingStep{
		mockStep: newApprovalStep(name, findings),
		verify:   verify,
	}
}

func runApprovalOverrideCase(t *testing.T, step Step) *string {
	t.Helper()
	database, p, run, repo := setupTest(t)
	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, t.TempDir()) }()

	waitForStepStatus(t, database, run.ID, step.Name(), types.StepStatusAwaitingApproval)
	if err := exec.Respond(step.Name(), types.ActionApprove, nil); err != nil {
		t.Fatalf("Respond(approve) error = %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() error = %v, want approval to complete the run", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not complete after approval")
	}

	got, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want %s (approval must still proceed)", got.Status, types.RunCompleted)
	}

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != types.StepStatusCompleted {
		t.Fatalf("steps after approval = %+v, want one completed step", steps)
	}
	return steps[0].OverrideReason
}

func TestExecutor_ApprovalOverride_StillFailingIsRecorded(t *testing.T) {
	step := newOverrideVerifyingApprovalStep(types.StepCI, `{"summary":"CI check failing: required-check"}`,
		func(*StepContext) (string, error) {
			return "live checks for https://example/pr/1 still failing: required-check", nil
		})
	reason := runApprovalOverrideCase(t, step)
	if reason == nil || *reason == "" {
		t.Fatal("OverrideReason = nil, want the still-unresolved reason recorded")
	}
	if want := "required-check"; !strings.Contains(*reason, want) {
		t.Errorf("OverrideReason = %q, want it to name %q", *reason, want)
	}
}

func TestExecutor_ApprovalOverride_BecameGreenLeavesNoMark(t *testing.T) {
	step := newOverrideVerifyingApprovalStep(types.StepCI, `{"summary":"CI check failing: required-check"}`,
		func(*StepContext) (string, error) { return "", nil })
	reason := runApprovalOverrideCase(t, step)
	if reason != nil {
		t.Fatalf("OverrideReason = %q, want nil once the live condition cleared", *reason)
	}
}

func TestExecutor_ApprovalOverride_VerificationErrorFailsClosed(t *testing.T) {
	step := newOverrideVerifyingApprovalStep(types.StepCI, `{"summary":"CI check failing: required-check"}`,
		func(*StepContext) (string, error) { return "", errors.New("provider unreachable") })
	reason := runApprovalOverrideCase(t, step)
	if reason == nil || *reason == "" {
		t.Fatal("OverrideReason = nil, want a verification error to fail closed and record an override")
	}
	if !strings.Contains(*reason, "provider unreachable") {
		t.Errorf("OverrideReason = %q, want it to surface the verification error", *reason)
	}
}

func TestExecutor_ApprovalOverride_OrdinaryStepUnaffected(t *testing.T) {
	step := newApprovalStep(types.StepReview, `{"issues":["nit"]}`)
	reason := runApprovalOverrideCase(t, step)
	if reason != nil {
		t.Fatalf("OverrideReason = %q, want nil: %s does not implement ApprovalOverrideVerifier", *reason, step.Name())
	}
}

func TestExecutor_ApprovalOverride_RecoveredPathStillFailing(t *testing.T) {
	database, p, run, repo := setupTest(t)
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	stepResult, err := database.InsertStepResult(run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.StartStep(stepResult.ID); err != nil {
		t.Fatal(err)
	}
	findings := `{"summary":"CI check failing: required-check"}`
	if err := database.SetStepFindings(stepResult.ID, findings); err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertStepRound(stepResult.ID, 1, "initial", &findings, nil, 10); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateStepStatusWithDuration(stepResult.ID, types.StepStatusAwaitingApproval, 10); err != nil {
		t.Fatal(err)
	}
	if err := database.SetRunAwaitingAgent(run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	wantReason := "live checks for https://example/pr/1 still failing: required-check"
	step := newOverrideVerifyingApprovalStep(types.StepCI, findings, func(sctx *StepContext) (string, error) {
		if sctx.StepResultID != stepResult.ID {
			return "wrong recovered step result: " + sctx.StepResultID, nil
		}
		return wantReason, nil
	})
	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() { done <- exec.Resume(context.Background(), run, repo, t.TempDir()) }()

	// The step is already parked at awaiting_approval in the DB before Resume
	// even starts (that is what "recovered" means), so waitForStepStatus
	// resolves immediately and proves nothing about whether Resume's goroutine
	// has reached e.waiting = true yet. Retry Respond instead of gating on a
	// step-local channel: this step deliberately implements no
	// ApprovalGateReconciler hook to observe.
	deadline := time.Now().Add(5 * time.Second)
	var respondErr error
	for time.Now().Before(deadline) {
		if respondErr = exec.Respond(types.StepCI, types.ActionApprove, nil); respondErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if respondErr != nil {
		t.Fatalf("Respond(approve) error = %v", respondErr)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Resume() error = %v, want approval to complete the run", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resumed executor did not complete after approval")
	}

	got, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want %s", got.Status, types.RunCompleted)
	}

	// Serialized durable state: re-read the step fresh from the DB (not the
	// in-memory value from before Resume) so this proves the override marker
	// actually persisted, not merely that the in-process struct carries it.
	reread, err := database.GetStepResult(stepResult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.OverrideReason == nil {
		t.Fatal("recovered-path OverrideReason = nil, want the still-unresolved reason recorded and durable")
	}
	if *reread.OverrideReason != wantReason {
		t.Fatalf("recovered-path OverrideReason = %q, want %q", *reread.OverrideReason, wantReason)
	}
}

func TestExecutor_CIOverrideReasonIgnoresTestOverride(t *testing.T) {
	database, _, run, _ := setupTest(t)
	testStep, err := database.InsertStepResult(run.ID, types.StepTest)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetStepOverrideReason(testStep.ID, "configured test command failed"); err != nil {
		t.Fatal(err)
	}
	ciStep, err := database.InsertStepResult(run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	const want = "live checks still failing: required-check"
	if err := database.SetStepOverrideReason(ciStep.ID, want); err != nil {
		t.Fatal(err)
	}

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := completionOverrideReasons(steps); got != want {
		t.Fatalf("completionOverrideReasons() CI = %q, want %q", got, want)
	}
}

func TestExecutor_ApprovalOverride_RunCompletedEventCarriesReason(t *testing.T) {
	database, p, run, repo := setupTest(t)

	var mu sync.Mutex
	var completedReason *string
	var sawCompleted bool
	onEvent := func(e ipc.Event) {
		if e.Type != ipc.EventRunCompleted {
			return
		}
		mu.Lock()
		sawCompleted = true
		completedReason = e.CIOverrideReason
		mu.Unlock()
	}

	step := newOverrideVerifyingApprovalStep(types.StepCI, `{"summary":"CI check failing: required-check"}`,
		func(*StepContext) (string, error) {
			return "live checks for https://example/pr/1 still failing: required-check", nil
		})
	exec := NewExecutor(database, p, nil, nil, []Step{step}, onEvent)

	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, t.TempDir()) }()

	waitForStepStatus(t, database, run.ID, step.Name(), types.StepStatusAwaitingApproval)
	if err := exec.Respond(step.Name(), types.ActionApprove, nil); err != nil {
		t.Fatalf("Respond(approve) error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not complete after approval")
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawCompleted {
		t.Fatal("no run_completed event emitted")
	}
	if completedReason == nil || *completedReason == "" {
		t.Fatal("run_completed event CIOverrideReason = nil, want the override reason carried on the delta")
	}
	if !strings.Contains(*completedReason, "required-check") {
		t.Errorf("run_completed CIOverrideReason = %q, want it to name %q", *completedReason, "required-check")
	}
}

func TestExecutor_ApprovalOverride_RequiredReviewRefusesApprovalAndKeepsTheGateParked(t *testing.T) {
	for _, blocked := range []string{"review rate limited", "could not verify live CI state: gh: connection reset"} {
		for _, recovered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recovered=%t", blocked, recovered), func(t *testing.T) {
				database, p, run, repo := setupTest(t)
				findings := `{"summary":"Required review incomplete"}`
				var mu sync.Mutex
				complete := false
				step := newOverrideVerifyingApprovalStep(types.StepCI, findings, func(*StepContext) (string, error) {
					mu.Lock()
					defer mu.Unlock()
					if complete {
						return "", nil
					}
					return "", fmt.Errorf("%w: %s", ErrApprovalBlocked, blocked)
				})
				if recovered {
					if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
						t.Fatal(err)
					}
					sr, err := database.InsertStepResult(run.ID, types.StepCI)
					if err != nil {
						t.Fatal(err)
					}
					if err := database.StartStep(sr.ID); err != nil {
						t.Fatal(err)
					}
					if err := database.SetStepFindings(sr.ID, findings); err != nil {
						t.Fatal(err)
					}
					if _, err := database.InsertStepRound(sr.ID, 1, "initial", &findings, nil, 10); err != nil {
						t.Fatal(err)
					}
					if err := database.UpdateStepStatusWithDuration(sr.ID, types.StepStatusAwaitingApproval, 10); err != nil {
						t.Fatal(err)
					}
					if err := database.SetRunAwaitingAgent(run.ID); err != nil {
						t.Fatal(err)
					}
					run, err = database.GetRun(run.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
				done := make(chan error, 1)
				dir := t.TempDir()
				go func() {
					if recovered {
						done <- exec.Resume(context.Background(), run, repo, dir)
					} else {
						done <- exec.Execute(context.Background(), run, repo, dir)
					}
				}()
				deadline := time.Now().Add(5 * time.Second)
				var refusal *RespondRefusal
				for {
					err := exec.Respond(types.StepCI, types.ActionApprove, nil)
					if errors.As(err, &refusal) {
						break
					}
					if err == nil {
						t.Fatal("approval over an incomplete required review was accepted")
					}
					if time.Now().After(deadline) {
						t.Fatal(err)
					}
					time.Sleep(20 * time.Millisecond)
				}
				if !strings.Contains(refusal.Message, blocked) {
					t.Fatalf("refusal = %q, want it to name %q", refusal.Message, blocked)
				}
				select {
				case err := <-done:
					t.Fatalf("refused approval ended the run: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				got, err := database.GetRun(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status == types.RunFailed {
					t.Fatalf("run status=%s, want the run kept alive", got.Status)
				}
				rows, err := database.GetStepsByRun(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 || rows[0].Status != types.StepStatusAwaitingApproval || rows[0].OverrideReason != nil {
					t.Fatalf("refused approval changed the gate: %+v", rows)
				}

				mu.Lock()
				complete = true
				mu.Unlock()
				if err := exec.Respond(types.StepCI, types.ActionApprove, nil); err != nil {
					t.Fatalf("approval after the review completed: %v", err)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("execution error=%v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("approval did not settle")
				}
				rows, err = database.GetStepsByRun(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 || rows[0].Status != types.StepStatusCompleted || rows[0].OverrideReason != nil {
					t.Fatalf("approved step = %+v, want a completed step with no override", rows)
				}
			})
		}
	}
}

func TestExecutor_ApprovalOverride_VerificationIsBoundedByTheGateReconcileTimeout(t *testing.T) {
	database, p, run, repo := setupTest(t)
	step := newOverrideVerifyingApprovalStep(types.StepCI, `{"summary":"Required review incomplete"}`, func(sctx *StepContext) (string, error) {
		<-sctx.Ctx.Done()
		return "", fmt.Errorf("%w: %w", ErrApprovalBlocked, sctx.Ctx.Err())
	})
	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	exec.SetGateReconcileTimings(time.Hour, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- exec.Execute(ctx, run, repo, t.TempDir()) }()

	waitForStepStatus(t, database, run.ID, step.Name(), types.StepStatusAwaitingApproval)
	responded := make(chan error, 1)
	go func() { responded <- exec.Respond(step.Name(), types.ActionApprove, nil) }()
	select {
	case err := <-responded:
		var refusal *RespondRefusal
		if !errors.As(err, &refusal) || !strings.Contains(refusal.Message, context.DeadlineExceeded.Error()) {
			t.Fatalf("Respond(approve) = %v, want a refusal naming the verification deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval verification was not bounded by the gate reconcile timeout")
	}
	rows, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != types.StepStatusAwaitingApproval {
		t.Fatalf("timed-out verification changed the gate: %+v", rows)
	}
	cancel()
	<-done
}
