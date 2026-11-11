package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

var ErrAgentInvocationFailed = errors.New("agent invocation failed")

type agentInvocationFailure struct{ err error }

func (f *agentInvocationFailure) Error() string { return f.err.Error() }

func (f *agentInvocationFailure) Unwrap() error { return f.err }

func (f *agentInvocationFailure) Is(target error) bool { return target == ErrAgentInvocationFailed }

type invocationFailureAgent struct {
	inner agent.Agent
}

func (a *invocationFailureAgent) Name() string { return a.inner.Name() }

func (a *invocationFailureAgent) Close() error { return a.inner.Close() }

func (a *invocationFailureAgent) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	result, err := a.inner.Run(ctx, opts)
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return result, err
	}
	return result, &agentInvocationFailure{err: err}
}

func (a *invocationFailureAgent) SupportsSessionResume() bool {
	return agent.SupportsSessionResume(a.inner)
}

func (a *invocationFailureAgent) SupportsSessionProvider(provider string) bool {
	return agent.SupportsSessionProvider(a.inner, provider)
}

func (a *invocationFailureAgent) ReportsAgentAttempts() bool {
	return agent.ReportsAgentAttempts(a.inner)
}

func (a *invocationFailureAgent) NeutralizesGateInstructions() bool {
	return agent.NeutralizesGateInstructions(a.inner)
}

type RetryCheckpoint struct {
	Index int
	Steps []*db.StepResult
	Round int
}

func (c *RetryCheckpoint) Failed() *db.StepResult { return c.Steps[c.Index] }

const (
	RetryProofCheckpoint = "durable checkpoint"
	RetryProofCause      = "eligible failure"
	RetryProofEvidence   = "reusable evidence"
)

type RetryRefusal struct {
	Proof  string
	Reason string
}

func (r *RetryRefusal) Error() string { return r.Reason }

func refuseCheckpoint(proof, format string, args ...any) error {
	return &RetryRefusal{Proof: proof, Reason: fmt.Sprintf(format, args...)}
}

func PlanRetryCheckpoint(database *db.DB, run *db.Run, steps []Step) (*RetryCheckpoint, error) {
	rows, err := database.GetStepsByRun(run.ID)
	if err != nil {
		return nil, fmt.Errorf("read the run's step records: %w", err)
	}
	if len(rows) != len(steps) {
		return nil, refuseCheckpoint(RetryProofCheckpoint, "the run recorded %d steps but its pipeline has %d", len(rows), len(steps))
	}
	failed := -1
	for index, row := range rows {
		if row.StepName != steps[index].Name() {
			return nil, refuseCheckpoint(RetryProofCheckpoint, "the run's step %d is %q, but the pipeline's is %q", index, row.StepName, steps[index].Name())
		}
		switch {
		case row.Status == types.StepStatusFailed:
			if failed >= 0 {
				return nil, refuseCheckpoint(RetryProofCheckpoint, "steps %s and %s both failed", rows[failed].StepName, row.StepName)
			}
			failed = index
		case failed < 0 && row.Status != types.StepStatusCompleted && row.Status != types.StepStatusSkipped:
			return nil, refuseCheckpoint(RetryProofCheckpoint, "step %s is %s before the failed step", row.StepName, row.Status)
		case failed >= 0 && row.Status != types.StepStatusPending && row.Status != types.StepStatusSkipped:
			return nil, refuseCheckpoint(RetryProofCheckpoint, "step %s is %s after the failed step", row.StepName, row.Status)
		}
	}
	if failed < 0 {
		return nil, refuseCheckpoint(RetryProofCheckpoint, "no step failed")
	}
	row := rows[failed]
	if row.FailureCause == nil || *row.FailureCause != db.StepFailureCauseAgentInvocation {
		return nil, refuseCheckpoint(RetryProofCause, "step %s did not fail on an agent invocation error (provider, transport, or agent process), so the failure may be about the change itself", row.StepName)
	}
	for _, carried := range rows[:failed] {
		if carried.StepName.Order() >= types.StepTest.Order() {
			return nil, refuseCheckpoint(RetryProofEvidence, "step %s failed after %s, and a continuation cannot carry %s or any later step because their evidence is not kept after a run ends", row.StepName, carried.StepName, types.StepTest)
		}
	}
	rounds, err := database.GetRoundsByStep(row.ID)
	if err != nil {
		return nil, fmt.Errorf("read step %s rounds: %w", row.StepName, err)
	}
	return &RetryCheckpoint{Index: failed, Steps: rows, Round: len(rounds) + 1}, nil
}
