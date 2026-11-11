package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type perfRecordingAgent struct {
	inner    agent.Agent
	db       *db.DB
	runID    string
	stepName types.StepName
	round    func() int
}

func (a *perfRecordingAgent) Name() string { return a.inner.Name() }

func (a *perfRecordingAgent) Close() error { return a.inner.Close() }

func (a *perfRecordingAgent) SupportsSessionResume() bool {
	return agent.SupportsSessionResume(a.inner)
}

func (a *perfRecordingAgent) SupportsSessionProvider(provider string) bool {
	return agent.SupportsSessionProvider(a.inner, provider)
}

func (a *perfRecordingAgent) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	attempts := 0
	previous := opts.OnAttempt
	opts.OnAttempt = func(attempt agent.Attempt) {
		if previous != nil {
			previous(attempt)
		}
		attempts++
		attemptOpts := opts
		attemptOpts.ConfiguredModel = attempt.ConfiguredModel
		attemptOpts.Session = attempt.Session
		attemptOpts.SessionFallback = attempt.SessionFallback
		a.record(ctx, attemptOpts, attempt.Agent, attempt.Result, attempt.Err, attempt.StartedAt, attempt.CompletedAt)
	}
	start := time.Now()
	result, err := a.inner.Run(ctx, opts)
	if attempts == 0 {
		a.record(ctx, opts, a.inner.Name(), result, err, start, time.Now())
	}
	return result, err
}

func (a *perfRecordingAgent) record(ctx context.Context, opts agent.RunOpts, agentName string, result *agent.Result, runErr error, startedAt, completedAt time.Time) {
	if a.db == nil {
		return
	}

	purpose := opts.Purpose
	if purpose == "" {
		purpose = string(a.stepName)
	}

	sessionKey := invocationSessionKey(opts, result)
	inv := db.AgentInvocation{
		RunID:       a.runID,
		StepName:    string(a.stepName),
		Round:       a.round(),
		Purpose:     purpose,
		Agent:       agentName,
		SessionMode: invocationSessionMode(opts, result, runErr),
		SessionKey:  sessionKey,
		StartedAt:   startedAt.Unix(),
		CompletedAt: completedAt.Unix(),
		DurationMS:  completedAt.Sub(startedAt).Milliseconds(),
		ExitStatus:  "ok",
	}
	if opts.ConfiguredModel != "" {
		model := opts.ConfiguredModel
		inv.ConfiguredModel = &model
	}
	if opts.SessionFallback && opts.SessionFallbackReason != "" {
		reason := opts.SessionFallbackReason
		inv.FallbackReason = &reason
	}
	if opts.Workload != nil {
		files, lines := opts.Workload.Files, opts.Workload.Lines
		inv.WorkloadFiles = &files
		inv.WorkloadLines = &lines
	}
	a.recordResult(&inv, sessionKey, result)
	if runErr != nil {
		if ctx.Err() != nil || errors.Is(runErr, context.Canceled) {
			inv.ExitStatus = "cancelled"
			inv.FailureCategory = "cancelled"
		} else {
			inv.ExitStatus = "error"
			inv.FailureCategory = classifyInvocationFailure(runErr)
		}
	}

	if _, dbErr := a.db.InsertAgentInvocation(inv); dbErr != nil {
		slog.Warn("failed to record agent invocation", "step", a.stepName, "error", dbErr)
	}
}

func (a *perfRecordingAgent) recordResult(inv *db.AgentInvocation, sessionKey string, result *agent.Result) {
	if result == nil {
		return
	}
	inv.Model = result.Model
	if result.ModelProvider != "" {
		provider := result.ModelProvider
		inv.ModelProvider = &provider
	}
	if result.UsageReported {
		in := result.Usage.InputTokens
		out := result.Usage.OutputTokens
		cache := result.Usage.CacheReadTokens
		inv.InputTokens = &in
		inv.OutputTokens = &out
		inv.CacheReadTokens = &cache
		fresh := agent.FreshInputTokens(in, cache)
		inv.FreshInputTokens = &fresh
	}

	if result.CacheCreationReported {
		cacheCreation := result.Usage.CacheCreationTokens
		inv.CacheCreationTokens = &cacheCreation
	}

	// Per-round deltas: for a resumed session whose raw counters are cumulative,
	// subtract the same session's prior cumulative so the row cannot be mistaken
	// for per-round usage. Read the prior BEFORE this row is inserted.
	if result.UsageReported {
		priorInput, priorOutput, priorCache, _ := a.db.LatestSessionCumulative(a.runID, sessionKey)
		deltaInput := agent.PerRoundTokens(result.Usage.InputTokens, priorInput, result.SessionUsageCumulative)
		deltaOutput := agent.PerRoundTokens(result.Usage.OutputTokens, priorOutput, result.SessionUsageCumulative)
		deltaCache := agent.PerRoundTokens(result.Usage.CacheReadTokens, priorCache, result.SessionUsageCumulative)
		inv.DeltaInputTokens = &deltaInput
		inv.DeltaOutputTokens = &deltaOutput
		inv.DeltaCacheReadTokens = &deltaCache
	}

	if result.UsageReported && result.Usage.ReasoningReported {
		reasoning := result.Usage.ReasoningTokens
		inv.ReasoningTokens = &reasoning
	}

	if result.Metrics != nil {
		m := result.Metrics
		roundtrips := m.ModelRoundtrips
		inv.ModelRoundtrips = &roundtrips
		toolCalls := m.ToolCalls
		inv.ToolCalls = &toolCalls
		wait := m.ToolCategories.Wait
		testLint := m.ToolCategories.TestLint
		edit := m.ToolCategories.Edit
		read := m.ToolCategories.Read
		git := m.ToolCategories.Git
		other := m.ToolCategories.Other
		inv.ToolWaitCalls = &wait
		inv.ToolTestLintCalls = &testLint
		inv.ToolEditCalls = &edit
		inv.ToolReadCalls = &read
		inv.ToolGitCalls = &git
		inv.ToolOtherCalls = &other
		subprocessWait := m.SubprocessWaitMS
		inv.SubprocessWaitMS = &subprocessWait
	}

	if count, ok := countOutputFindings(result.Output); ok {
		inv.FindingCount = &count
	}
}

func countOutputFindings(output json.RawMessage) (int, bool) {
	if len(output) == 0 {
		return 0, false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(output, &envelope); err != nil {
		return 0, false
	}
	raw, ok := envelope["findings"]
	if !ok {
		return 0, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, false
	}
	return len(items), true
}

func invocationSessionMode(opts agent.RunOpts, result *agent.Result, runErr error) string {
	switch {
	case opts.SessionFallback:
		return db.InvocationModeFallback
	case opts.Session == nil:
		return db.InvocationModeCold
	case opts.Session.ID != "":
		// A session was requested but the adapter did not report resuming it
		// (e.g. agy silently replaced a stale conversation with a fresh one).
		// Record as fallback so the stale-session path is not mistaken for a
		// successful resume. That absence is only evidence when the turn
		// succeeded, because most adapters set Resumed after finalizing and a
		// failed turn never gets there - unless the adapter positively named a
		// different session, which is evidence whether or not the turn failed.
		if result != nil && !result.Resumed &&
			(runErr == nil || (result.SessionID != "" && result.SessionID != opts.Session.ID)) {
			return db.InvocationModeFallback
		}
		return db.InvocationModeResumed
	default:
		return db.InvocationModeStarted
	}
}

func invocationSessionKey(opts agent.RunOpts, result *agent.Result) string {
	id := ""
	if result != nil && result.SessionID != "" {
		id = result.SessionID
	} else if opts.Session != nil && opts.Session.ID != "" {
		id = opts.Session.ID
	}
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

func classifyInvocationFailure(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "parse events") || strings.Contains(msg, "output parse"):
		return "parse"
	case strings.Contains(msg, "exited"):
		return "exit"
	case strings.Contains(msg, "start"):
		return "spawn"
	default:
		return "other"
	}
}

func classifyFallbackReason(err error) string {
	if err == nil {
		return db.FallbackReasonOther
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unexpected argument") || strings.Contains(msg, "unrecognized") ||
		strings.Contains(msg, "unknown flag") || strings.Contains(msg, "unexpected flag"):
		return db.FallbackReasonUnsupported
	case strings.Contains(msg, "parse events") || strings.Contains(msg, "output parse"):
		return db.FallbackReasonParse
	case strings.Contains(msg, "exited"):
		return db.FallbackReasonExit
	case strings.Contains(msg, "start"):
		return db.FallbackReasonSpawn
	case strings.Contains(msg, "temporarily") || strings.Contains(msg, "capacity") ||
		strings.Contains(msg, "rate limit") || strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "timeout"):
		return db.FallbackReasonTransient
	default:
		return db.FallbackReasonOther
	}
}
