package pipeline

import (
	"context"

	"github.com/kunchenguid/no-mistakes/internal/agent"
)

type roundStampingAgent struct {
	inner agent.Agent
	round func() int
}

func (a *roundStampingAgent) Name() string { return a.inner.Name() }

func (a *roundStampingAgent) Close() error { return a.inner.Close() }

func (a *roundStampingAgent) SupportsSessionResume() bool {
	return agent.SupportsSessionResume(a.inner)
}

func (a *roundStampingAgent) SupportsSessionProvider(provider string) bool {
	return agent.SupportsSessionProvider(a.inner, provider)
}

func (a *roundStampingAgent) ReportsAgentAttempts() bool {
	return agent.ReportsAgentAttempts(a.inner)
}

func (a *roundStampingAgent) NeutralizesGateInstructions() bool {
	return agent.NeutralizesGateInstructions(a.inner)
}

func (a *roundStampingAgent) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	if opts.Round == 0 && a.round != nil {
		opts.Round = a.round()
	}
	return a.inner.Run(ctx, opts)
}
