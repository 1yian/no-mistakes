package pipeline

import (
	"errors"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

const protectedPathFindingID = "protected-path-refusal"

const ReviewQuestionsUnreadableFindingID = "review-questions-unreadable"

func IsReservedFindingID(id string) bool {
	switch id {
	case protectedPathFindingID, types.FindingIDTestAgentTimeout, types.FindingIDTestAgentUnvalidatedWork, ReviewQuestionsUnreadableFindingID, types.FindingIDReviewFixRoundsExhausted:
		return true
	default:
		return false
	}
}

func HasProtectedPathRefusal(findingsJSON string) bool {
	return hasFindingID(findingsJSON, protectedPathFindingID)
}

func HasUnvalidatedWorkRefusal(findingsJSON string) bool {
	return hasFindingID(findingsJSON, types.FindingIDTestAgentUnvalidatedWork)
}

func hasFindingID(findingsJSON, id string) bool {
	findings, _ := types.ParseFindingsJSON(findingsJSON)
	for _, finding := range findings.Items {
		if finding.ID == id {
			return true
		}
	}
	return false
}

func HasUnansweredReviewQuestion(findingsJSON string) bool {
	findings, err := types.ParseFindingsJSON(findingsJSON)
	if err != nil {
		return false
	}
	return types.HasReviewQuestion(findings)
}

func HasUnreadableReviewQuestionHistory(findingsJSON string) bool {
	findings, err := types.ParseFindingsJSON(findingsJSON)
	if err != nil {
		return false
	}
	for _, finding := range findings.Items {
		if finding.ID == ReviewQuestionsUnreadableFindingID {
			return true
		}
	}
	return false
}

func approvalRefusal(step types.StepName, findingsJSON string) string {
	switch {
	case HasProtectedPathRefusal(findingsJSON) && HasReviewFixRoundLimit(findingsJSON):
		return fmt.Sprintf("cannot approve a protected-path refusal, and the Review fix-round budget is exhausted so fix cannot retry %s either; skip this step or abort the run", step)
	case HasProtectedPathRefusal(findingsJSON):
		return fmt.Sprintf("cannot approve a protected-path refusal: resolve the reported edit, then use fix to retry %s; approval would skip unfinished work", step)
	case HasUnvalidatedWorkRefusal(findingsJSON):
		return fmt.Sprintf("cannot approve %s: the run worktree holds work no Test turn validated and approval would publish it; inspect it as the findings describe, then use fix to validate it, or abort", step)
	}
	return ""
}

type ProtectedPathError struct {
	Path string
	Rule string
}

func (e *ProtectedPathError) Error() string {
	return fmt.Sprintf("refusing automatic commit: dirty protected path %q matches protected_paths rule %q; index and worktree preserved, inspect and resolve the edit before retrying", e.Path, e.Rule)
}

func ProtectedPathOutcome(err error) *StepOutcome {
	var refusal *ProtectedPathError
	if !errors.As(err, &refusal) {
		return nil
	}
	findings, _ := types.MarshalFindingsJSON(types.Findings{
		Summary: "Automatic commit refused for a protected path",
		Items: []types.Finding{{
			ID:          protectedPathFindingID,
			Severity:    "error",
			File:        refusal.Path,
			Description: err.Error(),
			Action:      types.ActionAskUser,
		}},
	})
	return &StepOutcome{NeedsApproval: true, Findings: findings}
}
