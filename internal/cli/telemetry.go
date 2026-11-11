package cli

import (
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/telemetry"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func trackAxiSurface(command, path string, fields telemetry.Fields, fn func() error) error {
	telemetry.Pageview(path, fields)
	return trackCommand(command, fn)
}

func sanitizeAxiTelemetryAction(action string) string {
	action = strings.TrimSpace(action)
	switch types.ApprovalAction(action) {
	case types.ActionApprove, types.ActionFix, types.ActionSkip, types.ActionAbort, types.ActionCompleteCoverage:
		return action
	default:
		return "invalid"
	}
}

func trackCommand(name string, fn func() error) (err error) {
	return trackCommandStatus(name, func() (string, error) {
		if err := fn(); err != nil {
			return "", err
		}
		return "success", nil
	})
}

func trackCommandStatus(name string, fn func() (string, error)) (err error) {
	start := time.Now()
	status, err := fn()
	telemetry.Track("command", telemetry.Fields{
		"command":     name,
		"status":      commandStatus(status, err),
		"duration_ms": time.Since(start).Milliseconds(),
	})
	return err
}

func commandStatus(status string, err error) string {
	if status != "" {
		return status
	}
	if err != nil {
		return "error"
	}
	return "success"
}
