package cimonitor

import "testing"

func TestParseActivityRecognizesAwaitingApproval(t *testing.T) {
	t.Parallel()

	logs := []string{ChecksPassedMsg, ChecksAwaitingApprovalMsg}
	activity := ParseActivity(logs)
	if activity.Ready || activity.DeclaredNoCI {
		t.Errorf("activity = %+v, want not ready", activity)
	}
	if activity.LastEvent != ChecksAwaitingApprovalMsg {
		t.Errorf("LastEvent = %q, want the awaiting-approval message", activity.LastEvent)
	}
	if ChecksPassed(logs) {
		t.Error("a held workflow must never report checks passed")
	}
}

func TestParseActivityRecognizesAwaitingManualJob(t *testing.T) {
	t.Parallel()

	logs := []string{ChecksPassedMsg, ChecksAwaitingManualJobMsg}
	activity := ParseActivity(logs)
	if activity.Ready || activity.DeclaredNoCI {
		t.Errorf("activity = %+v, want not ready", activity)
	}
	if activity.LastEvent != ChecksAwaitingManualJobMsg {
		t.Errorf("LastEvent = %q, want the awaiting-manual-job message", activity.LastEvent)
	}
	if ChecksPassed(logs) {
		t.Error("a pipeline held on a manual job must never report checks passed")
	}
}
