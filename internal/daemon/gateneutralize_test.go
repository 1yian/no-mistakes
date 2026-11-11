package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func fakeLookPath(bin string) (string, error) { return "/fake/bin/" + bin, nil }

func TestNewPipelineAgent_OptOut_AdmitsVerifiedHarness(t *testing.T) {
	for _, name := range []types.AgentName{types.AgentCodex, types.AgentClaude, types.AgentPi} {
		cfg := &config.Config{Agent: name, DisableProjectSettings: true}
		ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil)
		if err != nil {
			t.Fatalf("%s must pass under opt-out, got: %v", name, err)
		}
		if !agent.NeutralizesGateInstructions(ag) {
			t.Errorf("%s pipeline agent must report neutralized under opt-out", name)
		}
		_ = ag.Close()
	}
}

func TestNewPipelineAgent_OptOut_AdmitsOMPACPTarget(t *testing.T) {
	cfg := &config.Config{Agent: types.AgentName("acp:omp"), DisableProjectSettings: true}
	ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil)
	if err != nil {
		t.Fatalf("acp:omp must pass under opt-out, got: %v", err)
	}
	if !agent.NeutralizesGateInstructions(ag) {
		t.Error("acp:omp pipeline agent must report neutralized under opt-out")
	}
	_ = ag.Close()

	overridden := &config.Config{
		Agent:                  types.AgentName("acp:omp"),
		DisableProjectSettings: true,
		ACPRegistryOverrides:   map[string]string{"omp": "omp acp --profile custom"},
	}
	if _, _, err := newPipelineAgent(context.Background(), overridden, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil); err == nil {
		t.Fatal("acp:omp with an operator raw-command override must be refused under opt-out")
	} else if !strings.Contains(err.Error(), "does not neutralize") {
		t.Errorf("refusal should explain the reason, got: %v", err)
	}
}

func TestNewPipelineAgent_OptOut_RefusesUnverifiedHarness(t *testing.T) {
	for _, name := range []types.AgentName{types.AgentGrok, types.AgentOpenCode, types.AgentCopilot, types.AgentCursor, types.AgentDevin, "acp:devin"} {
		cfg := &config.Config{Agent: name, DisableProjectSettings: true}
		if _, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil); err == nil {
			t.Fatalf("%s must be refused under opt-out", name)
		} else if !strings.Contains(err.Error(), "does not neutralize") || !strings.Contains(err.Error(), string(name)) {
			t.Errorf("%s refusal should name the harness and reason, got: %v", name, err)
		}
	}
}

func TestNewPipelineAgent_NoOptOut_AdmitsEveryHarness(t *testing.T) {
	// rovodev is omitted: its resolution runs a real version probe that a fake
	// binary path cannot satisfy. opencode/pi/copilot already prove that an
	// unverified adapter is admitted when the repo did not opt out.
	for _, name := range []types.AgentName{types.AgentCodex, types.AgentClaude, types.AgentGrok, types.AgentOpenCode, types.AgentPi, types.AgentCopilot} {
		cfg := &config.Config{Agent: name} // DisableProjectSettings defaults false
		ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil)
		if err != nil {
			t.Fatalf("%s must be admitted when the repo did not opt out, got: %v", name, err)
		}
		_ = ag.Close()
	}
}

func TestNewPipelineAgent_OptOut_RefusesDefeatedKnob(t *testing.T) {
	cfg := &config.Config{
		Agent:                  types.AgentCodex,
		DisableProjectSettings: true,
		AgentArgsOverride:      map[string][]string{"codex": {"-c", "project_doc_max_bytes=8192"}},
	}
	if _, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil); err == nil {
		t.Fatal("codex with its knob overridden must be refused under opt-out")
	} else if !strings.Contains(err.Error(), "does not neutralize") {
		t.Errorf("refusal should explain the reason, got: %v", err)
	}
}

func TestNewPipelineAgent_OptOut_FallbackRefusesAnyUnverifiedMember(t *testing.T) {
	cfg := &config.Config{Agents: []types.AgentName{types.AgentCodex, types.AgentOpenCode}, DisableProjectSettings: true}
	if _, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil); err == nil {
		t.Fatal("a fallback list containing an unverified harness must be refused under opt-out")
	}
	cfg = &config.Config{Agents: []types.AgentName{types.AgentCodex, types.AgentClaude}, DisableProjectSettings: true}
	if ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil); err != nil {
		t.Fatalf("a fallback list of only verified harnesses must pass under opt-out, got: %v", err)
	} else {
		_ = ag.Close()
	}
}
