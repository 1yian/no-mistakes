package agent

import (
	"context"
	"errors"
	"time"
)

type RoundedRole struct {
	Agent    Agent
	Late     Agent
	LateFrom int
}

type ReviewRoles struct{ Reviewer, Fixer RoundedRole }

func (r RoundedRole) agents() []Agent {
	var out []Agent
	if r.Agent != nil {
		out = append(out, r.Agent)
	}
	if r.Late != nil {
		out = append(out, r.Late)
	}
	return out
}

func (r RoundedRole) pick(round int, fallback Agent) Agent {
	if r.Late != nil && r.LateFrom > 0 && round >= r.LateFrom {
		return r.Late
	}
	if r.Agent != nil {
		return r.Agent
	}
	return fallback
}

func WithReviewAgents(primary, reviewer, fixer Agent) Agent {
	return WithReviewRoles(primary, ReviewRoles{
		Reviewer: RoundedRole{Agent: reviewer},
		Fixer:    RoundedRole{Agent: fixer},
	})
}

func WithReviewRoles(primary Agent, roles ReviewRoles) Agent {
	if len(roles.Reviewer.agents()) == 0 && len(roles.Fixer.agents()) == 0 {
		return primary
	}
	return &reviewAgents{primary: primary, reviewer: roles.Reviewer, fixer: roles.Fixer}
}

type reviewAgents struct {
	primary  Agent
	reviewer RoundedRole
	fixer    RoundedRole
}

func (a *reviewAgents) Name() string { return a.primary.Name() }

func (a *reviewAgents) fixAgents() []Agent {
	candidates := a.fixer.agents()
	if len(candidates) == 0 {
		return []Agent{a.primary}
	}
	if a.fixer.Agent == nil {
		return append([]Agent{a.primary}, candidates...)
	}
	return candidates
}

func (a *reviewAgents) SupportsSessionResume() bool {
	for _, current := range a.fixAgents() {
		if !SupportsSessionResume(current) {
			return false
		}
	}
	return true
}

func (a *reviewAgents) SupportsSessionProvider(provider string) bool {
	for _, current := range a.fixAgents() {
		if !SupportsSessionProvider(current, provider) {
			return false
		}
	}
	return true
}

func (a *reviewAgents) ReportsAgentAttempts() bool { return true }

func (a *reviewAgents) all() []Agent {
	return append([]Agent{a.primary}, append(a.reviewer.agents(), a.fixer.agents()...)...)
}

func (a *reviewAgents) NeutralizesGateInstructions() bool {
	for _, current := range a.all() {
		if !NeutralizesGateInstructions(current) {
			return false
		}
	}
	return true
}

func (a *reviewAgents) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	selected := a.primary
	switch opts.Purpose {
	case "review", "review-coverage":
		// "review-coverage" is the review step's focused coverage completion
		// turn: it is a review turn (same schema, same pass, no code change in
		// between) and must route through the same reviewer chain.
		selected = a.reviewer.pick(opts.Round, a.primary)
		opts.Session = nil
	case "review-fix":
		selected = a.fixer.pick(opts.Round, a.primary)
	}
	started := time.Now()
	result, err := selected.Run(ctx, opts)
	if !ReportsAgentAttempts(selected) {
		emitAgentAttempt(opts, selected.Name(), result, err, started, time.Now())
	}
	if result != nil && result.Provider == "" {
		result.Provider = selected.Name()
	}
	return result, err
}

func (a *reviewAgents) Close() error {
	var errs []error
	for _, current := range a.all() {
		errs = append(errs, current.Close())
	}
	return errors.Join(errs...)
}
