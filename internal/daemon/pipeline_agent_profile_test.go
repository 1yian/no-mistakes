package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestNewPipelineAgent_ThreadsTheAgentProfile(t *testing.T) {
	cfg := &config.Config{
		Agent:       types.AgentAntigravity,
		AgentConfig: map[string]agentcfg.Profile{"antigravity": {Model: "some-model"}},
	}
	_, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil)
	if err == nil {
		t.Fatal("a model on a harness that cannot express one must fail setup")
	}
	if !strings.Contains(err.Error(), "cannot express model") {
		t.Fatalf("refusal should explain the reason, got: %v", err)
	}
}

func TestNewPipelineAgent_ProfileIsPerAgent(t *testing.T) {
	cfg := &config.Config{
		Agents:      []types.AgentName{types.AgentClaude, types.AgentAntigravity},
		AgentConfig: map[string]agentcfg.Profile{"claude": {Model: "sonnet", Effort: agentcfg.EffortHigh}},
	}
	ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil)
	if err != nil {
		t.Fatalf("a profile set only for claude must not reach antigravity: %v", err)
	}
	_ = ag.Close()
}

func TestNewPipelineAgent_NoProfileIsUnchanged(t *testing.T) {
	cfg := &config.Config{
		Agent:             types.AgentCodex,
		AgentArgsOverride: map[string][]string{"codex": {"-m", "gpt-5.4"}},
	}
	ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil)
	if err != nil {
		t.Fatalf("newPipelineAgent = %v", err)
	}
	_ = ag.Close()
}
