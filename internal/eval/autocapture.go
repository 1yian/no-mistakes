package eval

import (
	"context"
	"errors"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

type AutoCaptureResult struct {
	Captured int
	Pruned   int
	Skipped  bool
	Reason   string
}

func AutoCapture(ctx context.Context, p *paths.Paths, database *db.DB, runID string, maxCases int) (AutoCaptureResult, error) {
	if p == nil || database == nil {
		return AutoCaptureResult{}, fmt.Errorf("eval auto-capture requires paths and a database")
	}
	store, err := Open(p.EvalDir())
	if err != nil {
		return AutoCaptureResult{}, err
	}
	defer store.Close()

	cases, err := Capture(ctx, store, p, database, runID)
	if err != nil {
		if errors.Is(err, ErrNoCapturableReview) {
			return AutoCaptureResult{Skipped: true, Reason: err.Error()}, nil
		}
		return AutoCaptureResult{}, err
	}
	result := AutoCaptureResult{Captured: len(cases)}
	pruned, err := store.Prune(ctx, maxCases)
	result.Pruned = pruned
	if err != nil {
		return result, err
	}
	return result, nil
}
