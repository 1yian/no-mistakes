package pipeline

import (
	"log/slog"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
)

func BindBranchDecisions(sctx *StepContext) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil {
		return
	}
	branch := strings.TrimSpace(sctx.Run.Branch)
	if sctx.Repo.ID == "" || branch == "" {
		return
	}
	decisions, truncated, err := sctx.DB.GetBranchDecisionRounds(sctx.Repo.ID, branch, sctx.Run.ID, db.MaxBranchDecisionRounds)
	if err != nil {
		slog.Warn("failed to read prior branch decisions; continuing without them", "repo_id", sctx.Repo.ID, "error", err)
		return
	}
	sctx.PriorBranchDecisions = decisions
	sctx.PriorBranchDecisionsTruncated = truncated
}

func BindPreviousRunReviewRounds(sctx *StepContext) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil {
		return
	}
	// The supersede channel exists for the conversation, so it is off with it:
	// a repository that did not ask for the conversation gets the review prompt
	// it got before this feature, and a superseded run's rounds stay where they
	// already were - the uncertified-range channel and the branch decisions.
	if sctx.Config == nil || !sctx.Config.Review.Conversation {
		return
	}
	branch := strings.TrimSpace(sctx.Run.Branch)
	if sctx.Repo.ID == "" || branch == "" {
		return
	}
	previous, err := sctx.DB.GetPreviousRunReviewRounds(sctx.Repo.ID, branch, sctx.Run.ID)
	if err != nil {
		slog.Warn("failed to read the previous run's review rounds; continuing without them", "repo_id", sctx.Repo.ID, "error", err)
		return
	}
	if previous == nil || previous.RunID == strings.TrimSpace(sctx.UncertifiedSourceRunID) {
		return
	}
	sctx.PreviousRunReviewRounds = previous.Rounds
}
