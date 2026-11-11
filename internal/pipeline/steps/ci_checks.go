package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type lastFixedIssues struct {
	Checks          []scm.CheckTarget `json:"checks,omitempty"`
	MergeConflict   bool              `json:"mergeConflict,omitempty"`
	ConflictBaseSHA string            `json:"conflictBaseSHA,omitempty"`
}

func pollInterval(elapsed time.Duration) time.Duration {
	switch {
	case elapsed < 5*time.Minute:
		return 30 * time.Second
	case elapsed < 15*time.Minute:
		return 60 * time.Second
	default:
		return 120 * time.Second
	}
}

func hasFailingChecks(checks []scm.Check) bool {
	for _, c := range checks {
		if c.Failing() {
			return true
		}
	}
	return false
}

func hasPendingChecks(checks []scm.Check) bool {
	for _, c := range checks {
		if c.Pending() {
			return true
		}
	}
	return false
}

func hasExecutingPendingChecks(checks []scm.Check) bool {
	for _, c := range checks {
		if c.Pending() && !c.HeldForHuman() {
			return true
		}
	}
	return false
}

func hasAwaitingApprovalChecks(checks []scm.Check) bool {
	for _, c := range checks {
		if c.AwaitingApproval && c.Pending() {
			return true
		}
	}
	return false
}

func hasAwaitingManualJobChecks(checks []scm.Check) bool {
	for _, c := range checks {
		if c.AwaitingManualJob && c.Pending() {
			return true
		}
	}
	return false
}

func hasUnresolvedChecks(checks []scm.Check) bool {
	for _, c := range checks {
		switch c.Bucket {
		case scm.CheckBucketPass, scm.CheckBucketFail, scm.CheckBucketSkip:
		default:
			return true
		}
	}
	return false
}

func allChecksPassed(checks []scm.Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if c.Bucket != scm.CheckBucketPass && c.Bucket != scm.CheckBucketSkip {
			return false
		}
	}
	return true
}

func failingCheckNames(checks []scm.Check) []string {
	var names []string
	for _, c := range checks {
		if c.Failing() {
			names = append(names, c.Name)
		}
	}
	return names
}

func unresolvedCheckNames(checks []scm.Check) []string {
	var names []string
	for _, c := range checks {
		if c.Bucket == scm.CheckBucketPass || c.Bucket == scm.CheckBucketSkip {
			continue
		}
		names = append(names, fmt.Sprintf("%s (%s)", c.Name, c.Bucket))
	}
	return names
}

type checkFreshness struct {
	CompletedAt time.Time
	ExecutionID string
}

func terminalFailureCompletionTimes(checks []scm.Check) map[string]checkFreshness {
	freshness := make(map[string]checkFreshness)
	for _, c := range checks {
		if !checkFailedTerminally(c) {
			continue
		}
		executionID := c.ExecutionID
		if executionID == "" {
			executionID = c.ProviderID
		}
		if c.CompletedAt.IsZero() && executionID == "" {
			continue
		}
		key := checkTrackingKey(c)
		previous := freshness[key]
		if previous.CompletedAt.IsZero() || c.CompletedAt.After(previous.CompletedAt) {
			freshness[key] = checkFreshness{CompletedAt: c.CompletedAt, ExecutionID: executionID}
		}
	}
	if len(freshness) == 0 {
		return nil
	}
	return freshness
}

func completionTimesForTargets(completedAt map[string]checkFreshness, targets []scm.CheckTarget) map[string]checkFreshness {
	selected := make(map[string]checkFreshness, len(targets))
	for _, target := range targets {
		key := checkTargetTrackingKey(target)
		if freshness, ok := completedAt[key]; ok {
			selected[key] = freshness
		}
	}
	if len(selected) == 0 {
		return nil
	}
	return selected
}

func terminalFailureCompletedAfter(checks []scm.Check, after map[string]checkFreshness) bool {
	if len(after) == 0 {
		return false
	}
	for _, c := range checks {
		if !checkFailedTerminally(c) {
			continue
		}
		previous, ok := after[checkTrackingKey(c)]
		if !ok {
			continue
		}
		executionID := c.ExecutionID
		if executionID == "" {
			executionID = c.ProviderID
		}
		if executionID != "" && previous.ExecutionID != "" && executionID != previous.ExecutionID {
			return true
		}
		if !c.CompletedAt.IsZero() && c.CompletedAt.After(previous.CompletedAt) {
			return true
		}
	}
	return false
}

func pendingCheckMatchesLastFixed(checks []scm.Check, lastFixedChecks string) bool {
	issues, ok := decodeLastFixedChecks(lastFixedChecks)
	if !ok {
		return false
	}

	if len(issues.Checks) == 0 {
		return issues.MergeConflict && hasPendingChecks(checks)
	}

	for _, c := range checks {
		if !c.Pending() {
			continue
		}
		for _, target := range issues.Checks {
			if checkMatchesTarget(c, target) {
				return true
			}
		}
	}

	return false
}

func encodeLastFixedChecks(checks []scm.CheckTarget, mergeConflict bool, conflictBaseSHA string) string {
	if len(checks) == 0 && !mergeConflict {
		return ""
	}
	encoded, err := json.Marshal(lastFixedIssues{Checks: checks, MergeConflict: mergeConflict, ConflictBaseSHA: conflictBaseSHA})
	if err != nil {
		return ""
	}
	return string(encoded)
}

func decodeLastFixedChecks(raw string) (lastFixedIssues, bool) {
	if raw == "" {
		return lastFixedIssues{}, false
	}
	var issues lastFixedIssues
	if err := json.Unmarshal([]byte(raw), &issues); err != nil {
		return lastFixedIssues{}, false
	}
	if len(issues.Checks) == 0 && !issues.MergeConflict {
		return lastFixedIssues{}, false
	}
	return issues, true
}

func (s *CIStep) lastRepairStillUnverified(checks []scm.Check, mergeConflict bool, conflictBaseSHA string) bool {
	issues, ok := decodeLastFixedChecks(s.lastFixedChecks)
	if !ok {
		return false
	}
	if issues.MergeConflict {
		if !mergeConflict {
			return false
		}
		if issues.ConflictBaseSHA != "" && conflictBaseSHA != "" && issues.ConflictBaseSHA != conflictBaseSHA {
			return false
		}
	}
	for _, target := range issues.Checks {
		if !checkTargetFailedTerminally(checks, target) {
			return false
		}
	}
	return true
}

func checkTargetStillFailing(checks []scm.Check, target scm.CheckTarget) bool {
	for _, check := range checks {
		if checkMatchesTarget(check, target) && check.Failing() {
			return true
		}
	}
	return false
}

func checkTargetFailedTerminally(checks []scm.Check, target scm.CheckTarget) bool {
	for _, check := range checks {
		if checkMatchesTarget(check, target) && checkFailedTerminally(check) {
			return true
		}
	}
	return false
}

func checkMatchesTarget(check scm.Check, target scm.CheckTarget) bool {
	if target.ProviderID != "" {
		return check.ProviderID == target.ProviderID
	}
	return check.Name == target.Name
}

func checkTrackingKey(check scm.Check) string {
	return checkTargetTrackingKey(scm.CheckTarget{Name: check.Name, ProviderID: check.ProviderID})
}

func checkTargetTrackingKey(target scm.CheckTarget) string {
	if target.ProviderID != "" {
		return "id:" + target.ProviderID
	}
	return target.Name
}

func ciFailureOutcome(failing []scm.CheckTarget, mergeConflict bool, summary string) *pipeline.StepOutcome {
	findings := Findings{Summary: summary}
	for _, target := range failing {
		findings.Items = append(findings.Items, Finding{
			Severity:    "warning",
			Description: fmt.Sprintf("CI check failing: %s", target.Name),
			Action:      types.ActionAskUser,
			Category:    types.FindingCategoryCICheck,
			Check:       target.Name,
			CheckID:     target.ProviderID,
		})
	}
	if mergeConflict {
		findings.Items = append(findings.Items, Finding{
			Severity:    "warning",
			Description: "PR has merge conflicts with the base branch",
			Action:      types.ActionAskUser,
			Category:    types.FindingCategoryCIMergeConflict,
		})
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}

const consecutiveCheckErrorLimit = 6

func ConsecutiveCheckErrorLimit() int { return consecutiveCheckErrorLimit }

func terminalCheckTargetsForNames(checks []scm.Check, names []string) []scm.CheckTarget {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	var targets []scm.CheckTarget
	for _, check := range checks {
		if wanted[check.Name] && checkFailedTerminally(check) {
			targets = append(targets, scm.CheckTarget{Name: check.Name, ProviderID: check.ProviderID})
		}
	}
	return targets
}

func ghChecksJSONUnsupported(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "gh pr checks:") && strings.Contains(msg, "unknown flag: --json")
}

func ciCheckReadFailureOutcome(err error) *pipeline.StepOutcome {
	desc := fmt.Sprintf("CI checks could not be read from the provider: %v. Verify that the provider CLI or credentials are installed, authenticated, and support the required check-reading command.", err)
	if ghChecksJSONUnsupported(err) {
		desc += " The GitHub CLI rejected 'pr checks --json'; gh >= 2.50 is required."
	}
	findings := Findings{
		Summary: "CI checks could not be read from the provider",
		Items: []Finding{{
			Severity:    "warning",
			Description: desc,
			Action:      types.ActionAskUser,
		}},
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}

func ciFixAgentTimeoutOutcome(issueDesc string, leftover string, err error) *pipeline.StepOutcome {
	description := fmt.Sprintf(
		"The CI auto-fix agent did not finish within its invocation budget while repairing: %s. "+
			"Reported: %v. The cut itself reflects budget or provider slowness; it does not clear the findings listed with it. "+
			"Re-running the same request costs another full budget, so no further attempt is made automatically. "+
			"Check that the configured agent CLI is authenticated and responsive, then respond with a fix selection to spend another budget, or resolve the CI failure outside the pipeline.",
		issueDesc, err)
	if leftover != "" {
		description += " " + leftover
	}
	findings := Findings{
		Summary: "CI auto-fix agent exceeded its invocation budget",
		Items: []Finding{{
			ID:          "ci-fix-agent-timeout",
			Severity:    "warning",
			Description: description,
			Action:      types.ActionAskUser,
		}},
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}

func ciMergeabilityOutcome(summary, description string) *pipeline.StepOutcome {
	findings := Findings{
		Summary: summary,
		Items: []Finding{{
			Severity:    "warning",
			Description: description,
			Action:      types.ActionAskUser,
		}},
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}

func ciMonitoringTimeoutOutcome() *pipeline.StepOutcome {
	findings := Findings{
		Summary: "CI monitoring timed out before PR was merged or closed",
		Items: []Finding{{
			Severity:    "warning",
			Description: "PR was still open when CI monitoring timed out",
			Action:      types.ActionAskUser,
		}},
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}

func getCIChecks(ctx context.Context, host scm.Host, pr *scm.PR) ([]scm.Check, string, bool, error) {
	requestedHead := pr.HeadSHA
	pr.ObservedHeadSHA = ""
	checks, err := host.GetChecks(ctx, pr)
	observedHead := pr.ObservedHeadSHA
	moved := errors.Is(err, scm.ErrHeadChanged) || observedHead != "" && observedHead != requestedHead
	if moved && err != nil && !pluginContractBroken(err) {
		err = fmt.Errorf("%w: %v", scm.ErrHeadChanged, err)
	}
	return checks, observedHead, moved, err
}
