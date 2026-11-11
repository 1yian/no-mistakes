package gate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
)

type StaleBranchReconciliation struct {
	Reconciled   bool
	PreviousHead string
	ArchivedTag  string
}

type ReconciliationUpstream struct {
	URL        string
	BaseBranch string
}

type ReconciliationUpstreamResolver func() ReconciliationUpstream

type StaleBranchPlan struct {
	PreserveDescendantOf string
	Reconcile            bool
	Branch               string
	BranchRef            string
	PreviousHead         string
	ArchiveTag           string
	PreservedByRecovery  map[string]string
}

func ReconcileStaleBranch(ctx context.Context, gateDir, workDir, branch, liveHead, runOwnedHead string, resolveUpstream ReconciliationUpstreamResolver) (StaleBranchReconciliation, error) {
	plan, err := PlanStaleBranchReconciliation(ctx, gateDir, workDir, branch, liveHead, runOwnedHead, resolveUpstream)
	if err != nil || !plan.Reconcile {
		return StaleBranchReconciliation{}, err
	}
	return ApplyStaleBranchReconciliation(ctx, gateDir, plan)
}

func PlanStaleBranchReconciliation(ctx context.Context, gateDir, workDir, branch, liveHead, runOwnedHead string, resolveUpstream ReconciliationUpstreamResolver) (StaleBranchPlan, error) {
	return planStaleBranchReconciliation(ctx, gateDir, workDir, branch, liveHead, resolveUpstream, false, runOwnedHead)
}

func PlanMirrorPublicationReconciliation(ctx context.Context, gateDir, workDir, branch, liveHead string, resolveUpstream ReconciliationUpstreamResolver, runOwnedHeads ...string) (StaleBranchPlan, error) {
	return planStaleBranchReconciliation(ctx, gateDir, workDir, branch, liveHead, resolveUpstream, true, runOwnedHeads...)
}

func planStaleBranchReconciliation(ctx context.Context, gateDir, workDir, branch, liveHead string, resolveUpstream ReconciliationUpstreamResolver, preserveDescendants bool, runOwnedHeads ...string) (StaleBranchPlan, error) {
	var plan StaleBranchPlan
	branch = strings.TrimSpace(branch)
	liveHead = strings.TrimSpace(liveHead)
	if branch == "" || liveHead == "" {
		return plan, fmt.Errorf("reconcile stale gate branch: branch and live head are required")
	}
	if err := git.ValidateBareRepository(ctx, gateDir); err != nil {
		return plan, fmt.Errorf("reconcile stale gate branch: %w", err)
	}
	if _, err := git.Run(ctx, workDir, "check-ref-format", "--branch", branch); err != nil {
		return plan, fmt.Errorf("reconcile stale gate branch %q: invalid branch name: %w", branch, err)
	}
	resolvedLive, err := git.Run(ctx, workDir, "rev-parse", "--verify", liveHead+"^{commit}")
	if err != nil || resolvedLive != liveHead {
		return plan, fmt.Errorf("reconcile stale gate branch %s: live head %s is not an exact commit", branch, liveHead)
	}
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return plan, fmt.Errorf("reconcile stale gate branch %s: resolve worktree path: %w", branch, err)
	}
	branchRef := "refs/heads/" + branch
	gateHead, exists, err := git.DirectRefTarget(ctx, gateDir, branchRef)
	if err != nil {
		return plan, fmt.Errorf("inspect private mirror ref %s: %w", branchRef, err)
	}
	if !exists {
		return plan, nil
	}
	archiveTag := "refs/tags/no-mistakes-abandoned/" + branch + "/" + gateHead
	archivedHead, archived, err := git.DirectRefTarget(ctx, gateDir, archiveTag)
	if err != nil {
		return plan, fmt.Errorf("inspect private mirror archive tag %s: %w", archiveTag, err)
	}
	if archived && archivedHead != gateHead {
		return plan, fmt.Errorf("private mirror archive tag %s already points at %s, not %s", archiveTag, archivedHead, gateHead)
	}
	if gateHead == liveHead {
		return plan, nil
	}
	if objectType, err := git.Run(ctx, gateDir, "cat-file", "-t", gateHead); err != nil || objectType != "commit" {
		return plan, fmt.Errorf("private mirror ref %s does not point at a commit", branchRef)
	}
	if err := git.FetchRemoteRef(ctx, gateDir, workDir, liveHead, liveHead); err != nil {
		return plan, fmt.Errorf("stage live head for private mirror reconciliation: %w", err)
	}

	// An ancestor needs no reconciliation: the caller's ordinary push is
	// already a fast-forward and preserves the private head by ancestry.
	if _, err := git.Run(ctx, gateDir, "merge-base", "--is-ancestor", gateHead, liveHead); err == nil {
		return plan, nil
	}
	if preserveDescendants {
		plan.PreserveDescendantOf = liveHead
		if _, err := git.Run(ctx, gateDir, "merge-base", "--is-ancestor", liveHead, gateHead); err == nil {
			return plan, nil
		}
	}
	if !isRunOwnedHead(gateHead, runOwnedHeads) {
		atRiskCommits, err := privateCommitsAbsentFromLive(ctx, gateDir, liveHead, gateHead)
		if err != nil {
			return plan, fmt.Errorf("compare private mirror content for %s: %w", branchRef, err)
		}
		// Recovery anchors are the tool's own sanctioned preservation record:
		// terminalization pins unpublished heads under recover/, and rewritten
		// remote recovery pins superseded pushes under recover-rewritten/ in
		// the worktree or gate. PreserveRecoveryAnchor never replaces evidence
		// (a conflicting anchor fails closed). A commit that
		// one of those anchors keeps reachable is not at risk of being lost, so
		// it is re-derived out of the at-risk set instead of deadlocking the
		// recover -> rerun -> push loop that wrote the anchor in the first
		// place. This is a preservation credit, not containment evidence: the
		// surviving content still has to be proven by the checks above for any
		// commit no anchor holds.
		satisfiers := reconcileSatisfiers(runOwnedHeads)
		preserved, err := preservedByRecoveryAnchors(ctx, gateDir, liveHead, atRiskCommits, workDir)
		if err != nil {
			return plan, fmt.Errorf("inspect recovery anchors for %s: %w", branchRef, err)
		}
		remaining := make([]string, 0, len(atRiskCommits))
		for _, commit := range atRiskCommits {
			if _, ok := preserved[commit]; !ok {
				remaining = append(remaining, commit)
			}
		}
		if len(remaining) > 0 && resolveUpstream != nil {
			if contained, err := privateTreeSurvivesBaseDeletion(ctx, gateDir, liveHead, gateHead, resolveUpstream()); contained && err == nil {
				remaining = nil
			}
		}
		if len(remaining) > 0 {
			atRisk := make([]string, 0, len(remaining))
			for _, commit := range remaining {
				description, describeErr := git.Run(ctx, gateDir, "show", "-s", "--format=%H %s", commit)
				if describeErr != nil {
					return plan, fmt.Errorf("describe at-risk private mirror commit %s: %w", commit, describeErr)
				}
				atRisk = append(atRisk, description)
			}
			refusal := fmt.Errorf(
				"refusing to reconcile private mirror ref %s: %d at-risk commit(s) contain content absent from live head %s: %s. "+
					"Clears on any of: %s",
				branchRef, len(remaining), liveHead, strings.Join(atRisk, "; "),
				joinSatisfiers(satisfiers),
			)
			return plan, refusal
		}
		plan.PreservedByRecovery = preserved
	}

	return StaleBranchPlan{
		PreserveDescendantOf: plan.PreserveDescendantOf,
		Reconcile:            true,
		Branch:               branch,
		BranchRef:            branchRef,
		PreviousHead:         gateHead,
		ArchiveTag:           archiveTag,
		PreservedByRecovery:  plan.PreservedByRecovery,
	}, nil
}

func isRunOwnedHead(gateHead string, runOwnedHeads []string) bool {
	for _, owned := range runOwnedHeads {
		if owned = strings.TrimSpace(owned); owned != "" && owned == gateHead {
			return true
		}
	}
	return false
}

func hasRunOwnedHead(runOwnedHeads []string) bool {
	for _, owned := range runOwnedHeads {
		if strings.TrimSpace(owned) != "" {
			return true
		}
	}
	return false
}

func ApplyStaleBranchReconciliation(ctx context.Context, gateDir string, plan StaleBranchPlan) (StaleBranchReconciliation, error) {
	var result StaleBranchReconciliation
	if !plan.Reconcile {
		return result, nil
	}
	if plan.BranchRef == "" || plan.PreviousHead == "" || plan.ArchiveTag == "" {
		return result, fmt.Errorf("apply private mirror reconciliation: incomplete plan for %q", plan.Branch)
	}
	if err := git.ValidateBareRepository(ctx, gateDir); err != nil {
		return result, fmt.Errorf("apply private mirror reconciliation: %w", err)
	}
	currentHead, exists, err := git.DirectRefTarget(ctx, gateDir, plan.BranchRef)
	if err != nil {
		return result, fmt.Errorf("inspect private mirror ref %s: %w", plan.BranchRef, err)
	}
	if !exists {
		return result, nil
	}
	if currentHead != plan.PreviousHead {
		if plan.PreserveDescendantOf != "" {
			if _, err := git.Run(ctx, gateDir, "merge-base", "--is-ancestor", plan.PreserveDescendantOf, currentHead); err == nil {
				return result, nil
			}
		}
		return result, fmt.Errorf(
			"private mirror ref %s moved to %s after it was proven stale at %s",
			plan.BranchRef, currentHead, plan.PreviousHead,
		)
	}
	archivedHead, archived, err := git.DirectRefTarget(ctx, gateDir, plan.ArchiveTag)
	if err != nil {
		return result, fmt.Errorf("inspect private mirror archive tag %s: %w", plan.ArchiveTag, err)
	}
	if archived && archivedHead != plan.PreviousHead {
		return result, fmt.Errorf("private mirror archive tag %s already points at %s, not %s", plan.ArchiveTag, archivedHead, plan.PreviousHead)
	}
	if !archived {
		if _, err := git.Run(ctx, gateDir, "update-ref", "--no-deref", plan.ArchiveTag, plan.PreviousHead, strings.Repeat("0", len(plan.PreviousHead))); err != nil {
			return result, fmt.Errorf("archive stale private mirror head %s at %s: %w", plan.PreviousHead, plan.ArchiveTag, err)
		}
	}
	if _, err := git.Run(ctx, gateDir, "update-ref", "--no-deref", "-d", plan.BranchRef, plan.PreviousHead); err != nil {
		return result, fmt.Errorf("delete archived stale private mirror ref %s at %s: %w", plan.BranchRef, plan.PreviousHead, err)
	}
	return StaleBranchReconciliation{Reconciled: true, PreviousHead: plan.PreviousHead, ArchivedTag: plan.ArchiveTag}, nil
}

func RestoreReconciledBranch(ctx context.Context, gateDir, branch string, result StaleBranchReconciliation) error {
	if !result.Reconciled {
		return nil
	}
	if err := git.ValidateBareRepository(ctx, gateDir); err != nil {
		return err
	}
	if !ArchivedHeadRecorded(ctx, gateDir, branch, result.PreviousHead) {
		return fmt.Errorf("restore private mirror %q: archived head %s is unavailable", branch, result.PreviousHead)
	}
	ref := "refs/heads/" + branch
	if _, exists, err := git.DirectRefTarget(ctx, gateDir, ref); err != nil || exists {
		return err
	}
	if _, err := git.Run(ctx, gateDir, "update-ref", "--no-deref", ref, result.PreviousHead, strings.Repeat("0", len(result.PreviousHead))); err != nil {
		if _, exists, readErr := git.DirectRefTarget(ctx, gateDir, ref); readErr == nil && exists {
			return nil
		}
		return fmt.Errorf("restore private mirror %s: %w", ref, err)
	}
	return nil
}

func ArchivedHeadRecorded(ctx context.Context, gateDir, branch, head string) bool {
	branch = strings.TrimSpace(branch)
	head = strings.TrimSpace(head)
	if branch == "" || head == "" {
		return false
	}
	if _, err := git.Run(ctx, gateDir, "check-ref-format", "--branch", branch); err != nil {
		return false
	}
	tag := "refs/tags/no-mistakes-abandoned/" + branch + "/" + head
	archivedHead, archived, err := git.DirectRefTarget(ctx, gateDir, tag)
	if err != nil || !archived || archivedHead != head {
		return false
	}
	objectType, err := git.Run(ctx, gateDir, "cat-file", "-t", head)
	return err == nil && objectType == "commit"
}

const recoveryAnchorPrefix = "refs/no-mistakes/recover/"

const rewrittenRecoveryAnchorPrefix = "refs/no-mistakes/recover-rewritten/"

type recoveryAnchor struct {
	ref  string
	head string
}

func listRecoveryAnchors(ctx context.Context, repoDir string, prefixes ...string) ([]recoveryAnchor, error) {
	if len(prefixes) == 0 {
		prefixes = []string{recoveryAnchorPrefix, rewrittenRecoveryAnchorPrefix}
	}
	args := append([]string{"for-each-ref", "--format=%(refname) %(objectname) %(objecttype) %(symref)"}, prefixes...)
	out, err := git.Run(ctx, repoDir, args...)
	if err != nil {
		return nil, err
	}
	var anchors []recoveryAnchor
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "commit" {
			continue
		}
		ref, head := fields[0], fields[1]
		if git.IsZeroSHA(head) {
			continue
		}
		anchors = append(anchors, recoveryAnchor{ref: ref, head: head})
	}
	return anchors, nil
}

func reconcileSatisfiers(runOwnedHeads []string) []string {
	satisfiers := []string{
		"the live head descending from the private head (ancestry)",
		"tree-survival proof (a tree-preserving merge placing the content in the live history)",
	}
	if hasRunOwnedHead(runOwnedHeads) {
		satisfiers = append(satisfiers, "the private head being exactly the publishing run's Run.SubmittedHeadSHA or Run.LastPushedSHA (Decision 41-A)")
	}
	return append(satisfiers, "a refs/no-mistakes/recover/<run> or refs/no-mistakes/recover-rewritten/<run>/<generation> recovery anchor reaching the commit")
}

func joinSatisfiers(satisfiers []string) string {
	if len(satisfiers) == 1 {
		return satisfiers[0]
	}
	return strings.Join(satisfiers[:len(satisfiers)-1], "; ") + "; or " + satisfiers[len(satisfiers)-1]
}

const maxRecoveryCandidates = 512

func preservedByRecoveryAnchors(ctx context.Context, gateDir, liveHead string, commits []string, workDirs ...string) (map[string]string, error) {
	preserved := make(map[string]string)
	if len(commits) == 0 {
		return preserved, nil
	}
	anchors, err := listRecoveryAnchors(ctx, gateDir)
	if err != nil {
		return nil, err
	}
	// recoverRemoteRewritten prefers the invoking worktree when it has the
	// superseded object. Consult those direct anchors too, staging their exact
	// targets for the same bounded membership proof in the gate. Fetching by
	// ref with an expected commit fails closed if it moved. No durable ref is
	// created or replaced while planning; apply still archives the exact
	// private head before deleting its branch.
	for _, workDir := range workDirs {
		local, err := listRecoveryAnchors(ctx, workDir, rewrittenRecoveryAnchorPrefix)
		if err != nil {
			return nil, err
		}
		for _, anchor := range local {
			if err := git.FetchRemoteRef(ctx, gateDir, workDir, anchor.ref, anchor.head); err != nil {
				return nil, fmt.Errorf("stage rewritten recovery anchor %s: %w", anchor.ref, err)
			}
			anchors = append(anchors, anchor)
		}
	}
	if len(anchors) == 0 {
		return preserved, nil
	}
	anchorHeads := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		anchorHeads = append(anchorHeads, anchor.head)
	}

	// One scan per batch: the candidates NO anchor reaches. Its complement is
	// preserved.
	unreachedSet := make(map[string]bool)
	for start := 0; start < len(commits); start += maxRecoveryCandidates {
		batch := commits[start:min(start+maxRecoveryCandidates, len(commits))]
		args := make([]string, 0, len(batch)+len(anchorHeads)+2)
		args = append(args, batch...)
		args = append(args, "--not", liveHead)
		args = append(args, anchorHeads...)
		unreached, err := commitList(ctx, gateDir, args...)
		if err != nil {
			return nil, err
		}
		for _, commit := range unreached {
			unreachedSet[commit] = true
		}
	}
	for _, commit := range commits {
		if unreachedSet[commit] {
			continue
		}
		// Attribution only, for the credited commits, stopping at the first
		// anchor that covers each one.
		for _, anchor := range anchors {
			if _, err := git.Run(ctx, gateDir, "merge-base", "--is-ancestor", commit, anchor.head); err == nil {
				preserved[commit] = anchor.ref
				break
			}
		}
	}
	return preserved, nil
}

func privateCommitsAbsentFromLive(ctx context.Context, repoDir, liveHead, privateHead string) ([]string, error) {
	privateOnly, err := commitList(ctx, repoDir, "--right-only", liveHead+"..."+privateHead)
	if err != nil {
		return nil, err
	}
	if len(privateOnly) == 0 {
		return nil, nil
	}
	if contained, err := mergeLeavesTree(ctx, repoDir, liveHead, privateHead); err != nil || contained {
		return nil, err
	}
	paths, err := privateOnlyPaths(ctx, repoDir, privateOnly)
	if err != nil {
		return nil, err
	}
	survives, err := privateTreeSurvives(ctx, repoDir, liveHead, privateHead, paths)
	if err != nil {
		return nil, err
	}
	if survives {
		return nil, nil
	}
	return privateOnly, nil
}

func privateTreeSurvivesBaseDeletion(ctx context.Context, repoDir, liveHead, privateHead string, upstream ReconciliationUpstream) (bool, error) {
	selectedBase := strings.TrimSpace(upstream.BaseBranch)
	upstreamURL := strings.TrimSpace(upstream.URL)
	if selectedBase == "" || upstreamURL == "" {
		return false, nil
	}
	if _, err := git.Run(ctx, repoDir, "check-ref-format", "refs/heads/"+selectedBase); err != nil {
		return false, err
	}
	upstreamRef := "refs/heads/" + selectedBase
	upstreamHead, err := git.LsRemote(ctx, repoDir, upstreamURL, upstreamRef)
	if err != nil || upstreamHead == "" {
		return false, err
	}
	if err := git.FetchRemoteRef(ctx, repoDir, upstreamURL, upstreamRef, upstreamHead); err != nil {
		return false, err
	}
	base, err := git.Run(ctx, repoDir, "merge-base", "--all", liveHead, upstreamHead)
	if err != nil {
		return false, err
	}
	common, err := git.Run(ctx, repoDir, "merge-base", "--all", liveHead, privateHead)
	if err != nil {
		return false, err
	}
	// Criss-cross ancestry has no unique deletion boundary.
	if len(strings.Fields(base)) != 1 || len(strings.Fields(common)) != 1 {
		return false, nil
	}
	// A selected base rolled back behind the fork is not evidence that the
	// branch integrated a deletion. Comparing its older tree would otherwise
	// misread files introduced before the fork as upstream deletions.
	if _, err := git.Run(ctx, repoDir, "merge-base", "--is-ancestor", common, base); err != nil {
		return false, nil
	}
	deleted, err := git.RunRaw(ctx, repoDir, "diff-tree", "--no-commit-id", "--name-only", "--diff-filter=D", "--no-renames", "-r", "-z", common, base)
	if err != nil || len(deleted) == 0 {
		return false, err
	}
	livePaths, err := git.RunRaw(ctx, repoDir, "ls-tree", "--name-only", "-r", "-z", liveHead)
	if err != nil {
		return false, err
	}
	present := make(map[string]bool)
	for _, path := range strings.Split(string(livePaths), "\x00") {
		present[path] = true
	}
	var removed strings.Builder
	for _, path := range strings.Split(strings.TrimSuffix(string(deleted), "\x00"), "\x00") {
		if path != "" && !present[path] {
			removed.WriteString(path)
			removed.WriteByte(0)
		}
	}
	if removed.Len() == 0 {
		return false, nil
	}
	indexDir, err := os.MkdirTemp("", "no-mistakes-deletion-proof-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(indexDir)
	// update-index requires a worktree even for --force-remove. Give the
	// copied index an empty scratch worktree, never the invoking worktree.
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(indexDir, "index"), "GIT_WORK_TREE=" + indexDir}
	if _, err := git.RunWithEnv(ctx, repoDir, env, "read-tree", privateHead); err != nil {
		return false, err
	}
	if _, err := git.RunWithEnvAndInput(ctx, repoDir, env, removed.String(), "update-index", "--force-remove", "-z", "--stdin"); err != nil {
		return false, err
	}
	tree, err := git.RunWithEnv(ctx, repoDir, env, "write-tree")
	if err != nil {
		return false, err
	}
	env = append(env, "GIT_AUTHOR_NAME=no-mistakes", "GIT_AUTHOR_EMAIL=no-mistakes@localhost",
		"GIT_COMMITTER_NAME=no-mistakes", "GIT_COMMITTER_EMAIL=no-mistakes@localhost",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
	projected, err := git.RunWithEnv(ctx, repoDir, env, "-c", "commit.gpgsign=false", "commit-tree", tree, "-p", privateHead, "-m", "private mirror proof with upstream deletions")
	if err != nil {
		return false, err
	}
	absent, err := privateCommitsAbsentFromLive(ctx, repoDir, liveHead, projected)
	return len(absent) == 0 && err == nil, err
}

func privateOnlyPaths(ctx context.Context, repoDir string, commits []string) (map[string]bool, error) {
	paths := make(map[string]bool)
	for _, commit := range commits {
		rawPaths, err := git.RunRaw(ctx, repoDir, "diff-tree", "--root", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", commit)
		if err != nil {
			return nil, err
		}
		for _, rawPath := range strings.Split(strings.TrimSuffix(string(rawPaths), "\x00"), "\x00") {
			if rawPath != "" {
				paths[rawPath] = true
			}
		}
	}
	return paths, nil
}

const maxSurvivalCandidates = 64

func privateTreeSurvives(ctx context.Context, repoDir, liveHead, privateHead string, paths map[string]bool) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	if ok, err := mergeLeavesTree(ctx, repoDir, liveHead, privateHead, "-X", "ours"); err != nil || !ok {
		return false, err
	}
	args := []string{"--first-parent", liveHead, "^" + privateHead, "--"}
	for path := range paths {
		args = append(args, ":(literal)"+path)
	}
	candidates, err := commitList(ctx, repoDir, args...)
	if err != nil {
		return false, err
	}
	for i, candidate := range candidates {
		if i == maxSurvivalCandidates {
			break
		}
		if candidate == liveHead {
			continue
		}
		if ok, err := mergeLeavesTree(ctx, repoDir, candidate, privateHead); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

func mergeLeavesTree(ctx context.Context, repoDir, liveCommit, privateHead string, options ...string) (bool, error) {
	args := append([]string{"merge-tree", "--write-tree"}, options...)
	mergedTree, mergeErr := git.Run(ctx, repoDir, append(args, liveCommit, privateHead)...)
	if mergeErr != nil {
		return false, nil
	}
	liveTree, err := git.Run(ctx, repoDir, "rev-parse", "--verify", liveCommit+"^{tree}")
	if err != nil {
		return false, err
	}
	return mergedTree == liveTree, nil
}

func commitList(ctx context.Context, repoDir string, args ...string) ([]string, error) {
	out, err := git.Run(ctx, repoDir, append([]string{"rev-list"}, args...)...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}
