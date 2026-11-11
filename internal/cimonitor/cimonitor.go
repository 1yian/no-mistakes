package cimonitor

import "strings"

const (
	ChecksPassedMsg            = "all CI checks passed - still monitoring until merged or closed"
	ChecksPassedCompleteMsg    = "all CI checks passed - validation complete, releasing without waiting for the merge"
	NoChecksPassedMsg          = "repository declares no CI (no_ci: true) - treating as all checks passed - still monitoring until merged or closed"
	NoChecksPassedCompleteMsg  = "repository declares no CI (no_ci: true) - treating as all checks passed - validation complete, releasing without waiting for the merge"
	ChecksRunningMsg           = "CI checks running, waiting for results..."
	ChecksHeadWaitingPrefix    = "waiting for the delivered commit's own checks: "
	ChecksAwaitingApprovalMsg  = "CI workflows are held awaiting maintainer approval - no jobs have run, waiting..."
	ChecksAwaitingManualJobMsg = "CI is held on a blocking manual job awaiting a person to run it, waiting..."
	BehindBaseMsg              = "all CI checks passed but the PR is behind a base that requires up-to-date branches, waiting for the base to settle before rebasing..."
)

type Activity struct {
	CIFixes      int    // number of auto-fix attempts observed
	AutoFixing   bool   // an auto-fix is currently in progress
	Ready        bool   // checks have passed (or declared no-CI); PR ready to merge
	DeclaredNoCI bool   // Ready because of an explicit no_ci declaration, not green checks
	LastEvent    string // the most recent recognized log line
}

func FromAuthoritative(ready, declaredNoCI bool, logs []string) Activity {
	a := ParseActivity(logs)
	a.Ready = ready
	a.DeclaredNoCI = ready && declaredNoCI
	return a
}

func ParseActivity(logs []string) Activity {
	var a Activity
	for _, line := range logs {
		switch {
		case strings.Contains(line, "running agent to fix CI"):
			// Emitted once per fix attempt by autoFixCI in real runs (and by
			// demo mode), so it is the reliable signal for counting fixes.
			a.CIFixes++
			a.AutoFixing = true
			a.Ready = false
			a.DeclaredNoCI = false
			a.LastEvent = line
		case strings.Contains(line, "committed and pushed fixes"):
			a.AutoFixing = false
			a.Ready = false
			a.DeclaredNoCI = false
			a.LastEvent = line
		case strings.Contains(line, "CI failures detected"):
			a.AutoFixing = true
			a.Ready = false
			a.DeclaredNoCI = false
			a.LastEvent = line
		case line == ChecksPassedMsg:
			a.AutoFixing = false
			a.Ready = true
			a.DeclaredNoCI = false
			a.LastEvent = line
		case line == ChecksPassedCompleteMsg:
			a.AutoFixing = false
			a.Ready = true
			a.DeclaredNoCI = false
			a.LastEvent = line
		case line == NoChecksPassedMsg:
			a.AutoFixing = false
			a.Ready = true
			a.DeclaredNoCI = true
			a.LastEvent = line
		case line == NoChecksPassedCompleteMsg:
			a.AutoFixing = false
			a.Ready = true
			a.DeclaredNoCI = true
			a.LastEvent = line
		case strings.Contains(line, "issues detected"),
			strings.Contains(line, "CI checks running"),
			line == ChecksAwaitingApprovalMsg,
			line == ChecksAwaitingManualJobMsg,
			strings.HasPrefix(line, ChecksHeadWaitingPrefix),
			line == BehindBaseMsg,
			strings.Contains(line, "mergeable state still pending"),
			strings.Contains(line, "no CI checks reported"),
			strings.Contains(line, "waiting for checks to register"),
			strings.Contains(line, "warning: could not check CI"),
			strings.Contains(line, "warning: could not check mergeable state"),
			strings.Contains(line, "warning: could not check PR state"):
			a.AutoFixing = false
			a.Ready = false
			a.DeclaredNoCI = false
			a.LastEvent = line
		case strings.Contains(line, "monitoring CI for PR"):
			a.Ready = false
			a.DeclaredNoCI = false
			a.LastEvent = line
		case strings.Contains(line, "PR has been merged"):
			a.Ready = false
			a.DeclaredNoCI = false
			a.LastEvent = line
		case strings.Contains(line, "PR has been closed"):
			a.Ready = false
			a.DeclaredNoCI = false
			a.LastEvent = line
		case strings.Contains(line, "CI timeout"):
			a.Ready = false
			a.DeclaredNoCI = false
			a.LastEvent = line
		}
	}
	return a
}

func ChecksPassed(logs []string) bool {
	return ParseActivity(logs).Ready
}

func DeclaredNoCI(logs []string) bool {
	return ParseActivity(logs).DeclaredNoCI
}
