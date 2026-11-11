package cli

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type passedDeadlineCtx struct{ context.Context }

func (passedDeadlineCtx) Deadline() (time.Time, bool) { return time.Now().Add(-time.Second), true }

func TestRunStateReadAfterItsDeadlineIsAnError(t *testing.T) {
	socketPath := filepath.Join(makeSocketSafeTempDir(t), "reconcile.sock")
	srv := ipc.NewServer()
	srv.Handle(ipc.MethodGetRun, func(context.Context, json.RawMessage) (interface{}, error) {
		return &ipc.GetRunResult{Run: &ipc.RunInfo{ID: "run-1", Status: types.RunRunning}}, nil
	})
	startIPCServer(t, srv, socketPath)

	run, err := (&ipcRunStateSource{socketPath: socketPath}).Reconcile(passedDeadlineCtx{context.Background()}, "run-1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Reconcile past its deadline = (%v, %v), want context.DeadlineExceeded", run, err)
	}
}

func TestAxiWaitElapsedBeforeTheDriveTimerFires(t *testing.T) {
	if !isAxiWaitElapsed(context.Background(), passedDeadlineCtx{context.Background()}, context.DeadlineExceeded) {
		t.Fatal("a passed drive deadline whose timer has not fired was not classified as an elapsed wait")
	}
	if isAxiWaitElapsed(context.Background(), context.Background(), context.DeadlineExceeded) {
		t.Fatal("a deadline error with no drive deadline was classified as an elapsed wait")
	}
}

func TestQuestionParkGraceIsNotCreditedWithTheWaitsOwnExpiry(t *testing.T) {
	elapsedWait := passedDeadlineCtx{context.Background()}
	if graceExpired(elapsedWait, context.DeadlineExceeded) {
		t.Fatal("an elapsed --wait whose timer has not fired was credited to the question-park grace")
	}
	if !isAxiWaitElapsed(context.Background(), elapsedWait, context.DeadlineExceeded) {
		t.Fatal("the same deadline was not classified as an elapsed wait")
	}
	live, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if !graceExpired(live, context.DeadlineExceeded) {
		t.Fatal("the grace expiring under a live --wait was not credited to the grace")
	}
	if graceExpired(live, context.Canceled) {
		t.Fatal("a cancelled wait was read as the grace expiring")
	}
}
