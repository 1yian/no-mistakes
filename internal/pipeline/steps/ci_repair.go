package steps

import (
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func verifyGuardedCIRepairPR(sctx *pipeline.StepContext, host scm.Host) error {
	if sctx == nil || sctx.DB == nil || sctx.Run == nil {
		return nil
	}
	a, err := sctx.DB.CIRepairObligation(sctx.Run.ID)
	if err != nil || a == nil {
		return err
	}
	if a.Phase != "repair" && a.Phase != "revalidate" && a.Phase != "fulfilled" {
		return fmt.Errorf("guarded CI repair PR operation is held unvalidated")
	}
	run, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		return err
	}
	if run.Status != types.RunRunning || run.CustodyReturnedAt != nil || run.HeadSHA != sctx.Run.HeadSHA || run.LastPushedSHA == nil || *run.LastPushedSHA == "" || run.PRURL == nil || *run.PRURL != a.PRURL || sctx.Run.PRURL == nil || *sctx.Run.PRURL != a.PRURL {
		return fmt.Errorf("guarded CI repair canonical identity or publication changed")
	}
	_, _, _, err = guardedCIRepairPRProof(sctx, host, *run.LastPushedSHA)
	return err
}

func recordGuardedCIRepair(sctx *pipeline.StepContext, head string) (ciRepairResult, bool, error) {
	a, err := sctx.DB.CIRepairObligation(sctx.Run.ID)
	if err != nil || a == nil {
		return ciRepairResult{}, false, err
	}
	if sctx.CIRepairOwner == "" || a.Owner != sctx.CIRepairOwner {
		return ciRepairResult{}, true, fmt.Errorf("guarded CI repair monitor ownership changed")
	}
	if _, err := stepGitRun(sctx, "merge-base", "--is-ancestor", sctx.Run.HeadSHA, head); err != nil {
		return ciRepairResult{}, true, fmt.Errorf("guarded CI repair must extend the current pipeline history: %w", err)
	}
	if err := verifyGuardedCIRepairPR(sctx, nil); err != nil {
		return ciRepairResult{}, true, err
	}
	if err := updateNonSharedBranchRef(sctx, head); err != nil {
		return ciRepairResult{}, true, err
	}
	if err := sctx.DB.RecordCIRepairHead(sctx.Run.ID, sctx.CIRepairOwner, sctx.Run.HeadSHA, head); err != nil {
		return ciRepairResult{}, true, err
	}
	sctx.Run.HeadSHA = head
	sctx.Run.ReviewApprovedHeadSHA = nil
	return ciRepairResult{HeadAdvanced: true, Revalidate: true}, true, nil
}

func GuardedCIRepairAdmissionOutcome() *pipeline.StepOutcome {
	findings, _ := types.MarshalFindingsJSON(Findings{Items: []types.Finding{{ID: "guarded-ci-repair", Severity: types.FindingSeverityWarning, Action: types.ActionAskUser, Description: "Guarded CI repair remains unvalidated. Select fix to create genuine CI or repair the real failing CI on top of the original pipeline head, preserving every existing commit. No-edit or failed fixes do not discharge this obligation. The pipeline must freshly run Review onward and actual repaired-head CI; do not fabricate success, declare no_ci, skip phases or publish directly."}}})
	return &pipeline.StepOutcome{NeedsApproval: true, Findings: findings}
}

func verifyCIRepairPublishHead(sctx *pipeline.StepContext, head string) error {
	if err := sctx.DB.VerifyCIRepairPublication(sctx.Run.ID); err != nil {
		return err
	}
	a, err := sctx.DB.CIRepairObligation(sctx.Run.ID)
	if err != nil || a == nil {
		return err
	}
	for _, ancestor := range []string{a.OriginalHead, a.RepairHead} {
		if ancestor == "" {
			return fmt.Errorf("guarded CI repair has no recorded repair head")
		}
		if _, err := stepGitRun(sctx, "merge-base", "--is-ancestor", ancestor, head); err != nil {
			return fmt.Errorf("guarded CI repair publication must preserve the original and repaired histories: %w", err)
		}
	}
	return verifyGuardedCIRepairPR(sctx, nil)
}

func genuineCIRepairPass(checks []scm.Check) bool {
	for _, check := range checks {
		_, bot := scm.ReviewBotForCheck(scm.ReviewBots, check)
		if check.Bucket == scm.CheckBucketPass && !bot && check.ExternalReview == "" {
			return true
		}
	}
	return false
}
