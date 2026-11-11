//go:build windows

package agent

import (
	"os/exec"

	"github.com/kunchenguid/no-mistakes/internal/winproc"
)

func configureManagedServerCmd(cmd *exec.Cmd) { winproc.Harden(cmd) }

func signalManagedProcess(cmd *exec.Cmd, force bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
