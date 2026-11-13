package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func WithdrawActiveRecoveryAttestation(sctx *pipeline.StepContext, preserve func(string) error) error {
	host, reason := buildHost(sctx, resolvedProvider(sctx))
	if host == nil {
		return fmt.Errorf("cannot withdraw attestation: %s", reason)
	}
	if err := host.Available(sctx.Ctx); err != nil {
		return err
	}
	reader, ok := host.(scm.PRContentReader)
	if !ok {
		return fmt.Errorf("recovery requires live PR content reads")
	}
	discovered, err := host.FindPR(sctx.Ctx, sctx.Run.Branch, "")
	if err != nil {
		return err
	}
	pr, err := bindExistingPR(sctx, host, discovered)
	if err != nil {
		return err
	}
	if pr == nil || sctx.Run.PRURL == nil || pr.URL != *sctx.Run.PRURL {
		return fmt.Errorf("recovery PR identity changed")
	}
	state, err := host.GetPRState(sctx.Ctx, pr)
	if err != nil {
		return err
	}
	if state != scm.PRStateOpen {
		return fmt.Errorf("recovery requires an open PR")
	}
	content, err := reader.GetPRContent(sctx.Ctx, pr)
	if err != nil {
		return err
	}
	if strings.Count(content.Body, pipelineAttestationCommentPrefix) != 1 {
		return fmt.Errorf("recovery requires exactly one live attestation")
	}
	_, payload, _ := strings.Cut(content.Body, pipelineAttestationCommentPrefix)
	payload, _, ok = strings.Cut(payload, pipelineAttestationCommentClosingToken)
	var attestation pipelineAttestation
	if !ok || json.Unmarshal([]byte(payload), &attestation) != nil || attestation.HeadSHA != sctx.Run.HeadSHA {
		return fmt.Errorf("recovery attestation does not bind the pinned published head")
	}
	invalid := make([]*db.StepResult, 0, len(types.AllSteps()))
	for _, name := range types.AllSteps() {
		invalid = append(invalid, &db.StepResult{StepName: name, Status: types.StepStatusFailed})
	}
	updated, rebound, err := rebindOwnedPRAttestation(content.Body, sctx.Run.HeadSHA, invalid, pipelineAttestationPolicy{})
	if err != nil {
		return err
	}
	if !rebound {
		return fmt.Errorf("cannot withdraw the existing attestation")
	}
	const notice = "> Validation withdrawn for preservation-first recovery. This PR is not validated; fresh full validation is required."
	if !strings.Contains(updated, notice) {
		updated = strings.Replace(updated, pipelineAttestationCommentPrefix, notice+"\n\n"+pipelineAttestationCommentPrefix, 1)
	}
	if err := validateOwnedPRBudget(updated, scm.HostMaxPRBodyChars(host)); err != nil {
		return err
	}
	if err := preserve(content.Body); err != nil {
		return err
	}
	latest, err := reader.GetPRContent(sctx.Ctx, pr)
	if err != nil {
		return err
	}
	if latest.Body != content.Body {
		return fmt.Errorf("PR content changed before attestation withdrawal")
	}
	if _, err := host.UpdatePR(sctx.Ctx, pr, scm.PRContent{Body: updated}); err != nil {
		return err
	}
	confirmed, err := reader.GetPRContent(sctx.Ctx, pr)
	if err != nil {
		return err
	}
	if confirmed.Body != updated {
		return fmt.Errorf("attestation withdrawal could not be confirmed")
	}
	state, err = host.GetPRState(sctx.Ctx, pr)
	if err != nil || state != scm.PRStateOpen {
		return fmt.Errorf("PR is no longer open after validation withdrawal: %v", err)
	}
	return nil
}
