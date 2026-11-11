package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

type PreviousReviewRounds struct {
	RunID  string
	Rounds []*StepRound
}

func (d *DB) GetPreviousRunReviewRounds(repoID, branch, excludeRunID string) (*PreviousReviewRounds, error) {
	var runID, stepResultID string
	err := d.sql.QueryRow(
		`SELECT res.run_id, COALESCE(res.carried_from, res.id)
		   FROM step_results res
		   JOIN runs r ON r.id = res.run_id
		  WHERE r.repo_id = ? AND r.branch = ? AND r.id != ? AND res.step_name = ?
		  ORDER BY r.created_at DESC, r.id DESC
		  LIMIT 1`,
		repoID, branch, excludeRunID, string(types.StepReview),
	).Scan(&runID, &stepResultID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get previous run review step: %w", err)
	}

	rounds, err := d.GetRoundsByStep(stepResultID)
	if err != nil {
		return nil, fmt.Errorf("get previous run review rounds: %w", err)
	}
	if len(rounds) == 0 {
		return nil, nil
	}
	return &PreviousReviewRounds{RunID: runID, Rounds: rounds}, nil
}
