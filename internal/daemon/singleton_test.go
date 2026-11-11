package daemon

import (
	"errors"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

func TestRunWithResources_SecondDaemonForSameRootFailsWithoutStealingSocket(t *testing.T) {
	p, _ := startTestDaemon(t)

	if alive, err := daemonHealthCheck(p); err != nil || !alive {
		t.Fatalf("daemon 1 not healthy before second attempt: alive=%v err=%v", alive, err)
	}

	p2 := paths.WithRoot(p.Root())
	d2, err := db.Open(p2.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()

	err = RunWithResources(p2, d2)
	if err == nil {
		t.Fatal("expected second RunWithResources against the same root to fail")
	}
	if !errors.Is(err, ErrSingletonLockHeld) {
		t.Fatalf("expected ErrSingletonLockHeld, got %v", err)
	}

	if alive, err := daemonHealthCheck(p); err != nil || !alive {
		t.Fatalf("daemon 1 became unreachable after the second attempt: alive=%v err=%v", alive, err)
	}
}
