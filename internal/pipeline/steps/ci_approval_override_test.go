package steps

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps/internal/stepstest"
)

func TestCIStep_VerifyApprovalOverride(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		checksJSON     string
		wantUnresolved bool
		wantContains   string
	}{
		{
			name:           "still failing",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"PR must be raised via no-mistakes","state":"FAILURE","bucket":"fail"}]`,
			wantUnresolved: true,
			wantContains:   "PR must be raised via no-mistakes",
		},
		{
			name:           "became green",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"PR must be raised via no-mistakes","state":"SUCCESS","bucket":"pass"}]`,
			wantUnresolved: false,
		},
		// Regression for the upstream review P1 "unresolved checks become
		// clean passes": before this fix, VerifyApprovalOverride used
		// !hasFailingChecks, which reads pending/cancelled/unknown-bucket
		// checks (none of them Failing()) as a clean pass. It must instead
		// use allChecksPassed, the same trusted all-green semantics the CI
		// step's own polling loop uses, so anything short of every check
		// being pass/skip is reported as unresolved.
		{
			name:           "still pending",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"deploy","state":"IN_PROGRESS","bucket":"pending"}]`,
			wantUnresolved: true,
			wantContains:   "deploy",
		},
		{
			name:           "cancelled",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"deploy","state":"CANCELLED","bucket":"cancel"}]`,
			wantUnresolved: true,
			wantContains:   "deploy",
		},
		{
			// The verifier now names the delivered commit, so this reads the
			// commit's check rollup, and that read refuses a context whose state
			// it cannot bucket rather than carrying it forward: still unresolved,
			// never a clean pass. The bucket mapping itself stays covered below,
			// where the GitLab reader keeps an unrecognized status visible.
			name:           "unknown bucket refuses the commit read",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"legacy","state":"SOMETHING_NEW","bucket":"weird"}]`,
			wantUnresolved: true,
			wantContains:   "incomplete context",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The verifier names the delivered commit on every read now, so the
			// fixture is a run worktree whose HEAD is that commit, the same setup
			// the CI monitor tests use.
			dir, baseSHA, headSHA := stepstest.SetupGitRepo(t)
			env := fakeCIGH(t, "OPEN", tc.checksJSON)

			prURL := "https://github.com/test/repo/pull/42"
			sctx := newTestContext(t, nil, dir, baseSHA, headSHA, config.Commands{})
			sctx.Env = env
			sctx.Run.PRURL = &prURL

			step := &CIStep{}
			unresolved, err := step.VerifyApprovalOverride(sctx)
			if err != nil {
				t.Fatalf("VerifyApprovalOverride() error = %v", err)
			}
			if tc.wantUnresolved && unresolved == "" {
				t.Fatal("unresolved = \"\", want a reason naming the still-failing check")
			}
			if !tc.wantUnresolved && unresolved != "" {
				t.Fatalf("unresolved = %q, want \"\" once every check passed", unresolved)
			}
			if tc.wantContains != "" && !strings.Contains(unresolved, tc.wantContains) {
				t.Errorf("unresolved = %q, want it to name %q", unresolved, tc.wantContains)
			}
		})
	}
}

func TestCIStep_VerifyApprovalOverride_NoPRURL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sctx := newTestContext(t, nil, dir, "base", "deadbeef", config.Commands{})

	step := &CIStep{}
	unresolved, err := step.VerifyApprovalOverride(sctx)
	if err != nil {
		t.Fatalf("VerifyApprovalOverride() error = %v", err)
	}
	if unresolved == "" {
		t.Fatal("unresolved = \"\", want a fail-closed reason when there is no PR URL to verify")
	}
}

func TestCIStep_VerifyApprovalOverride_EmptyChecks(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := stepstest.SetupGitRepo(t)
	env := fakeCIGHNoChecks(t)

	prURL := "https://github.com/test/repo/pull/42"
	sctx := newTestContext(t, nil, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Run.PRURL = &prURL

	step := &CIStep{}
	unresolved, err := step.VerifyApprovalOverride(sctx)
	if err != nil {
		t.Fatalf("VerifyApprovalOverride() error = %v", err)
	}
	if unresolved == "" {
		t.Fatal("unresolved = \"\", want a fail-closed reason when the PR reports no checks at all")
	}
}

func fakeCIGlabOverride(t *testing.T, headSHA, pipelineSHA, checksJSON string) []string {
	t.Helper()
	binDir := fakeCLIBinDir(t)
	linkTestBinary(t, binDir, "glab")
	return fakeCLIEnv(binDir, map[string]string{
		"FAKE_CLI_MODE":         "ci-glab",
		"FAKE_CLI_STATE":        "opened",
		"FAKE_CLI_CHECKS":       checksJSON,
		"FAKE_CLI_MR_HEAD_SHA":  headSHA,
		"FAKE_CLI_PIPELINE_SHA": pipelineSHA,
	})
}

func TestCIStep_VerifyApprovalOverride_NamesTheDeliveredCommit(t *testing.T) {
	t.Parallel()

	t.Run("stranded pipeline refuses", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := fakeCIGlabOverride(t, "deadbeef", strings.Repeat("b", 40), `[{"id":1,"name":"build","status":"success"}]`)

		prURL := "https://gitlab.com/test/repo/-/merge_requests/42"
		sctx := newTestContext(t, nil, dir, "base", "deadbeef", config.Commands{})
		sctx.Env = env
		sctx.Repo.UpstreamURL = "https://gitlab.com/test/repo.git"
		sctx.Run.PRURL = &prURL

		unresolved, err := (&CIStep{}).VerifyApprovalOverride(sctx)
		if err != nil {
			t.Fatalf("VerifyApprovalOverride() error = %v", err)
		}
		if unresolved == "" {
			t.Fatal("unresolved = \"\", want an override reason: a pipeline that ran at another commit is not this commit's green")
		}
		if strings.Contains(unresolved, "could not verify live CI state") {
			t.Fatalf("unresolved = %q, want the head-binding refusal, not a broken-tool message", unresolved)
		}
		if !strings.Contains(unresolved, "not for the commit being delivered") {
			t.Fatalf("unresolved = %q, want it to name the head-binding refusal", unresolved)
		}
	})

	t.Run("unrecognized job status is unresolved and named", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := fakeCIGlabOverride(t, "deadbeef", "deadbeef",
			`[{"id":1,"name":"build","status":"success"},{"id":2,"name":"legacy","status":"something_new"}]`)

		prURL := "https://gitlab.com/test/repo/-/merge_requests/42"
		sctx := newTestContext(t, nil, dir, "base", "deadbeef", config.Commands{})
		sctx.Env = env
		sctx.Repo.UpstreamURL = "https://gitlab.com/test/repo.git"
		sctx.Run.PRURL = &prURL

		unresolved, err := (&CIStep{}).VerifyApprovalOverride(sctx)
		if err != nil {
			t.Fatalf("VerifyApprovalOverride() error = %v", err)
		}
		if !strings.Contains(unresolved, "legacy") {
			t.Fatalf("unresolved = %q, want it to name the check whose status could not be bucketed", unresolved)
		}
	})

	t.Run("pipeline at the delivered commit is a clean pass", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := fakeCIGlabOverride(t, "deadbeef", "deadbeef", `[{"id":1,"name":"build","status":"success"}]`)

		prURL := "https://gitlab.com/test/repo/-/merge_requests/42"
		sctx := newTestContext(t, nil, dir, "base", "deadbeef", config.Commands{})
		sctx.Env = env
		sctx.Repo.UpstreamURL = "https://gitlab.com/test/repo.git"
		sctx.Run.PRURL = &prURL

		unresolved, err := (&CIStep{}).VerifyApprovalOverride(sctx)
		if err != nil {
			t.Fatalf("VerifyApprovalOverride() error = %v", err)
		}
		if unresolved != "" {
			t.Fatalf("unresolved = %q, want \"\" when the delivered commit's own pipeline is green", unresolved)
		}
	})
}
