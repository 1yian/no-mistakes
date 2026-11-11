package db

import (
	"encoding/json"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	RoundSelectionSourceUser         = "user"
	RoundSelectionSourceAutoFix      = "auto_fix"
	RoundSelectionSourceUserDeclined = "user_declined"
)

const DeclinedSelectionJSON = "[]"

type StepRound struct {
	ID                 string
	StepResultID       string
	Round              int
	Trigger            string
	FindingsJSON       *string // nullable - findings produced by this round
	ReviewedHeadSHA    *string // non-authoritative commit candidate captured by a review round
	StartingHeadSHA    *string
	TrustedConfigSHA   *string
	GlobalConfigYAML   []byte
	RepoConfigYAML     []byte
	UserFindingsJSON   *string
	SelectedFindingIDs *string
	ResponseOrigin     types.ResponseOrigin
	SelectionSource    *string
	FixSummary         *string
	RepairPublished    bool
	DurationMS         int64
	CreatedAt          int64
}

const roundReadColumns = "id, step_result_id, round, trigger_type, findings_json, reviewed_head_sha, starting_head_sha, trusted_config_sha, global_config_yaml, repo_config_yaml, user_findings_json, selected_finding_ids, selection_source, fix_summary, repair_published, duration_ms, created_at, response_origin"

func roundScanTargets(r *StepRound) []any {
	return []any{&r.ID, &r.StepResultID, &r.Round, &r.Trigger, &r.FindingsJSON, &r.ReviewedHeadSHA, &r.StartingHeadSHA, &r.TrustedConfigSHA, &r.GlobalConfigYAML, &r.RepoConfigYAML, &r.UserFindingsJSON, &r.SelectedFindingIDs, &r.SelectionSource, &r.FixSummary, &r.RepairPublished, &r.DurationMS, &r.CreatedAt, &r.ResponseOrigin}
}

type StepRoundStats struct {
	TotalRounds        int
	FixRounds          int
	LatestRound        int
	LatestTrigger      string
	LatestSelection    string
	LatestRoundAt      int64
	LatestFixRound     int
	LatestFixRoundAt   int64
	SelectedForFix     bool
	AutoSelectedForFix bool
	PendingFixSource   string
}

func (r *StepRound) IsFixRound() bool {
	return r.Trigger == "auto_fix" || r.Trigger == "user_fix"
}

func (d *DB) StepFixSummaries(stepResultID string) ([]string, error) {
	rounds, err := d.GetRoundsByStep(stepResultID)
	if err != nil {
		return nil, err
	}
	var summaries []string
	for _, r := range rounds {
		if !r.IsFixRound() {
			continue
		}
		summary := ""
		if r.FixSummary != nil {
			summary = *r.FixSummary
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (d *DB) StepRoundStats(stepResultID string) (StepRoundStats, error) {
	rounds, err := d.GetRoundsByStep(stepResultID)
	if err != nil {
		return StepRoundStats{}, err
	}
	var stats StepRoundStats
	latestSelectedRound := 0
	latestSelectedSource := ""
	for _, r := range rounds {
		stats.TotalRounds++
		stats.LatestRound = r.Round
		stats.LatestTrigger = r.Trigger
		stats.LatestRoundAt = r.CreatedAt
		if r.SelectionSource != nil {
			stats.LatestSelection = *r.SelectionSource
		}
		if hasSelectedFinding(r.SelectedFindingIDs) {
			stats.SelectedForFix = true
			stats.AutoSelectedForFix = r.SelectionSource != nil && *r.SelectionSource == RoundSelectionSourceAutoFix
			latestSelectedRound = r.Round
			latestSelectedSource = stats.LatestSelection
		}
		if r.IsFixRound() {
			stats.FixRounds++
			stats.LatestFixRound = stats.FixRounds
			stats.LatestFixRoundAt = r.CreatedAt
		}
	}
	if latestSelectedRound == stats.LatestRound {
		stats.PendingFixSource = latestSelectedSource
	}
	return stats, nil
}

func hasSelectedFinding(raw *string) bool {
	if raw == nil {
		return false
	}
	var ids []string
	if json.Unmarshal([]byte(*raw), &ids) != nil {
		return false
	}
	for _, id := range ids {
		if id != "" {
			return true
		}
	}
	return false
}

func (d *DB) InsertStepRound(stepResultID string, round int, trigger string, findingsJSON *string, fixSummary *string, durationMS int64) (*StepRound, error) {
	return d.InsertStepRoundWithRepair(stepResultID, round, trigger, findingsJSON, fixSummary, false, durationMS)
}

func (d *DB) InsertStepRoundWithRepair(stepResultID string, round int, trigger string, findingsJSON *string, fixSummary *string, repairPublished bool, durationMS int64) (*StepRound, error) {
	return d.insertStepRound(stepResultID, round, trigger, findingsJSON, fixSummary, nil, nil, nil, nil, nil, repairPublished, durationMS)
}

func (d *DB) InsertReviewStepRound(stepResultID string, round int, trigger string, findingsJSON *string, fixSummary *string, reviewedHeadSHA string, durationMS int64) (*StepRound, error) {
	return d.InsertReviewStepRoundWithProvenance(stepResultID, round, trigger, findingsJSON, fixSummary, reviewedHeadSHA, "", "", nil, nil, durationMS)
}

func (d *DB) InsertReviewStepRoundWithProvenance(stepResultID string, round int, trigger string, findingsJSON *string, fixSummary *string, reviewedHeadSHA, startingHeadSHA, trustedConfigSHA string, globalConfigYAML, repoConfigYAML []byte, durationMS int64) (*StepRound, error) {
	var reviewed, starting, trusted *string
	if reviewedHeadSHA != "" {
		reviewed = &reviewedHeadSHA
	}
	if startingHeadSHA != "" {
		starting = &startingHeadSHA
	}
	if trustedConfigSHA != "" {
		trusted = &trustedConfigSHA
	}
	return d.insertStepRound(stepResultID, round, trigger, findingsJSON, fixSummary, reviewed, starting, trusted, globalConfigYAML, repoConfigYAML, false, durationMS)
}

func (d *DB) insertStepRound(stepResultID string, round int, trigger string, findingsJSON *string, fixSummary, reviewedHeadSHA, startingHeadSHA, trustedConfigSHA *string, globalConfigYAML, repoConfigYAML []byte, repairPublished bool, durationMS int64) (*StepRound, error) {
	r := &StepRound{
		ID:               newID(),
		StepResultID:     stepResultID,
		Round:            round,
		Trigger:          trigger,
		FindingsJSON:     findingsJSON,
		ReviewedHeadSHA:  reviewedHeadSHA,
		StartingHeadSHA:  startingHeadSHA,
		TrustedConfigSHA: trustedConfigSHA,
		GlobalConfigYAML: append([]byte(nil), globalConfigYAML...),
		RepoConfigYAML:   append([]byte(nil), repoConfigYAML...),
		FixSummary:       fixSummary,
		RepairPublished:  repairPublished,
		DurationMS:       durationMS,
		CreatedAt:        now(),
		ResponseOrigin:   types.ResponseOriginUnknown,
	}
	_, err := d.sql.Exec(
		`INSERT INTO step_rounds (id, step_result_id, round, trigger_type, findings_json, reviewed_head_sha, starting_head_sha, trusted_config_sha, global_config_yaml, repo_config_yaml, user_findings_json, selected_finding_ids, selection_source, fix_summary, repair_published, duration_ms, created_at, response_origin) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.StepResultID, r.Round, r.Trigger, r.FindingsJSON, r.ReviewedHeadSHA, r.StartingHeadSHA, r.TrustedConfigSHA, r.GlobalConfigYAML, r.RepoConfigYAML, r.UserFindingsJSON, r.SelectedFindingIDs, r.SelectionSource, r.FixSummary, r.RepairPublished, r.DurationMS, r.CreatedAt, r.ResponseOrigin,
	)
	if err != nil {
		return nil, fmt.Errorf("insert step round: %w", err)
	}
	return r, nil
}

func (d *DB) SetStepRoundSelection(id string, selectedFindingIDs *string, source string) error {
	var selectionSource *string
	if selectedFindingIDs != nil && *selectedFindingIDs != "" && source != "" {
		selectionSource = &source
	}
	if _, err := d.sql.Exec(
		`UPDATE step_rounds SET selected_finding_ids = ?, selection_source = ? WHERE id = ?`,
		selectedFindingIDs, selectionSource, id,
	); err != nil {
		return fmt.Errorf("set step round selection: %w", err)
	}
	return nil
}

func (d *DB) SetStepRoundUserDecision(id string, selectedFindingIDs *string, source string, userFindingsJSON *string, origins ...types.ResponseOrigin) error {
	origin := types.ResponseOriginUnknown
	if len(origins) > 0 {
		origin = origins[0].OrUnknown()
	}
	var selectionSource *string
	if selectedFindingIDs != nil && *selectedFindingIDs != "" && source != "" {
		selectionSource = &source
	}
	if _, err := d.sql.Exec(
		`UPDATE step_rounds SET selected_finding_ids = ?, selection_source = ?, user_findings_json = ?, response_origin = ? WHERE id = ?`,
		selectedFindingIDs, selectionSource, userFindingsJSON, origin, id,
	); err != nil {
		return fmt.Errorf("set step round user decision: %w", err)
	}
	return nil
}

func (d *DB) SetStepRoundSelectedFindingIDs(id string, selectedFindingIDs *string) error {
	return d.SetStepRoundSelection(id, selectedFindingIDs, RoundSelectionSourceUser)
}

func (d *DB) SetStepRoundUserFindings(id string, userFindingsJSON *string) error {
	if _, err := d.sql.Exec(
		`UPDATE step_rounds SET user_findings_json = ? WHERE id = ?`,
		userFindingsJSON, id,
	); err != nil {
		return fmt.Errorf("set step round user findings: %w", err)
	}
	return nil
}

func (d *DB) GetRoundsByStep(stepResultID string) ([]*StepRound, error) {
	columns, err := d.readProjection("step_rounds", roundReadColumns, roundScanTargets(&StepRound{})...)
	if err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(
		`SELECT `+columns+` FROM step_rounds WHERE step_result_id = ? ORDER BY round`,
		stepResultID,
	)
	if err != nil {
		return nil, fmt.Errorf("get rounds by step: %w", err)
	}
	defer rows.Close()
	var rounds []*StepRound
	for rows.Next() {
		r := &StepRound{}
		if err := rows.Scan(roundScanTargets(r)...); err != nil {
			return nil, fmt.Errorf("scan step round: %w", err)
		}
		r.ResponseOrigin = r.ResponseOrigin.OrUnknown()
		rounds = append(rounds, r)
	}
	return rounds, rows.Err()
}
