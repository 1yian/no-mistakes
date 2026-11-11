//go:build !unix && !windows

package shellenv

import (
	"errors"
	"os/exec"
	"time"
)

func ConfigureShellCommand(cmd *exec.Cmd) {}

func ConfigureCooperativeShellCommand(cmd *exec.Cmd) {}

func StartShellCommand(cmd *exec.Cmd) error {
	return cmd.Start()
}

func TerminateShellCommandGroup(cmd *exec.Cmd) {}

func ShellCommandGroupActive(cmd *exec.Cmd, _ time.Duration) (bool, string, error) {
	return false, "", errors.New("process-group completion cannot be verified on this platform")
}

func detachFromTerminal(cmd *exec.Cmd) {}
