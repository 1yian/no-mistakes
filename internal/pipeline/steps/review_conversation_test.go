package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/reviewqa"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func questionFindings(t *testing.T, findingsJSON string) []types.Finding {
	t.Helper()
	parsed, err := types.ParseFindingsJSON(findingsJSON)
	if err != nil {
		t.Fatalf("parse findings: %v", err)
	}
	var out []types.Finding
	for _, f := range parsed.Items {
		if f.Category == types.FindingCategoryReviewQuestion {
			out = append(out, f)
		}
	}
	return out
}

func enableReviewConversation(cfg *config.Config) {
	cfg.Review.Conversation = true
}

func withReviewConversation(sctx *pipeline.StepContext) *pipeline.StepContext {
	enableReviewConversation(sctx.Config)
	return sctx
}

func appendAgentQuestionLine(dir, line string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, reviewqa.QuestionsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

func TestReviewStep_QuestionEmittedMidTurnParksInWaitingOnAnswers(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	var convDir string
	ag := &mockAgent{}
	ag.runFn = func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		// Stand in for the reviewer's own file tools: emit the question the
		// moment it is substantiated, then carry on and return findings.
		// The prompt's worked example verbatim. It carries no asked_at,
		// because nothing in that prompt mentions one.
		if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"question","question":"Should the legacy /v1 route keep answering?","options":["Keep answering","Remove it"],"weight":"major","file":"internal/api/router.go","line":88,"area":"routing"}`); err != nil {
			return nil, err
		}
		return &agent.Result{Output: []byte(
			`{"findings":[{"id":"f-1","severity":"info","description":"PENDING ANSWER (q1): depends on the route decision","action":"no-op"}],"summary":"one open question","risk_level":"low","risk_rationale":"pending","risk_scope":"source-or-external"}`,
		)}, nil
	}
	sctx := withReviewConversation(newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{}))
	convDir = reviewConversationDir(sctx)

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// The prompt must actually describe the channel, or the reviewer has no
	// way to use it.
	if !strings.Contains(lastReviewPrompt(t, ag), reviewqa.QuestionsFile) || !strings.Contains(lastReviewPrompt(t, ag), "KEEP REVIEWING while a question is open") {
		t.Fatalf("review prompt is missing the question protocol:\n%s", lastReviewPrompt(t, ag))
	}

	conv, err := reviewqa.Load(convDir)
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if len(conv.Open()) != 1 || conv.Open()[0].ID != "q1" {
		t.Fatalf("conversation = %+v, want one open question q1", conv)
	}

	questions := questionFindings(t, outcome.Findings)
	if len(questions) != 1 {
		t.Fatalf("findings = %s, want one review-question finding", outcome.Findings)
	}
	q := questions[0]
	if q.Action != types.ActionAskUser {
		t.Fatalf("question finding action = %q, want ask-user so the step parks", q.Action)
	}
	if q.Severity != types.FindingSeverityWarning {
		t.Fatalf("question finding severity = %q, want warning: an open question is not a defect", q.Severity)
	}
	if q.File != "internal/api/router.go" || q.Line != 88 {
		t.Fatalf("question finding lost its anchor: %+v", q)
	}
	if !strings.Contains(q.Description, "Keep answering | Remove it") {
		t.Fatalf("question finding lost its options: %q", q.Description)
	}
	if q.ID != "question-q1" {
		t.Fatalf("finding id %q does not carry the question id", q.ID)
	}
	// An open question must NOT read as auto-fixable work: there is nothing
	// for a fixer to do, and the answer is the only thing that resolves it.
	if !types.HasAskUserFindings(mustParseFindings(t, outcome.Findings)) {
		t.Fatalf("open question did not produce an ask-user gate: %s", outcome.Findings)
	}
}

func TestReviewStep_RetractedQuestionDoesNotPark(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	var convDir string
	ag := &mockAgent{}
	ag.runFn = func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"question","question":"does the migration cover this?","options":["yes","no"],"weight":"major"}`); err != nil {
			return nil, err
		}
		// The retraction exactly as the prompt spells it: id, kind, reason,
		// and no `at`.
		if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"retract","reason":"the migration note answers it"}`); err != nil {
			return nil, err
		}
		return &agent.Result{Output: []byte(cleanReviewJSON)}, nil
	}
	sctx := withReviewConversation(newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{}))
	convDir = reviewConversationDir(sctx)

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := questionFindings(t, outcome.Findings); len(got) != 0 {
		t.Fatalf("withdrawn question still parked the step: %+v", got)
	}
	if outcome.NeedsApproval {
		t.Fatal("withdrawn question must not need approval")
	}
}

func TestReviewStep_AnswersResumeTheSameSessionAndFinalize(t *testing.T) {
	t.Parallel()
	turn := 0
	mock := &sessionMockAgent{}
	var convDir string
	mock.respond = func(opts agent.RunOpts) *agent.Result {
		if opts.Purpose != "review" {
			t.Errorf("unexpected agent purpose %q", opts.Purpose)
			return &agent.Result{Output: []byte(`{}`)}
		}
		turn++
		if turn == 1 {
			// A model that trimmed the example to the fields it was told are
			// required: no kind, no weight, no asked_at.
			if err := appendAgentQuestionLine(convDir, `{"id":"q1","question":"keep the legacy route?","options":["keep","remove"]}`); err != nil {
				t.Errorf("append question: %v", err)
			}
			return &agent.Result{Output: []byte(cleanReviewJSON)}
		}
		return &agent.Result{Output: []byte(cleanReviewJSON)}
	}

	exec, database, run, repo, workDir := reviewSessionHarness(t, mock, []pipeline.Step{&ReviewStep{}}, enableReviewConversation)
	convDir = exec.ReviewConversationDir(run.ID)

	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, workDir) }()

	waitForReviewStatus(t, database, run.ID, types.StepStatusAwaitingApproval)
	if err := reviewqa.AppendAnswer(convDir, reviewqa.Answer{ID: "q1", Answer: "keep", AnsweredBy: "captain", AskOrdinal: 1}); err != nil {
		t.Fatalf("append answer: %v", err)
	}
	if err := exec.Respond(types.StepReview, types.ActionAnswer, nil); err != nil {
		t.Fatalf("respond with answers: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("executor timed out")
	}

	reviews := reviewCalls(mock.snapshot())
	if len(reviews) != 2 {
		t.Fatalf("expected an asking turn and a finalize turn, got %d", len(reviews))
	}
	if reviews[0].Session == nil || reviews[0].Session.ID != "" {
		t.Fatalf("the asking turn must START a reviewer session, got %+v", reviews[0].Session)
	}
	if reviews[1].Session == nil || reviews[1].Session.ID != "sess-1" {
		t.Fatalf("the finalize turn must RESUME the asking session, got %+v", reviews[1].Session)
	}
	if !strings.Contains(reviews[1].Prompt, `answer="keep"`) || !strings.Contains(reviews[1].Prompt, "answered_by=captain") {
		t.Fatalf("finalize prompt is missing the answer:\n%s", reviews[1].Prompt)
	}
	if !strings.Contains(reviews[1].Prompt, "settles ONLY the question it answers") {
		t.Fatalf("finalize prompt lost the scope-of-an-answer instruction:\n%s", reviews[1].Prompt)
	}
	// A resume must not turn the finalize turn into a bare message: the prompt
	// has to stand on its own so a failed resume degrades to a cold review
	// rather than a meaningless one.
	if !strings.Contains(reviews[1].Prompt, "Do a full review pass before returning") {
		t.Fatalf("finalize prompt is not self-sufficient:\n%s", reviews[1].Prompt)
	}

	// The answer is persisted where the next COLD reviewer reads it.
	answers, _, err := database.GetBranchReviewAnswers(repo.ID, run.Branch, 0)
	if err != nil {
		t.Fatalf("read branch answers: %v", err)
	}
	if len(answers) != 1 || answers[0].Answer != "keep" || answers[0].AnsweredBy != "captain" {
		t.Fatalf("branch answers = %#v", answers)
	}

	// The answer round is not a fix round: no code changed, so nothing may
	// count it as one.
	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatalf("get steps: %v", err)
	}
	rounds, err := database.GetRoundsByStep(steps[0].ID)
	if err != nil {
		t.Fatalf("get rounds: %v", err)
	}
	if len(rounds) != 2 {
		t.Fatalf("expected 2 rounds, got %d", len(rounds))
	}
	if rounds[1].Trigger != "answer" || rounds[1].IsFixRound() {
		t.Fatalf("answer round = %q (fix=%v), want trigger answer and not a fix round", rounds[1].Trigger, rounds[1].IsFixRound())
	}
	if rounds[0].SelectionSource != nil {
		t.Fatalf("answering must not record a human selection on the asking round, got %q", *rounds[0].SelectionSource)
	}
}

func TestReviewStep_ParkedWaitDoesNotCountAgainstTheReviewAgentTimeout(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	var remaining []time.Duration
	var convDir string
	turn := 0
	ag := &deadlineRecordingAgent{onRun: func(ctx context.Context, _ agent.RunOpts) (*agent.Result, error) {
		if deadline, ok := ctx.Deadline(); ok {
			remaining = append(remaining, time.Until(deadline))
		} else {
			t.Error("review invocation ran without a deadline")
		}
		turn++
		if turn == 1 {
			if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"question","question":"keep it?","options":["keep","drop"],"weight":"major"}`); err != nil {
				return nil, err
			}
		}
		return &agent.Result{Output: []byte(cleanReviewJSON)}, nil
	}}
	sctx := withReviewConversation(newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{}))
	sctx.Config.ReviewAgentTimeout = 30 * time.Minute
	convDir = reviewConversationDir(sctx)

	step := &ReviewStep{}
	outcome, err := step.Execute(sctx)
	if err != nil {
		t.Fatalf("asking turn: %v", err)
	}
	if len(questionFindings(t, outcome.Findings)) != 1 {
		t.Fatalf("asking turn did not park on its question: %s", outcome.Findings)
	}

	if err := reviewqa.AppendAnswer(convDir, reviewqa.Answer{ID: "q1", Answer: "keep", AskOrdinal: 1}); err != nil {
		t.Fatalf("append answer: %v", err)
	}
	sctx.FinalizingAnswers = true
	outcome, err = step.Execute(sctx)
	if err != nil {
		t.Fatalf("finalize turn: %v", err)
	}
	if got := questionFindings(t, outcome.Findings); len(got) != 0 {
		t.Fatalf("finalized review still carries an open question: %+v", got)
	}

	if len(remaining) != 2 {
		t.Fatalf("expected 2 review invocations, got %d", len(remaining))
	}
	want := 30 * time.Minute
	for i, got := range remaining {
		if got > want+time.Second || got < want-time.Minute {
			t.Fatalf("review turn %d started with %s of budget, want a fresh %s silent budget", i+1, got, want)
		}
	}
}

func TestReviewStep_SettledQuestionReachesTheNextColdReviewer(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newStaticReviewAgent(cleanReviewJSON)
	sctx := withReviewConversation(newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{}))

	earlier, err := sctx.DB.InsertRun(sctx.Repo.ID, sctx.Run.Branch, "older-head", baseSHA)
	if err != nil {
		t.Fatalf("insert earlier run: %v", err)
	}
	if err := sctx.DB.RecordReviewAnswer(db.ReviewAnswer{
		RepoID: sctx.Repo.ID, Branch: sctx.Run.Branch, QuestionID: "q1", RunID: earlier.ID, AskOrdinal: 1,
		Question: "Should seed bytes be computed in the browser?",
		Answer:   "Yes, keep it in the browser", AnsweredBy: "captain",
	}); err != nil {
		t.Fatalf("record answer: %v", err)
	}

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(lastReviewPrompt(t, ag), "Settled questions on this branch (do not re-raise):") {
		t.Fatalf("cold reviewer prompt has no settled-questions section:\n%s", lastReviewPrompt(t, ag))
	}
	if !strings.Contains(lastReviewPrompt(t, ag), `answer="Yes, keep it in the browser"`) {
		t.Fatalf("settled section lost the answer:\n%s", lastReviewPrompt(t, ag))
	}
	// Separated from acceptance criteria on purpose: a settled question must
	// not read as a requirement the change has to satisfy.
	if !strings.Contains(lastReviewPrompt(t, ag), "These are settled decisions, not acceptance criteria") {
		t.Fatalf("settled section is not distinguished from acceptance criteria:\n%s", lastReviewPrompt(t, ag))
	}
}

func TestReviewStep_SupersedeCarriesThePreviousRunsReviewRounds(t *testing.T) {
	t.Parallel()
	t.Run("conversation on", func(t *testing.T) {
		assertSupersedeSection(t, true)
	})
	t.Run("conversation off", func(t *testing.T) {
		assertSupersedeSection(t, false)
	})
}

func assertSupersedeSection(t *testing.T, conversation bool) {
	t.Helper()
	mock := &sessionMockAgent{}
	mock.respond = func(agent.RunOpts) *agent.Result {
		return &agent.Result{Output: []byte(cleanReviewJSON)}
	}
	var tweaks []func(*config.Config)
	if conversation {
		tweaks = append(tweaks, enableReviewConversation)
	}
	exec, database, run, repo, workDir := reviewSessionHarness(t, mock, []pipeline.Step{&ReviewStep{}}, tweaks...)

	// The run the author's push superseded: its review found something, and a
	// human declined to have the pipeline fix it.
	superseded, err := database.InsertRun(repo.ID, run.Branch, "superseded-head", run.BaseSHA)
	if err != nil {
		t.Fatalf("insert superseded run: %v", err)
	}
	sr, err := database.InsertStepResult(superseded.ID, types.StepReview)
	if err != nil {
		t.Fatalf("insert step result: %v", err)
	}
	findings := `{"findings":[{"id":"f-9","severity":"error","description":"drops the straggler","action":"ask-user"}],"summary":"1 issue","risk_level":"high","risk_rationale":"bug","risk_scope":"source-or-external"}`
	if _, err := database.InsertStepRound(sr.ID, 1, "initial", &findings, nil, 0); err != nil {
		t.Fatalf("insert round: %v", err)
	}

	if err := exec.Execute(context.Background(), run, repo, workDir); err != nil {
		t.Fatalf("execute: %v", err)
	}

	reviews := reviewCalls(mock.snapshot())
	if len(reviews) != 1 {
		t.Fatalf("expected one review turn, got %d", len(reviews))
	}
	prompt := reviews[0].Prompt
	const heading = "Previous run's review rounds on this branch:"
	if !conversation {
		if strings.Contains(prompt, heading) || strings.Contains(prompt, "drops the straggler") {
			t.Fatalf("the superseded section reached a review prompt with the conversation off:\n%s", prompt)
		}
		return
	}
	if !strings.Contains(prompt, heading) {
		t.Fatalf("prompt lost the superseded run's rounds:\n%s", prompt)
	}
	if !strings.Contains(prompt, "drops the straggler") {
		t.Fatalf("superseded section lost the prior finding:\n%s", prompt)
	}
	// The author wrote the fix, not the pipeline's fixer, so the adversarial
	// pipeline-authored framing must NOT be applied to it.
	if strings.Contains(prompt, "Fix-round provenance:") {
		t.Fatalf("author-fixed code was framed as pipeline-authored:\n%s", prompt)
	}
}

func TestBuildReviewConversationSection(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := withReviewConversation(newTestContextWithDBRecords(t, newStaticReviewAgent(cleanReviewJSON), dir, baseSHA, headSHA, config.Commands{}))

	if got := buildReviewConversationSection(sctx); got != "" {
		t.Fatalf("no conversation should render nothing, got %q", got)
	}

	if err := sctx.DB.RecordReviewAnswer(db.ReviewAnswer{
		RepoID: sctx.Repo.ID, Branch: sctx.Run.Branch, QuestionID: "q1", RunID: sctx.Run.ID, AskOrdinal: 1,
		Question: "Should the legacy route keep answering?", Answer: "Keep it behind a flag", AnsweredBy: "captain",
	}); err != nil {
		t.Fatalf("record answer: %v", err)
	}
	if err := sctx.DB.RecordReviewAnswer(db.ReviewAnswer{
		RepoID: sctx.Repo.ID, Branch: sctx.Run.Branch, QuestionID: "q2", RunID: sctx.Run.ID, AskOrdinal: 1,
		Question: "Is the widened scope intended?", Answer: "No, narrow it",
	}); err != nil {
		t.Fatalf("record answer: %v", err)
	}
	convDir := reviewConversationDir(sctx)
	for _, line := range []string{
		`{"id":"q3","kind":"question","question":"does the migration cover this?","options":["yes","no"],"weight":"major"}`,
		`{"id":"q3","kind":"retract","reason":"the migration note answers it"}`,
	} {
		if err := appendAgentQuestionLine(convDir, line); err != nil {
			t.Fatalf("append question: %v", err)
		}
	}

	// A question a human approved the gate over: the review step never
	// completes on its own with one open, but approval can, and that is the
	// line a reader of the PR most needs.
	if err := appendAgentQuestionLine(convDir, `{"id":"q4","question":"is widening this scope intended?","options":["yes","no"]}`); err != nil {
		t.Fatalf("append question: %v", err)
	}

	section := buildReviewConversationSection(sctx)
	for _, want := range []string{
		"### Review conversation",
		"Should the legacy route keep answering?",
		"**A** (captain)**:** Keep it behind a flag",
		"**A** (unattributed)**:** No, narrow it",
		"**Withdrawn by the reviewer:** the migration note answers it",
		"is widening this scope intended?",
		"**Unanswered:** the review gate was resolved with this question still open.",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("section missing %q:\n%s", want, section)
		}
	}
	// Unanswered must precede withdrawn, so the length bound drops the
	// withdrawn record first.
	if strings.Index(section, "**Unanswered:**") > strings.Index(section, "**Withdrawn by the reviewer:**") {
		t.Fatalf("unanswered questions must be listed before withdrawn ones:\n%s", section)
	}
}

func TestPublishedConversationTextFlattensAndBounds(t *testing.T) {
	t.Parallel()
	// A newline would break out of the markdown list item it belongs to.
	if got := publishedConversationText("two\nlines"); got != "two lines" {
		t.Fatalf("got %q, want the lines flattened", got)
	}
	long := strings.Repeat("x", maxPublishedConversationChars+50)
	got := publishedConversationText(long)
	if len(got) <= maxPublishedConversationChars || !strings.Contains(got, "truncated") {
		t.Fatalf("overlong text was not bounded with disclosure: %q", got)
	}
}

func TestPublishedConversationTextBoundsRunesNotBytes(t *testing.T) {
	t.Parallel()
	const runeCount = maxPublishedConversationChars + 50
	text := strings.Repeat("a", maxPublishedConversationChars-1) + strings.Repeat("—", runeCount-(maxPublishedConversationChars-1))

	got := publishedConversationText(text)

	if !utf8.ValidString(got) {
		t.Fatalf("published text is not valid UTF-8: %q", got)
	}
	body, disclosure, ok := strings.Cut(got, "… (truncated,")
	if !ok {
		t.Fatalf("overlong text was not bounded with disclosure: %q", got)
	}
	if n := utf8.RuneCountInString(body); n != maxPublishedConversationChars {
		t.Fatalf("kept %d runes, want %d: %q", n, maxPublishedConversationChars, body)
	}
	// The disclosure must report the rune count too, not the byte length.
	if want := fmt.Sprintf(" %d chars total)", runeCount); disclosure != want {
		t.Fatalf("disclosure %q, want %q (bytes were %d)", disclosure, want, len(text))
	}
}

func mustParseFindings(t *testing.T, raw string) types.Findings {
	t.Helper()
	parsed, err := types.ParseFindingsJSON(raw)
	if err != nil {
		t.Fatalf("parse findings: %v", err)
	}
	return parsed
}

type deadlineRecordingAgent struct {
	onRun func(context.Context, agent.RunOpts) (*agent.Result, error)
}

func (a *deadlineRecordingAgent) Name() string { return "deadline-recorder" }
func (a *deadlineRecordingAgent) Close() error { return nil }
func (a *deadlineRecordingAgent) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	return a.onRun(ctx, opts)
}

func newStaticReviewAgent(output string) *mockAgent {
	m := &mockAgent{}
	m.runFn = func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
		return &agent.Result{Output: []byte(output)}, nil
	}
	return m
}

func lastReviewPrompt(t *testing.T, m *mockAgent) string {
	t.Helper()
	if len(m.calls) == 0 {
		t.Fatal("agent was never invoked")
	}
	return m.calls[len(m.calls)-1].Prompt
}

func TestReviewStep_AnsweringARereviewQuestionDoesNotReRunTheFixer(t *testing.T) {
	t.Parallel()
	reviewTurn := 0
	mock := &sessionMockAgent{}
	var convDir string
	mock.respond = func(opts agent.RunOpts) *agent.Result {
		switch opts.Purpose {
		case "review":
			reviewTurn++
			if reviewTurn == 1 {
				return &agent.Result{Output: []byte(
					`{"findings":[{"id":"f-1","severity":"error","file":"feature.txt","description":"bug","action":"auto-fix"}],"summary":"1 issue","risk_level":"medium","risk_rationale":"bug","risk_scope":"source-or-external"}`,
				)}
			}
			if reviewTurn == 2 {
				if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"question","question":"was the fix meant to change this behaviour?","options":["yes","no"],"weight":"major"}`); err != nil {
					t.Errorf("append question: %v", err)
				}
			}
			return &agent.Result{Output: []byte(cleanReviewJSON)}
		case "review-fix":
			return &agent.Result{Output: []byte(`{"summary":"fix the bug"}`)}
		default:
			t.Errorf("unexpected agent purpose %q", opts.Purpose)
			return &agent.Result{Output: []byte(`{}`)}
		}
	}

	exec, database, run, repo, workDir := reviewSessionHarness(t, mock, []pipeline.Step{&ReviewStep{}}, enableReviewConversation)
	convDir = exec.ReviewConversationDir(run.ID)

	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, workDir) }()

	waitForReviewStatus(t, database, run.ID, types.StepStatusFixReview)
	if err := reviewqa.AppendAnswer(convDir, reviewqa.Answer{ID: "q1", Answer: "yes", AskOrdinal: 1}); err != nil {
		t.Fatalf("append answer: %v", err)
	}
	if err := exec.Respond(types.StepReview, types.ActionAnswer, nil); err != nil {
		t.Fatalf("respond with answers: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("executor timed out")
	}

	calls := mock.snapshot()
	if got := len(fixCalls(calls)); got != 1 {
		t.Fatalf("fixer ran %d times, want exactly 1: answering a question is not a new fix round", got)
	}
	reviews := reviewCalls(calls)
	if len(reviews) != 3 {
		t.Fatalf("expected 3 review turns (initial, post-fix rereview, finalize), got %d", len(reviews))
	}
	// The rereview and its finalize replay both judge pipeline-authored code,
	// so both stay cold: a reviewer session must never span a code change.
	for i := 1; i < 3; i++ {
		if reviews[i].Session != nil {
			t.Fatalf("review turn %d ran with session %+v, want cold across a fix round", i+1, reviews[i].Session)
		}
	}
	if !strings.Contains(reviews[2].Prompt, `answer="yes"`) {
		t.Fatalf("finalize replay is missing the answer:\n%s", reviews[2].Prompt)
	}
}

func TestReviewStep_OnlyAFinalizeTurnResumesTheReviewerSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name              string
		finalizingAnswers bool
		fixing            bool
		wantResumeOf      string
	}{
		{name: "finalize turn resumes the asking session", finalizingAnswers: true, wantResumeOf: "stale-sess"},
		{name: "plain re-entry starts fresh", wantResumeOf: ""},
		{name: "fix round runs cold", fixing: true, wantResumeOf: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, baseSHA, headSHA := setupGitRepo(t)
			mock := &sessionMockAgent{}
			mock.respond = func(agent.RunOpts) *agent.Result {
				return &agent.Result{Output: []byte(cleanReviewJSON)}
			}
			sctx := withReviewConversation(newTestContextWithDBRecords(t, mock, dir, baseSHA, headSHA, config.Commands{}))
			if err := sctx.DB.UpsertRunAgentSession(sctx.Run.ID, string(pipeline.SessionRoleReviewer), mock.Name(), "stale-sess"); err != nil {
				t.Fatalf("seed reviewer session: %v", err)
			}
			sctx.Sessions = pipeline.NewRunSessions(sctx.DB, sctx.Run.ID, mock, true)
			sctx.FinalizingAnswers = tc.finalizingAnswers
			sctx.Fixing = tc.fixing
			sctx.SkipFixExecution = true

			if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
				t.Fatalf("execute: %v", err)
			}
			reviews := reviewCalls(mock.snapshot())
			if len(reviews) != 1 {
				t.Fatalf("expected one review turn, got %d", len(reviews))
			}
			switch {
			case tc.fixing:
				if reviews[0].Session != nil {
					t.Fatalf("a fix round's rereview must be session-free, got %+v", reviews[0].Session)
				}
			case tc.wantResumeOf == "":
				if reviews[0].Session == nil || reviews[0].Session.ID != "" {
					t.Fatalf("re-entry must start a fresh reviewer session, got %+v", reviews[0].Session)
				}
			default:
				if reviews[0].Session == nil || reviews[0].Session.ID != tc.wantResumeOf {
					t.Fatalf("finalize turn must resume %q, got %+v", tc.wantResumeOf, reviews[0].Session)
				}
			}
		})
	}
}

func TestReviewStep_ConversationOffIsTodaysReview(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, on bool) (*pipeline.StepContext, *mockAgent, *pipeline.StepOutcome) {
		t.Helper()
		dir, baseSHA, headSHA := setupGitRepo(t)
		ag := newStaticReviewAgent(cleanReviewJSON)
		sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
		if on {
			enableReviewConversation(sctx.Config)
		}
		sctx.Sessions = pipeline.NewRunSessions(sctx.DB, sctx.Run.ID, ag, true)
		outcome, err := (&ReviewStep{}).Execute(sctx)
		if err != nil {
			t.Fatalf("execute (conversation on=%v): %v", on, err)
		}
		// The worktree and evidence paths are per-subtest temp dirs, so they are
		// normalized before the two prompts are compared.
		for i := range ag.calls {
			p := strings.ReplaceAll(ag.calls[i].Prompt, dir, "<WORKDIR>")
			ag.calls[i].Prompt = strings.ReplaceAll(p, sctx.EvidenceDir, "<EVIDENCE>")
		}
		return sctx, ag, outcome
	}

	offSctx, offAgent, offOutcome := run(t, false)
	_, onAgent, _ := run(t, true)

	offPrompt := lastReviewPrompt(t, offAgent)
	onPrompt := lastReviewPrompt(t, onAgent)
	if offPrompt == onPrompt {
		t.Fatal("the conversation changed nothing in the review prompt when on; the protocol section is not reaching the reviewer")
	}
	// Appends only: the on prompt is the off prompt plus the protocol section.
	if !strings.HasPrefix(onPrompt, offPrompt) {
		t.Fatalf("turning the conversation on rewrote the review prompt instead of appending to it.\noff:\n%s\n\non:\n%s", offPrompt, onPrompt)
	}
	for _, marker := range []string{reviewqa.QuestionsFile, reviewqa.AnswersFile, "Asking questions while you work", "KEEP REVIEWING while a question is open"} {
		if strings.Contains(offPrompt, marker) {
			t.Fatalf("the off review prompt carries %q:\n%s", marker, offPrompt)
		}
	}

	// No channel on disk: the reviewer was never told to write one, and
	// nothing else may create it on its behalf.
	if dir := reviewConversationDir(offSctx); dir != "" {
		t.Fatalf("conversation directory resolved to %q with the setting off", dir)
	}
	if entries, err := os.ReadDir(reviewqa.Dir(offSctx.EvidenceDir)); err == nil && len(entries) > 0 {
		t.Fatalf("the off run left %d conversation file(s) behind", len(entries))
	}

	// No reviewer identity: every review turn stays session-free, which is the
	// property the fix-round independence guarantee rests on.
	if len(offAgent.calls) != 1 {
		t.Fatalf("expected 1 review call, got %d", len(offAgent.calls))
	}
	if offAgent.calls[0].Session != nil {
		t.Fatalf("the off review turn was given a session: %+v", offAgent.calls[0].Session)
	}
	sessions, err := offSctx.DB.GetRunAgentSessions(offSctx.Run.ID)
	if err != nil {
		t.Fatalf("get sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("the off run persisted %d role session(s): %+v", len(sessions), sessions)
	}

	// The agent contract is part of the off-state guarantee, not just the
	// prompt: a repository that never turned the conversation on must not be
	// offered a property whose description names a concept its reviewer is
	// never told about. The schema is a machine-consumed contract, so it is
	// decoded and interrogated rather than matched as text.
	offProps := schemaPropertyNames(t, offAgent.calls[0].JSONSchema)
	if _, ok := offProps["withdrawn_findings"]; ok {
		t.Fatalf("the off review turn was handed the conversation's retraction property; declared: %v", offProps)
	}
	onProps := schemaPropertyNames(t, onAgent.calls[0].JSONSchema)
	if _, ok := onProps["withdrawn_findings"]; ok {
		t.Fatalf("an ASKING turn was handed the retraction property; only a finalize turn may retract. declared: %v", onProps)
	}

	// No question findings, and no PR conversation group.
	if got := questionFindings(t, offOutcome.Findings); len(got) != 0 {
		t.Fatalf("the off run produced %d review-question finding(s)", len(got))
	}
	if section := buildReviewConversationSection(offSctx); section != "" {
		t.Fatalf("the off run published a review conversation section:\n%s", section)
	}
}

func TestReviewStep_FinalizeTurnDeliversAnswersEvenWithTheSettingOff(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newStaticReviewAgent(cleanReviewJSON)
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})

	// Written exactly as the ENABLED path writes it, then the setting is off:
	// the files on disk are the proof the channel was open when it was asked.
	convDir := reviewqa.Dir(sctx.EvidenceDir)
	const (
		questionSentinel = "q1-question-sentinel-v1"
		answerSentinel   = "q1-answer-sentinel-keep-v1"
	)
	if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"question","question":"`+questionSentinel+`: is the legacy route going?","options":["keep","remove"],"weight":"major"}`); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	if err := reviewqa.AppendAnswer(convDir, reviewqa.Answer{ID: "q1", Answer: answerSentinel, AnsweredBy: "captain", AskOrdinal: 1}); err != nil {
		t.Fatalf("seed answer: %v", err)
	}
	sctx.FinalizingAnswers = true

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatalf("finalize turn: %v", err)
	}

	prompt := lastReviewPrompt(t, ag)
	if !strings.Contains(prompt, "Answers to the questions you asked in this pass") {
		t.Fatalf("the finalize turn carried no answers section, so the answer reached no agent:\n%s", prompt)
	}
	if !strings.Contains(prompt, answerSentinel) {
		t.Fatalf("the answers section does not carry the answer itself:\n%s", prompt)
	}
	if !strings.Contains(prompt, questionSentinel) {
		t.Fatalf("the answers section does not carry the question the answer settles:\n%s", prompt)
	}
	// The ask side stays off: this turn may READ what was asked, never invite
	// a new question.
	for _, marker := range []string{
		"Asking questions while you work",
		"Append one JSON object per line to " + reviewqa.QuestionsFile,
		"KEEP REVIEWING while a question is open",
	} {
		if strings.Contains(prompt, marker) {
			t.Fatalf("an off repository's finalize turn was told it may ask questions (%q):\n%s", marker, prompt)
		}
	}
}

func TestReviewStep_ConversationOffIgnoresQuestionsAlreadyOnDisk(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newStaticReviewAgent(cleanReviewJSON)
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})

	// Written as the enabled path would write it, then the setting is off.
	if err := appendAgentQuestionLine(reviewqa.Dir(sctx.EvidenceDir), `{"id":"q1","kind":"question","question":"keep the legacy route?","options":["keep","remove"],"weight":"major"}`); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	if err := sctx.DB.RecordReviewAnswer(db.ReviewAnswer{
		RepoID:     sctx.Repo.ID,
		Branch:     sctx.Run.Branch,
		QuestionID: "q0",
		RunID:      sctx.Run.ID,
		AskOrdinal: 1,
		Question:   "was the old route intentional?",
		Answer:     "yes",
		AnsweredBy: "captain",
	}); err != nil {
		t.Fatalf("seed settled answer: %v", err)
	}

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := questionFindings(t, outcome.Findings); len(got) != 0 {
		t.Fatalf("a question on disk parked a review with the conversation off: %+v", got)
	}
	if outcome.NeedsApproval {
		t.Fatal("a question on disk made the off review park for approval")
	}
	prompt := lastReviewPrompt(t, ag)
	if strings.Contains(prompt, "Settled questions on this branch") {
		t.Fatalf("the off review prompt carries the settled-questions section:\n%s", prompt)
	}
	if section := buildReviewConversationSection(sctx); section != "" {
		t.Fatalf("the off run published a review conversation section:\n%s", section)
	}
}

func TestUnreadableQuestionHistoryParksEvenWithNothingOpen(t *testing.T) {
	t.Parallel()
	conv := reviewqa.Conversation{QuestionsIncomplete: true}
	if len(conv.Open()) != 0 {
		t.Fatal("fixture is meant to have nothing open")
	}

	findings := openReviewQuestionFindings(conv)

	if len(findings) != 1 {
		t.Fatalf("an unreadable question history emitted %d findings, want one that parks the gate", len(findings))
	}
	got := findings[0]
	if got.Action != types.ActionAskUser {
		t.Fatalf("finding action = %q, want ask-user so the gate parks: %+v", got.Action, got)
	}
	// Not the review-question category: that tells every automatic resolver to
	// stand aside and wait for an answer, and the daemon refuses answers while
	// the history is unreadable - which would park a gate nobody could
	// release.
	if got.Category == types.FindingCategoryReviewQuestion {
		t.Fatalf("an unreadable history parked as a review question, so it needs an answer the daemon refuses: %+v", got)
	}
	if strings.HasPrefix(got.ID, "question-") {
		t.Fatalf("finding %q reads as an answerable question row", got.ID)
	}

	// A readable conversation with nothing open still emits nothing.
	if rest := openReviewQuestionFindings(reviewqa.Conversation{}); rest != nil {
		t.Fatalf("a readable, empty conversation emitted %+v", rest)
	}
}

func TestUnreadableQuestionHistoryReplacesTheAnswerableRows(t *testing.T) {
	t.Parallel()
	conv := reviewqa.Conversation{
		QuestionsIncomplete: true,
		Entries: []reviewqa.Entry{
			{Question: reviewqa.Question{ID: "q1", Question: "keep the legacy route?", Options: []string{"keep", "remove"}}},
			{Question: reviewqa.Question{ID: "q2", Question: "split the module?", Options: []string{"yes", "no"}}},
		},
	}
	if len(conv.Open()) != 2 {
		t.Fatalf("fixture must have open questions, got %d", len(conv.Open()))
	}

	findings := openReviewQuestionFindings(conv)

	if len(findings) != 1 {
		t.Fatalf("an incomplete history with open questions emitted %d findings, want only the marker: %+v", len(findings), findings)
	}
	got := findings[0]
	if got.ID != pipeline.ReviewQuestionsUnreadableFindingID {
		t.Fatalf("finding ID = %q, want the unreadable marker", got.ID)
	}
	if got.Category == types.FindingCategoryReviewQuestion {
		t.Fatalf("the marker parked as a review question, so it needs an answer the daemon refuses: %+v", got)
	}
	if strings.Contains(got.Description, "axi answer") {
		t.Fatalf("the marker instructs an answer the daemon refuses: %s", got.Description)
	}
	// The open ids are the only handle a human has on what the retained window
	// did hold, so the marker has to name them and say how many.
	for _, want := range []string{"q1", "q2", "2 question(s)", "questions.ndjson"} {
		if !strings.Contains(got.Description, want) {
			t.Fatalf("marker missing %q: %s", want, got.Description)
		}
	}
}

func TestOpenReviewQuestionFindingsAreBounded(t *testing.T) {
	t.Parallel()
	var conv reviewqa.Conversation
	const open = maxReviewQuestionFindings + 7
	for i := 0; i < open; i++ {
		conv.Entries = append(conv.Entries, reviewqa.Entry{Question: reviewqa.Question{
			ID:       fmt.Sprintf("q%d", i),
			Kind:     reviewqa.KindQuestion,
			Question: "keep the legacy route? " + strings.Repeat("é", maxReviewQuestionDescription),
			Options:  []string{"keep", "remove"},
		}})
	}

	findings := openReviewQuestionFindings(conv)

	// One marker beyond the cap, and it is NOT an answerable row.
	if len(findings) != maxReviewQuestionFindings+1 {
		t.Fatalf("emitted %d findings for %d open questions, want %d plus one marker", len(findings), open, maxReviewQuestionFindings)
	}
	marker := findings[len(findings)-1]
	if strings.HasPrefix(marker.ID, "question-") {
		t.Fatalf("the omission marker looks like an answerable question: %+v", marker)
	}
	// It must still park and still stand aside from every auto-resolver.
	if marker.Category != types.FindingCategoryReviewQuestion || marker.Action != types.ActionAskUser {
		t.Fatalf("marker does not park as a question: %+v", marker)
	}
	if !strings.Contains(marker.Description, "7 further review question") {
		t.Fatalf("marker does not report how many were omitted: %q", marker.Description)
	}
	// It must also NAME them. Both release paths require the conversation to
	// have nothing open, and a dropped question is never re-emitted as a row,
	// so an id that appears nowhere is a question that can never be answered
	// and a gate that parks forever.
	for i := maxReviewQuestionFindings; i < open; i++ {
		id := fmt.Sprintf("q%d", i)
		if !strings.Contains(marker.Description, id) {
			t.Fatalf("marker does not name omitted question %s, so it cannot be answered: %q", id, marker.Description)
		}
	}
	if utf8.RuneCountInString(marker.Description) > maxReviewQuestionDescription+64 || !utf8.ValidString(marker.Description) {
		t.Fatalf("marker description is unbounded or not valid UTF-8: %q", marker.Description)
	}

	for _, f := range findings[:maxReviewQuestionFindings] {
		if n := utf8.RuneCountInString(f.Description); n > maxReviewQuestionDescription+64 {
			t.Fatalf("description is %d runes, over the bound: %q", n, f.Description)
		}
		// Rune-safe, not byte-sliced: the text is multi-byte throughout, so a
		// byte cut would leave invalid UTF-8 on the event stream.
		if !utf8.ValidString(f.Description) {
			t.Fatalf("description is not valid UTF-8: %q", f.Description)
		}
	}
}

func TestReviewQuestionPromptSectionsAreBounded(t *testing.T) {
	t.Parallel()
	var conv reviewqa.Conversation
	const open = maxReviewQuestionPromptEntries + 5
	for i := 0; i < open; i++ {
		conv.Entries = append(conv.Entries, reviewqa.Entry{Question: reviewqa.Question{
			ID:       fmt.Sprintf("q%d", i),
			Kind:     reviewqa.KindQuestion,
			Question: strings.Repeat("é", maxReviewQuestionPromptChars+200),
			Options:  []string{"keep", "remove"},
		}})
	}

	protocol := reviewQuestionProtocolSection("/tmp/evidence/review", conv)
	if !utf8.ValidString(protocol) {
		t.Fatal("the protocol section is not valid UTF-8")
	}
	if !strings.Contains(protocol, "5 more still unanswered") {
		t.Fatalf("the open list was not bounded:\n%s", protocol)
	}
	if got := strings.Count(protocol, "\n  - "); got > maxReviewQuestionPromptEntries+1 {
		t.Fatalf("protocol section listed %d entries, over the bound", got)
	}

	// Answer every one, so the answers section carries the same set.
	for i := range conv.Entries {
		conv.Entries[i].Answer = &reviewqa.Answer{ID: conv.Entries[i].ID, Answer: "keep", AnsweredBy: "captain"}
	}
	answers := reviewAnswersPromptSection(conv)
	if !utf8.ValidString(answers) {
		t.Fatal("the answers section is not valid UTF-8")
	}
	if !strings.Contains(answers, "5 more answers not listed") {
		t.Fatalf("the answers list was not bounded:\n%s", answers)
	}
}

func TestRetractedQuestionIsNeitherPersistedNorRenderedTwice(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := withReviewConversation(newTestContextWithDBRecords(t, newStaticReviewAgent(cleanReviewJSON), dir, baseSHA, headSHA, config.Commands{}))
	convDir := reviewConversationDir(sctx)

	for _, line := range []string{
		`{"id":"q1","kind":"question","question":"keep the legacy route?","options":["keep","remove"],"weight":"major"}`,
		`{"id":"q1","kind":"retract","reason":"the migration note answers it"}`,
	} {
		if err := appendAgentQuestionLine(convDir, line); err != nil {
			t.Fatalf("append question: %v", err)
		}
	}
	if err := reviewqa.AppendAnswer(convDir, reviewqa.Answer{ID: "q1", Answer: "keep it", AnsweredBy: "captain", AskOrdinal: 1}); err != nil {
		t.Fatalf("append answer: %v", err)
	}

	conv, err := reviewqa.Load(convDir)
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if len(conv.Withdrawn()) != 1 {
		t.Fatalf("the seed did not read back as one withdrawn question: %+v", conv.Entries)
	}

	recordAnsweredQuestions(sctx, conv)

	answers, _, err := sctx.DB.GetBranchReviewAnswers(sctx.Repo.ID, sctx.Run.Branch, 0)
	if err != nil {
		t.Fatalf("read branch answers: %v", err)
	}
	if len(answers) != 0 {
		t.Fatalf("a withdrawn question was persisted as a settled branch decision: %#v", answers)
	}

	section := buildReviewConversationSection(sctx)
	if n := strings.Count(section, "keep the legacy route?"); n != 1 {
		t.Fatalf("the withdrawn question is rendered %d times in the PR body:\n%s", n, section)
	}
	if !strings.Contains(section, "**Withdrawn by the reviewer:** the migration note answers it") {
		t.Fatalf("the one rendering is not the withdrawal:\n%s", section)
	}
	if strings.Contains(section, "keep it") {
		t.Fatalf("the late answer was published as a decision:\n%s", section)
	}
}

func TestSupersededSectionDoesNotClaimThePreviousRunParked(t *testing.T) {
	t.Parallel()
	mock := &sessionMockAgent{}
	mock.respond = func(agent.RunOpts) *agent.Result {
		return &agent.Result{Output: []byte(cleanReviewJSON)}
	}
	exec, database, run, repo, workDir := reviewSessionHarness(t, mock, []pipeline.Step{&ReviewStep{}}, enableReviewConversation)

	previous, err := database.InsertRun(repo.ID, run.Branch, "previous-head", run.BaseSHA)
	if err != nil {
		t.Fatalf("insert previous run: %v", err)
	}
	sr, err := database.InsertStepResult(previous.ID, types.StepReview)
	if err != nil {
		t.Fatalf("insert step result: %v", err)
	}
	findings := `{"findings":[{"id":"f-9","severity":"error","description":"drops the straggler","action":"ask-user"}],"summary":"1 issue","risk_level":"high","risk_rationale":"bug","risk_scope":"source-or-external"}`
	if _, err := database.InsertStepRound(sr.ID, 1, "initial", &findings, nil, 0); err != nil {
		t.Fatalf("insert round: %v", err)
	}
	if err := database.UpdateRunStatus(previous.ID, types.RunCompleted); err != nil {
		t.Fatalf("complete the previous run: %v", err)
	}

	if err := exec.Execute(context.Background(), run, repo, workDir); err != nil {
		t.Fatalf("execute: %v", err)
	}

	reviews := reviewCalls(mock.snapshot())
	if len(reviews) != 1 {
		t.Fatalf("expected one review turn, got %d", len(reviews))
	}
	prompt := reviews[0].Prompt
	// The rounds still travel: the selection is unchanged, only the claim is.
	if !strings.Contains(prompt, "drops the straggler") {
		t.Fatalf("a completed previous run's rounds were dropped:\n%s", prompt)
	}
	for _, claim := range []string{
		"superseded by a later push",
		"That run's review parked",
		"fixed findings in their own worktree and pushed",
		"judge whether each claimed fix actually holds",
	} {
		if strings.Contains(prompt, claim) {
			t.Fatalf("the prompt asserts %q about a run that completed:\n%s", claim, prompt)
		}
	}
}

func TestReviewPromptNamesTheConversationDirectoryAsABoundaryException(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newStaticReviewAgent(cleanReviewJSON)
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	enableReviewConversation(sctx.Config)

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatalf("execute: %v", err)
	}

	prompt := lastReviewPrompt(t, ag)
	convDir := reviewConversationDir(sctx)
	if convDir == "" {
		t.Fatal("no conversation directory resolved with the setting on")
	}
	if !strings.Contains(prompt, "EXPLICIT exception to the workspace boundary") {
		t.Fatalf("the protocol section does not authorize the write the boundary forbids:\n%s", prompt)
	}
	var exception string
	for _, line := range strings.Split(prompt, "\n") {
		if strings.Contains(line, "EXPLICIT exception to the workspace boundary") {
			exception = line
			break
		}
	}
	if !strings.Contains(exception, convDir) {
		t.Fatalf("the exception does not name %q, so it reads as a general licence: %q", convDir, exception)
	}
}

func TestReviewStep_UnreadableConversationFailsTheReview(t *testing.T) {
	t.Parallel()
	t.Run("unreadable", func(t *testing.T) {
		dir, baseSHA, headSHA := setupGitRepo(t)
		sctx := withReviewConversation(newTestContextWithDBRecords(t, newStaticReviewAgent(cleanReviewJSON), dir, baseSHA, headSHA, config.Commands{}))
		convDir := reviewConversationDir(sctx)
		if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"question","question":"keep the legacy route?","options":["keep","remove"]}`); err != nil {
			t.Fatalf("seed question: %v", err)
		}
		path := filepath.Join(convDir, reviewqa.QuestionsFile)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Skip("cannot restrict file permissions")
		}
		t.Cleanup(func() { os.Chmod(path, 0o644) })
		if f, err := os.Open(path); err == nil {
			f.Close()
			t.Skip("this environment reads a mode 0000 file anyway")
		}

		if _, err := (&ReviewStep{}).Execute(sctx); err == nil {
			t.Fatal("an unreadable conversation completed the review with no questions")
		} else if !strings.Contains(err.Error(), "read the review conversation") {
			t.Fatalf("execute error = %v, want it to name the unreadable conversation", err)
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		dir, baseSHA, headSHA := setupGitRepo(t)
		sctx := withReviewConversation(newTestContextWithDBRecords(t, newStaticReviewAgent(cleanReviewJSON), dir, baseSHA, headSHA, config.Commands{}))
		if _, err := os.Stat(reviewConversationDir(sctx)); !os.IsNotExist(err) {
			t.Fatalf("conversation directory already exists: %v", err)
		}

		outcome, err := (&ReviewStep{}).Execute(sctx)
		if err != nil {
			t.Fatalf("a run whose reviewer asked nothing failed: %v", err)
		}
		if got := questionFindings(t, outcome.Findings); len(got) != 0 {
			t.Fatalf("question findings without a conversation: %+v", got)
		}
	})
}

func TestReviewStep_FinalizeTurnCanActuallyRetractWhatItCarried(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	var handed agent.RunOpts
	ag := &mockAgent{}
	ag.runFn = func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		handed = opts
		return &agent.Result{Output: []byte(`{"findings":[],"risk_level":"low","risk_rationale":"the answer disproved it","risk_scope":"source-or-external","reviewed_paths":["feature.txt"],"withdrawn_findings":[{"id":"review-1","reason":"the answer settled that /v1 is staying"}]}`)}, nil
	}
	sctx := withReviewConversation(newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{}))
	convDir := reviewConversationDir(sctx)
	if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"question","question":"is /v1 going?","options":["keep","remove"],"weight":"major"}`); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	if err := reviewqa.AppendAnswer(convDir, reviewqa.Answer{ID: "q1", Answer: "keep", AskOrdinal: 1}); err != nil {
		t.Fatalf("append answer: %v", err)
	}
	sctx.FinalizingAnswers = true
	sctx.CarriedFindings = `{"findings":[{"id":"review-1","severity":"warning","file":"feature.txt","line":1,"description":"PENDING ANSWER (q1): the legacy route is only wrong if /v1 is going","action":"ask-user"}],"summary":"1 carried finding"}`

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatalf("finalize turn: %v", err)
	}

	var schema struct {
		Properties map[string]struct {
			Type  string `json:"type"`
			Items struct {
				Type       string              `json:"type"`
				Properties map[string]struct{} `json:"properties"`
				Required   []string            `json:"required"`
			} `json:"items"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(handed.JSONSchema, &schema); err != nil {
		t.Fatalf("the review step handed the agent an unparseable schema: %v", err)
	}
	withdrawn, ok := schema.Properties["withdrawn_findings"]
	if !ok {
		t.Fatalf("the review turn's schema declares no withdrawn_findings, so the finalize prompt asks for a field the agent cannot emit; declared: %v", schema.Properties)
	}
	if withdrawn.Type != "array" || withdrawn.Items.Type != "object" {
		t.Fatalf("withdrawn_findings is %q of %q, want an array of objects", withdrawn.Type, withdrawn.Items.Type)
	}
	for _, field := range []string{"id", "reason"} {
		if _, ok := withdrawn.Items.Properties[field]; !ok {
			t.Errorf("a withdrawal cannot carry %q, so the executor cannot match it to a carried finding", field)
		}
	}
	for _, name := range schema.Required {
		if name == "withdrawn_findings" {
			t.Fatal("withdrawn_findings is required, so an ordinary review round with nothing to retract can no longer validate")
		}
	}

	if got := outcome.WithdrawnFindings; len(got) != 1 || got[0].ID != "review-1" {
		t.Fatalf("the retraction the finalize turn emitted did not reach the executor: %v", got)
	} else if got[0].Reason == "" {
		t.Fatal("the retraction reached the executor without the reason the schema requires and the prompt demands")
	}
}

func schemaPropertyNames(t *testing.T, raw json.RawMessage) map[string]struct{} {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("the step handed the agent no JSON schema")
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("the step handed the agent an unparseable schema: %v", err)
	}
	names := make(map[string]struct{}, len(schema.Properties))
	for name := range schema.Properties {
		names[name] = struct{}{}
	}
	return names
}

func TestReviewStep_OnlyAFinalizeTurnIsOfferedTheRetractionProperty(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newStaticReviewAgent(cleanReviewJSON)
	sctx := withReviewConversation(newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{}))
	convDir := reviewConversationDir(sctx)
	if err := appendAgentQuestionLine(convDir, `{"id":"q1","kind":"question","question":"is /v1 going?","options":["keep","remove"],"weight":"major"}`); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	if err := reviewqa.AppendAnswer(convDir, reviewqa.Answer{ID: "q1", Answer: "keep", AskOrdinal: 1}); err != nil {
		t.Fatalf("append answer: %v", err)
	}
	sctx.FinalizingAnswers = true
	sctx.CarriedFindings = `{"findings":[{"id":"review-1","severity":"warning","file":"feature.txt","line":1,"description":"PENDING ANSWER (q1): only wrong if /v1 is going","action":"ask-user"}],"summary":"1 carried finding"}`

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatalf("finalize turn: %v", err)
	}

	props := schemaPropertyNames(t, ag.calls[len(ag.calls)-1].JSONSchema)
	if _, ok := props["withdrawn_findings"]; !ok {
		t.Fatalf("the finalize turn is told to retract by name but its schema declares no withdrawn_findings, so the retraction path is unreachable; declared: %v", props)
	}
}
