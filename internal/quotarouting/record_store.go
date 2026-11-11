package quotarouting

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

type SwitchStore interface {
	GetRunQuotaRouting(id string) (string, error)
	SetRunQuotaRouting(id, payload string) error
}

func RecordSwitch(store SwitchStore, runID string, event SwitchEvent) {
	payload, err := store.GetRunQuotaRouting(runID)
	if err != nil {
		slog.Warn("failed to read quota routing record for a quota switch", "run", runID, "error", err)
		return
	}
	record, err := ParseRecord(payload)
	if err != nil {
		slog.Warn("run carries an unreadable quota routing record; quota switch not recorded", "run", runID, "error", err)
		return
	}
	if record == nil {
		// A switch can only fire when the chain was built with the quota
		// switch active, which only happens on an enabled record; a missing
		// record means the run predates the feature or its write was lost.
		slog.Warn("quota switch fired without a quota routing record", "run", runID)
		return
	}
	updated := record.AppendSwitch(types.AgentName(event.From), types.AgentName(event.To), event.Reason, event.At)
	encoded, err := json.Marshal(updated)
	if err != nil {
		slog.Warn("failed to encode the amended quota routing record", "run", runID, "error", err)
		return
	}
	if err := store.SetRunQuotaRouting(runID, string(encoded)); err != nil {
		slog.Warn("failed to record quota switch", "run", runID, "error", err)
	}
}

type SwitchEvent struct {
	From   string
	To     string
	Reason string
	At     time.Time
}
