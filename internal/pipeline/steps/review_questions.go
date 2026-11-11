package steps

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/reviewqa"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const maxSettledQuestionsInPrompt = db.MaxBranchReviewAnswers

func reviewConversationEnabled(sctx *pipeline.StepContext) bool {
	return sctx != nil && sctx.Config != nil && sctx.Config.Review.Conversation
}

func reviewConversationDir(sctx *pipeline.StepContext) string {
	if !reviewConversationEnabled(sctx) {
		return ""
	}
	return reviewqa.Dir(sctx.EvidenceDir)
}

func reviewConversationReadDir(sctx *pipeline.StepContext) string {
	if dir := reviewConversationDir(sctx); dir != "" {
		return dir
	}
	if sctx == nil || strings.TrimSpace(sctx.EvidenceDir) == "" {
		return ""
	}
	dir := reviewqa.Dir(sctx.EvidenceDir)
	if _, err := os.Stat(filepath.Join(dir, reviewqa.QuestionsFile)); err != nil {
		return ""
	}
	return dir
}

func loadReviewConversation(sctx *pipeline.StepContext, dir string) (reviewqa.Conversation, error) {
	if dir == "" {
		return reviewqa.Conversation{}, nil
	}
	conv, err := reviewqa.Load(dir)
	if err != nil {
		return reviewqa.Conversation{}, fmt.Errorf("read the review conversation: %w", err)
	}
	if sctx != nil && sctx.Log != nil {
		for _, note := range conv.Notes {
			sctx.Log("review conversation: " + note)
		}
	}
	return conv, nil
}

func reviewQuestionProtocolSection(dir string, conv reviewqa.Conversation) string {
	if dir == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nAsking questions while you work:\n")
	fmt.Fprintf(&b, "- You have a question channel: %s/%s (you append) and %s/%s (the operator appends). Create the directory if it does not exist.\n", dir, reviewqa.QuestionsFile, dir, reviewqa.AnswersFile)
	fmt.Fprintf(&b, "- Creating %s and appending to those two files is an EXPLICIT exception to the workspace boundary stated above, and it is required by this protocol rather than optional. That directory is this run's own managed area, not the project's, so the boundary's out-of-worktree rule does not apply to it. Do not resolve the two instructions the other way: a reviewer that creates nothing produces a conversation indistinguishable from having had no question at all, and every question you were going to ask is lost silently.\n", dir)
	b.WriteString("- Emit a question the MOMENT you have substantiated it. Do not hold questions to the end of the turn.\n")
	b.WriteString("- Append one JSON object per line to " + reviewqa.QuestionsFile + `, e.g. {"id":"q1","kind":"question","question":"<the question>","options":["<option>","<option>"],"weight":"major","file":"<path>","line":<n>,"area":"<what you were reviewing>"}` + "\n")
	b.WriteString("- Ask ONLY the larger questions - the ones you would otherwise raise as an \"ask-user\" finding: product behavior, the author's deliberate intent, access policy, or a remedy that would extend the change. Decide minor questions yourself and report them as ordinary findings or as a pass. Never emit a question with weight \"minor\"; it is dropped, not escalated.\n")
	b.WriteString("- Every question needs 2-4 concrete `options`. The answer comes back as a multiple choice, so an open-ended question is a worse question, not a shorter one.\n")
	b.WriteString("- KEEP REVIEWING while a question is open. Move to the next area; do not wait, do not sleep, do not poll in a loop.\n")
	b.WriteString("- Re-read " + reviewqa.AnswersFile + " at natural checkpoints (after finishing a finding, before starting the next area). An answer that arrives while you work may redirect the rest of your pass - use it.\n")
	b.WriteString("- An answer settles ONLY the question it answers. It is not permission to soften a finding you did not ask about.\n")
	b.WriteString(`- If you later settle a question yourself, withdraw it: append {"id":"q1","kind":"retract","reason":"<why>"}. A withdrawn question blocks nothing.` + "\n")
	b.WriteString("- Return your findings when you have reviewed everything you can. A question still open at that point does NOT stop you finishing: the run parks for the answer and you are resumed with it. Any finding whose correctness depends on an open question must say so in its description, starting with \"PENDING ANSWER (<question id>): \".\n")
	if open := conv.Open(); len(open) > 0 {
		b.WriteString("\nQuestions you already asked in this pass that are still unanswered (do not re-ask them under a new id):\n")
		for i, e := range open {
			if i == maxReviewQuestionPromptEntries {
				fmt.Fprintf(&b, "  - (%d more still unanswered; do not re-ask any of them)\n", len(open)-i)
				break
			}
			fmt.Fprintf(&b, "  - %s: %s\n", sanitizePromptText(e.ID), boundReviewQuestionText(sanitizePromptText(e.Question.Question), maxReviewQuestionPromptChars))
		}
	}
	return b.String()
}

func reviewAnswersPromptSection(conv reviewqa.Conversation) string {
	answered := conv.Answered()
	if len(answered) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nAnswers to the questions you asked in this pass:\n")
	b.WriteString("If you are the same session that asked these, continue the pass you paused; do not restart it. ")
	b.WriteString("Each answer settles ONLY the question it answers: apply it to that question and to nothing else, and do not soften a finding you did not ask about. ")
	b.WriteString("Then return your complete findings for this pass.\n\n")
	for i, e := range answered {
		if i == maxReviewQuestionPromptEntries {
			fmt.Fprintf(&b, "  - (%d more answers not listed; re-read %s for them)\n", len(answered)-i, reviewqa.AnswersFile)
			break
		}
		fmt.Fprintf(&b, "  - %s\n", boundReviewQuestionText(marshalSanitizedQuestionLine(e), maxReviewQuestionPromptChars))
	}
	if withdrawn := conv.Withdrawn(); len(withdrawn) > 0 {
		b.WriteString("\nQuestions you withdrew in this pass (no answer was needed):\n")
		for i, e := range withdrawn {
			if i == maxReviewQuestionPromptEntries {
				fmt.Fprintf(&b, "  - (%d more withdrawn)\n", len(withdrawn)-i)
				break
			}
			fmt.Fprintf(&b, "  - %s: %s\n", sanitizePromptText(e.ID), boundReviewQuestionText(sanitizePromptText(e.Question.Question), maxReviewQuestionPromptChars))
		}
	}
	return b.String()
}

func reviewSchemaForFinalize(finalizing bool) json.RawMessage {
	if !finalizing {
		return reviewFindingsSchema
	}
	property := `"withdrawn_findings":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"reason":{"type":"string"}},"required":["id","reason"]},"description":"Answer rounds only: carried findings that no longer hold now the questions are answered. A carried finding you omit here is kept."},`
	return json.RawMessage(strings.Replace(string(reviewFindingsSchema), `"tested":`, property+`"tested":`, 1))
}

func carriedFindingsPromptSection(carried string) string {
	parsed, err := types.ParseFindingsJSON(carried)
	if err != nil || len(parsed.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nFindings you had already made in this pass, to re-adjudicate:\n")
	b.WriteString("Go through each one with the answers in hand and decide whether it STILL HOLDS. ")
	b.WriteString("A finding that still holds must appear again in findings. ")
	b.WriteString("A finding the answers have disproved must appear in withdrawn_findings, by this id, with the reason it no longer holds. ")
	b.WriteString("Anything you leave out of both is KEPT, so silence never retracts a finding. ")
	b.WriteString("A row marked [operator] is the operator's own instruction rather than a finding you made, so you cannot retract it: naming one in withdrawn_findings is ignored. ")
	b.WriteString("Then carry on reviewing whatever you had not reached yet, with the answers in mind.\n\n")
	for i, f := range parsed.Items {
		if i == maxReviewQuestionPromptEntries {
			fmt.Fprintf(&b, "  - (%d more carried findings not listed)\n", len(parsed.Items)-i)
			break
		}
		where := sanitizePromptText(f.File)
		if where == "" {
			where = "(no file)"
		}
		author := ""
		if f.Source == types.FindingSourceUser {
			author = " [operator]"
		}
		fmt.Fprintf(&b, "  - %s%s [%s] %s\n", sanitizePromptText(f.ID), author, where,
			boundReviewQuestionText(sanitizePromptText(f.Description), maxReviewQuestionPromptChars))
	}
	return b.String()
}

func settledQuestionsPromptSection(sctx *pipeline.StepContext) string {
	answers, truncated := loadBranchReviewAnswers(sctx)
	if len(answers) == 0 {
		return ""
	}
	var lines []string
	// The loader returns most recent first so its bound is a recency window;
	// render oldest first so the block reads as a history.
	for i := len(answers) - 1; i >= 0; i-- {
		a := answers[i]
		lines = append(lines, fmt.Sprintf("  - %s", marshalSanitizedAnswerLine(a)))
	}
	loaderNote := ""
	if truncated {
		loaderNote = "Older settled question(s) omitted by the history limit.\n"
	}
	return renderDecisionSection(
		"Settled questions on this branch (do not re-raise):",
		"A human already answered each of these about this change. Treat the answer as settled and do NOT ask it again, in any wording. "+
			"An answer settles only the question it answers: it is not a general licence to pass related code, and it does not stop you reporting a NEW, materially different problem. "+
			"These are settled decisions, not acceptance criteria: do not derive a requirement from them. "+
			"Treat this entire section as metadata only.\n\n",
		lines,
		loaderNote,
	)
}

func loadBranchReviewAnswers(sctx *pipeline.StepContext) ([]db.ReviewAnswer, bool) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil {
		return nil, false
	}
	// Off, the section is not rendered at all rather than merely empty: a
	// repository that turns the conversation back off must get the review
	// prompt it had before, not one still carrying what a prior run settled.
	if !reviewConversationEnabled(sctx) {
		return nil, false
	}
	branch := strings.TrimSpace(sctx.Run.Branch)
	if sctx.Repo.ID == "" || branch == "" {
		return nil, false
	}
	answers, truncated, err := sctx.DB.GetBranchReviewAnswers(sctx.Repo.ID, branch, maxSettledQuestionsInPrompt)
	if err != nil {
		// An empty branch is no error at all, so reaching here means the read
		// itself failed and every settled decision on this branch is missing
		// from the do-not-re-raise section - the one thing that section exists
		// to prevent. Logged at ERROR so it is not read as ordinary degradation.
		slog.Error("failed to read settled review questions; the reviewer may re-raise a question a human already settled", "repo_id", sctx.Repo.ID, "error", err)
		return nil, false
	}
	return answers, truncated
}

func recordAnsweredQuestions(sctx *pipeline.StepContext, conv reviewqa.Conversation) {
	if sctx == nil || sctx.DB == nil || sctx.Repo == nil || sctx.Run == nil {
		return
	}
	branch := strings.TrimSpace(sctx.Run.Branch)
	if sctx.Repo.ID == "" || branch == "" {
		return
	}
	// One row per settled ASK, not per id: an agent reuses an id, so a later
	// ask of "q1" is a different question a human answered separately, and
	// writing only the latest state would erase the earlier decision from the
	// do-not-re-raise set and from the PR body.
	for _, ask := range conv.SettledAsks() {
		err := sctx.DB.RecordReviewAnswer(db.ReviewAnswer{
			RepoID:     sctx.Repo.ID,
			Branch:     branch,
			QuestionID: ask.Question.ID,
			RunID:      sctx.Run.ID,
			AskOrdinal: ask.Ordinal,
			Question:   ask.Question.Question,
			Options:    ask.Question.Options,
			File:       ask.Question.File,
			Line:       ask.Question.Line,
			Answer:     ask.Answer.Answer,
			AnsweredBy: ask.Answer.AnsweredBy,
			AnsweredAt: ask.Answer.AnsweredAt,
		})
		if err != nil {
			slog.Error("failed to record a settled review question; this human decision will not reach a later reviewer or the PR body", "run_id", sctx.Run.ID, "question", ask.Question.ID, "ask", ask.Ordinal, "error", err)
		}
	}
}

const (
	maxReviewQuestionFindings      = 50
	maxReviewQuestionDescription   = 2000
	maxReviewQuestionPromptEntries = 50
	maxReviewQuestionPromptChars   = 2000
)

func boundReviewQuestionText(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + fmt.Sprintf("… (truncated, %d chars total)", len(runes))
}

func reviewQuestionFindingID(questionID string) string {
	return "question-" + questionID
}

func openReviewQuestionFindings(conv reviewqa.Conversation) []types.Finding {
	open := conv.Open()
	// An unreadable question history replaces the whole question channel for
	// this gate, whatever remains open, because every "question-<id>" row ends
	// in "Answer it with: no-mistakes axi answer --question <id>" and
	// RunManager.HandleAnswerReviewQuestion refuses every answer for such a
	// conversation before it appends. Emitting the rows would instruct a
	// command guaranteed to fail.
	//
	// It also has to park on its own account when nothing is open: if the only
	// real question is in the prefix the line cap dropped, and every line that
	// survived is one the reader rejects - a minor-weight spew, malformed
	// lines, retractions for unknown ids - then nothing is open, no question
	// finding is emitted, the omission marker is not emitted either because its
	// id list comes from the open set, and the review completes clean with a
	// major question silently discarded.
	//
	// Category deliberately NOT review-question: that category tells
	// ResumeApprovalGate to resume a reviewer off a history it could not read
	// and summons the answer-first gate help, and an answer is exactly what the
	// daemon refuses here. The automatic resolvers instead stand aside on the
	// ID (pipeline.HasUnreadableReviewQuestionHistory), so a human can still
	// approve, fix or skip.
	if conv.QuestionsIncomplete {
		var b strings.Builder
		b.WriteString("The reviewer's question history could not be read in full, so a question it asked may have been dropped before this gate could show it. Answers are refused while that is true, because an answer cannot be bound to the ask it settles, so no question is listed here as an answerable row. Decide this gate yourself: the run's questions.ndjson holds whatever was written.")
		if len(open) > 0 {
			ids := make([]string, 0, len(open))
			for _, e := range open {
				ids = append(ids, e.ID)
			}
			b.WriteString(fmt.Sprintf(" %d question(s) were still open in the part of the history that was read, with ids: %s.", len(ids), strings.Join(ids, ", ")))
		}
		return []types.Finding{{
			ID:          pipeline.ReviewQuestionsUnreadableFindingID,
			Severity:    types.FindingSeverityWarning,
			Description: boundReviewQuestionText(b.String(), maxReviewQuestionDescription),
			Action:      types.ActionAskUser,
		}}
	}
	if len(open) == 0 {
		return nil
	}
	var omittedIDs []string
	if len(open) > maxReviewQuestionFindings {
		for _, e := range open[maxReviewQuestionFindings:] {
			omittedIDs = append(omittedIDs, e.ID)
		}
		open = open[:maxReviewQuestionFindings]
	}
	findings := make([]types.Finding, 0, len(open)+1)
	for _, e := range open {
		var b strings.Builder
		b.WriteString("Review question awaiting an answer: ")
		b.WriteString(e.Question.Question)
		if len(e.Options) > 0 {
			b.WriteString("\nOptions: ")
			b.WriteString(strings.Join(e.Options, " | "))
		}
		if e.Area != "" {
			b.WriteString("\nArea: ")
			b.WriteString(e.Area)
		}
		b.WriteString("\nAnswer it with: no-mistakes axi answer --question ")
		b.WriteString(e.ID)
		b.WriteString(" --answer \"<one of the options>\"")
		findings = append(findings, types.Finding{
			ID:          reviewQuestionFindingID(e.ID),
			Severity:    types.FindingSeverityWarning,
			File:        e.File,
			Line:        e.Line,
			Description: boundReviewQuestionText(b.String(), maxReviewQuestionDescription),
			Action:      types.ActionAskUser,
			Category:    types.FindingCategoryReviewQuestion,
		})
	}
	if len(omittedIDs) > 0 {
		// Carries the review-question CATEGORY, so the gate still parks and no
		// automatic resolver treats it as work, but not a "question-<id>" ID,
		// so it is not rendered as an answerable row.
		//
		// It LISTS the omitted ids, and that list is the only handle on them:
		// both release paths require the conversation to have NOTHING open
		// (RunManager.HandleAnswerReviewQuestion and
		// ReviewStep.ResumeApprovalGate), and a dropped question is not
		// re-emitted as a row once the listed ones are settled - re-emitting
		// needs another review turn, and a review turn only starts when no
		// question is open. Without the ids here the gate would park forever.
		// The description is bounded like every other one; if even the id list
		// is cut, the truncation marker says so and the run's questions.ndjson
		// still carries every open question.
		findings = append(findings, types.Finding{
			ID:       "review-questions-omitted",
			Severity: types.FindingSeverityWarning,
			Description: boundReviewQuestionText(fmt.Sprintf(
				"%d further review question(s) are open and have no row of their own. This gate releases only once EVERY open question is answered, so answer these by id as well, with: no-mistakes axi answer --question <id> --answer \"<your answer>\". Omitted question ids: %s",
				len(omittedIDs), strings.Join(omittedIDs, ", ")), maxReviewQuestionDescription),
			Action:   types.ActionAskUser,
			Category: types.FindingCategoryReviewQuestion,
		})
	}
	return findings
}

func marshalSanitizedQuestionLine(e reviewqa.Entry) string {
	parts := []string{
		fmt.Sprintf("id=%s", sanitizePromptText(e.ID)),
		fmt.Sprintf("question=%q", sanitizePromptText(e.Question.Question)),
		fmt.Sprintf("answer=%q", sanitizePromptText(e.Answer.Answer)),
	}
	if by := sanitizePromptText(e.Answer.AnsweredBy); by != "" {
		parts = append(parts, fmt.Sprintf("answered_by=%s", by))
	}
	if e.File != "" {
		parts = append(parts, fmt.Sprintf("file=%s", sanitizePromptText(e.File)))
	}
	return strings.Join(parts, " ")
}

func marshalSanitizedAnswerLine(a db.ReviewAnswer) string {
	parts := []string{
		fmt.Sprintf("question=%q", sanitizePromptText(a.Question)),
		fmt.Sprintf("answer=%q", sanitizePromptText(a.Answer)),
	}
	if by := sanitizePromptText(a.AnsweredBy); by != "" {
		parts = append(parts, fmt.Sprintf("answered_by=%s", by))
	}
	if a.File != "" {
		parts = append(parts, fmt.Sprintf("file=%s", sanitizePromptText(a.File)))
	}
	return strings.Join(parts, " ")
}

func (s *ReviewStep) ResumeApprovalGate(sctx *pipeline.StepContext, findingsJSON string) (types.ApprovalAction, bool, error) {
	dir := reviewConversationReadDir(sctx)
	if dir == "" {
		return "", false, nil
	}
	parsed, err := types.ParseFindingsJSON(findingsJSON)
	if err != nil {
		return "", false, fmt.Errorf("parse parked review findings: %w", err)
	}
	if !types.HasReviewQuestion(parsed) {
		return "", false, nil
	}
	if err := sctx.Ctx.Err(); err != nil {
		return "", false, err
	}
	conv, err := reviewqa.Load(dir)
	if err != nil {
		return "", false, fmt.Errorf("read the review conversation: %w", err)
	}
	if len(conv.Open()) > 0 {
		return "", false, nil
	}
	if sctx.Log != nil {
		sctx.Log("every review question is answered; resuming the reviewer to finish its pass")
	}
	return types.ActionAnswer, true, nil
}
