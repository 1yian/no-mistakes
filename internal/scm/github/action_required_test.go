package github

import (
	"context"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func heldWorkflowRunResponses(runsJSON string, extra map[string]githubTestResponse) map[string]githubTestResponse {
	responses := map[string]githubTestResponse{
		"gh pr view 123 --repo test/repo --json headRefOid --jq .headRefOid": {stdout: "deadbeef\n"},
		"gh pr checks 123 --repo test/repo --json name,state,bucket,completedAt,link": {
			stderr: "no checks reported on the 'feature' branch\n",
			code:   1,
		},
		"gh api --method GET repos/test/repo/actions/runs -f head_sha=deadbeef -f per_page=100 --paginate --slurp": {
			stdout: runsJSON,
		},
	}
	for key, response := range extra {
		responses[key] = response
	}
	return responses
}

const heldWorkflowRun = `[{"total_count":1,"workflow_runs":[
	{"id":101,"name":"CI","display_title":"","status":"completed","conclusion":"action_required","updated_at":"2026-09-24T12:34:56Z"}
]}]` + "\n"

func TestGetChecksTreatsHeldWorkflowRunWithNoJobsAsAwaitingApproval(t *testing.T) {
	t.Parallel()

	host := New(githubTestCmdFactory(heldWorkflowRunResponses(heldWorkflowRun, map[string]githubTestResponse{
		"gh run view 101 --repo test/repo --json jobs": {stdout: `{"jobs":[]}` + "\n"},
	})), nil, "", "test/repo")

	checks, err := host.GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "deadbeef"})
	if err != nil {
		t.Fatalf("GetChecks() error = %v", err)
	}
	if len(checks) != 1 {
		t.Fatalf("GetChecks() returned %d checks, want 1: %+v", len(checks), checks)
	}
	got := checks[0]
	if got.Bucket != scm.CheckBucketPending {
		t.Errorf("held workflow run bucket = %q, want %q", got.Bucket, scm.CheckBucketPending)
	}
	if !got.AwaitingApproval {
		t.Errorf("held workflow run AwaitingApproval = false, want true: %+v", got)
	}
	if got.State != "ACTION_REQUIRED" {
		t.Errorf("held workflow run State = %q, want ACTION_REQUIRED", got.State)
	}
}

func TestGetChecksKeepsActionRequiredWorkflowRunWithJobsFailing(t *testing.T) {
	t.Parallel()

	host := New(githubTestCmdFactory(heldWorkflowRunResponses(heldWorkflowRun, map[string]githubTestResponse{
		"gh run view 101 --repo test/repo --json jobs": {
			stdout: `{"jobs":[{"databaseId":9001,"name":"build","status":"completed","conclusion":"action_required"}]}` + "\n",
		},
	})), nil, "", "test/repo")

	checks, err := host.GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "deadbeef"})
	if err != nil {
		t.Fatalf("GetChecks() error = %v", err)
	}
	if len(checks) != 1 {
		t.Fatalf("GetChecks() returned %d checks, want 1: %+v", len(checks), checks)
	}
	if got := checks[0]; got.Bucket != scm.CheckBucketFail || got.AwaitingApproval {
		t.Fatalf("action_required run with jobs = %+v, want fail bucket and AwaitingApproval false", got)
	}
}

func TestGetChecksKeepsActionRequiredWorkflowRunFailingWhenJobsUnreadable(t *testing.T) {
	t.Parallel()

	// No "gh run view 101" mapping: the job read fails.
	host := New(githubTestCmdFactory(heldWorkflowRunResponses(heldWorkflowRun, nil)), nil, "", "test/repo")

	checks, err := host.GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "deadbeef"})
	if err != nil {
		t.Fatalf("GetChecks() error = %v", err)
	}
	if len(checks) != 1 {
		t.Fatalf("GetChecks() returned %d checks, want 1: %+v", len(checks), checks)
	}
	if got := checks[0]; got.Bucket != scm.CheckBucketFail || got.AwaitingApproval {
		t.Fatalf("unreadable action_required run = %+v, want fail bucket and AwaitingApproval false", got)
	}
}

func TestGetChecksKeepsGenuineWorkflowRunFailureFailing(t *testing.T) {
	t.Parallel()

	runs := `[{"total_count":1,"workflow_runs":[
		{"id":101,"name":"CI","display_title":"","status":"completed","conclusion":"failure","updated_at":"2026-09-24T12:34:56Z"}
	]}]` + "\n"
	host := New(githubTestCmdFactory(heldWorkflowRunResponses(runs, nil)), nil, "", "test/repo")

	checks, err := host.GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "deadbeef"})
	if err != nil {
		t.Fatalf("GetChecks() error = %v", err)
	}
	if len(checks) != 1 {
		t.Fatalf("GetChecks() returned %d checks, want 1: %+v", len(checks), checks)
	}
	if got := checks[0]; got.Bucket != scm.CheckBucketFail || got.AwaitingApproval {
		t.Fatalf("failed workflow run = %+v, want fail bucket and AwaitingApproval false", got)
	}
}

func TestGetChecksKeepsActionRequiredWorkflowRunFailingWhenJobsAreAbsent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"jobs key missing", `{}` + "\n"},
		{"jobs explicitly null", `{"jobs":null}` + "\n"},
		{"jobs missing alongside other fields", `{"databaseId":101}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			host := New(githubTestCmdFactory(heldWorkflowRunResponses(heldWorkflowRun, map[string]githubTestResponse{
				"gh run view 101 --repo test/repo --json jobs": {stdout: tc.body},
			})), nil, "", "test/repo")

			checks, err := host.GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "deadbeef"})
			if err != nil {
				t.Fatalf("GetChecks() error = %v", err)
			}
			if len(checks) != 1 {
				t.Fatalf("GetChecks() returned %d checks, want 1: %+v", len(checks), checks)
			}
			if got := checks[0]; got.Bucket != scm.CheckBucketFail || got.AwaitingApproval {
				t.Fatalf("action_required run with absent job data = %+v, want fail bucket and AwaitingApproval false", got)
			}
		})
	}
}
