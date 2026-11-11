package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func BindUncertifiedPipelineRange(sctx *StepContext) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil || sctx.Fixing {
		return
	}
	rng, err := sctx.DB.GetUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch)
	if err != nil {
		slog.Warn("failed to read uncertified pipeline range; not applying provenance", "repo_id", sctx.Repo.ID, "error", err)
		return
	}
	if rng == nil {
		return
	}
	head := strings.TrimSpace(sctx.Run.HeadSHA)
	if head == "" {
		head = strings.TrimSpace(sctx.ReviewStartingHeadSHA)
	}
	if !commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, rng.ToSHA, head) {
		warnUncertifiedRangeSkipped(sctx, rng, "uncertified range %s..%s not in gate; not applying provenance")
		return
	}
	sctx.UncertifiedFromSHA = rng.FromSHA
	sctx.UncertifiedToSHA = rng.ToSHA
	sctx.UncertifiedSourceRunID = rng.SourceRunID
	sctx.UncertifiedPriorRounds = loadUncertifiedPriorRounds(sctx.DB, rng.SourceRunID)
}

func PersistUncertifiedPipelineRange(sctx *StepContext, fromSHA, toSHA string) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil {
		return
	}
	fromSHA = strings.TrimSpace(fromSHA)
	toSHA = strings.TrimSpace(toSHA)
	if fromSHA == "" || toSHA == "" || fromSHA == toSHA {
		return
	}
	existing, err := sctx.DB.GetUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch)
	if err != nil {
		slog.Warn("failed to read uncertified pipeline range before persist", "run_id", sctx.Run.ID, "error", err)
		existing = nil
	}
	if existing != nil && strings.TrimSpace(existing.FromSHA) != "" &&
		uncertifiedRangeStillInLineage(sctx, existing.ToSHA, fromSHA, toSHA) {
		fromSHA = existing.FromSHA
	}
	if err := sctx.DB.UpsertUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch, fromSHA, toSHA, sctx.Run.ID); err != nil {
		slog.Warn("failed to persist uncertified pipeline range", "run_id", sctx.Run.ID, "error", err)
		if sctx.Log != nil {
			sctx.Log("warning: failed to persist uncertified fixer commit range")
		}
	}
}

func ClearUncertifiedPipelineRangeIfCertified(ctx context.Context, database *db.DB, repoID, branch, approvedHead, workDir string) {
	if database == nil {
		return
	}
	rng, err := database.GetUncertifiedPipelineRange(repoID, branch)
	if err != nil {
		slog.Warn("failed to read uncertified pipeline range before clear", "repo_id", repoID, "error", err)
		return
	}
	if rng == nil {
		return
	}
	approvedHead = strings.TrimSpace(approvedHead)
	if approvedHead == "" {
		return
	}
	if rng.ToSHA != approvedHead && !commitIsSelfOrAncestor(ctx, workDir, rng.ToSHA, approvedHead) {
		return
	}
	if err := database.DeleteUncertifiedPipelineRange(repoID, branch); err != nil {
		slog.Warn("failed to clear uncertified pipeline range after certified review", "repo_id", repoID, "error", err)
	}
}

func RemapUncertifiedPipelineRangeAfterRebase(sctx *StepContext, oldHead, newHead string) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil {
		return
	}
	oldHead = strings.TrimSpace(oldHead)
	newHead = strings.TrimSpace(newHead)
	if oldHead == "" || newHead == "" || oldHead == newHead {
		return
	}
	if commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, oldHead, newHead) {
		return
	}
	rng, err := sctx.DB.GetUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch)
	if err != nil {
		slog.Warn("failed to read uncertified pipeline range before rebase remap", "run_id", sctx.Run.ID, "error", err)
		return
	}
	if rng == nil {
		return
	}
	if !commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, rng.ToSHA, oldHead) {
		return
	}
	if commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, rng.ToSHA, newHead) {
		return
	}
	fromBehind, ok := commitBehindCount(sctx.Ctx, sctx.WorkDir, rng.FromSHA, oldHead)
	if !ok {
		warnUncertifiedRemapSkipped(sctx, rng)
		return
	}
	toBehind, ok := commitBehindCount(sctx.Ctx, sctx.WorkDir, rng.ToSHA, oldHead)
	if !ok {
		warnUncertifiedRemapSkipped(sctx, rng)
		return
	}
	newFrom, ok := commitNthAncestor(sctx.Ctx, sctx.WorkDir, newHead, fromBehind)
	if !ok {
		warnUncertifiedRemapSkipped(sctx, rng)
		return
	}
	newTo, ok := commitNthAncestor(sctx.Ctx, sctx.WorkDir, newHead, toBehind)
	if !ok || newFrom == "" || newTo == "" || newFrom == newTo {
		warnUncertifiedRemapSkipped(sctx, rng)
		return
	}
	if err := sctx.DB.UpsertUncertifiedPipelineRange(sctx.Repo.ID, sctx.Run.Branch, newFrom, newTo, rng.SourceRunID); err != nil {
		slog.Warn("failed to remap uncertified pipeline range after rebase", "run_id", sctx.Run.ID, "error", err)
		if sctx.Log != nil {
			sctx.Log("warning: failed to remap uncertified fixer commit range after rebase")
		}
	}
}

func uncertifiedRangeStillInLineage(sctx *StepContext, existingTo, newFrom, newTo string) bool {
	if sctx == nil {
		return false
	}
	return commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, existingTo, newFrom) ||
		commitIsSelfOrAncestor(sctx.Ctx, sctx.WorkDir, existingTo, newTo)
}

func warnUncertifiedRemapSkipped(sctx *StepContext, rng *db.UncertifiedPipelineRange) {
	msg := fmt.Sprintf("uncertified range %s..%s could not be remapped after rebase; not updating provenance", rng.FromSHA, rng.ToSHA)
	slog.Warn(msg, "repo_id", sctx.Repo.ID, "branch", sctx.Run.Branch)
	if sctx.Log != nil {
		sctx.Log("warning: " + msg)
	}
}

func commitBehindCount(ctx context.Context, workDir, ancestor, descendent string) (int, bool) {
	if !commitIsSelfOrAncestor(ctx, workDir, ancestor, descendent) {
		return 0, false
	}
	if strings.TrimSpace(ancestor) == strings.TrimSpace(descendent) {
		return 0, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := git.Run(ctx, workDir, "rev-list", "--count", ancestor+".."+descendent)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func commitNthAncestor(ctx context.Context, workDir, sha string, n int) (string, bool) {
	sha = strings.TrimSpace(sha)
	if sha == "" || n < 0 || workDir == "" {
		return "", false
	}
	if n == 0 {
		return sha, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := git.Run(ctx, workDir, "rev-parse", "--verify", fmt.Sprintf("%s~%d", sha, n))
	if err != nil {
		return "", false
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", false
	}
	return out, true
}

func warnUncertifiedRangeSkipped(sctx *StepContext, rng *db.UncertifiedPipelineRange, format string) {
	msg := fmt.Sprintf(format, rng.FromSHA, rng.ToSHA)
	slog.Warn(msg, "repo_id", sctx.Repo.ID, "branch", sctx.Run.Branch)
	if sctx.Log != nil {
		sctx.Log("warning: " + msg)
	}
}

func commitIsSelfOrAncestor(ctx context.Context, workDir, ancestor, descendent string) bool {
	ancestor = strings.TrimSpace(ancestor)
	descendent = strings.TrimSpace(descendent)
	if ancestor == "" || descendent == "" || workDir == "" {
		return false
	}
	if ancestor == descendent {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := git.Run(ctx, workDir, "merge-base", "--is-ancestor", ancestor, descendent)
	return err == nil
}

func loadUncertifiedPriorRounds(database *db.DB, sourceRunID string) []*db.StepRound {
	sourceRunID = strings.TrimSpace(sourceRunID)
	if database == nil || sourceRunID == "" {
		return nil
	}
	steps, err := database.GetStepsByRun(sourceRunID)
	if err != nil {
		slog.Warn("failed to read uncertified source-run steps", "run_id", sourceRunID, "error", err)
		return nil
	}
	for _, step := range steps {
		if step.StepName != types.StepReview {
			continue
		}
		rounds, err := database.GetRoundsByStep(step.ID)
		if err != nil {
			slog.Warn("failed to read uncertified source-run review rounds", "run_id", sourceRunID, "error", err)
			return nil
		}
		return rounds
	}
	return nil
}
