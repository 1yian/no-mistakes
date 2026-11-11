package steps

import (
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func pipelineDeliveryPhaseClause() string {
	return "\n\nPipeline phase (review is pre-push): this same run owns push, pull-request creation or update, and CI monitoring in later pipeline steps. Do NOT emit findings solely because the remote branch, push, pull request, or CI for this run's change is missing or not yet present - those are outputs this pipeline produces later. Continue reviewing the implementation and every source-verifiable acceptance criterion. Requirements about a pre-existing external PR, a specific third-party artifact, or lifecycle state not owned by the current run remain fully enforceable."
}

func stripDeferredPipelineOwnedDeliveryFindings(findings Findings) (Findings, int) {
	if len(findings.Items) == 0 {
		return findings, 0
	}
	kept := make([]Finding, 0, len(findings.Items))
	dropped := 0
	for _, item := range findings.Items {
		if isDeferredPipelineOwnedDeliveryFinding(item) {
			dropped++
			continue
		}
		kept = append(kept, item)
	}
	if dropped == 0 {
		return findings, 0
	}
	out := findings
	out.Items = kept
	out.Summary = filteredReviewSummary(kept)
	switch findings.RiskScope {
	case types.FindingsRiskScopePipelineOwnedDelivery:
		out.RiskLevel = "low"
		out.RiskRationale = "no delivery-independent review risk was reported"
		out.RiskScope = types.FindingsRiskScopeSourceOrExternal
	case types.FindingsRiskScopeSourceOrExternal:
	default:
		out.RiskRationale = "review risk retained after deferred delivery filtering"
	}
	return out, dropped
}

func filteredReviewSummary(items []Finding) string {
	if len(items) == 0 {
		return "no review findings remain"
	}
	if len(items) == 1 {
		return "1 review finding remains"
	}
	return fmt.Sprintf("%d review findings remain", len(items))
}

func isDeferredPipelineOwnedDeliveryFinding(item Finding) bool {
	return item.ReviewScope == types.FindingReviewScopePipelineOwnedDelivery
}
