package daemon

import (
	"github.com/kunchenguid/no-mistakes/internal/types"
	"log/slog"
)

func (m *RunManager) retainCIRepair(runID string) bool {
	a, err := m.db.CIRepairObligation(runID)
	if err != nil {
		slog.Error("cannot inspect guarded repair custody; retaining artifacts", "run_id", runID, "error", err)
		return true
	}
	if a == nil {
		return false
	}
	run, err := m.db.GetRun(runID)
	if err != nil {
		return true
	}
	if a.Phase == "fulfilled" && run != nil && run.HeadSHA == a.CertifiedHead && (run.Status == types.RunChecksPassed || run.Status == types.RunHandedOff || run.Status == types.RunCompleted || run.Status == types.RunCancelled) {
		return false
	}
	reason := "guarded CI repair held unvalidated: monitor ended; adoption is unsupported"
	if run != nil && run.Error != nil {
		reason = *run.Error
	}
	if err := m.db.HoldCIRepair(runID, reason); err != nil {
		slog.Error("cannot persist guarded repair hold; retaining artifacts", "run_id", runID, "error", err)
	}
	return true
}
