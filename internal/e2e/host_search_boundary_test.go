//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

var hostSearchBoundaryClauses = []string{
	"Do not search the host filesystem",
	"Never run a filesystem-wide search such as `find /` or `mdfind /`",
	"never hunt the machine for an installed tool",
	"does not make a whole-root search bounded",
	"Bounded searches inside the worktree",
	"external evidence path a prompt explicitly names",
	"repository-local path you were given remain fine",
	"not on PATH and no repository-local path is supplied",
	"report the missing tool and the work it blocked in your normal result",
}

const testOnlyUntestedFallback = `report the affected scenario as "untested"`

func TestHostSearchBoundaryReachesEveryPipelinePrompt(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: cleanReviewScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	const branch = "feature/host-search-boundary"
	h.CommitChange(branch, "hello.txt", "hello boundary\n", "add a file to validate")
	h.PushToGate(branch)

	run := h.WaitForRun(branch, 90*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want completed (error=%v)", run.Status, run.Error)
	}

	invocations := h.AgentInvocations()
	if len(invocations) == 0 {
		t.Fatal("no agent invocations recorded; the pipeline never launched an agent")
	}

	// The two surfaces the incident and the fix care about: the Test evidence
	// turn that froze, and a non-Test role to prove the shared rule is role
	// neutral rather than Test-only.
	testPrompt := findInvocationContaining(invocations, "You are validating a code change by driving the product itself")
	if testPrompt == "" {
		t.Fatalf("the test step never ran, so this test proves nothing; invocations:\n%s", dumpPrompts(invocations))
	}
	reviewPrompt := findInvocationContaining(invocations, "Review the code changes")
	if reviewPrompt == "" {
		t.Fatalf("no review prompt observed; invocations:\n%s", dumpPrompts(invocations))
	}

	// Every prompting surface the daemon steers must carry the shared rule.
	steered := 0
	for _, inv := range invocations {
		if !strings.HasPrefix(inv.Prompt, "Workspace boundary (important)") {
			continue
		}
		steered++
		for _, want := range hostSearchBoundaryClauses {
			if !strings.Contains(inv.Prompt, want) {
				t.Errorf("a steered pipeline prompt is missing host-search clause %q:\n%s", want, truncate(inv.Prompt, 4000))
			}
		}
	}
	if steered == 0 {
		t.Fatal("no steered prompt observed; the daemon is not wrapping agents with the workspace-boundary preamble")
	}

	// The Test step must add its own scenario-shaped fallback: the whole point
	// of the fix is that a missing tool triggers workspace-local setup before
	// an honest "untested" scenario, never a 30-minute whole-host search.
	if !strings.Contains(testPrompt, testOnlyUntestedFallback) {
		t.Errorf("test prompt is missing the scenario untested fallback %q:\n%s", testOnlyUntestedFallback, truncate(testPrompt, 4000))
	}
	for _, want := range []string{
		"do not search the host machine for it or install it system-wide or globally",
		"obtain, install, or build the tool inside the disposable workspace and use it there",
		`Only if no workspace-local route can drive the scenario live, report the affected scenario as "untested"`,
	} {
		if !strings.Contains(testPrompt, want) {
			t.Errorf("test prompt missing local-setup fallback %q:\n%s", want, truncate(testPrompt, 4000))
		}
	}

	// Role neutrality: only Test may set up disposable tools and has a
	// scenario-shaped untested result; neither belongs in Review.
	if strings.Contains(reviewPrompt, "obtain, install, or build the tool inside the disposable workspace") {
		t.Errorf("review prompt leaked Test-only tool setup permission:\n%s", truncate(reviewPrompt, 4000))
	}
	if strings.Contains(reviewPrompt, testOnlyUntestedFallback) {
		t.Errorf("review prompt leaked the Test-only scenario untested fallback:\n%s", truncate(reviewPrompt, 4000))
	}

	// The boundary narrows an unbounded host search; it must not ban reading
	// outside the worktree, which every step relies on for tooling and git.
	for name, prompt := range map[string]string{"test": testPrompt, "review": reviewPrompt} {
		if !strings.Contains(prompt, "You may read files outside the worktree and run read-only commands") {
			t.Errorf("%s prompt dropped the out-of-worktree read allowance:\n%s", name, truncate(prompt, 4000))
		}
	}

	// Reviewer-visible evidence: the exact boundary lines the daemon delivered
	// to the agent process in this run, not a summary of them.
	t.Logf("test prompt host-search boundary, as delivered:\n%s", hostSearchBoundaryLines(testPrompt))
	t.Logf("review prompt host-search boundary, as delivered:\n%s", hostSearchBoundaryLines(reviewPrompt))
}

func hostSearchBoundaryLines(prompt string) string {
	var sb strings.Builder
	for _, line := range strings.Split(prompt, "\n") {
		switch {
		case strings.Contains(line, "Do not search the host filesystem"),
			strings.Contains(line, "not on PATH and no repository-local path is supplied"),
			strings.Contains(line, "not on PATH and has no repository-local path"):
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
