package db

import (
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

const MaxBranchDecisionRounds = 20

type BranchDecisionRound struct {
	RunID    string
	StepName types.StepName
	Round    *StepRound
}

func (d *DB) GetBranchDecisionRounds(repoID, branch, excludeRunID string, limit int) ([]*BranchDecisionRound, bool, error) {
	if limit <= 0 {
		limit = MaxBranchDecisionRounds
	}
	queryLimit := limit + 1
	rows, err := d.sql.Query(
		`SELECT sr.id, sr.step_result_id, sr.round, sr.trigger_type, sr.findings_json,
		        sr.reviewed_head_sha, sr.starting_head_sha, sr.trusted_config_sha,
		        sr.global_config_yaml, sr.repo_config_yaml, sr.user_findings_json,
		        sr.selected_finding_ids, sr.selection_source, sr.fix_summary,
		        sr.duration_ms, sr.created_at, sr.response_origin,
		        res.step_name, res.run_id
		   FROM step_rounds sr
		   JOIN step_results res ON res.id = sr.step_result_id
		   JOIN runs r ON r.id = res.run_id
		  WHERE r.repo_id = ? AND r.branch = ? AND r.id != ?
		    AND sr.selection_source IN (?, ?)
		    AND sr.findings_json IS NOT NULL
		  ORDER BY sr.created_at DESC, sr.id DESC
		  LIMIT ?`,
		repoID, branch, excludeRunID,
		RoundSelectionSourceUser, RoundSelectionSourceUserDeclined,
		queryLimit,
	)
	if err != nil {
		return nil, false, fmt.Errorf("get branch decision rounds: %w", err)
	}
	defer rows.Close()

	var decisions []*BranchDecisionRound
	for rows.Next() {
		r := &StepRound{}
		entry := &BranchDecisionRound{Round: r}
		if err := rows.Scan(
			&r.ID, &r.StepResultID, &r.Round, &r.Trigger, &r.FindingsJSON,
			&r.ReviewedHeadSHA, &r.StartingHeadSHA, &r.TrustedConfigSHA,
			&r.GlobalConfigYAML, &r.RepoConfigYAML, &r.UserFindingsJSON,
			&r.SelectedFindingIDs, &r.SelectionSource, &r.FixSummary,
			&r.DurationMS, &r.CreatedAt, &r.ResponseOrigin,
			&entry.StepName, &entry.RunID,
		); err != nil {
			return nil, false, fmt.Errorf("scan branch decision round: %w", err)
		}
		decisions = append(decisions, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(decisions) > limit
	if truncated {
		decisions = decisions[:limit]
	}
	return decisions, truncated, nil
}

func (d *DB) SetStepRoundDeclined(id string, origins ...types.ResponseOrigin) error {
	origin := types.ResponseOriginUnknown
	if len(origins) > 0 {
		origin = origins[0].OrUnknown()
	}
	declined := DeclinedSelectionJSON
	if _, err := d.sql.Exec(
		`UPDATE step_rounds
		    SET selected_finding_ids = CASE WHEN selection_source IS NULL THEN ? ELSE selected_finding_ids END,
		        selection_source = CASE WHEN selection_source IS NULL THEN ? ELSE selection_source END,
		        response_origin = ?
		  WHERE id = ? AND (selection_source IS NULL OR selection_source = ?)`,
		declined, RoundSelectionSourceUserDeclined, origin, id, RoundSelectionSourceUserDeclined,
	); err != nil {
		return fmt.Errorf("set step round declined: %w", err)
	}
	return nil
}
