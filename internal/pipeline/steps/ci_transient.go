package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type failureClass string

const (
	classGenuine   failureClass = "genuine"
	classTransient failureClass = "transient"
	classUnknown   failureClass = "unknown"
)

func classifyCheckFailure(check scm.Check) failureClass {
	if !checkFailedTerminally(check) {
		return classUnknown
	}
	// A failure the provider produced before the repository's own steps ran is an
	// infrastructure outcome, not a verdict on the code: the job's setup phase
	// failed (e.g. a GitHub Actions action-download outage) so no repository step
	// executed. It is as re-runnable as a cancellation, and it can never mask a
	// real failure - a genuine test or lint failure cleared setup and failed a
	// later step, so it is never marked PreRunFailure.
	if check.PreRunFailure {
		return classTransient
	}
	switch strings.ToUpper(strings.TrimSpace(check.State)) {
	case "CANCELLED", "CANCELED":
		return classTransient
	case "FAILURE", "FAILED", "ERROR", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return classGenuine
	default:
		// Includes the empty state: a provider that reported a failed bucket
		// without a state cannot tell us why, and "we do not know" is not
		// "transient".
		return classUnknown
	}
}

func checkFailedTerminally(check scm.Check) bool {
	return check.Failing() || check.Bucket == scm.CheckBucketCancel
}

const rerunRollupGracePolls = 2

type rerunRollupState struct {
	completedAt    time.Time
	graceRemaining int
	headSHA        string
	observedLinks  map[string]bool
}

type checkRerunBudget struct {
	spent  map[string]int
	rollup map[string]rerunRollupState
}

type persistedRerunBudget struct {
	Spent  map[string]int                  `json:"spent,omitempty"`
	Rollup map[string]persistedRollupState `json:"rollup,omitempty"`
}

type persistedRollupState struct {
	CompletedAt    time.Time `json:"completed_at"`
	GraceRemaining int       `json:"grace_remaining"`
	HeadSHA        string    `json:"head_sha,omitempty"`
	ObservedLinks  []string  `json:"observed_links,omitempty"`
}

func (b *checkRerunBudget) marshal() (string, error) {
	if len(b.spent) == 0 && len(b.rollup) == 0 {
		return "", nil
	}
	payload := persistedRerunBudget{Spent: b.spent}
	if len(b.rollup) > 0 {
		payload.Rollup = make(map[string]persistedRollupState, len(b.rollup))
		for name, state := range b.rollup {
			observedLinks := make([]string, 0, len(state.observedLinks))
			for link := range state.observedLinks {
				observedLinks = append(observedLinks, link)
			}
			sort.Strings(observedLinks)
			payload.Rollup[name] = persistedRollupState{
				CompletedAt:    state.completedAt,
				GraceRemaining: state.graceRemaining,
				HeadSHA:        state.headSHA,
				ObservedLinks:  observedLinks,
			}
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal rerun budget: %w", err)
	}
	return string(encoded), nil
}

func (b *checkRerunBudget) unmarshal(encoded string) error {
	if strings.TrimSpace(encoded) == "" {
		return nil
	}
	var payload persistedRerunBudget
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		return fmt.Errorf("unmarshal rerun budget: %w", err)
	}
	b.spent = payload.Spent
	if b.spent == nil {
		b.spent = map[string]int{}
	}
	b.rollup = make(map[string]rerunRollupState, len(payload.Rollup))
	for name, state := range payload.Rollup {
		observedLinks := make(map[string]bool, len(state.ObservedLinks))
		for _, link := range state.ObservedLinks {
			if link != "" {
				observedLinks[link] = true
			}
		}
		b.rollup[name] = rerunRollupState{
			completedAt:    state.CompletedAt,
			graceRemaining: state.GraceRemaining,
			headSHA:        state.HeadSHA,
			observedLinks:  observedLinks,
		}
	}
	return nil
}

func (b *checkRerunBudget) remaining(name string, limit int) int {
	if limit <= 0 {
		return 0
	}
	return limit - b.spent[name]
}

func (b *checkRerunBudget) used(name string) int { return b.spent[name] }

func (b *checkRerunBudget) spend(check scm.Check, checks []scm.Check, headSHA string) int {
	if b.spent == nil {
		b.spent = map[string]int{}
	}
	if b.rollup == nil {
		b.rollup = map[string]rerunRollupState{}
	}
	b.spent[check.Name]++
	observedLinks := map[string]bool{}
	for _, observed := range checks {
		if observed.Name == check.Name && observed.Link != "" {
			observedLinks[observed.Link] = true
		}
	}
	b.rollup[check.Name] = rerunRollupState{
		completedAt:    check.CompletedAt,
		graceRemaining: rerunRollupGracePolls,
		headSHA:        headSHA,
		observedLinks:  observedLinks,
	}
	return b.spent[check.Name]
}

func (b *checkRerunBudget) retireResolvedReruns(checks []scm.Check, currentHead string, persist func(*checkRerunBudget) error) (bool, error) {
	if len(b.rollup) == 0 {
		return false, nil
	}
	retirable := map[string]bool{}
	for name, state := range b.rollup {
		if state.headSHA != "" && state.headSHA != currentHead {
			retirable[name] = true
		}
	}
	resolved := map[string]bool{}
	outstanding := map[string]bool{}
	for _, check := range checks {
		state, tracked := b.rollup[check.Name]
		if !tracked || retirable[check.Name] {
			continue
		}
		if checkFailedTerminally(check) && classifyCheckFailure(check) == classTransient {
			outstanding[check.Name] = true
			continue
		}
		switch check.Bucket {
		case scm.CheckBucketPass, scm.CheckBucketFail, scm.CheckBucketSkip:
			if check.Link != "" && !state.observedLinks[check.Link] {
				resolved[check.Name] = true
			}
		default:
			outstanding[check.Name] = true
		}
	}
	for name := range resolved {
		if !outstanding[name] {
			retirable[name] = true
		}
	}
	// A delayed same-named sibling can look identical to a rerun replacement
	// here. That name-keyed masking already exists on the default branch in
	// cancelledAfterRerun; distinguishing it requires provider truth outside
	// this policy.
	if len(retirable) == 0 {
		return false, nil
	}
	candidate := &checkRerunBudget{
		spent:  b.spent,
		rollup: make(map[string]rerunRollupState, len(b.rollup)-len(retirable)),
	}
	for name, state := range b.rollup {
		if !retirable[name] {
			candidate.rollup[name] = state
		}
	}
	if err := persist(candidate); err != nil {
		return false, err
	}
	for name := range retirable {
		delete(b.rollup, name)
	}
	return true, nil
}

func (b *checkRerunBudget) cancelledAfterRerun(checks []scm.Check) (unresolved, awaiting []string) {
	refreshed := map[string]bool{}
	present := map[string]bool{}
	var order []string
	for _, check := range checks {
		if b.used(check.Name) == 0 {
			continue
		}
		// A rerun this run issued is outstanding until its replacement is
		// published, whatever bucket the check currently reports. Recording
		// presence for every bucket keeps a check that moved from cancelled to
		// running or success from being read as missing below.
		present[check.Name] = true
		if check.Bucket != scm.CheckBucketCancel {
			continue
		}
		if _, seen := refreshed[check.Name]; !seen {
			order = append(order, check.Name)
		}
		// Same-named checks share one budget key, so one refreshed instance is
		// enough to say the name's rollup has moved on.
		refreshed[check.Name] = refreshed[check.Name] || !b.rollupUnchanged(check)
	}

	var missing []string
	for name := range b.rollup {
		if present[name] || b.used(name) == 0 {
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	order = append(order, missing...)

	for _, name := range order {
		if refreshed[name] || !b.consumeRollupGrace(name) {
			unresolved = append(unresolved, name)
			continue
		}
		awaiting = append(awaiting, name)
	}
	return unresolved, awaiting
}

func (b *checkRerunBudget) cancelledWithoutRerun(checks []scm.Check) []string {
	var names []string
	seen := map[string]struct{}{}
	for _, check := range checks {
		if check.Bucket != scm.CheckBucketCancel || b.used(check.Name) > 0 {
			continue
		}
		if _, ok := seen[check.Name]; ok {
			continue
		}
		seen[check.Name] = struct{}{}
		names = append(names, check.Name)
	}
	return names
}

func (b *checkRerunBudget) rollupUnchanged(check scm.Check) bool {
	state, ok := b.rollup[check.Name]
	if !ok || state.completedAt.IsZero() || check.CompletedAt.IsZero() {
		return false
	}
	return check.CompletedAt.Equal(state.completedAt)
}

func (b *checkRerunBudget) awaitingRollupPublication(check scm.Check) bool {
	state, ok := b.rollup[check.Name]
	return ok && state.graceRemaining > 0 && b.rollupUnchanged(check)
}

func (b *checkRerunBudget) consumeRollupGrace(name string) bool {
	state, ok := b.rollup[name]
	if !ok || state.graceRemaining <= 0 {
		return false
	}
	state.graceRemaining--
	b.rollup[name] = state
	return true
}

func transientRerunCandidates(checks []scm.Check, budget *checkRerunBudget, limit int) []scm.Check {
	if limit <= 0 {
		return nil
	}
	var candidates []scm.Check
	// reserved counts what this selection has already promised to spend.
	// Two terminally failed checks can carry the same name - the same job name
	// in two workflow files, or a matrix leg the provider reports without a
	// distinguishing suffix - and they share one budget key, so reading only
	// what was spent on earlier polls would admit both and blow the limit on a
	// single poll.
	reserved := map[string]int{}
	for _, check := range checks {
		if !checkFailedTerminally(check) {
			continue
		}
		if classifyCheckFailure(check) != classTransient {
			return nil
		}
		// A rerun was already requested for this exact observation and the
		// provider has not published it yet, so the spent-count alone would
		// read a budget of two or more as licence to ask again: the job is
		// already re-running, and the request either bounces or bills the
		// repository a duplicate workflow run. Declining leaves the poll with
		// no candidates, which is what routes the check to cancelledAfterRerun
		// to spend a poll of its grace instead.
		if budget.awaitingRollupPublication(check) {
			continue
		}
		if budget.remaining(check.Name, limit)-reserved[check.Name] > 0 {
			reserved[check.Name]++
			candidates = append(candidates, check)
		}
	}
	return candidates
}

func mergeCheckNames(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, name := range base {
		seen[name] = struct{}{}
	}
	merged := base
	for _, name := range extra {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	return merged
}

func (s *CIStep) loadRerunBudget(sctx *pipeline.StepContext) {
	if sctx.DB == nil || sctx.Run == nil {
		return
	}
	encoded, err := sctx.DB.GetRunCIRerunState(sctx.Run.ID)
	if err != nil {
		sctx.Log(fmt.Sprintf("warning: could not read the persisted rerun budget: %v", err))
		return
	}
	if err := s.transientReruns.unmarshal(encoded); err != nil {
		sctx.Log(fmt.Sprintf("warning: could not restore the persisted rerun budget: %v", err))
	}
}

func (s *CIStep) persistRerunBudget(sctx *pipeline.StepContext) error {
	return s.persistRerunBudgetCandidate(sctx, &s.transientReruns)
}

func (s *CIStep) persistRerunBudgetCandidate(sctx *pipeline.StepContext, candidate *checkRerunBudget) error {
	if sctx.DB == nil || sctx.Run == nil {
		return nil
	}
	encoded, err := candidate.marshal()
	if err != nil {
		return err
	}
	return sctx.DB.SetRunCIRerunState(sctx.Run.ID, encoded)
}

func (s *CIStep) retireResolvedReruns(sctx *pipeline.StepContext, checks []scm.Check) (bool, error) {
	return s.transientReruns.retireResolvedReruns(checks, sctx.Run.HeadSHA, func(candidate *checkRerunBudget) error {
		return s.persistRerunBudgetCandidate(sctx, candidate)
	})
}

func (s *CIStep) rerunTransientChecks(sctx *pipeline.StepContext, host scm.Host, pr *scm.PR, checks []scm.Check) (bool, *pipeline.StepOutcome) {
	limit := sctx.Config.CI.RerunTransient
	if limit <= 0 {
		return false, nil
	}
	// The rerun capability is optional: providers without one keep behaving
	// exactly as they did before this policy existed.
	rerunner, ok := host.(scm.CheckRerunner)
	if !ok {
		return false, nil
	}
	candidates := transientRerunCandidates(checks, &s.transientReruns, limit)
	if len(candidates) == 0 {
		return false, nil
	}

	// A rerun runs the checks again for whatever commit the branch now points
	// at, so it is only meaningful while that is still the commit this run
	// delivered. If the branch moved, the checks being re-run would certify a
	// revision this run never produced: terminate with the evidence instead.
	published, err := publishedBranchHead(sctx)
	if err != nil {
		sctx.Log(fmt.Sprintf("warning: could not verify the published branch head before re-running checks: %v", err))
		return false, nil
	}
	if published != sctx.Run.HeadSHA {
		sctx.Log(fmt.Sprintf("published branch head moved (expected %s, observed %s); not re-running checks", shortSHA(sctx.Run.HeadSHA), shortSHA(published)))
		return false, ciRerunHeadMismatchOutcome(sctx.Run.HeadSHA, published)
	}

	issued := false
	for _, check := range candidates {
		used := s.transientReruns.spend(check, checks, sctx.Run.HeadSHA)
		// Reserve durably before asking the provider. A crash between this
		// write and the request costs the budget, which is the safe direction:
		// the alternative is a recovered run believing the rerun never
		// happened and issuing another one past the documented limit.
		if err := s.persistRerunBudget(sctx); err != nil {
			sctx.Log(fmt.Sprintf("warning: could not reserve the rerun budget for %s: %v", check.Name, err))
			return issued, nil
		}
		if err := rerunner.RerunCheck(sctx.Ctx, pr, check); err != nil {
			sctx.Log(fmt.Sprintf("warning: could not re-run transient CI check %s: %v", check.Name, err))
			continue
		}
		issued = true
		// used can never exceed limit: selection reserved this rerun against
		// the same shared budget key before it was spent.
		sctx.Log(fmt.Sprintf("re-running CI check %s (%d/%d): %s, not a job failure", check.Name, used, limit, transientReason(check)))
	}
	return issued, nil
}

func publishedHeadBoundedContext(sctx *pipeline.StepContext) (*pipeline.StepContext, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(sctx.Ctx, publishedHeadResolveWindow)
	bounded := *sctx
	bounded.Ctx = ctx
	return &bounded, cancel
}

func publishedBranchHead(sctx *pipeline.StepContext) (string, error) {
	ref := normalizedBranchRef(sctx.Run.Branch)
	bounded, cancel := publishedHeadBoundedContext(sctx)
	defer cancel()
	out, err := stepGitRunTree(bounded, "ls-remote", resolvePushURL(sctx), ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("%s is not present on the push target", ref)
	}
	return fields[0], nil
}

func transientStateLabel(check scm.Check) string {
	if state := strings.ToLower(strings.TrimSpace(check.State)); state != "" {
		return state
	}
	return string(check.Bucket)
}

func transientReason(check scm.Check) string {
	if check.PreRunFailure {
		return "it failed before the repository's own steps ran (setup/action resolution)"
	}
	return fmt.Sprintf("provider reported %s", transientStateLabel(check))
}

func markPreRunInfraFailures(sctx *pipeline.StepContext, host scm.Host, checks []scm.Check) {
	if sctx.Config.CI.RerunTransient <= 0 {
		return
	}
	detector, ok := host.(scm.PreRunFailureDetector)
	if !ok {
		return
	}
	var failedIdx []int
	for i := range checks {
		if checks[i].Failing() {
			failedIdx = append(failedIdx, i)
		}
	}
	if len(failedIdx) == 0 {
		return
	}
	failed := make([]scm.Check, len(failedIdx))
	for j, idx := range failedIdx {
		failed[j] = checks[idx]
	}
	infra, err := detector.PreRunFailures(sctx.Ctx, failed)
	if err != nil {
		sctx.Log(fmt.Sprintf("warning: could not classify pre-run CI failures: %v", err))
		return
	}
	if len(infra) != len(failed) {
		sctx.Log(fmt.Sprintf("warning: pre-run CI classifier returned %d results for %d checks; ignoring", len(infra), len(failed)))
		return
	}
	for j, idx := range failedIdx {
		if infra[j] {
			checks[idx].PreRunFailure = true
			checks[idx].Bucket = scm.CheckBucketCancel
		}
	}
}

func unresolvedTransientFindings(names []string, checks []scm.Check, reruns func(string) int) ([]Finding, string) {
	unresolved := unresolvedTransientChecks(names, checks)
	if len(unresolved) == 0 {
		return nil, ""
	}
	preRunCount := 0
	for _, check := range unresolved {
		if check.PreRunFailure {
			preRunCount++
		}
	}
	items := make([]Finding, 0, len(unresolved))
	for _, check := range unresolved {
		items = append(items, Finding{
			Severity:    "warning",
			Description: unresolvedTransientDescription(check.Name, reruns(check.Name), check.PreRunFailure),
			Action:      types.ActionAskUser,
			Category:    types.FindingCategoryCITransient,
			Check:       check.Name,
			CheckID:     check.ProviderID,
		})
	}
	return items, unresolvedTransientSummary(len(unresolved), preRunCount)
}

func unresolvedTransientChecks(names []string, checks []scm.Check) []scm.Check {
	var unresolved []scm.Check
	for _, name := range names {
		matched := false
		for _, check := range checks {
			if check.Name != name || check.Bucket != scm.CheckBucketCancel {
				continue
			}
			matched = true
			unresolved = append(unresolved, check)
		}
		if !matched {
			unresolved = append(unresolved, scm.Check{Name: name, Bucket: scm.CheckBucketCancel})
		}
	}
	return unresolved
}

func unresolvedTransientSummary(total, preRun int) string {
	switch {
	case total > 0 && preRun == total:
		return "CI checks failed before repository steps ran"
	case preRun > 0:
		return "CI checks ended without reporting a code verdict"
	default:
		return "CI checks were cancelled without reporting a verdict"
	}
}

func unresolvedTransientDescription(name string, reruns int, preRunFailure bool) string {
	if preRunFailure {
		if reruns > 0 {
			return fmt.Sprintf("CI check failed during setup again after its rerun: %s - repository steps never ran, so it needs a decision rather than a code fix", name)
		}
		return fmt.Sprintf("CI check failed during setup before repository steps ran: %s - no rerun is outstanding to replace that result, so it needs a decision rather than a code fix", name)
	}
	if reruns > 0 {
		return fmt.Sprintf("CI check cancelled again after its rerun: %s - the provider cancelled it rather than reporting a job failure, so it needs a decision rather than a code fix", name)
	}
	return fmt.Sprintf("CI check cancelled without a verdict: %s - the provider cancelled it rather than reporting a job failure, and no rerun is outstanding to replace that result, so it needs a decision rather than a code fix", name)
}

func ciRerunHeadMismatchOutcome(expected, observed string) *pipeline.StepOutcome {
	findings := Findings{
		Summary: "published branch head no longer matches the commit this run delivered",
		Items: []Finding{{
			Severity:    "warning",
			Description: fmt.Sprintf("CI checks could not be re-run: expected head %s, observed %s on the push target", expected, observed),
			Action:      types.ActionAskUser,
		}},
	}
	findingsJSON, _ := json.Marshal(findings)
	return &pipeline.StepOutcome{
		NeedsApproval: true,
		Findings:      string(findingsJSON),
	}
}
