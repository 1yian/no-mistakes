package db

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

type StepResult struct {
	ID                 string
	RunID              string
	StepName           types.StepName
	StepOrder          int
	Status             types.StepStatus
	ExitCode           *int
	DurationMS         *int64
	LogPath            *string
	FindingsJSON       *string
	Error              *string
	StartedAt          *int64
	RoundStartedAt     *int64
	CompletedAt        *int64
	LastActivityAt     *int64
	LastActivity       *string
	AgentPID           *int
	AutoFixLimit       *int
	ReviewMaxFixRounds *int
	OverrideReason     *string
	ApprovalReason     *string
	SkipReason         *string
	ResponseOrigin     types.ResponseOrigin
	FailureCause       *string
	CarriedFrom        *string
}

const StepFailureCauseAgentInvocation = "agent_invocation"

const stepResultColumns = `id, run_id, step_name, step_order, status, exit_code, duration_ms, log_path, findings_json, error, started_at, completed_at, last_activity_at, last_activity, agent_pid, auto_fix_limit, round_started_at, override_reason, skip_reason, approval_reason, failure_cause, carried_from, review_max_fix_rounds, response_origin`

func (d *DB) readableStepResultColumns() (string, error) {
	return d.readProjection("step_results", stepResultColumns, stepScanTargets(&StepResult{})...)
}

func stepScanTargets(s *StepResult) []any {
	return []any{&s.ID, &s.RunID, &s.StepName, &s.StepOrder, &s.Status, &s.ExitCode, &s.DurationMS, &s.LogPath, &s.FindingsJSON, &s.Error, &s.StartedAt, &s.CompletedAt, &s.LastActivityAt, &s.LastActivity, &s.AgentPID, &s.AutoFixLimit, &s.RoundStartedAt, &s.OverrideReason, &s.SkipReason, &s.ApprovalReason, &s.FailureCause, &s.CarriedFrom, &s.ReviewMaxFixRounds, &s.ResponseOrigin}
}

func scanStepResult(row interface{ Scan(...any) error }, s *StepResult) error {
	if err := row.Scan(stepScanTargets(s)...); err != nil {
		return err
	}
	s.ResponseOrigin = s.ResponseOrigin.OrUnknown()
	return nil
}

func (d *DB) InsertStepResult(runID string, stepName types.StepName) (*StepResult, error) {
	s := &StepResult{
		ID:             newID(),
		RunID:          runID,
		StepName:       stepName,
		StepOrder:      stepName.Order(),
		Status:         types.StepStatusPending,
		ResponseOrigin: types.ResponseOriginUnknown,
	}
	_, err := d.sql.Exec(
		`INSERT INTO step_results (id, run_id, step_name, step_order, status) VALUES (?, ?, ?, ?, ?)`,
		s.ID, s.RunID, s.StepName, s.StepOrder, s.Status,
	)
	if err != nil {
		return nil, fmt.Errorf("insert step result: %w", err)
	}
	return s, nil
}

func (d *DB) InsertCarriedStepResult(runID string, source *StepResult) (*StepResult, error) {
	if source == nil || (source.Status != types.StepStatusCompleted && source.Status != types.StepStatusSkipped) {
		return nil, fmt.Errorf("insert carried step result: only a completed or skipped step can be carried")
	}
	origin := source.RoundsStepID()
	s := &StepResult{
		ID:             newID(),
		RunID:          runID,
		StepName:       source.StepName,
		StepOrder:      source.StepName.Order(),
		Status:         source.Status,
		ExitCode:       source.ExitCode,
		DurationMS:     source.DurationMS,
		StartedAt:      source.StartedAt,
		CompletedAt:    source.CompletedAt,
		AutoFixLimit:   source.AutoFixLimit,
		OverrideReason: source.OverrideReason,
		ApprovalReason: source.ApprovalReason,
		SkipReason:     source.SkipReason,
		CarriedFrom:    &origin,
	}
	_, err := d.sql.Exec(
		`INSERT INTO step_results (id, run_id, step_name, step_order, status, exit_code, duration_ms, started_at, completed_at, auto_fix_limit, override_reason, approval_reason, skip_reason, carried_from) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.RunID, s.StepName, s.StepOrder, s.Status, s.ExitCode, s.DurationMS, s.StartedAt, s.CompletedAt, s.AutoFixLimit, s.OverrideReason, s.ApprovalReason, s.SkipReason, s.CarriedFrom,
	)
	if err != nil {
		return nil, fmt.Errorf("insert carried step result: %w", err)
	}
	return s, nil
}

func (s *StepResult) RoundsStepID() string {
	if s.CarriedFrom != nil && *s.CarriedFrom != "" {
		return *s.CarriedFrom
	}
	return s.ID
}

func (d *DB) GetStepResult(id string) (*StepResult, error) {
	columns, err := d.readableStepResultColumns()
	if err != nil {
		return nil, err
	}
	s := &StepResult{}
	err = scanStepResult(d.sql.QueryRow(
		`SELECT `+columns+` FROM step_results WHERE id = ?`, id,
	), s)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get step result: %w", err)
	}
	return s, nil
}

func (d *DB) GetStepsByRun(runID string) ([]*StepResult, error) {
	columns, err := d.readableStepResultColumns()
	if err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(
		`SELECT `+columns+` FROM step_results WHERE run_id = ? ORDER BY step_order, id`, runID,
	)
	if err != nil {
		return nil, fmt.Errorf("get steps by run: %w", err)
	}
	defer rows.Close()
	var steps []*StepResult
	for rows.Next() {
		s := &StepResult{}
		if err := scanStepResult(rows, s); err != nil {
			return nil, fmt.Errorf("scan step result: %w", err)
		}
		steps = append(steps, s)
	}
	return steps, rows.Err()
}

func (d *DB) ResetStepsFrom(runID string, stepOrder int) error {
	_, err := d.sql.Exec(`
		UPDATE step_results
		SET status = ?, exit_code = NULL, duration_ms = NULL, log_path = NULL,
			findings_json = NULL, error = NULL, started_at = NULL,
			round_started_at = NULL, completed_at = NULL, last_activity_at = NULL, last_activity = NULL,
			agent_pid = NULL, auto_fix_limit = NULL, override_reason = NULL, approval_reason = NULL, response_origin = 'unknown',
			failure_cause = NULL, carried_from = NULL
		WHERE run_id = ? AND step_order >= ? AND status != ?`, types.StepStatusPending, runID, stepOrder, types.StepStatusSkipped)
	if err != nil {
		return fmt.Errorf("reset steps for revalidation: %w", err)
	}
	return nil
}

func (d *DB) UpdateStepStatus(id string, status types.StepStatus) error {
	_, err := d.sql.Exec(`UPDATE step_results SET status = ?, last_activity_at = ?, last_activity = ? WHERE id = ?`, status, now(), fmt.Sprintf("status: %s", status), id)
	if err != nil {
		return fmt.Errorf("update step status: %w", err)
	}
	return nil
}

func (d *DB) UpdateStepStatusWithDuration(id string, status types.StepStatus, durationMS int64) error {
	_, err := d.sql.Exec(`UPDATE step_results SET status = ?, duration_ms = ?, last_activity_at = ?, last_activity = ? WHERE id = ?`, status, durationMS, now(), fmt.Sprintf("status: %s", status), id)
	if err != nil {
		return fmt.Errorf("update step status with duration: %w", err)
	}
	return nil
}

func (d *DB) ParkStepForApproval(runID, stepID string, status types.StepStatus, exitCode int, durationMS int64, findingsJSON *string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin approval park: %w", err)
	}
	defer tx.Rollback()

	ts := now()
	stepResult, err := tx.Exec(
		`UPDATE step_results SET status = ?, exit_code = ?, duration_ms = ?, findings_json = ?, last_activity_at = ?, last_activity = ? WHERE id = ?`,
		status, exitCode, durationMS, findingsJSON, ts, fmt.Sprintf("status: %s", status), stepID,
	)
	if err != nil {
		return fmt.Errorf("park step for approval: %w", err)
	}
	changed, err := stepResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("park step for approval rows affected: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("park step for approval: updated %d rows", changed)
	}
	runResult, err := tx.Exec(
		`UPDATE runs SET awaiting_agent_since = ?, updated_at = ? WHERE id = ?`,
		ts, ts, runID,
	)
	if err != nil {
		return fmt.Errorf("mark run awaiting approval: %w", err)
	}
	changed, err = runResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark run awaiting approval rows affected: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("mark run awaiting approval: updated %d rows", changed)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit approval park: %w", err)
	}
	return nil
}

func (d *DB) StartStep(id string) error {
	return d.StartStepWithAutoFixLimit(id, 0)
}

func (d *DB) StartStepWithAutoFixLimit(id string, autoFixLimit int) error {
	ts := now()
	_, err := d.sql.Exec(`UPDATE step_results SET status = ?, started_at = ?, round_started_at = ?, last_activity_at = ?, last_activity = ?, agent_pid = NULL, auto_fix_limit = ? WHERE id = ?`, types.StepStatusRunning, ts, ts, ts, "step started", autoFixLimitDBValue(autoFixLimit), id)
	if err != nil {
		return fmt.Errorf("start step: %w", err)
	}
	return nil
}

func (d *DB) PinReviewMaxFixRounds(id string, limit int) error {
	if limit < 0 {
		return fmt.Errorf("review.max_fix_rounds must be non-negative")
	}
	if _, err := d.sql.Exec(`UPDATE step_results SET review_max_fix_rounds = COALESCE(review_max_fix_rounds, ?) WHERE id = ? AND step_name = 'review'`, limit, id); err != nil {
		return fmt.Errorf("pin Review fix-round limit: %w", err)
	}
	return nil
}

func (d *DB) StartStepFixRound(id string, autoFixLimit int) error {
	ts := now()
	_, err := d.sql.Exec(`UPDATE step_results SET status = ?, round_started_at = ?, last_activity_at = ?, last_activity = ?, auto_fix_limit = ?, override_reason = NULL, approval_reason = NULL WHERE id = ?`, types.StepStatusFixing, ts, ts, fmt.Sprintf("status: %s", types.StepStatusFixing), autoFixLimitDBValue(autoFixLimit), id)
	if err != nil {
		return fmt.Errorf("start step fix round: %w", err)
	}
	return nil
}

func (d *DB) SetStepAutoFixLimit(id string, autoFixLimit int) error {
	if _, err := d.sql.Exec(`UPDATE step_results SET auto_fix_limit = ? WHERE id = ?`, autoFixLimitDBValue(autoFixLimit), id); err != nil {
		return fmt.Errorf("set step auto-fix limit: %w", err)
	}
	return nil
}

func (d *DB) SetStepOverrideReason(id string, reason string) error {
	if reason == "" {
		return fmt.Errorf("set step override reason: reason must not be empty")
	}
	if _, err := d.sql.Exec(`UPDATE step_results SET override_reason = ? WHERE id = ?`, reason, id); err != nil {
		return fmt.Errorf("set step override reason: %w", err)
	}
	return nil
}

func (d *DB) SetTestApprovalReason(id, reason string) error {
	result, err := d.sql.Exec(`UPDATE step_results SET approval_reason = ? WHERE id = ? AND step_name = ? AND status IN (?, ?)`,
		reason, id, types.StepTest, types.StepStatusAwaitingApproval, types.StepStatusFixReview)
	if err != nil {
		return fmt.Errorf("record Test approval reason: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("record Test approval reason: parked Test step not found")
	}
	return nil
}

func (s *StepResult) TestOverrideReason() string {
	if s.StepName != types.StepTest || s.Status != types.StepStatusCompleted {
		return ""
	}
	condition := s.testExceptionCondition()
	if condition == "" {
		return ""
	}
	if s.ApprovalReason == nil {
		return condition
	}
	reason := *s.ApprovalReason
	if strings.TrimSpace(reason) == "" {
		reason = "no operator reason supplied"
	}
	return strings.TrimSpace(condition + "\nTest exception approved: " + reason)
}

func (s *StepResult) testExceptionCondition() string {
	if s.OverrideReason != nil && strings.TrimSpace(*s.OverrideReason) != "" {
		return *s.OverrideReason
	}
	if s.FindingsJSON == nil {
		return ""
	}
	findings, err := types.ParseFindingsJSON(*s.FindingsJSON)
	if err != nil {
		if s.ApprovalReason != nil {
			return "approved Test evidence could not be read"
		}
		return ""
	}
	switch findings.Verdict {
	case types.TestVerdictNoGo, types.TestVerdictInconclusive:
		return "live validation verdict: " + findings.Verdict
	}
	for _, item := range findings.Items {
		if item.ID == types.FindingIDTestAgentTimeout {
			return "test agent invocation budget exhausted"
		}
	}
	return ""
}

func autoFixLimitDBValue(autoFixLimit int) any {
	if autoFixLimit <= 0 {
		return nil
	}
	return autoFixLimit
}

func (d *DB) CompleteStep(id string, exitCode int, durationMS int64, logPath string) error {
	return d.CompleteStepWithStatus(id, types.StepStatusCompleted, exitCode, durationMS, logPath)
}

func (d *DB) CompleteStepWithStatus(id string, status types.StepStatus, exitCode int, durationMS int64, logPath string) error {
	return d.completeStep(id, status, exitCode, durationMS, logPath, "")
}

func (d *DB) CompleteSkippedStep(id string, exitCode int, durationMS int64, logPath, reason string) error {
	return d.completeStep(id, types.StepStatusSkipped, exitCode, durationMS, logPath, reason)
}

func (d *DB) completeStep(id string, status types.StepStatus, exitCode int, durationMS int64, logPath, skipReason string) error {
	_, err := d.sql.Exec(
		`UPDATE step_results SET status = ?, exit_code = ?, duration_ms = ?, log_path = ?, completed_at = ?, last_activity_at = ?, last_activity = ?, agent_pid = NULL, skip_reason = NULLIF(?, ''), override_reason = CASE WHEN ? THEN NULL ELSE override_reason END, approval_reason = CASE WHEN ? THEN NULL ELSE approval_reason END WHERE id = ?`,
		status, exitCode, durationMS, logPath, now(), now(), fmt.Sprintf("status: %s", status), skipReason, status == types.StepStatusSkipped, status == types.StepStatusSkipped, id,
	)
	if err != nil {
		return fmt.Errorf("complete step: %w", err)
	}
	return nil
}

func (d *DB) CompleteCIStep(id, runID string, declaredNoCI bool, exitCode int, durationMS int64, logPath string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin complete CI step: %w", err)
	}
	defer tx.Rollback()
	ts := now()
	result, err := tx.Exec(
		`UPDATE step_results SET status = ?, exit_code = ?, duration_ms = ?, log_path = ?, completed_at = ?, last_activity_at = ?, last_activity = ?, agent_pid = NULL WHERE id = ? AND run_id = ? AND step_name = ?`,
		types.StepStatusCompleted, exitCode, durationMS, logPath, ts, ts, "status: completed", id, runID, types.StepCI,
	)
	if err != nil {
		return fmt.Errorf("complete CI step: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return fmt.Errorf("complete CI step: step row not found")
	}
	result, err = tx.Exec(`UPDATE runs SET ci_ready_at = ?, ci_ready_no_ci = ?, ci_released_at = ?, updated_at = ? WHERE id = ?`, ts, declaredNoCI, ts, ts, runID)
	if err != nil {
		return fmt.Errorf("record CI readiness: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return fmt.Errorf("record CI readiness: run row not found")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit completed CI: %w", err)
	}
	return nil
}

func (d *DB) CompleteReviewStep(id, runID, approvedHeadSHA string, exitCode int, durationMS int64, logPath string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin complete review step: %w", err)
	}
	defer tx.Rollback()

	ts := now()
	result, err := tx.Exec(
		`UPDATE step_results SET status = ?, exit_code = ?, duration_ms = ?, log_path = ?, completed_at = ?, last_activity_at = ?, last_activity = ?, agent_pid = NULL WHERE id = ?`,
		types.StepStatusCompleted, exitCode, durationMS, logPath, ts, ts, fmt.Sprintf("status: %s", types.StepStatusCompleted), id,
	)
	if err != nil {
		return fmt.Errorf("complete review step: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return fmt.Errorf("complete review step: step row not found")
	}
	result, err = tx.Exec(`UPDATE runs SET review_approved_head_sha = ?, updated_at = ? WHERE id = ?`, approvedHeadSHA, ts, runID)
	if err != nil {
		return fmt.Errorf("record review-approved head: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return fmt.Errorf("record review-approved head: run row not found")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit completed review: %w", err)
	}
	return nil
}

func (d *DB) FailStep(id string, errMsg string, durationMS int64) error {
	return d.FailStepWithCause(id, errMsg, durationMS, "")
}

func (d *DB) FailStepWithCause(id string, errMsg string, durationMS int64, cause string) error {
	var recorded *string
	if cause != "" {
		recorded = &cause
	}
	_, err := d.sql.Exec(
		`UPDATE step_results SET status = ?, error = ?, failure_cause = ?, duration_ms = ?, completed_at = ?, last_activity_at = ?, last_activity = ?, agent_pid = NULL WHERE id = ?`,
		types.StepStatusFailed, errMsg, recorded, durationMS, now(), now(), "step failed: "+errMsg, id,
	)
	if err != nil {
		return fmt.Errorf("fail step: %w", err)
	}
	return nil
}

func (d *DB) TouchStepActivity(id string, text string) error {
	_, err := d.sql.Exec(`UPDATE step_results SET last_activity_at = ?, last_activity = ? WHERE id = ?`, now(), text, id)
	if err != nil {
		return fmt.Errorf("touch step activity: %w", err)
	}
	return nil
}

func (d *DB) SetStepAgentActivity(id string, text string, agentPID *int) error {
	_, err := d.sql.Exec(`UPDATE step_results SET last_activity_at = ?, last_activity = ?, agent_pid = ? WHERE id = ?`, now(), text, agentPID, id)
	if err != nil {
		return fmt.Errorf("set step agent activity: %w", err)
	}
	return nil
}

func (d *DB) SetStepDuration(id string, durationMS int64) error {
	_, err := d.sql.Exec(`UPDATE step_results SET duration_ms = ? WHERE id = ?`, durationMS, id)
	if err != nil {
		return fmt.Errorf("set step duration: %w", err)
	}
	return nil
}

func (d *DB) SetStepFindings(id string, findingsJSON string) error {
	_, err := d.sql.Exec(`UPDATE step_results SET findings_json = ? WHERE id = ?`, findingsJSON, id)
	if err != nil {
		return fmt.Errorf("set step findings: %w", err)
	}
	return nil
}

func (d *DB) ClearStepFindings(id string) error {
	_, err := d.sql.Exec(`UPDATE step_results SET findings_json = NULL WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("clear step findings: %w", err)
	}
	return nil
}
