package ipc

import (
	"encoding/json"
	"sync/atomic"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/kunchenguid/no-mistakes/internal/verificationplan"
)

const MaxFrameBytes = 1024 * 1024

const (
	MethodPushReceived              = "push_received"
	MethodResolvePiProfile          = "resolve_pi_profile"
	MethodResolveReviewerProfile    = "resolve_reviewer_profile"
	MethodProbeOmitIntent           = "probe_omit_intent"
	MethodProbeRunPRText            = "probe_run_pr_text"
	MethodProbeTranscriptHomes      = "probe_transcript_homes"
	MethodProbeCommitIdentity       = "probe_commit_identity"
	MethodReleaseVerificationPlan   = "release_verification_plan"
	MethodCaptureVerificationPlan   = "capture_verification_plan"
	MethodStartFreshRun             = "start_fresh_run"
	MethodClaimLaunchReceipt        = "claim_launch_receipt"
	MethodGetRun                    = "get_run"
	MethodGetStepDiff               = "get_step_diff"
	MethodGetRuns                   = "get_runs"
	MethodGetRunsForHead            = "get_runs_for_head"
	MethodGetActiveRun              = "get_active_run"
	MethodRerun                     = "rerun"
	MethodRetryRun                  = "retry_run"
	MethodSubscribe                 = "subscribe"
	MethodRespond                   = "respond"
	MethodAnswerReview              = "answer_review_question"
	MethodCancelRun                 = "cancel_run"
	MethodHandoffRun                = "handoff_run"
	MethodGateContext               = "gate_context"
	MethodAdmitPush                 = "admit_push"
	MethodHealth                    = "health"
	MethodShutdown                  = "shutdown"
	MethodUpdateRunClosingIssueRefs = "update_run_closing_issue_refs"
	MethodScheduleRestart           = "schedule_restart"
	MethodRestartStatus             = "restart_status"
)

const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

type Request struct {
	JSONRPC          string          `json:"jsonrpc"`
	Method           string          `json:"method"`
	Params           json.RawMessage `json:"params,omitempty"`
	ID               int64           `json:"id"`
	DeadlineUnixNano int64           `json:"deadline_unix_nano,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      int64           `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// --- Method parameters ---

type PushReceivedParams struct {
	CommitIdentity         *git.CommitIdentity       `json:"commit_identity,omitempty"`
	TranscriptHomes        *runenv.TranscriptHomes   `json:"transcript_homes,omitempty"`
	ReviewerProfile        *agentcfg.ReviewerProfile `json:"reviewer_profile,omitempty"`
	VerificationPlanID     string                    `json:"verification_plan_id,omitempty"`
	PiProfile              *agentcfg.PiProfile       `json:"pi_profile,omitempty"`
	Gate                   string                    `json:"gate"`
	Ref                    string                    `json:"ref"`
	Old                    string                    `json:"old"`
	New                    string                    `json:"new"`
	SkipSteps              []types.StepName          `json:"skip_steps,omitempty"`
	Intent                 string                    `json:"intent,omitempty"`
	LaunchNonce            string                    `json:"launch_nonce,omitempty"`
	ValidationGeneration   string                    `json:"validation_generation,omitempty"`
	PRBaseBranch           string                    `json:"pr_base_branch,omitempty"`
	OmitIntent             bool                      `json:"omit_intent,omitempty"`
	ReconciledPreviousHead string                    `json:"reconciled_previous_head,omitempty"`
	ClosingIssueRefs       []string                  `json:"closing_issue_refs,omitempty"`
	PRTitle                string                    `json:"pr_title,omitempty"`
	PRBody                 string                    `json:"pr_body,omitempty"`
}

type StartFreshRunParams struct {
	CommitIdentity     *git.CommitIdentity       `json:"commit_identity,omitempty"`
	TranscriptHomes    *runenv.TranscriptHomes   `json:"transcript_homes,omitempty"`
	ReviewerProfile    *agentcfg.ReviewerProfile `json:"reviewer_profile,omitempty"`
	VerificationPlanID string                    `json:"verification_plan_id,omitempty"`
	PiProfile          *agentcfg.PiProfile       `json:"pi_profile,omitempty"`

	RepoID               string           `json:"repo_id"`
	Branch               string           `json:"branch"`
	HeadSHA              string           `json:"head_sha"`
	SkipSteps            []types.StepName `json:"skip_steps,omitempty"`
	Intent               string           `json:"intent"`
	LaunchNonce          string           `json:"launch_nonce"`
	ValidationGeneration string           `json:"validation_generation"`
	PRBaseBranch         string           `json:"pr_base_branch,omitempty"`
	OmitIntent           bool             `json:"omit_intent,omitempty"`
	ClosingIssueRefs     []string         `json:"closing_issue_refs,omitempty"`
	PRTitle              string           `json:"pr_title,omitempty"`
	PRBody               string           `json:"pr_body,omitempty"`
}

type CaptureVerificationPlanParams struct {
	SourcePath string `json:"source_path"`
	RepoID     string `json:"repo_id"`
	Branch     string `json:"branch"`
	HeadSHA    string `json:"head_sha"`
}

type ReleaseVerificationPlanParams struct {
	CaptureID string `json:"capture_id"`
	RepoID    string `json:"repo_id"`
	Branch    string `json:"branch"`
	HeadSHA   string `json:"head_sha"`
}

type ProbeOmitIntentParams struct{}

type ProbeOmitIntentResult struct {
	OK bool `json:"ok"`
}

type ProbeRunPRTextParams struct{}

type ProbeRunPRTextResult struct {
	OK bool `json:"ok"`
}

type ProbeTranscriptHomesResult struct {
	OK bool `json:"ok"`
}

type ClaimLaunchReceiptParams struct {
	ReviewerProfile *agentcfg.ReviewerProfile `json:"reviewer_profile,omitempty"`
	PiProfile       *agentcfg.PiProfile       `json:"pi_profile,omitempty"`

	RepoID               string `json:"repo_id"`
	Branch               string `json:"branch"`
	LaunchNonce          string `json:"launch_nonce"`
	SubmittedHeadSHA     string `json:"submitted_head_sha"`
	ValidationGeneration string `json:"validation_generation"`
	IntentDigest         string `json:"intent_digest"`
	PRBaseBranch         string `json:"pr_base_branch,omitempty"`
	OmitIntent           bool   `json:"omit_intent,omitempty"`
	PRTextDigest         string `json:"pr_text_digest,omitempty"`
}

type GetRunParams struct {
	RunID string `json:"run_id"`
}

type GetStepDiffParams struct {
	RunID string `json:"run_id"`
}

type GetStepDiffResult struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated,omitempty"`
}

type GetRunsParams struct {
	RepoID string `json:"repo_id"`
}

type GetRunsForHeadParams struct {
	RepoID  string `json:"repo_id"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
}

type GetActiveRunParams struct {
	RepoID string `json:"repo_id"`
	Branch string `json:"branch,omitempty"`
}

type RerunParams struct {
	CommitIdentity     *git.CommitIdentity       `json:"commit_identity,omitempty"`
	TranscriptHomes    *runenv.TranscriptHomes   `json:"transcript_homes,omitempty"`
	FreshLaunch        bool                      `json:"fresh_launch,omitempty"`
	ReviewerProfile    *agentcfg.ReviewerProfile `json:"reviewer_profile,omitempty"`
	VerificationPlanID string                    `json:"verification_plan_id,omitempty"`
	PiProfile          *agentcfg.PiProfile       `json:"pi_profile,omitempty"`

	RepoID           string           `json:"repo_id"`
	Branch           string           `json:"branch"`
	PreviousRunID    string           `json:"previous_run_id,omitempty"`
	SkipSteps        []types.StepName `json:"skip_steps,omitempty"`
	Intent           string           `json:"intent,omitempty"`
	PRBaseBranch     string           `json:"pr_base_branch,omitempty"`
	OmitIntent       bool             `json:"omit_intent,omitempty"`
	CallerHeadSHA    string           `json:"caller_head_sha,omitempty"`
	ClosingIssueRefs []string         `json:"closing_issue_refs,omitempty"`
	PRTitle          string           `json:"pr_title,omitempty"`
	PRBody           string           `json:"pr_body,omitempty"`
}

type RetryRunParams struct {
	RepoID        string `json:"repo_id"`
	Branch        string `json:"branch"`
	RunID         string `json:"run_id"`
	CallerHeadSHA string `json:"caller_head_sha,omitempty"`
}

type RetryRunResult struct {
	RunID       string         `json:"run_id"`
	SourceRunID string         `json:"source_run_id"`
	Step        types.StepName `json:"step"`
	SourceRound int            `json:"source_round"`
}

type SubscribeParams struct {
	RunID string `json:"run_id"`
}

type RespondParams struct {
	Origin                 types.ResponseOrigin `json:"origin,omitempty"`
	Coverage               *CoverageBinding     `json:"coverage,omitempty"`
	RunID                  string               `json:"run_id"`
	Step                   types.StepName       `json:"step"`
	Action                 types.ApprovalAction `json:"action"`
	FindingIDs             []string             `json:"finding_ids,omitempty"`
	IgnoreFindingIDs       []string             `json:"ignore_finding_ids,omitempty"`
	Instructions           map[string]string    `json:"instructions,omitempty"`
	InstructionsFromDriver bool                 `json:"instructions_from_driver,omitempty"`
	AddedFindings          []types.Finding      `json:"added_findings,omitempty"`
	ApprovalReason         string               `json:"approval_reason,omitempty"` // Test approval, or the reason recorded with an abort
}

type CoverageBinding struct {
	HeadSHA      string `json:"head_sha"`
	StepResultID string `json:"step_result_id"`
	RoundID      string `json:"round_id"`
}

type CancelRunParams struct {
	RunID string `json:"run_id"`
}

type GateContextParams struct {
	CWD           string `json:"cwd,omitempty"`
	MarkerPresent bool   `json:"marker_present,omitempty"`
}

type AdmitPushParams struct {
	Gate            string                    `json:"gate"`
	ReviewerProfile *agentcfg.ReviewerProfile `json:"reviewer_profile,omitempty"`
}

type ResolveReviewerProfileParams struct {
	RepoID          string                    `json:"repo_id"`
	ReviewerProfile *agentcfg.ReviewerProfile `json:"reviewer_profile"`
}

type HealthParams struct{}

type ShutdownParams struct{}

type ScheduleRestartParams struct{}

type RestartStatusResult struct {
	Pending     bool `json:"pending"`
	WaitingRuns int  `json:"waiting_runs"`
	HeldPushes  int  `json:"held_pushes"`
}

type UpdateRunClosingIssueRefsParams struct {
	RunID            string   `json:"run_id"`
	ClosingIssueRefs []string `json:"closing_issue_refs"`
}

const ClosingIssueRefsRejectedPRBodyComposed = "pr_body_already_composed"

type UpdateRunClosingIssueRefsResult struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// --- Method results ---

type PushReceivedResult struct {
	RunID string `json:"run_id"`
}

type LaunchReceipt struct {
	ReviewerProfile *agentcfg.ReviewerProfile `json:"reviewer_profile,omitempty"`
	PiProfile       *agentcfg.PiProfile       `json:"pi_profile,omitempty"`

	RunID                string `json:"run_id"`
	Disposition          string `json:"disposition"`
	LaunchNonce          string `json:"launch_nonce"`
	ValidationGeneration string `json:"validation_generation"`
	Branch               string `json:"branch"`
	HeadSHA              string `json:"head_sha"`
	SubmittedHeadSHA     string `json:"submitted_head_sha"`
	IntentDigest         string `json:"intent_digest"`
}

type StartFreshRunResult struct {
	Receipt LaunchReceipt `json:"receipt"`
}

type ClaimLaunchReceiptResult struct {
	Receipt *LaunchReceipt `json:"receipt,omitempty"`
}

type GetRunResult struct {
	Run *RunInfo `json:"run"`
}

type GetRunsResult struct {
	Runs []RunInfo `json:"runs"`
}

type GetActiveRunResult struct {
	Run *RunInfo `json:"run,omitempty"`
}

type RerunResult struct {
	RunID string `json:"run_id"`
}

type RespondResult struct {
	OK                 bool     `json:"ok"`
	Fixed              []string `json:"fixed,omitempty"`
	Ignored            []string `json:"ignored,omitempty"`
	Kept               []string `json:"kept,omitempty"`
	Refusal            string   `json:"refusal,omitempty"`
	Missing            []string `json:"missing,omitempty"`
	DeclinedEarlierFix []string `json:"declined_earlier_fix,omitempty"`
	Help               string   `json:"help,omitempty"`
}

type AnswerReviewQuestionParams struct {
	RunID      string `json:"run_id"`
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by,omitempty"`
}

type AnswerReviewQuestionResult struct {
	OK         bool     `json:"ok"`
	Open       int      `json:"open"`
	OpenIDs    []string `json:"open_ids,omitempty"`
	Resumed    bool     `json:"resumed"`
	ClosedLast bool     `json:"closed_last,omitempty"`
	Note       string   `json:"note,omitempty"`
}

type CancelRunResult struct {
	OK bool `json:"ok"`
}

type GateContextResult struct {
	Nested           bool           `json:"nested"`
	ManagedGit       bool           `json:"managed_git,omitempty"`
	AgentDescendant  bool           `json:"agent_descendant,omitempty"`
	DaemonDescendant bool           `json:"daemon_descendant,omitempty"`
	MarkerPresent    bool           `json:"marker_present,omitempty"`
	RunID            string         `json:"run_id,omitempty"`
	Phase            types.StepName `json:"phase,omitempty"`
}

type AdmitPushResult struct {
	ReviewerValidated bool              `json:"reviewer_validated,omitempty"`
	Context           GateContextResult `json:"context"`
}

type HealthResult struct {
	Status string `json:"status"`
}

type ShutdownResult struct {
	OK bool `json:"ok"`
}

// --- Wire types ---

type RunInfo struct {
	ReviewerProfile  *agentcfg.ReviewerProfile  `json:"reviewer_profile,omitempty"`
	VerificationPlan *verificationplan.Snapshot `json:"verification_plan"`
	PiProfile        *agentcfg.PiProfile        `json:"pi_profile,omitempty"`

	ID                 string           `json:"id"`
	RepoID             string           `json:"repo_id"`
	Branch             string           `json:"branch"`
	HeadSHA            string           `json:"head_sha"`
	SubmittedHeadSHA   *string          `json:"submitted_head_sha,omitempty"`
	BaseSHA            string           `json:"base_sha"`
	Status             types.RunStatus  `json:"status"`
	PRURL              *string          `json:"pr_url,omitempty"`
	Error              *string          `json:"error,omitempty"`
	CIReady            bool             `json:"ci_ready,omitempty"`
	CIReadyNoCI        bool             `json:"ci_ready_no_ci,omitempty"`
	PRBaseBranch       *string          `json:"pr_base_branch,omitempty"`
	OmitIntent         bool             `json:"omit_intent,omitempty"`
	PRTitleDigest      string           `json:"pr_title_digest,omitempty"`
	PRBodyDigest       string           `json:"pr_body_digest,omitempty"`
	QuotaRouting       string           `json:"quota_routing,omitempty"`
	AwaitingAgent      bool             `json:"awaiting_agent,omitempty"`
	AwaitingAgentSince *int64           `json:"awaiting_agent_since,omitempty"`
	Steps              []StepResultInfo `json:"steps,omitempty"`
	CIOverrideReason   string           `json:"ci_override_reason,omitempty"`
	TestOverrideReason string           `json:"test_override_reason,omitempty"`
	RetryOf            *RunRetryInfo    `json:"retry_of,omitempty"`
	RetriedBy          string           `json:"retried_by,omitempty"`
	StateRev           int64            `json:"state_rev,omitempty"`
	CreatedAt          int64            `json:"created_at"`
	UpdatedAt          int64            `json:"updated_at"`
}

type RunRetryInfo struct {
	RunID             string         `json:"run_id"`
	Step              types.StepName `json:"step"`
	Round             int            `json:"round"`
	Error             string         `json:"error"`
	CheckpointHeadSHA string         `json:"checkpoint_head_sha"`
}

const WorkScopeDocumentLintHousekeeping = "document+lint housekeeping"

type StepResultInfo struct {
	LatestRoundID    string           `json:"latest_round_id,omitempty"`
	ID               string           `json:"id"`
	RunID            string           `json:"run_id"`
	StepName         types.StepName   `json:"step_name"`
	StepOrder        int              `json:"step_order"`
	Status           types.StepStatus `json:"status"`
	ExitCode         *int             `json:"exit_code,omitempty"`
	DurationMS       *int64           `json:"duration_ms,omitempty"`
	WorkScope        string           `json:"work_scope,omitempty"`
	FindingsJSON     *string          `json:"findings_json,omitempty"`
	ReportedFindings int              `json:"reported_findings,omitempty"`
	FixedFindings    int              `json:"fixed_findings,omitempty"`
	FixSummaries     []string         `json:"fix_summaries,omitempty"`
	RoundCount       int              `json:"round_count,omitempty"`
	FixRoundCount    int              `json:"fix_round_count,omitempty"`
	AutoFixLimit     int              `json:"auto_fix_limit,omitempty"`
	PendingFixSource string           `json:"pending_fix_source,omitempty"`
	Error            *string          `json:"error,omitempty"`
	StartedAt        *int64           `json:"started_at,omitempty"`
	RoundStartedAt   *int64           `json:"round_started_at,omitempty"`
	CompletedAt      *int64           `json:"completed_at,omitempty"`
	LastActivityAt   *int64           `json:"last_activity_at,omitempty"`
	LastActivity     *string          `json:"last_activity,omitempty"`
	AgentPID         *int             `json:"agent_pid,omitempty"`
	OverrideReason   string           `json:"override_reason,omitempty"`
	SkipReason       string           `json:"skip_reason,omitempty"`
}

// --- Events (for subscribe stream) ---

type EventType string

const (
	EventRunCreated         EventType = "run_created"
	EventRunUpdated         EventType = "run_updated"
	EventRunCompleted       EventType = "run_completed"
	EventCIReadinessChanged EventType = "ci_readiness_changed"
	EventStepStarted        EventType = "step_started"
	EventStepCompleted      EventType = "step_completed"
	EventLogChunk           EventType = "log_chunk"
	EventStreamGap          EventType = "stream_gap"
)

type Event struct {
	Type               EventType       `json:"type"`
	RunID              string          `json:"run_id"`
	RepoID             string          `json:"repo_id"`
	StepName           *types.StepName `json:"step_name,omitempty"`
	Status             *string         `json:"status,omitempty"`
	Error              *string         `json:"error,omitempty"`
	Stream             *string         `json:"stream,omitempty"`
	Content            *string         `json:"content,omitempty"`
	Branch             *string         `json:"branch,omitempty"`
	Findings           *string         `json:"findings,omitempty"` // JSON-encoded findings for step_completed events
	ReportedFindings   *int            `json:"reported_findings,omitempty"`
	FixedFindings      *int            `json:"fixed_findings,omitempty"`
	DurationMS         *int64          `json:"duration_ms,omitempty"` // execution-only duration for step events
	WorkScope          string          `json:"work_scope,omitempty"`  // shared work attributed to this step
	PRURL              *string         `json:"pr_url,omitempty"`      // PR URL for run_updated/run_completed events
	StateRev           int64           `json:"state_rev,omitempty"`
	CIReady            *bool           `json:"ci_ready,omitempty"`
	CIReadyNoCI        *bool           `json:"ci_ready_no_ci,omitempty"`
	CIOverrideReason   *string         `json:"ci_override_reason,omitempty"`
	TestOverrideReason *string         `json:"test_override_reason,omitempty"`
}

// --- Helpers ---

var reqID atomic.Int64

func NewRequest(method string, params interface{}) (*Request, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return &Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  raw,
		ID:      reqID.Add(1),
	}, nil
}

func NewResponse(id int64, result interface{}) (*Response, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{
		JSONRPC: "2.0",
		Result:  raw,
		ID:      id,
	}, nil
}

func NewErrorResponse(id int64, code int, message string) *Response {
	return &Response{
		JSONRPC: "2.0",
		Error:   &RPCError{Code: code, Message: message},
		ID:      id,
	}
}
