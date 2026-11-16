package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

var errFreshCIRepairRequired = errors.New("guarded CI repair requires a fresh completed phase without carry or skip")

type CIRepairObligation struct {
	RunID          string `json:"run_id"`
	Owner          string `json:"owner"`
	OriginalHead   string `json:"original_head"`
	PRURL          string `json:"pr_url"`
	SnapshotDir    string `json:"snapshot_dir"`
	SnapshotDigest string `json:"snapshot_digest"`
	Phase          string `json:"phase"`
	RepairHead     string `json:"repair_head"`
	CertifiedHead  string `json:"certified_head"`
	Baselines      string `json:"baselines"`
}

func (d *DB) CIRepairObligation(id string) (*CIRepairObligation, error) {
	a := &CIRepairObligation{}
	err := d.sql.QueryRow(`SELECT run_id, owner, original_head, pr_url, snapshot_dir, snapshot_digest, phase, repair_head, certified_head, baselines FROM ci_repair_obligations WHERE run_id = ?`, id).Scan(&a.RunID, &a.Owner, &a.OriginalHead, &a.PRURL, &a.SnapshotDir, &a.SnapshotDigest, &a.Phase, &a.RepairHead, &a.CertifiedHead, &a.Baselines)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

func oneCIRepairRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("guarded CI repair identity, ownership or phase changed")
	}
	return nil
}

func (d *DB) BeginCIRepair(a CIRepairObligation) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := oneCIRepairRow(tx.Exec(`UPDATE runs SET ci_ready_at = NULL, ci_ready_no_ci = 0, review_approved_head_sha = NULL, updated_at = ? WHERE id = ? AND status = 'running' AND head_sha = ? AND last_pushed_sha = ? AND pr_url = ? AND push_active = 0 AND custody_returned_at IS NULL`, now(), a.RunID, a.OriginalHead, a.OriginalHead, a.PRURL)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO ci_repair_obligations (run_id, owner, original_head, pr_url, snapshot_dir, snapshot_digest, phase) VALUES (?, ?, ?, ?, ?, ?, 'withdrawing')`, a.RunID, a.Owner, a.OriginalHead, a.PRURL, a.SnapshotDir, a.SnapshotDigest); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) AdmitCIRepair(id, owner string) error {
	return oneCIRepairRow(d.sql.Exec(`UPDATE ci_repair_obligations SET phase = 'repair' WHERE run_id = ? AND owner = ? AND phase = 'withdrawing' AND EXISTS (SELECT 1 FROM runs WHERE id = ? AND status = 'running' AND head_sha = original_head AND last_pushed_sha = original_head AND pr_url = ci_repair_obligations.pr_url AND push_active = 0 AND custody_returned_at IS NULL)`, id, owner, id))
}

func (d *DB) RecordCIRepairHead(id, owner, previous, head string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT s.step_name, COALESCE(MAX(r.round), 0) FROM step_results s LEFT JOIN step_rounds r ON r.step_result_id = s.id WHERE s.run_id = ? GROUP BY s.id`, id)
	if err != nil {
		return err
	}
	baselines := map[string]int{}
	for rows.Next() {
		var name string
		var round int
		if err := rows.Scan(&name, &round); err != nil {
			rows.Close()
			return err
		}
		baselines[name] = round
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(baselines)
	if err != nil {
		return err
	}
	if err := oneCIRepairRow(tx.Exec(`UPDATE ci_repair_obligations SET phase = 'revalidate', repair_head = ?, certified_head = '', baselines = ? WHERE run_id = ? AND owner = ? AND phase IN ('repair', 'revalidate', 'fulfilled') AND original_head != ?`, head, string(encoded), id, owner, head)); err != nil {
		return err
	}
	if err := oneCIRepairRow(tx.Exec(`UPDATE runs SET head_sha = ?, review_approved_head_sha = NULL, ci_ready_at = NULL, ci_ready_no_ci = 0, ci_test_restart_pending = 0, ci_rebase_review_head_sha = NULL, ci_rebase_review_base_sha = NULL, updated_at = ? WHERE id = ? AND status = 'running' AND head_sha = ? AND custody_returned_at IS NULL AND pr_url = (SELECT pr_url FROM ci_repair_obligations WHERE run_id = ?)`, head, now(), id, previous, id)); err != nil {
		return err
	}
	return tx.Commit()
}

func verifyFreshCIRepairSteps(tx *sql.Tx, id, baselines string, through types.StepName) error {
	var prior map[string]int
	if json.Unmarshal([]byte(baselines), &prior) != nil || len(prior) == 0 {
		return fmt.Errorf("CI repair round baselines are unreadable")
	}
	for _, name := range []types.StepName{types.StepReview, types.StepTest, types.StepDocument, types.StepLint, types.StepPush, types.StepPR} {
		if name.Order() > through.Order() {
			break
		}
		var status string
		var carried sql.NullString
		var round int
		if err := tx.QueryRow(`SELECT s.status, s.carried_from, COALESCE(MAX(r.round), 0) FROM step_results s LEFT JOIN step_rounds r ON r.step_result_id = s.id WHERE s.run_id = ? AND s.step_name = ? GROUP BY s.id`, id, name).Scan(&status, &carried, &round); err != nil {
			return err
		}
		baseline, exists := prior[string(name)]
		if !exists || status != string(types.StepStatusCompleted) || carried.Valid || round <= baseline {
			return fmt.Errorf("%w: %s", errFreshCIRepairRequired, name)
		}
	}
	return nil
}

func certifyCIRepair(tx *sql.Tx, id string, declaredNoCI bool) error {
	var phase, original, repaired, baselines, expectedPR string
	err := tx.QueryRow(`SELECT phase, original_head, repair_head, baselines, pr_url FROM ci_repair_obligations WHERE run_id = ?`, id).Scan(&phase, &original, &repaired, &baselines, &expectedPR)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if declaredNoCI || (phase != "revalidate" && phase != "fulfilled") || repaired == "" || repaired == original {
		return fmt.Errorf("guarded CI repair is unvalidated: actual repaired-head CI and fresh Review onward required")
	}
	var head string
	if err := tx.QueryRow(`SELECT head_sha FROM runs WHERE id = ? AND status = 'running' AND head_sha = last_pushed_sha AND head_sha != ? AND pr_url = ? AND review_approved_head_sha IS NOT NULL AND custody_returned_at IS NULL AND push_active = 0`, id, original, expectedPR).Scan(&head); err != nil {
		return fmt.Errorf("guarded CI repair publication binding is not valid: %w", err)
	}
	if err := verifyFreshCIRepairSteps(tx, id, baselines, types.StepPR); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE ci_repair_obligations SET phase = 'fulfilled', certified_head = ? WHERE run_id = ?`, head, id)
	return err
}

func (d *DB) VerifyCIRepairPublication(id string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var phase, baselines, expectedPR string
	err = tx.QueryRow(`SELECT phase, baselines, pr_url FROM ci_repair_obligations WHERE run_id = ?`, id).Scan(&phase, &baselines, &expectedPR)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if phase != "revalidate" && phase != "fulfilled" {
		return fmt.Errorf("guarded CI repair cannot publish before a repair and fresh validation")
	}
	var actualPR string
	if err := tx.QueryRow(`SELECT pr_url FROM runs WHERE id = ? AND status = 'running' AND custody_returned_at IS NULL`, id).Scan(&actualPR); err != nil {
		return err
	}
	if actualPR != expectedPR {
		return fmt.Errorf("guarded CI repair publication identity changed")
	}
	return verifyFreshCIRepairSteps(tx, id, baselines, types.StepLint)
}

func (d *DB) CIRepairValidationPending(id string) (bool, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var phase, baselines string
	err = tx.QueryRow(`SELECT phase, baselines FROM ci_repair_obligations WHERE run_id = ?`, id).Scan(&phase, &baselines)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if phase == "held" || phase == "withdrawing" {
		return false, fmt.Errorf("guarded CI repair is held unvalidated")
	}
	if phase == "repair" {
		return false, nil
	}
	err = verifyFreshCIRepairSteps(tx, id, baselines, types.StepPR)
	if errors.Is(err, errFreshCIRepairRequired) {
		return true, nil
	}
	return false, err
}

func (d *DB) CIRepairRetainsCustody(repoID, branch string) (bool, error) {
	var count int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM ci_repair_obligations a JOIN runs r ON r.id = a.run_id WHERE r.repo_id = ? AND r.branch = ? AND (a.phase != 'fulfilled' OR r.status IN ('running', 'pending'))`, repoID, branch).Scan(&count)
	return count != 0, err
}

func (d *DB) HoldCIRepair(id, reason string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE runs SET status = 'running', ci_ready_at = NULL, ci_ready_no_ci = 0, review_approved_head_sha = NULL, error = ?, updated_at = ? WHERE id = ? AND EXISTS (SELECT 1 FROM ci_repair_obligations WHERE run_id = ?)`, reason, now(), id, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE ci_repair_obligations SET phase = 'held', certified_head = '' WHERE run_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) VerifyCIRepairCompletion(id string) error {
	var head string
	err := d.sql.QueryRow(`SELECT a.certified_head FROM ci_repair_obligations a JOIN runs r ON r.id = a.run_id WHERE a.run_id = ? AND (a.phase != 'fulfilled' OR a.certified_head != r.head_sha OR r.head_sha != r.last_pushed_sha OR r.ci_ready_at IS NULL OR r.ci_ready_no_ci != 0 OR r.push_active != 0 OR r.custody_returned_at IS NOT NULL)`, id).Scan(&head)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("guarded CI repair cannot complete without actual fresh repaired-head CI")
}

func (d *DB) HoldCIRepairs() (map[string]struct{}, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT r.id FROM runs r JOIN ci_repair_obligations a ON a.run_id = r.id WHERE r.status IN ('running', 'pending')`)
	if err != nil {
		return nil, err
	}
	preserved := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		preserved[id] = struct{}{}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for id := range preserved {
		if _, err := tx.Exec(`UPDATE ci_repair_obligations SET phase = 'held' WHERE run_id = ?`, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE runs SET ci_ready_at = NULL, ci_ready_no_ci = 0, review_approved_head_sha = NULL, error = 'guarded CI repair held: monitor continuity lost; adoption is unsupported', updated_at = ? WHERE id = ?`, now(), id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return preserved, nil
}
