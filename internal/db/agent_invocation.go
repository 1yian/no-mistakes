package db

import (
	"database/sql"
	"fmt"
)

const (
	InvocationModeCold     = "cold"     // no durable session involved
	InvocationModeStarted  = "started"  // began a new durable session
	InvocationModeResumed  = "resumed"  // resumed an existing durable session
	InvocationModeFallback = "fallback" // fresh session after a failed resume
)

const (
	FallbackReasonTransient   = "transient"   // retryable provider/transport error
	FallbackReasonParse       = "parse"       // could not parse the resumed output
	FallbackReasonExit        = "exit"        // resumed process exited non-zero
	FallbackReasonSpawn       = "spawn"       // resumed process failed to start
	FallbackReasonUnsupported = "unsupported" // adapter rejected a resume flag
	FallbackReasonOther       = "other"       // anything else
)

type AgentInvocation struct {
	ID                   string
	RunID                string
	StepName             string
	Round                int
	Purpose              string
	Agent                string
	Model                string
	ConfiguredModel      *string
	ModelProvider        *string
	SessionMode          string
	SessionKey           string
	FallbackReason       *string
	StartedAt            int64
	CompletedAt          int64
	DurationMS           int64
	ExitStatus           string // ok | error | cancelled
	FailureCategory      string // parse | exit | spawn | cancelled | other ("" when ok)
	InputTokens          *int
	OutputTokens         *int
	CacheReadTokens      *int
	CacheCreationTokens  *int
	FreshInputTokens     *int
	ReasoningTokens      *int
	SubprocessWaitMS     *int64
	DeltaInputTokens     *int
	DeltaOutputTokens    *int
	DeltaCacheReadTokens *int
	ModelRoundtrips      *int
	ToolCalls            *int
	ToolWaitCalls        *int
	ToolTestLintCalls    *int
	ToolEditCalls        *int
	ToolReadCalls        *int
	ToolGitCalls         *int
	ToolOtherCalls       *int
	WorkloadFiles        *int
	WorkloadLines        *int
	FindingCount         *int
}

const agentInvocationColumns = `id, run_id, step_name, round, purpose, agent, model, model_provider, configured_model,
	session_mode, session_key, fallback_reason,
	started_at, completed_at, duration_ms, subprocess_wait_ms, exit_status, failure_category,
	input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
	fresh_input_tokens, reasoning_tokens,
	delta_input_tokens, delta_output_tokens, delta_cache_read_tokens,
	model_roundtrips, tool_calls,
	tool_wait_calls, tool_test_lint_calls, tool_edit_calls, tool_read_calls, tool_git_calls, tool_other_calls,
	workload_files, workload_lines, finding_count`

const agentInvocationInsertPlaceholders = `?, ?, ?, ?, ?, ?, ?, ?, ?,
	?, ?, ?,
	?, ?, ?, ?, ?, ?,
	?, ?, ?, ?,
	?, ?,
	?, ?, ?,
	?, ?,
	?, ?, ?, ?, ?, ?,
	?, ?, ?`

func (d *DB) InsertAgentInvocation(inv AgentInvocation) (*AgentInvocation, error) {
	inv.ID = newID()
	_, err := d.sql.Exec(
		`INSERT INTO agent_invocations (`+agentInvocationColumns+`)
		 VALUES (`+agentInvocationInsertPlaceholders+`)`,
		inv.ID, inv.RunID, inv.StepName, inv.Round, inv.Purpose, inv.Agent, inv.Model, inv.ModelProvider, inv.ConfiguredModel,
		inv.SessionMode, inv.SessionKey, inv.FallbackReason,
		inv.StartedAt, inv.CompletedAt, inv.DurationMS, inv.SubprocessWaitMS, inv.ExitStatus, inv.FailureCategory,
		inv.InputTokens, inv.OutputTokens, inv.CacheReadTokens, inv.CacheCreationTokens,
		inv.FreshInputTokens, inv.ReasoningTokens,
		inv.DeltaInputTokens, inv.DeltaOutputTokens, inv.DeltaCacheReadTokens,
		inv.ModelRoundtrips, inv.ToolCalls,
		inv.ToolWaitCalls, inv.ToolTestLintCalls, inv.ToolEditCalls, inv.ToolReadCalls, inv.ToolGitCalls, inv.ToolOtherCalls,
		inv.WorkloadFiles, inv.WorkloadLines, inv.FindingCount,
	)
	if err != nil {
		return nil, fmt.Errorf("insert agent invocation: %w", err)
	}
	return &inv, nil
}

func (d *DB) HasAgentInvocationPurpose(runID, stepName, purpose string) (bool, error) {
	columns, err := d.readProjection("agent_invocations", "run_id, step_name, purpose", new(string), new(string), new(string))
	if err != nil {
		return false, err
	}
	var found bool
	err = d.sql.QueryRow(
		`SELECT EXISTS(
			SELECT 1 FROM agent_invocations
			WHERE (`+columns+`) = (?, ?, ?)
		)`,
		runID, stepName, purpose,
	).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("check agent invocation purpose: %w", err)
	}
	return found, nil
}

func (d *DB) GetAgentInvocationsByRun(runID string) ([]AgentInvocation, error) {
	rows, err := d.sql.Query(
		`SELECT `+agentInvocationColumns+` FROM agent_invocations WHERE run_id = ? ORDER BY started_at, id`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("get agent invocations: %w", err)
	}
	defer rows.Close()

	var invocations []AgentInvocation
	for rows.Next() {
		inv, err := scanAgentInvocation(rows)
		if err != nil {
			return nil, err
		}
		invocations = append(invocations, inv)
	}
	return invocations, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAgentInvocation(row scanner) (AgentInvocation, error) {
	var inv AgentInvocation
	if err := row.Scan(
		&inv.ID, &inv.RunID, &inv.StepName, &inv.Round, &inv.Purpose, &inv.Agent, &inv.Model, &inv.ModelProvider, &inv.ConfiguredModel,
		&inv.SessionMode, &inv.SessionKey, &inv.FallbackReason,
		&inv.StartedAt, &inv.CompletedAt, &inv.DurationMS, &inv.SubprocessWaitMS, &inv.ExitStatus, &inv.FailureCategory,
		&inv.InputTokens, &inv.OutputTokens, &inv.CacheReadTokens, &inv.CacheCreationTokens,
		&inv.FreshInputTokens, &inv.ReasoningTokens,
		&inv.DeltaInputTokens, &inv.DeltaOutputTokens, &inv.DeltaCacheReadTokens,
		&inv.ModelRoundtrips, &inv.ToolCalls,
		&inv.ToolWaitCalls, &inv.ToolTestLintCalls, &inv.ToolEditCalls, &inv.ToolReadCalls, &inv.ToolGitCalls, &inv.ToolOtherCalls,
		&inv.WorkloadFiles, &inv.WorkloadLines, &inv.FindingCount,
	); err != nil {
		return AgentInvocation{}, fmt.Errorf("scan agent invocation: %w", err)
	}
	return inv, nil
}

func (d *DB) LatestSessionCumulative(runID, sessionKey string) (input, output, cacheRead int, found bool) {
	if sessionKey == "" {
		return 0, 0, 0, false
	}
	var in, out, cache sql.NullInt64
	err := d.sql.QueryRow(
		`SELECT input_tokens, output_tokens, cache_read_tokens
		 FROM agent_invocations
		 WHERE run_id = ? AND session_key = ?
		   AND input_tokens IS NOT NULL
		   AND output_tokens IS NOT NULL
		   AND cache_read_tokens IS NOT NULL
		 ORDER BY started_at DESC, id DESC LIMIT 1`,
		runID, sessionKey,
	).Scan(&in, &out, &cache)
	if err != nil || !in.Valid || !out.Valid || !cache.Valid {
		return 0, 0, 0, false
	}
	return int(in.Int64), int(out.Int64), int(cache.Int64), true
}

type AgentInvocationAggregate struct {
	Purpose             string
	Count               int
	TotalDurationMS     int64
	AvgDurationMS       int64
	SubprocessWaitMS    *int64
	Cold                int
	Started             int
	Resumed             int
	Fallback            int
	Errors              int
	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	CacheCreationTokens *int64
	FreshInputTokens    *int64
	ReasoningTokens     *int64
	ModelRoundtrips     *int64
	ToolCalls           *int64
	ToolWaitCalls       *int64
	ToolTestLintCalls   *int64
	ToolEditCalls       *int64
	ToolReadCalls       *int64
	ToolGitCalls        *int64
	ToolOtherCalls      *int64
	MetricsRows         int
}

func (d *DB) AgentInvocationAggregates() ([]AgentInvocationAggregate, error) {
	rows, err := d.sql.Query(`
		SELECT purpose,
		       COUNT(*),
		       COALESCE(SUM(duration_ms), 0),
		       CASE WHEN COUNT(subprocess_wait_ms) = COUNT(*) THEN SUM(subprocess_wait_ms) END,
		       COALESCE(SUM(CASE WHEN session_mode = 'cold' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN session_mode = 'started' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN session_mode = 'resumed' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN session_mode = 'fallback' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN exit_status != 'ok' THEN 1 ELSE 0 END), 0),
		       CASE WHEN COUNT(input_tokens) = COUNT(*) THEN SUM(input_tokens) END,
		       CASE WHEN COUNT(output_tokens) = COUNT(*) THEN SUM(output_tokens) END,
		       CASE WHEN COUNT(cache_read_tokens) = COUNT(*) THEN SUM(cache_read_tokens) END,
		       CASE WHEN COUNT(cache_creation_tokens) = COUNT(*) THEN SUM(cache_creation_tokens) END,
		       CASE WHEN COUNT(fresh_input_tokens) = COUNT(*) THEN SUM(fresh_input_tokens) END,
		       CASE WHEN COUNT(reasoning_tokens) = COUNT(*) THEN SUM(reasoning_tokens) END,
		       CASE WHEN COUNT(model_roundtrips) = COUNT(*) THEN SUM(model_roundtrips) END,
		       CASE WHEN COUNT(tool_calls) = COUNT(*) THEN SUM(tool_calls) END,
		       CASE WHEN COUNT(tool_wait_calls) = COUNT(*) THEN SUM(tool_wait_calls) END,
		       CASE WHEN COUNT(tool_test_lint_calls) = COUNT(*) THEN SUM(tool_test_lint_calls) END,
		       CASE WHEN COUNT(tool_edit_calls) = COUNT(*) THEN SUM(tool_edit_calls) END,
		       CASE WHEN COUNT(tool_read_calls) = COUNT(*) THEN SUM(tool_read_calls) END,
		       CASE WHEN COUNT(tool_git_calls) = COUNT(*) THEN SUM(tool_git_calls) END,
		       CASE WHEN COUNT(tool_other_calls) = COUNT(*) THEN SUM(tool_other_calls) END,
		       COALESCE(SUM(CASE WHEN model_roundtrips IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM agent_invocations
		GROUP BY purpose
		ORDER BY SUM(duration_ms) DESC`)
	if err != nil {
		return nil, fmt.Errorf("agent invocation aggregates: %w", err)
	}
	defer rows.Close()

	var aggregates []AgentInvocationAggregate
	for rows.Next() {
		var a AgentInvocationAggregate
		if err := rows.Scan(
			&a.Purpose, &a.Count, &a.TotalDurationMS, &a.SubprocessWaitMS,
			&a.Cold, &a.Started, &a.Resumed, &a.Fallback, &a.Errors,
			&a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheCreationTokens,
			&a.FreshInputTokens, &a.ReasoningTokens, &a.ModelRoundtrips, &a.ToolCalls,
			&a.ToolWaitCalls, &a.ToolTestLintCalls, &a.ToolEditCalls, &a.ToolReadCalls, &a.ToolGitCalls, &a.ToolOtherCalls,
			&a.MetricsRows,
		); err != nil {
			return nil, fmt.Errorf("scan agent invocation aggregate: %w", err)
		}
		if a.Count > 0 {
			a.AvgDurationMS = a.TotalDurationMS / int64(a.Count)
		}
		aggregates = append(aggregates, a)
	}
	return aggregates, rows.Err()
}

type RunInvocationSummary struct {
	Count           int
	Resumed         int
	Fallback        int
	TotalDurationMS int64
}

func (d *DB) AgentInvocationSummaryForRun(runID string) (RunInvocationSummary, error) {
	var s RunInvocationSummary
	err := d.sql.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN session_mode = 'resumed' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN session_mode = 'fallback' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(duration_ms), 0)
		FROM agent_invocations WHERE run_id = ?`, runID).
		Scan(&s.Count, &s.Resumed, &s.Fallback, &s.TotalDurationMS)
	if err != nil {
		return RunInvocationSummary{}, fmt.Errorf("agent invocation summary: %w", err)
	}
	return s, nil
}
