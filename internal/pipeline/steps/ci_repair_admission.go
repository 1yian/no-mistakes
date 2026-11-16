package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func guardedCIRepairPRProof(sctx *pipeline.StepContext, host scm.Host, head string) (scm.Host, *scm.PR, []scm.Check, error) {
	if err := sctx.Ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if sctx.Config == nil || sctx.Config.NoCI || head == "" {
		return nil, nil, nil, fmt.Errorf("guarded repair requires genuine exact-head CI")
	}
	remote, err := publishedBranchHead(sctx)
	if err != nil || remote != head {
		return nil, nil, nil, fmt.Errorf("guarded CI repair published head changed: %v", err)
	}
	if host == nil {
		var reason string
		host, reason = buildHost(sctx, resolvedProvider(sctx))
		if host == nil {
			return nil, nil, nil, fmt.Errorf("guarded CI repair provider unavailable: %s", reason)
		}
	}
	if err := host.Available(sctx.Ctx); err != nil {
		return nil, nil, nil, err
	}
	found, err := host.FindPR(sctx.Ctx, sctx.Run.Branch, "")
	if err != nil {
		return nil, nil, nil, err
	}
	if found == nil || sctx.Run.PRURL == nil || found.URL != *sctx.Run.PRURL {
		return nil, nil, nil, fmt.Errorf("guarded CI repair branch lookup no longer names the pinned PR")
	}
	number, err := scm.ExtractPRNumber(*sctx.Run.PRURL)
	if err != nil || found.Number != number {
		return nil, nil, nil, fmt.Errorf("guarded CI repair PR number does not match the pinned URL")
	}
	state, err := host.GetPRState(sctx.Ctx, found)
	if err != nil || state != scm.PRStateOpen {
		return nil, nil, nil, fmt.Errorf("guarded CI repair requires a proven open PR: %v", err)
	}
	found.HeadSHA = head
	checks, observed, moved, err := getCIChecks(sctx.Ctx, host, found)
	if err != nil || moved || observed != head {
		return nil, nil, nil, fmt.Errorf("guarded CI repair requires exact-head CI discovery: %v", err)
	}
	return host, found, checks, nil
}

func guardedCIRepairProof(sctx *pipeline.StepContext, head string, clean, defect bool) (scm.Host, *scm.PR, error) {
	if err := sctx.Ctx.Err(); err != nil {
		return nil, nil, err
	}
	local, err := stepGitHeadSHA(sctx)
	if err != nil || local != head || sctx.Run.HeadSHA != head {
		return nil, nil, fmt.Errorf("guarded CI repair managed head changed")
	}
	if clean {
		status, err := stepGitRun(sctx, "status", "--porcelain")
		if err != nil || status != "" {
			return nil, nil, fmt.Errorf("guarded CI repair admission requires a clean managed worktree")
		}
	}
	gate, exists, err := git.DirectRefTarget(sctx.Ctx, sctx.GateDir, "refs/heads/"+sctx.Run.Branch)
	if err != nil || !exists || gate != head {
		return nil, nil, fmt.Errorf("guarded CI repair gate head changed")
	}
	host, pr, checks, err := guardedCIRepairPRProof(sctx, nil, head)
	if err != nil {
		return nil, nil, err
	}
	if defect && len(checks) > 0 {
		genuineFailure := false
		for _, check := range checks {
			_, bot := scm.ReviewBotForCheck(scm.ReviewBots, check)
			if check.Failing() && !bot && check.ExternalReview == "" && !check.PreRunFailure {
				genuineFailure = true
			}
		}
		if !genuineFailure || hasExecutingPendingChecks(checks) {
			return nil, nil, fmt.Errorf("guarded CI repair requires genuinely absent CI or settled failing validation")
		}
	}
	return host, pr, nil
}

func PrepareGuardedCIRepair(sctx *pipeline.StepContext) (string, string, error) {
	host, pr, err := guardedCIRepairProof(sctx, sctx.Run.HeadSHA, true, true)
	if err != nil {
		return "", "", err
	}
	reader, ok := host.(scm.PRContentReader)
	if !ok {
		return "", "", fmt.Errorf("guarded CI repair requires live PR content reads")
	}
	content, err := reader.GetPRContent(sctx.Ctx, pr)
	if err != nil {
		return "", "", err
	}
	if strings.Count(content.Body, pipelineAttestationCommentPrefix) != 1 {
		return "", "", fmt.Errorf("guarded CI repair requires exactly one readable owned attestation")
	}
	_, payload, _ := strings.Cut(content.Body, pipelineAttestationCommentPrefix)
	payload, _, ok = strings.Cut(payload, pipelineAttestationCommentClosingToken)
	var attestation pipelineAttestation
	if !ok || json.Unmarshal([]byte(payload), &attestation) != nil || attestation.HeadSHA != sctx.Run.HeadSHA {
		return "", "", fmt.Errorf("guarded CI repair attestation does not bind the published head")
	}
	invalid := make([]*db.StepResult, 0, len(types.AllSteps()))
	for _, name := range types.AllSteps() {
		invalid = append(invalid, &db.StepResult{StepName: name, Status: types.StepStatusFailed})
	}
	updated, rebound, err := rebindOwnedPRAttestation(content.Body, sctx.Run.HeadSHA, invalid, pipelineAttestationPolicy{})
	if err != nil {
		return "", "", err
	}
	if !rebound {
		return "", "", fmt.Errorf("cannot withdraw guarded CI repair attestation")
	}
	const notice = "> Validation withdrawn for guarded same-run CI repair. Fresh Review onward and genuine repaired-head CI are required."
	if hasPRAppendixMarkers(updated) {
		parts, err := parsePROwnedBody(updated)
		if err != nil {
			return "", "", err
		}
		parts.appendix = strings.Replace(parts.appendix, pipelineAttestationCommentPrefix, notice+"\n\n"+pipelineAttestationCommentPrefix, 1)
		updated = parts.before + wrapPRAppendix(parts.appendix) + parts.after
	} else {
		updated = strings.Replace(updated, pipelineAttestationCommentPrefix, notice+"\n\n"+pipelineAttestationCommentPrefix, 1)
	}
	if err := validateOwnedPRBudget(updated, scm.HostMaxPRBodyChars(host)); err != nil {
		return "", "", err
	}
	return content.Body, updated, nil
}

func WithdrawGuardedCIRepair(sctx *pipeline.StepContext, original, withdrawn string) error {
	host, pr, err := guardedCIRepairProof(sctx, sctx.Run.HeadSHA, true, true)
	if err != nil {
		return err
	}
	reader, ok := host.(scm.PRContentReader)
	if !ok {
		return fmt.Errorf("guarded CI repair requires live PR content reads")
	}
	latest, err := reader.GetPRContent(sctx.Ctx, pr)
	if err != nil {
		return err
	}
	if latest.Body != original && latest.Body != withdrawn {
		return fmt.Errorf("guarded CI repair PR body changed outside the preserved withdrawal")
	}
	if latest.Body != withdrawn {
		if _, err := host.UpdatePR(sctx.Ctx, pr, scm.PRContent{Body: withdrawn}); err != nil {
			return err
		}
	}
	confirmed, err := reader.GetPRContent(sctx.Ctx, pr)
	if err != nil {
		return err
	}
	if confirmed.Body != withdrawn {
		return fmt.Errorf("guarded CI repair attestation withdrawal is unconfirmed")
	}
	_, _, err = guardedCIRepairProof(sctx, sctx.Run.HeadSHA, true, true)
	return err
}

func VerifyGuardedCIRepairHead(sctx *pipeline.StepContext, head string) error {
	_, _, err := guardedCIRepairProof(sctx, head, false, false)
	return err
}

func VerifyGuardedCIRepair(sctx *pipeline.StepContext, head, withdrawn string) error {
	host, pr, err := guardedCIRepairProof(sctx, head, false, false)
	if err != nil {
		return err
	}
	reader, ok := host.(scm.PRContentReader)
	if !ok {
		return fmt.Errorf("guarded CI repair requires live PR content reads")
	}
	body, err := reader.GetPRContent(sctx.Ctx, pr)
	if err != nil {
		return err
	}
	if body.Body != withdrawn {
		return fmt.Errorf("guarded CI repair withdrawal no longer matches the preserved body")
	}
	return nil
}
