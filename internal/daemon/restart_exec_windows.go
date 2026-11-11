//go:build windows

package daemon

import (
	"fmt"
	"os/exec"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

func execRestartImage(image restartImage) error {
	p, err := paths.New()
	if err != nil {
		return fmt.Errorf("restart for update: resolve paths: %w", err)
	}
	var args []string
	if len(image.args) > 1 {
		args = image.args[1:]
	}
	cmd := exec.Command(image.executable, args...)
	cmd.Env = image.env
	if err := launchDetachedDaemon(p, cmd); err != nil {
		return fmt.Errorf("restart for update: start %s: %w", image.executable, err)
	}
	return nil
}
