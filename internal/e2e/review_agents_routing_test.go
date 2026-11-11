//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	reviewTurnMarker   = "Review the code changes and return structured findings"
	fixTurnMarker      = "Previous review findings to address"
	rereviewTurnMarker = "Previous rounds for this step"
)

func writeReviewAgentsRoutingScenario(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "review-agents-routing-scenario.yaml")
	content := `actions:
  - match: "` + fixTurnMarker + `"
    text: "addressed the review finding"
    edits:
      - path: "feature.txt"
        old: "seed for review-agents routing\n"
        new: "fixed for review-agents routing\n"
    stage: ["feature.txt"]
    structured:
      summary: "resolve review finding"
  - match: "` + rereviewTurnMarker + `"
    text: "clean after fix"
    structured:
      findings: []
      summary: "clean after fix"
      risk_level: low
      risk_rationale: "issue resolved by the fixer"
      risk_scope: source-or-external
      reviewed_paths:
        - "feature.txt"
  - match: "` + reviewTurnMarker + `"
    text: "found one blocking issue"
    structured:
      findings:
        - id: "routing-check"
          severity: warning
          description: "mechanical issue routed through the fixer role"
          file: "feature.txt"
          action: auto-fix
          review_scope: source
      summary: "one blocking issue"
      risk_level: low
      risk_rationale: "single mechanical issue"
      risk_scope: source-or-external
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks detected in the diff"
      risk_scope: source-or-external
      tested:
        - "fakeagent: simulated test run"
      testing_summary: "simulated tests passed"
      artifacts: []
      scenarios:
        - name: "fakeagent: simulated end-to-end scenario"
          result: pass
          live: true
          evidence: "fakeagent: simulated test run"
          reason: ""
      verdict: go
      title: "feat: review-agents routing"
      body: "## Summary\nreview-agents routing e2e"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

func TestReviewAgentsRouteIndependentProfilesOnRealBinary(t *testing.T) {
	const (
		defaultModel  = "default-sonnet-e2e"
		reviewerModel = "reviewer-opus-e2e"
		fixerModel    = "fixer-flash-e2e"
	)
	extra := "agent_config:\n" +
		"  claude: {model: " + defaultModel + "}\n" +
		"review_agents:\n" +
		"  reviewer: {agent: claude, model: " + reviewerModel + "}\n" +
		"  fixer: {agent: claude, model: " + fixerModel + "}\n"

	h := NewHarness(t, SetupOpts{
		Agent:             "claude",
		Scenario:          writeReviewAgentsRoutingScenario(t),
		GlobalConfigExtra: extra,
	})

	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	const branch = "feature/review-agents-routing"
	h.CommitChange(branch, "feature.txt", "seed for review-agents routing\n", "add feature for review-agents routing")
	h.PushToGate(branch)

	// The initial review reports a blocking finding, so the run parks at the
	// review gate. Respond "fix" to drive the fixer's review-fix turn and the
	// subsequent rereview through the real pipeline.
	gated := waitForStepStatus(t, h, branch, types.StepReview, types.StepStatusAwaitingApproval, 90*time.Second)
	if gated == nil {
		t.Fatal("run did not park at the review gate")
	}
	h.RespondWithFindings(gated.ID, types.StepReview, types.ActionFix, []string{"routing-check"})

	run := h.WaitForRun(branch, 120*time.Second)
	if run.Status != types.RunCompleted {
		var runErr string
		if run.Error != nil {
			runErr = *run.Error
		}
		t.Fatalf("run status = %q, want completed (error: %s)", run.Status, runErr)
	}

	invs := h.AgentInvocations()

	initialReview := firstInvocationMatching(invs, func(prompt string) bool {
		return strings.Contains(prompt, reviewTurnMarker) &&
			!strings.Contains(prompt, rereviewTurnMarker) &&
			!strings.Contains(prompt, fixTurnMarker)
	})
	// The fix turn is the only one carrying the fix marker, and it is not a
	// review turn. Its prompt also carries round history, so the marker alone is
	// not enough to tell it apart from the rereview.
	fixTurn := firstInvocationMatching(invs, func(prompt string) bool {
		return strings.Contains(prompt, fixTurnMarker) &&
			!strings.Contains(prompt, reviewTurnMarker)
	})
	// The rereview is a fresh review turn that also carries round history but not
	// the fixer's fix marker.
	rereview := firstInvocationMatching(invs, func(prompt string) bool {
		return strings.Contains(prompt, reviewTurnMarker) &&
			strings.Contains(prompt, rereviewTurnMarker) &&
			!strings.Contains(prompt, fixTurnMarker)
	})
	defaultTurn := firstInvocationMatching(invs, func(prompt string) bool {
		return !strings.Contains(prompt, reviewTurnMarker) &&
			!strings.Contains(prompt, fixTurnMarker) &&
			!strings.Contains(prompt, rereviewTurnMarker)
	})

	if initialReview == nil || fixTurn == nil || rereview == nil || defaultTurn == nil {
		t.Fatalf("missing a role turn: initialReview=%v fixTurn=%v rereview=%v defaultTurn=%v",
			initialReview != nil, fixTurn != nil, rereview != nil, defaultTurn != nil)
	}

	t.Logf("role routing captured from the real agent subprocess argv:")
	t.Logf("  initial review -> %s", modelArg(initialReview.Args))
	t.Logf("  fixer review-fix -> %s", modelArg(fixTurn.Args))
	t.Logf("  post-fix rereview -> %s", modelArg(rereview.Args))
	t.Logf("  non-review step -> %s", modelArg(defaultTurn.Args))

	// The reviewer profile drives the initial review and the fresh rereview;
	// neither may leak the default or fixer model.
	assertModelArg(t, "initial review", initialReview.Args, reviewerModel)
	assertNotModelArg(t, "initial review", initialReview.Args, defaultModel, fixerModel)
	assertModelArg(t, "post-fix rereview", rereview.Args, reviewerModel)

	// The fixer profile drives the review-fix turn only.
	assertModelArg(t, "fixer review-fix", fixTurn.Args, fixerModel)
	assertNotModelArg(t, "fixer review-fix", fixTurn.Args, defaultModel, reviewerModel)

	// Every non-review step keeps the default profile: the review-loop scope
	// must not bleed into document/test/lint.
	assertModelArg(t, "non-review step", defaultTurn.Args, defaultModel)
	assertNotModelArg(t, "non-review step", defaultTurn.Args, reviewerModel, fixerModel)

	// The initial review and the post-fix rereview must be session-free: a fresh
	// review must never resume a prior turn's session (claude spells resume as
	// "--resume <id>"). review.conversation is off here, as it is by default, so
	// no review turn starts a reviewer session either.
	assertNoResume(t, "initial review", initialReview.Args)
	assertNoResume(t, "post-fix rereview", rereview.Args)
}

func assertNoResume(t *testing.T, role string, args []string) {
	t.Helper()
	for _, arg := range args {
		if arg == "--resume" {
			t.Fatalf("%s: expected a fresh session, but argv resumed one; args=%v", role, args)
		}
	}
}

func modelArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--model" {
			return args[i+1]
		}
	}
	return "<none>"
}

func TestPushedRepoConfigCannotSelectReviewAgent(t *testing.T) {
	const (
		operatorModel = "operator-reviewer-e2e"
		attackerModel = "attacker-reviewer-e2e"
	)
	extra := "agent_config:\n" +
		"  claude: {model: default-e2e}\n" +
		"review_agents:\n" +
		"  reviewer: {agent: claude, model: " + operatorModel + "}\n"

	// Default (all-clean) scenario: the review passes and the run completes, so
	// the initial review turn is the only review invocation.
	h := NewHarness(t, SetupOpts{Agent: "claude", GlobalConfigExtra: extra})

	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	const branch = "feature/repo-cannot-select-review-agent"
	h.CommitChange(branch, "feature.txt", "contributor change\n", "add feature")
	// The pushed branch attempts to hijack the reviewer profile. review_agents is
	// not a repo-config field, so this must be silently ignored.
	attackerRepoConfig := "ignore_patterns:\n  - '*.generated.go'\n  - 'vendor/**'\n" +
		"allow_repo_commands: true\n" +
		"review_agents:\n  reviewer: {agent: claude, model: " + attackerModel + "}\n"
	h.CommitChange(branch, ".no-mistakes.yaml", attackerRepoConfig, "attempt to hijack reviewer profile")
	h.PushToGate(branch)

	run := h.WaitForRun(branch, 120*time.Second)
	if run.Status != types.RunCompleted {
		var runErr string
		if run.Error != nil {
			runErr = *run.Error
		}
		t.Fatalf("run status = %q, want completed (error: %s)", run.Status, runErr)
	}

	review := firstInvocationMatching(h.AgentInvocations(), func(prompt string) bool {
		return strings.Contains(prompt, reviewTurnMarker)
	})
	if review == nil {
		t.Fatal("no review turn captured")
	}
	t.Logf("reviewer model with a hijack attempt on the pushed branch -> %s", modelArg(review.Args))
	assertModelArg(t, "reviewer", review.Args, operatorModel)
	assertNotModelArg(t, "reviewer", review.Args, attackerModel)
}

func firstInvocationMatching(invs []Invocation, pred func(prompt string) bool) *Invocation {
	for i := range invs {
		if pred(invs[i].Prompt) {
			return &invs[i]
		}
	}
	return nil
}

func assertModelArg(t *testing.T, role string, args []string, want string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--model" && args[i+1] == want {
			return
		}
	}
	t.Fatalf("%s: argv missing --model %q; args=%v", role, want, args)
}

func assertNotModelArg(t *testing.T, role string, args []string, unwanted ...string) {
	t.Helper()
	for _, arg := range args {
		for _, bad := range unwanted {
			if arg == bad {
				t.Fatalf("%s: argv leaked foreign model %q; args=%v", role, bad, args)
			}
		}
	}
}
