package pipeline

import (
	"context"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func (e *Executor) SupportsGuardedCIRepair() bool { return e.ciRepairCheckpoint != nil }

func (e *Executor) WithCIRepairGateCheckpoint(ctx context.Context, fn func(*StepContext) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.SupportsGuardedCIRepair() || e.crashRetryRecovered || !e.waiting || e.waitingStep != types.StepCI || e.waitingContext == nil || e.waitingContext.StepResultID != e.waitingStepResultID || e.waitingRoundID == "" {
		return fmt.Errorf("guarded CI repair requires a continuously owned current CI approval gate; adoption is unsupported")
	}
	callCtx, cancel := context.WithCancel(e.waitingContext.Ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	checkpoint := *e.waitingContext
	checkpoint.Ctx = callCtx
	return fn(&checkpoint)
}
