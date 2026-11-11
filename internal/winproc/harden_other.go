//go:build !windows

package winproc

import "os/exec"

func Harden(cmd *exec.Cmd) {}
