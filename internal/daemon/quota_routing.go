package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/quotarouting"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func quotaRoutingRecordForRun(ctx context.Context, cfg *config.Config, agents []types.AgentName) *quotarouting.Record {
	if len(agents) == 0 {
		return nil
	}
	configured := append([]types.AgentName{}, agents...)
	if !cfg.QuotaRouting.Enabled {
		return nil
	}
	candidates := make([]quotarouting.Candidate, 0, len(agents))
	for _, name := range agents {
		profile := cfg.AgentProfileFor(name)
		provider := cfg.QuotaProviderFor(name)
		if name == types.AgentPi && provider == "" {
			const reason = "pi has no quota_provider and none is built in; reordering disabled for this run"
			slog.Warn("quota routing: " + reason)
			return quotarouting.NewFailureRecord(false, configured, reason)
		}
		candidates = append(candidates, quotarouting.Candidate{
			Agent:    name,
			Model:    agentcfg.EffectiveModel(name, profile, cfg.AgentArgsFor(name)),
			Provider: provider,
		})
	}

	snapshot, err := quotarouting.ReadSnapshot(ctx, cfg.QuotaRouting.QuotaAXIPath, cfg.QuotaRouting.ReadTimeout)
	if err != nil {
		slog.Warn("quota routing: keeping the configured order", "error", err)
		return quotarouting.NewFailureRecord(true, configured, err.Error())
	}
	selection := quotarouting.Select(snapshot, candidates, cfg.QuotaRouting.ExpectedRunLength)
	if selection.Choice != configured[0] {
		slog.Info("quota routing: reordered agents by quota evidence",
			"head", string(selection.Choice), "configured_head", string(configured[0]), "order", agentNamesLog(selection.Order))
	}
	return quotarouting.NewRecord(true, configured, selection.Order, selection)
}

func agentNamesLog(names []types.AgentName) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, string(name))
	}
	return out
}

func applyQuotaRoutingRecord(record *quotarouting.Record, resolved []types.AgentName) ([]types.AgentName, bool) {
	if record == nil || !record.Enabled {
		return resolved, false
	}
	return record.OrderedAgents(resolved), true
}

func persistQuotaRoutingRecord(d *db.DB, runID string, record *quotarouting.Record) error {
	if record == nil {
		return nil
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode quota routing record: %w", err)
	}
	if err := d.SetRunQuotaRouting(runID, string(payload)); err != nil {
		return err
	}
	return nil
}

func storedQuotaRoutingRecord(payload string) (*quotarouting.Record, error) {
	record, err := quotarouting.ParseRecord(payload)
	if err != nil {
		return nil, fmt.Errorf("stored quota routing record is unusable: %w", err)
	}
	return record, nil
}
