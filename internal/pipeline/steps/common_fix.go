package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type fixExecutionOptions struct {
	RequirePreviousFindings   bool
	MissingFindingsError      string
	LogMessage                string
	Prompt                    string
	ErrorPrefix               string
	FallbackSummary           string
	AfterAgentRun             func(*agent.Result) error
	RunAgent                  func(agent.RunOpts) (*agent.Result, error)
	SessionRole               pipeline.SessionRole
	Purpose                   string
	Workload                  *agent.InvocationWorkload
	ReportBlockedVerification bool
}

type commitSummary struct {
	Summary string `json:"summary"`
}

var errRejectedCommitSummary = errors.New("rejected commit summary")

const (
	NoChangesAppliedSummary = "no changes applied"
	changesAppliedSummary   = "changes applied"
)

const fixerRemovalRule = `

Removal-first rule:
- When a problem can be solved by removing a code path that is not strictly required to satisfy the intent - an extra acceptance or matching branch, a fallback, an alias, a second definition of something the code already defines once, or handling for an input nobody intends - fix it by removing that path, not by validating, hardening, or documenting it. Judge what the intent strictly requires against the User intent section when present, otherwise against the change's own stated purpose. Later recorded human fix decisions supersede conflicting original intent. Removal is the smallest fix for such a path: hardening it leaves the unrequired path in place for the next review to find another hole in.`

func fixerPrompt(prompt string) string {
	return prompt + fixerRemovalRule + agent.MemoryFilesRule
}

const (
	commitSubjectLimit      = 72
	minFixSummaryBudget     = 20
	fixSummaryBudgetProbe   = "x"
	fixSummaryRuleTemplate  = "- The summary must be one concise sentence fragment suitable for a git commit subject.\n- Keep the summary under 10 words and at most %d characters: the commit subject adds a prefix and must stay within %d characters."
	fixSummaryRuleUnbounded = "- The summary must be one concise sentence fragment suitable for a git commit subject.\n- Keep the summary under 10 words."
)

func fixSummaryRules(sctx *pipeline.StepContext, stepName types.StepName) string {
	rendered, err := sctx.Config.Commit.RenderFixMessageForBranch(stepName, fixSummaryBudgetProbe, sctx.Run.Branch)
	if err != nil {
		return fixSummaryRuleUnbounded
	}
	doubled, err := sctx.Config.Commit.RenderFixMessageForBranch(stepName, fixSummaryBudgetProbe+fixSummaryBudgetProbe, sctx.Run.Branch)
	if err != nil {
		return fixSummaryRuleUnbounded
	}
	occurrences := utf8.RuneCountInString(doubled) - utf8.RuneCountInString(rendered)
	if occurrences < 1 {
		return fixSummaryRuleUnbounded
	}
	fixed := utf8.RuneCountInString(rendered) - occurrences*len(fixSummaryBudgetProbe)
	budget := (commitSubjectLimit - fixed) / occurrences
	return fmt.Sprintf(fixSummaryRuleTemplate, max(budget, minFixSummaryBudget), commitSubjectLimit)
}

var commitSummarySchema = json.RawMessage(fmt.Sprintf(`{
	"type": "object",
	"properties": {
		"summary": {"type": "string", "maxLength": %d}
	},
	"required": ["summary"]
}`, config.MaxFixMessageSummaryBytes))

const maxVerificationBlockedBytes = 300

var fixSummarySchema = json.RawMessage(fmt.Sprintf(`{
	"type": "object",
	"properties": {
		"summary": {"type": "string", "maxLength": %d},
		"verification_blocked": {"type": "string", "maxLength": %d}
	},
	"required": ["summary"]
}`, config.MaxFixMessageSummaryBytes, maxVerificationBlockedBytes))

func blockedVerification(result *agent.Result) string {
	if result == nil || result.Output == nil {
		return ""
	}
	var parsed struct {
		VerificationBlocked string `json:"verification_blocked"`
	}
	if err := json.Unmarshal(result.Output, &parsed); err != nil {
		return ""
	}
	blocked := strings.Join(strings.Fields(parsed.VerificationBlocked), " ")
	if len(blocked) > maxVerificationBlockedBytes {
		blocked = strings.ToValidUTF8(blocked[:maxVerificationBlockedBytes], "")
	}
	return blocked
}

func logBlockedVerification(sctx *pipeline.StepContext, blocked string, produced bool) {
	switch {
	case blocked == "":
	case produced:
		sctx.Log(fmt.Sprintf("fixes committed unverified, verification blocked: %s", blocked))
	default:
		sctx.Log(fmt.Sprintf("agent made no edits and reported verification blocked: %s; the findings were not fixed", blocked))
	}
}

func hasBlockingFindings(items []Finding) bool {
	for _, f := range items {
		if f.Severity == "error" || f.Severity == "warning" {
			return true
		}
	}
	return false
}

func reviewedPathsCoverReviewable(reviewedPaths, reviewablePaths []string) bool {
	allowed := make(map[string]bool, len(reviewablePaths))
	for _, candidate := range reviewablePaths {
		normalized := normalizeReviewedPath(candidate)
		if normalized == "" {
			return false
		}
		allowed[normalized] = true
	}
	covered := make(map[string]bool, len(reviewedPaths))
	for _, reviewed := range reviewedPaths {
		normalized := normalizeReviewedPath(reviewed)
		if normalized == "" || !allowed[normalized] {
			return false
		}
		covered[normalized] = true
	}
	for candidate := range allowed {
		if !covered[candidate] {
			return false
		}
	}
	return true
}

func uncoveredReviewablePaths(reviewedPaths, reviewablePaths []string) []string {
	covered := make(map[string]bool, len(reviewedPaths))
	for _, reviewed := range reviewedPaths {
		covered[normalizeReviewedPath(reviewed)] = true
	}
	var missing []string
	for _, candidate := range reviewablePaths {
		if !covered[normalizeReviewedPath(candidate)] {
			missing = append(missing, candidate)
		}
	}
	return missing
}

func hasInvalidReviewedPath(reviewedPaths []string) bool {
	for _, reviewed := range reviewedPaths {
		if normalizeReviewedPath(reviewed) == "" {
			return true
		}
	}
	return false
}

func mergeReviewedPaths(first, second []string) []string {
	seen := make(map[string]bool, len(first)+len(second))
	var merged []string
	invalidKept := false
	for _, path := range append(append([]string(nil), first...), second...) {
		key := normalizeReviewedPath(path)
		if key == "" {
			if invalidKept {
				continue
			}
			invalidKept = true
		} else {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		merged = append(merged, path)
	}
	return merged
}

func uncoveredReviewMessage(reviewedPaths, reviewablePaths []string) string {
	if reviewedPaths == nil {
		return fmt.Sprintf("review reported no reviewed_paths; parking for approval with %d reviewable file(s) unverified: %s", len(reviewablePaths), strings.Join(reviewablePaths, ", "))
	}
	allowed := make(map[string]bool, len(reviewablePaths))
	for _, candidate := range reviewablePaths {
		allowed[normalizeReviewedPath(candidate)] = true
	}
	covered := make(map[string]bool, len(reviewedPaths))
	var outOfScope []string
	for _, reviewed := range reviewedPaths {
		normalized := normalizeReviewedPath(reviewed)
		if normalized == "" {
			outOfScope = append(outOfScope, `""`)
			continue
		}
		if !allowed[normalized] {
			outOfScope = append(outOfScope, reviewed)
			continue
		}
		covered[normalized] = true
	}
	var missing []string
	for _, candidate := range reviewablePaths {
		if !covered[normalizeReviewedPath(candidate)] {
			missing = append(missing, candidate)
		}
	}
	msg := "review coverage is incomplete; parking for approval"
	if len(missing) > 0 {
		msg += fmt.Sprintf(" with %d reviewable file(s) unverified: %s", len(missing), strings.Join(missing, ", "))
	}
	if len(outOfScope) > 0 {
		msg += fmt.Sprintf("; %d reviewed_paths entry(ies) outside the reviewable set: %s", len(outOfScope), strings.Join(outOfScope, ", "))
	}
	return msg
}

func normalizeReviewedPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func assertPipelineHeadContinuity(sctx *pipeline.StepContext, stepName types.StepName) error {
	recorded := strings.TrimSpace(sctx.Run.HeadSHA)
	if recorded == "" {
		return nil
	}
	currentHead, err := git.HeadSHA(sctx.Ctx, sctx.WorkDir)
	if err != nil {
		return fmt.Errorf("resolve head before %s step: %w", stepName, err)
	}
	if currentHead == recorded {
		return nil
	}
	// Fail closed: refuse unless the recorded head is genuinely an ancestor of the
	// live HEAD (a legitimate forward move). A non-ancestor result OR any git error
	// (e.g. an unknown recorded object) aborts rather than proceeds.
	if _, err := git.Run(sctx.Ctx, sctx.WorkDir, "merge-base", "--is-ancestor", recorded, currentHead); err != nil {
		return fmt.Errorf("refusing to run %s step: worktree HEAD %s is not a descendant of the pipeline's recorded head %s; "+
			"the reviewed change was rewritten out-of-band and would be lost - aborting to protect it",
			stepName, currentHead, recorded)
	}
	return nil
}

func commitPipelineCorrection(ctx context.Context, workDir, message string, logf func(string)) error {
	return commitPipelineCorrectionWithCleanup(ctx, workDir, message, logf, os.RemoveAll)
}

func commitPipelineCorrectionWithCleanup(
	ctx context.Context,
	workDir, message string,
	logf func(string),
	cleanup func(string) error,
) error {
	gitRun := func(args ...string) (string, error) { return git.Run(ctx, workDir, args...) }
	staged, err := stagedChangesPresent(gitRun)
	if err != nil {
		return fmt.Errorf("inspect staged correction: %w", err)
	}
	if !staged {
		return nil
	}

	emptyHooksDir, err := os.MkdirTemp("", "no-mistakes-correction-hooks-")
	if err != nil {
		return fmt.Errorf("prepare hook-free commit environment: %w", err)
	}
	_, commitErr := git.Run(ctx, workDir, "-c", "core.hooksPath="+emptyHooksDir, "commit", "--no-verify", "-m", message)
	if cleanupErr := cleanup(emptyHooksDir); cleanupErr != nil {
		if logf != nil {
			logf(fmt.Sprintf("warning: failed to remove temporary hook-free commit directory %s: %v", emptyHooksDir, cleanupErr))
		} else {
			slog.Warn("failed to remove temporary hook-free commit directory", "path", emptyHooksDir, "error", cleanupErr)
		}
	}
	return commitErr
}

func withCommitTrailers(sctx *pipeline.StepContext, message string, producer *agent.Result) (string, error) {
	if producer == nil || len(sctx.Config.Commit.Trailers) == 0 {
		return message, nil
	}
	// A fallback chain reports the agent that actually ran in Provider; a bare
	// adapter leaves it empty, and then the step agent is the one that ran.
	agentName := producer.Provider
	if agentName == "" && sctx.Agent != nil {
		agentName = sctx.Agent.Name()
	}
	trailers, err := sctx.Config.Commit.RenderTrailers(config.TrailerData{Agent: agentName, Model: producer.Model})
	if err != nil {
		return "", err
	}
	return message + "\n\n" + strings.Join(trailers, "\n"), nil
}

func stagedChangesPresent(gitRun gitRunner) (bool, error) {
	staged, err := gitRun("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return false, err
	}
	return staged != "", nil
}

func commitAgentFixes(sctx *pipeline.StepContext, stepName types.StepName, summary, fallbackSummary string) error {
	_, err := commitAgentFixesWithResult(sctx, stepName, summary, fallbackSummary, nil)
	return err
}

func commitAgentFixesWithResult(sctx *pipeline.StepContext, stepName types.StepName, summary, fallbackSummary string, producer *agent.Result) (bool, error) {
	ctx := sctx.Ctx
	if err := assertPipelineHeadContinuity(sctx, stepName); err != nil {
		return false, err
	}
	render := func() (string, error) {
		return renderFixCommitMessage(sctx, stepName, summary, fallbackSummary, producer)
	}
	if err := conformAgentCommits(sctx, stepName, render); err != nil {
		return false, err
	}
	status, err := git.Run(ctx, sctx.WorkDir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("check %s changes: %w", stepName, err)
	}
	if strings.TrimSpace(status) == "" {
		sctx.Log("no agent changes to commit")
		headSHA, err := git.HeadSHA(ctx, sctx.WorkDir)
		if err != nil {
			return false, fmt.Errorf("resolve agent head: %w", err)
		}
		return false, recordAgentFixHead(sctx, stepName, headSHA)
	}
	commitMessage, err := render()
	if err != nil {
		return false, err
	}
	if err := stagePipelineChanges(sctx); err != nil {
		return false, fmt.Errorf("stage %s changes: %w", stepName, err)
	}
	headBeforeCommit, err := git.HeadSHA(ctx, sctx.WorkDir)
	if err != nil {
		return false, fmt.Errorf("resolve head before %s commit: %w", stepName, err)
	}
	if err := commitPipelineCorrection(ctx, sctx.WorkDir, commitMessage, sctx.Log); err != nil {
		return false, fmt.Errorf("commit %s changes: %w", stepName, err)
	}
	headSHA, err := git.HeadSHA(ctx, sctx.WorkDir)
	if err != nil {
		return false, fmt.Errorf("resolve head after %s commit: %w", stepName, err)
	}
	// An empty staged index is a successful no-op, not a commit. Reporting it
	// as one would claim a head advance that never happened.
	if headSHA == headBeforeCommit {
		sctx.Log("no staged agent changes to commit")
	} else {
		sctx.Log(fmt.Sprintf("committed agent fixes: %s", commitMessage))
	}
	if err := recordAgentFixHead(sctx, stepName, headSHA); err != nil {
		return false, err
	}
	return headSHA != headBeforeCommit, nil
}

func renderFixCommitMessage(sctx *pipeline.StepContext, stepName types.StepName, summary, fallbackSummary string, producer *agent.Result) (string, error) {
	if summary == "" {
		summary = fallbackSummary
	}
	if summary == "" {
		summary = "apply fixes"
	}
	message, err := sctx.Config.Commit.RenderFixMessageForBranch(stepName, summary, sctx.Run.Branch)
	if err != nil {
		return "", fmt.Errorf("render %s fix commit message: %w", stepName, err)
	}
	message, err = withCommitTrailers(sctx, message, producer)
	if err != nil {
		return "", fmt.Errorf("render %s fix commit trailers: %w", stepName, err)
	}
	return message, nil
}

func recordAgentFixHead(sctx *pipeline.StepContext, stepName types.StepName, headSHA string) error {
	if headSHA == sctx.Run.HeadSHA {
		return nil
	}
	if err := assertPipelineHeadContinuity(sctx, stepName); err != nil {
		return err
	}
	if err := updateNonSharedBranchRef(sctx, headSHA); err != nil {
		return err
	}
	startingHead := strings.TrimSpace(sctx.ReviewStartingHeadSHA)
	if startingHead == "" {
		startingHead = sctx.Run.HeadSHA
	}
	if err := sctx.DB.UpdateRunHeadSHA(sctx.Run.ID, headSHA); err != nil {
		return err
	}
	sctx.Run.HeadSHA = headSHA
	if stepName == types.StepReview {
		pipeline.PersistUncertifiedPipelineRange(sctx, startingHead, headSHA)
	}
	return nil
}

func fixResultSummary(committed bool) string {
	if committed {
		return changesAppliedSummary
	}
	return NoChangesAppliedSummary
}

func extractCommitSummary(result *agent.Result) (string, error) {
	var summary commitSummary
	if result.Output == nil {
		return "", fmt.Errorf("agent returned no structured summary")
	}
	if !utf8.Valid(result.Output) {
		return "", fmt.Errorf("%w: agent output must contain valid UTF-8", errRejectedCommitSummary)
	}
	if err := json.Unmarshal(result.Output, &summary); err != nil {
		return "", fmt.Errorf("parse commit summary: %w", err)
	}
	if len(summary.Summary) > config.MaxFixMessageSummaryBytes {
		return "", fmt.Errorf("%w: commit summary must not exceed %d bytes", errRejectedCommitSummary, config.MaxFixMessageSummaryBytes)
	}
	cleaned := strings.Join(strings.Fields(summary.Summary), " ")
	cleaned = strings.Trim(cleaned, " \t\r\n\"'.;:,-")
	return cleaned, nil
}

func executeFixMode(sctx *pipeline.StepContext, stepName types.StepName, opts fixExecutionOptions) (string, error) {
	if !sctx.Fixing {
		return "", nil
	}
	if opts.RequirePreviousFindings && sctx.PreviousFindings == "" {
		return "", errors.New(opts.MissingFindingsError)
	}
	if opts.LogMessage != "" {
		sctx.Log(opts.LogMessage)
	}
	purpose := opts.Purpose
	if purpose == "" {
		purpose = string(stepName) + "-fix"
	}
	schema := commitSummarySchema
	if opts.ReportBlockedVerification {
		schema = fixSummarySchema
	}
	runOpts := agent.RunOpts{
		Prompt:     fixerPrompt(opts.Prompt),
		CWD:        sctx.WorkDir,
		JSONSchema: schema,
		OnChunk:    sctx.LogChunk,
		Purpose:    purpose,
		Workload:   opts.Workload,
	}
	var err error
	headBeforeAgent := ""
	if opts.ReportBlockedVerification {
		headBeforeAgent, err = git.HeadSHA(sctx.Ctx, sctx.WorkDir)
		if err != nil {
			return "", fmt.Errorf("resolve head before %s fix: %w", stepName, err)
		}
	}
	var result *agent.Result
	if opts.RunAgent != nil {
		result, err = opts.RunAgent(runOpts)
	} else {
		result, err = sctx.RunAgentSessionContext(sctx.Ctx, opts.SessionRole, runOpts)
	}
	if err != nil {
		if opts.ErrorPrefix == "" {
			return "", err
		}
		return "", fmt.Errorf("%s: %w", opts.ErrorPrefix, err)
	}
	if opts.AfterAgentRun != nil {
		if err := opts.AfterAgentRun(result); err != nil {
			return "", err
		}
	}
	summary, err := extractCommitSummary(result)
	if err != nil {
		if errors.Is(err, errRejectedCommitSummary) {
			return "", fmt.Errorf("validate %s fix summary: %w", stepName, err)
		}
		sctx.Log(fmt.Sprintf("warning: could not parse fix summary: %v", err))
	}
	committed, err := commitAgentFixesWithResult(sctx, stepName, summary, opts.FallbackSummary, result)
	if err != nil {
		return "", err
	}
	if opts.ReportBlockedVerification {
		headAfterAgent, err := git.HeadSHA(sctx.Ctx, sctx.WorkDir)
		if err != nil {
			return "", fmt.Errorf("resolve head after %s fix: %w", stepName, err)
		}
		logBlockedVerification(sctx, blockedVerification(result), committed || headAfterAgent != headBeforeAgent)
	}
	return fixResultSummary(committed), nil
}

func updateNonSharedBranchRef(sctx *pipeline.StepContext, headSHA string) error {
	shared, err := worktreeSharesGateRefs(sctx)
	if err != nil || shared {
		return err
	}
	if _, err := stepGitRun(sctx, "update-ref", normalizedBranchRef(sctx.Run.Branch), headSHA); err != nil {
		return fmt.Errorf("update local branch ref: %w", err)
	}
	return nil
}

func worktreeSharesGateRefs(sctx *pipeline.StepContext) (bool, error) {
	if strings.TrimSpace(sctx.GateDir) == "" {
		return false, nil
	}
	gateInfo, err := os.Stat(sctx.GateDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect gate ref storage: %w", err)
	}
	commonDir, err := stepGitRun(sctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false, fmt.Errorf("resolve worktree ref storage: %w", err)
	}
	commonInfo, err := os.Stat(commonDir)
	if err != nil {
		return false, fmt.Errorf("inspect worktree ref storage: %w", err)
	}
	return os.SameFile(gateInfo, commonInfo), nil
}
