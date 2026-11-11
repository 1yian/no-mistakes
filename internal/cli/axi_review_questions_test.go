package cli

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func reviewQuestionGate(t *testing.T) stepView {
	t.Helper()
	return stepView{
		Name:   string(types.StepReview),
		Status: string(types.StepStatusAwaitingApproval),
		FindingsJSON: findingsJSON(t, []types.Finding{
			{
				ID: "review-1", Severity: "warning", File: "main.go",
				Action: types.ActionAskUser, Description: "calls os.Exit",
			},
			{
				ID:       "question-q1",
				Severity: types.FindingSeverityWarning,
				File:     "internal/api/router.go",
				Line:     88,
				Action:   types.ActionAskUser,
				Category: types.FindingCategoryReviewQuestion,
				Description: "Review question awaiting an answer: Should the legacy /v1 route keep answering?" +
					"\nOptions: Keep answering | Remove it" +
					"\nArea: routing" +
					"\nAnswer it with: no-mistakes axi answer --question q1 --answer \"<one of the options>\"",
			},
		}, "1 issue and 1 open question"),
	}
}

func TestReviewQuestionGate_LeadsWithAnswering(t *testing.T) {
	got := axiDoc(gateFields(reviewQuestionGate(t))...)

	for _, want := range []string{
		"question-q1",
		"Should the legacy /v1 route keep answering?",
		"no-mistakes axi answer --question",
		"Do not approve or fix to get past a review question",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review gate output is missing %q:\n%s", want, got)
		}
	}

	// The answering guidance must come before the approve/fix guidance, or an
	// agent that acts on the first help line takes the wrong action.
	answerAt := strings.Index(got, "no-mistakes axi answer --question")
	approveAt := strings.Index(got, "axi respond --action approve")
	if answerAt < 0 || approveAt < 0 || answerAt > approveAt {
		t.Fatalf("answering guidance must lead the review-question gate help:\n%s", got)
	}
}

func TestReviewQuestionGateHelpKeysOnTheCategory(t *testing.T) {
	gate := reviewQuestionGate(t)
	gate.FindingsJSON = findingsJSON(t, []types.Finding{
		{ID: "question-q1", Severity: "warning", Action: types.ActionAskUser, Description: "not actually a review question"},
	}, "1 issue")

	got := axiDoc(gateFields(gate)...)
	if strings.Contains(got, "axi answer --question") {
		t.Fatalf("a question-shaped ID summoned the answering help:\n%s", got)
	}
}

func TestReviewGateWithoutQuestionsIsUnchanged(t *testing.T) {
	gate := reviewQuestionGate(t)
	gate.FindingsJSON = findingsJSON(t, []types.Finding{
		{ID: "review-1", Severity: "warning", File: "main.go", Action: types.ActionAskUser, Description: "calls os.Exit"},
	}, "1 blocking issue")

	got := axiDoc(gateFields(gate)...)
	for _, unwanted := range []string{"axi answer --question", "Do not approve or fix"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("ordinary review gate leaked %q:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "axi respond --action approve") {
		t.Fatalf("ordinary review gate lost its approve guidance:\n%s", got)
	}
}

func TestAxiAnswerHasNoRunFlag(t *testing.T) {
	cmd := newAxiAnswerCmd()
	if f := cmd.Flags().Lookup("run"); f != nil {
		t.Fatalf("axi answer must not take --run; its run is the current branch's active run")
	}
	for _, want := range []string{"question", "answer", "by"} {
		if cmd.Flags().Lookup(want) == nil {
			t.Fatalf("axi answer is missing --%s", want)
		}
	}
}
