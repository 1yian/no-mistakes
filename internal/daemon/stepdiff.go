package daemon

import (
	"context"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
)

const maxStepDiffBytes = 512 * 1024

func (m *RunManager) StepDiff(ctx context.Context, runID string) (string, bool, error) {
	run, err := m.db.GetRun(runID)
	if err != nil {
		return "", false, fmt.Errorf("get run: %w", err)
	}
	if run == nil {
		return "", false, fmt.Errorf("run not found: %s", runID)
	}
	repo, err := m.db.GetRepo(run.RepoID)
	if err != nil {
		return "", false, fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return "", false, fmt.Errorf("repo not found for run %s", runID)
	}

	diff, err := git.DiffHead(ctx, worktrees.RecordedDir(m.paths, run.WorktreePath(), repo.ID, run.ID))
	if err != nil {
		return "", false, fmt.Errorf("diff worktree: %w", err)
	}
	if len(diff) > maxStepDiffBytes {
		return diff[:maxStepDiffBytes], true, nil
	}
	return diff, false, nil
}
