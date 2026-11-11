//go:build e2e

package e2e

import (
	"os"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/e2edaemon"
)

func TestMain(m *testing.M) {
	if inv, err := e2edaemon.Open(); err == nil {
		_ = inv.ReapAll()
	}
	code := m.Run()
	if inv, err := e2edaemon.Open(); err == nil {
		_ = inv.ReapAll()
	}
	os.Exit(code)
}
