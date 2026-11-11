package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

type RunRetry struct {
	RunID             string
	SourceRunID       string
	StepName          types.StepName
	SourceRound       int
	SourceError       string
	CheckpointHeadSHA string
	CreatedAt         int64
}

func (d *DB) InsertRunRetry(r RunRetry) error {
	_, err := d.sql.Exec(
		`INSERT INTO run_retries (run_id, source_run_id, step_name, source_round, source_error, checkpoint_head_sha, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.SourceRunID, r.StepName, r.SourceRound, r.SourceError, r.CheckpointHeadSHA, now(),
	)
	if err != nil {
		return fmt.Errorf("record run retry: %w", err)
	}
	return nil
}

func (d *DB) GetRunRetry(runID string) (*RunRetry, error) {
	return d.getRunRetry(`SELECT run_id, source_run_id, step_name, source_round, source_error, checkpoint_head_sha, created_at FROM run_retries WHERE run_id = ?`, runID)
}

func (d *DB) GetRunRetryBySource(sourceRunID string) (*RunRetry, error) {
	return d.getRunRetry(`SELECT run_id, source_run_id, step_name, source_round, source_error, checkpoint_head_sha, created_at FROM run_retries WHERE source_run_id = ?`, sourceRunID)
}

func (d *DB) getRunRetry(query, id string) (*RunRetry, error) {
	r := &RunRetry{}
	err := d.sql.QueryRow(query, id).Scan(&r.RunID, &r.SourceRunID, &r.StepName, &r.SourceRound, &r.SourceError, &r.CheckpointHeadSHA, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get run retry: %w", err)
	}
	return r, nil
}

func (d *DB) DeleteRun(id string) error {
	_, err := d.sql.Exec(`DELETE FROM runs WHERE id = ?`, id)
	return err
}
