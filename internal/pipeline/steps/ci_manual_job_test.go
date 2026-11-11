package steps

import (
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/cimonitor"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func TestCIWaitingMessage_NamesTheManualJobHold(t *testing.T) {
	t.Parallel()

	manualHold := scm.Check{Name: "deploy", Bucket: scm.CheckBucketPending, State: "MANUAL", AwaitingManualJob: true}
	cases := []struct {
		name   string
		checks []scm.Check
		want   string
	}{
		{
			name:   "blocking manual job",
			checks: []scm.Check{manualHold},
			want:   cimonitor.ChecksAwaitingManualJobMsg,
		},
		{
			name:   "blocking manual job alongside a running check",
			checks: []scm.Check{{Name: "test", Bucket: scm.CheckBucketPending, State: "RUNNING"}, manualHold},
			want:   cimonitor.ChecksAwaitingManualJobMsg,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ciWaitingMessage(tc.checks); got != tc.want {
				t.Fatalf("ciWaitingMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestManualJobHoldIsAWaitNotAFailure(t *testing.T) {
	t.Parallel()

	held := scm.Check{Name: "deploy", Bucket: scm.CheckBucketPending, State: "MANUAL", AwaitingManualJob: true}
	checks := []scm.Check{held}

	if hasFailingChecks(checks) {
		t.Error("a blocking manual job must not count as a failing check")
	}
	if names := failingCheckNames(checks); len(names) != 0 {
		t.Errorf("failingCheckNames() = %v, want none", names)
	}
	if checkFailedTerminally(held) {
		t.Error("a blocking manual job has not failed terminally")
	}
	if !hasPendingChecks(checks) {
		t.Error("a blocking manual job must keep the PR pending")
	}
	if allChecksPassed(checks) {
		t.Error("a blocking manual job must never read as passed")
	}

	// Nothing this run can do clears the hold, so it must not hold up the
	// escalation of a check that genuinely failed beside it.
	if hasExecutingPendingChecks(checks) {
		t.Error("a blocking manual job must not defer the escalation of other checks' failures")
	}
	withFailure := []scm.Check{held, {Name: "test", Bucket: scm.CheckBucketFail, State: "FAILED"}}
	if !hasFailingChecks(withFailure) {
		t.Error("a genuine failure alongside a manual hold must still be failing")
	}
	if hasExecutingPendingChecks(withFailure) {
		t.Error("a genuine failure alongside a manual hold must escalate instead of waiting")
	}

	// An ordinary pending check keeps its old meaning: it is work in flight and
	// does defer escalation.
	running := scm.Check{Name: "test", Bucket: scm.CheckBucketPending, State: "RUNNING"}
	if !hasExecutingPendingChecks([]scm.Check{running}) {
		t.Error("a running check must still defer escalation")
	}
}
