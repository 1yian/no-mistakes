package pipeline

import (
	"context"
	"errors"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/forgecontext"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

var ErrApprovalBlocked = errors.New("approval blocked by required review")

var ErrFatalGateReconciliation = errors.New("fatal gate reconciliation")

var ErrDaemonShutdown = errors.New("daemon shutting down")

var ErrHandoff = errors.New("green PR handed off for human review")

var ErrParkedAtShutdown = errors.New("run left parked at an approval gate by daemon shutdown")

type StepContext struct {
	Ctx                           context.Context
	Run                           *db.Run
	Repo                          *db.Repo
	WorkDir                       string
	GateDir                       string
	Agent                         agent.Agent
	Config                        *config.Config
	ForgeContext                  *forgecontext.Context
	DB                            *db.DB
	Log                           func(string) // discrete log line (newline-terminated, user-visible + file)
	LogChunk                      func(string) // raw streaming chunk (user-visible + file)
	LogFile                       func(string) // file-only log callback (not shown to user)
	Fixing                        bool         // true when re-executing after a "fix" action
	SkipFixExecution              bool         // replay an already-completed fix round's review turn only
	EvalReplay                    bool
	FinalizingAnswers             bool
	ReviewStartingHeadSHA         string
	CoverageContinuation          *db.StepRound
	CarriedFindings               string
	PreviousFindings              string // JSON findings selected for the current fix round
	DeferredFindings              string // JSON findings left unselected when the current fix round began
	AutoFixRound                  bool
	StepResultID                  string
	EvidenceDir                   string
	Env                           []string // extra environment variables for subprocesses (used in tests)
	UserIntent                    string
	PublicIntentBrief             string
	IntentSource                  string
	UncertifiedFromSHA            string
	UncertifiedToSHA              string
	UncertifiedSourceRunID        string
	UncertifiedPriorRounds        []*db.StepRound
	PriorBranchDecisions          []*db.BranchDecisionRound
	PriorBranchDecisionsTruncated bool
	PreviousRunReviewRounds       []*db.StepRound
	Sessions                      *RunSessions
	Shared                        *RunShared
	CIReadinessChanged            func(ready, declaredNoCI bool)
	SetCIReadiness                func(ready, declaredNoCI bool) error
	HandoffPending                func() bool
	SetPRState                    func(state string) error
	CIWake                        <-chan struct{}
	MarkRunning                   func() error
	OnPRMerged                    func(ctx context.Context, runID string)
	ClosingIssueRefs              []string

	crashRetryBaseSHA *string
}

func (sctx *StepContext) RunAgentSession(role SessionRole, opts agent.RunOpts) (*agent.Result, error) {
	return sctx.runAgent(sctx.Ctx, opts, role, 0, 0, nil)
}

type StepOutcome struct {
	CIReadyNoCI           *bool
	NeedsApproval         bool // whether the step pauses for user action
	AutoFixable           bool
	Findings              string // JSON findings for TUI display (optional)
	ReviewedPaths         []string
	ReviewablePaths       []string
	WithdrawnFindings     []types.WithdrawnFinding
	ExitCode              int    // process exit code (0 = success)
	PRURL                 string // PR/MR URL if this step created or found one
	Skipped               bool   // mark the step as skipped without failing the run
	SkipReason            string // automatic PR/CI skip cause; explicit per-run skips leave it empty
	SkipRemaining         bool   // skip all subsequent steps (e.g. empty diff after rebase)
	RestartFrom           types.StepName
	FixSummary            string
	RepairPublished       bool
	ReviewApprovedHeadSHA string

	DurationOverrideMS int64
}

type Step interface {
	Name() types.StepName

	Execute(sctx *StepContext) (*StepOutcome, error)
}

type ApprovalGateReconciler interface {
	ReconcileApprovalGate(sctx *StepContext) (resolved bool, err error)
}

type ApprovalGateResumer interface {
	ResumeApprovalGate(sctx *StepContext, findingsJSON string) (action types.ApprovalAction, resume bool, err error)
}

type ApprovalOverrideVerifier interface {
	VerifyApprovalOverride(sctx *StepContext) (unresolved string, err error)
}
