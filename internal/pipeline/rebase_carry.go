package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func (e *Executor) carryTestAcrossRebaseRepair(run *db.Run, repo *db.Repo, step Step, sr *db.StepResult, logDir string) (bool, error) {
	if step.Name() != types.StepTest {
		return false, nil
	}
	if e.config == nil || e.config.CI.RevalidateRepairs || e.config.CI.RebaseRepair != config.CIRebaseRepairReview {
		return false, nil
	}
	pending, _, err := e.db.RunRebaseReview(run.ID)
	if err != nil || pending == "" {
		return false, err
	}
	current, err := e.db.GetRun(run.ID)
	if err != nil {
		return false, err
	}
	if current == nil || current.ReviewApprovedHeadSHA == nil ||
		!strings.EqualFold(strings.TrimSpace(*current.ReviewApprovedHeadSHA), pending) ||
		!strings.EqualFold(strings.TrimSpace(current.HeadSHA), pending) {
		return false, nil
	}
	row, err := e.db.GetStepResult(sr.ID)
	if err != nil {
		return false, err
	}
	if row == nil || row.Status != types.StepStatusPending {
		return false, nil
	}
	if err := e.db.CarryStepAcrossRebase(sr.ID); err != nil {
		return false, err
	}
	text := fmt.Sprintf("Test carried, not re-run: Review approved the rebased head %s unchanged, so Test's last result belongs to the head published before the rebase. CI gates the tests on this head.\n\n", shortHead(pending))
	e.emitLogChunk(run, repo, step.Name(), text)
	if f, err := os.OpenFile(filepath.Join(logDir, string(step.Name())+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		fmt.Fprint(f, text)
		f.Close()
	}
	zero := int64(0)
	e.emitStepEventWithFindingsAndError(ipc.EventStepCompleted, run, repo, step.Name(), string(types.StepStatusCompleted), "", "", &zero)
	return true, nil
}

func shortHead(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
