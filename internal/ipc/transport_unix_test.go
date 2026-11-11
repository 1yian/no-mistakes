//go:build !windows

package ipc_test

import (
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
)

func TestServe_SecondListenerForLiveSocketDoesNotStealIt(t *testing.T) {
	sock := socketPath(t)
	srv1 := startServer(t, sock)
	_ = srv1

	if c, err := ipc.Dial(sock); err != nil {
		t.Fatalf("first server not reachable before second Serve attempt: %v", err)
	} else {
		c.Close()
	}

	srv2 := ipc.NewServer()
	err := srv2.Serve(sock)
	if err == nil {
		t.Fatal("expected second Serve on a live socket path to fail")
	}

	if c, err := ipc.Dial(sock); err != nil {
		t.Fatalf("first server became unreachable after second Serve attempt: %v", err)
	} else {
		c.Close()
	}
}
