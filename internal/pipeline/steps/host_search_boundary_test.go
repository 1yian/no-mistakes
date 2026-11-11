package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
)

func assertRoleNeutralHostSearchBoundary(t *testing.T, prompt string) {
	t.Helper()
	normalized := strings.Join(strings.Fields(prompt), " ")
	for _, want := range []string{
		// Whole-root searches are disallowed by name, including the observed shape.
		"Do not search the host filesystem",
		"Never run a filesystem-wide search such as `find /` or `mdfind /`",
		"never hunt the machine for an installed tool",
		"does not make a whole-root search bounded",
		// Bounded reads that must survive the new rule.
		"Bounded searches inside the worktree",
		"external evidence path a prompt explicitly names",
		"repository-local path you were given remain fine",
		// Role-neutral missing-tool fallback.
		"not on PATH and no repository-local path is supplied",
		"report the missing tool and the work it blocked in your normal result",
		"with the concrete reason, and stop searching there",
		"including any edits you were asked to make",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("emitted pipeline prompt missing role-neutral host-search boundary %q:\n%s", want, prompt)
		}
	}
}

func assertTestScenarioUntestedFallback(t *testing.T, prompt string) {
	t.Helper()
	normalized := strings.Join(strings.Fields(prompt), " ")
	for _, want := range []string{
		"not on PATH and has no repository-local path",
		"do not search the host machine for it or install it system-wide or globally",
		"Try another available route or obtain, install, or build the tool inside the disposable workspace and use it there",
		`Only if no workspace-local route can drive the scenario live, report the affected scenario as "untested"`,
		"with what you tried and why it could not work",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("emitted test prompt missing scenario untested fallback %q:\n%s", want, prompt)
		}
	}
}

func TestReviewPromptCarriesBoundedHostSearchBoundary(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	findingsJSON, _ := json.Marshal(cleanReviewFindings())
	inner := &mockAgent{
		name: "review",
		runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: findingsJSON}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, inner, dir, baseSHA, headSHA, config.Commands{})
	// Same wiring as internal/daemon/manager.go: the run's agent is wrapped so
	// the workspace-boundary preamble leads every prompt it sends.
	sctx.Agent = agent.WithSteering(inner, sctx.EvidenceDir)

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(inner.calls) != 1 {
		t.Fatalf("expected 1 review call, got %d", len(inner.calls))
	}
	prompt := inner.calls[0].Prompt
	if !strings.HasPrefix(prompt, agent.WorktreeSteering(sctx.EvidenceDir)) {
		t.Fatalf("review prompt does not lead with the workspace-boundary preamble from agent.WorktreeSteering:\n%s", prompt)
	}
	assertRoleNeutralHostSearchBoundary(t, prompt)
	// Review has no scenarios or disposable-tool cleanup duty. The Test-only
	// permission to install a tool must not leak into its delivered prompt.
	if strings.Contains(prompt, "obtain, install, or build the tool inside the disposable workspace") {
		t.Errorf("review prompt carries the Test-only tool setup permission:\n%s", prompt)
	}
	if strings.Contains(prompt, `report the affected scenario as "untested"`) {
		t.Errorf("review prompt carries the Test-only scenario untested fallback:\n%s", prompt)
	}
}

func TestTestPromptCarriesBoundedHostSearchBoundary(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	inner := &mockAgent{
		name: "test",
		runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: json.RawMessage(passingScenarioFindingsJSON)}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, inner, dir, baseSHA, headSHA, config.Commands{})
	sctx.Agent = agent.WithSteering(inner, sctx.EvidenceDir)

	if _, err := (&TestStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(inner.calls) != 1 {
		t.Fatalf("expected 1 evidence call, got %d", len(inner.calls))
	}
	prompt := inner.calls[0].Prompt
	if !strings.HasPrefix(prompt, agent.WorktreeSteering(sctx.EvidenceDir)) {
		t.Fatalf("test prompt does not lead with the workspace-boundary preamble from agent.WorktreeSteering:\n%s", prompt)
	}
	assertRoleNeutralHostSearchBoundary(t, prompt)
	assertTestScenarioUntestedFallback(t, prompt)
}
