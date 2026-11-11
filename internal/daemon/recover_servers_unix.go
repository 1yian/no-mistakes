//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

func terminateOrphanProcessGroup(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		pgid = pid
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("sigterm pgid %d: %w", pgid, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if alive, _ := processRunning(pid); !alive {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("sigkill pgid %d: %w", pgid, err)
	}
	return nil
}
