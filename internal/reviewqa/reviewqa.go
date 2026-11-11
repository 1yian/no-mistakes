package reviewqa

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	KindQuestion = "question"
	KindRetract  = "retract"
)

const WeightMinor = "minor"

const (
	QuestionsFile = "questions.ndjson"
	AnswersFile   = "answers.ndjson"
)

const (
	maxLines     = 2000
	maxFileBytes = 4 << 20 // 4 MiB
	maxLineBytes = 64 << 10
)

type Question struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Weight   string   `json:"weight,omitempty"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Area     string   `json:"area,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type Answer struct {
	ID         string `json:"id"`
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by,omitempty"`
	AnsweredAt string `json:"answered_at,omitempty"`
	AskOrdinal int    `json:"ask_ordinal,omitempty"`
}

type Entry struct {
	Question
	Answer    *Answer
	Retracted bool
}

func (e Entry) Open() bool { return !e.Retracted && e.Answer == nil }

func (e Entry) Answered() bool { return !e.Retracted && e.Answer != nil }

type Ask struct {
	Ordinal  int
	Question Question
	Answer   *Answer
}

type Conversation struct {
	Entries             []Entry
	Notes               []string
	Asks                []Ask
	QuestionsIncomplete bool
}

func (c Conversation) SettledAsks() []Ask {
	retracted := make(map[string]bool, len(c.Entries))
	for _, e := range c.Entries {
		if e.Retracted {
			retracted[e.ID] = true
		}
	}
	lastAsk := make(map[string]int, len(c.Asks))
	for _, a := range c.Asks {
		if a.Ordinal > lastAsk[a.Question.ID] {
			lastAsk[a.Question.ID] = a.Ordinal
		}
	}
	out := make([]Ask, 0, len(c.Asks))
	for _, a := range c.Asks {
		if a.Answer == nil {
			continue
		}
		if retracted[a.Question.ID] && a.Ordinal == lastAsk[a.Question.ID] {
			continue
		}
		out = append(out, a)
	}
	return out
}

func (c Conversation) Open() []Entry { return c.filter(Entry.Open) }

func (c Conversation) Answered() []Entry { return c.filter(Entry.Answered) }

func (c Conversation) Withdrawn() []Entry {
	return c.filter(func(e Entry) bool { return e.Retracted })
}

func (c Conversation) filter(keep func(Entry) bool) []Entry {
	var out []Entry
	for _, e := range c.Entries {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

const DirName = "review"

func Dir(evidenceDir string) string {
	if strings.TrimSpace(evidenceDir) == "" {
		return ""
	}
	return filepath.Join(evidenceDir, DirName)
}

func Load(dir string) (Conversation, error) {
	var conv Conversation
	if strings.TrimSpace(dir) == "" {
		return conv, nil
	}

	questionLines, droppedQuestionLine, questionsIncomplete, err := readLines(filepath.Join(dir, QuestionsFile))
	// Both read bounds have to fail the same way, and they did not. The byte
	// bound stops the scan, so the tail is unseen and nothing settles. The line
	// cap instead KEEPS the newest lines and drops a prefix - and Entries are
	// built from the retained lines only, so a question whose only line is in
	// that prefix has no Entry at all: it is missing from Open(), no
	// "question-<id>" finding is emitted for it, and it is absent from the
	// omission marker's id list too, because that list is built from Open().
	// Both release paths would then see nothing open and release the gate with
	// a major question unanswered, with a later answer for it recorded as an
	// orphan. Reaching it needs more than maxLines accepted question lines in
	// one run, which is exactly the runaway-appending reviewer the bound exists
	// for.
	//
	// So a dropped question line is an incomplete question history too, and
	// from here on the two are one condition. A dropped ANSWER line is not the
	// same and deliberately does not set it: an answer that scrolled out of the
	// window cannot settle anything either way, and the ask it belonged to
	// simply stays open, which is the safe direction.
	questionsIncomplete = questionsIncomplete || droppedQuestionLine
	if err != nil {
		return conv, err
	}
	answerLines, droppedAnswerLine, answersIncomplete, err := readLines(filepath.Join(dir, AnswersFile))
	if err != nil {
		return conv, err
	}

	order := make([]string, 0, len(questionLines))
	byID := make(map[string]*Entry, len(questionLines))
	// An id can be ASKED more than once: ids are chosen by the agent (the
	// protocol's worked example is literally "q1"), the conversation directory
	// is per RUN, and a cold rereview in a fix round is shown only the OPEN
	// questions - so it reuses "q1" for a genuinely different question. An
	// answer written before that re-ask answered the OLD question, and letting
	// it settle the new one meant the new question arrived pre-answered:
	// Open() was empty, no finding was emitted, the gate never parked, and a
	// major question reached nobody.
	//
	// So an ask is settled only by an answer stamped with ITS AskOrdinal, and
	// nothing else settles it. That is not a comparison of timestamps: the two
	// files are appended independently, asked_at and answered_at are optional
	// and written by whoever appends the line, and second-granularity RFC3339
	// from two writers cannot order a fast exchange. It fails toward OPEN - a
	// re-ask asks again rather than inheriting an answer written before it,
	// including a correction to the ask it supersedes - which is the safe
	// direction here and also the behaviour under a byte-truncated answers
	// file.
	asks := make(map[string]int, len(questionLines))
	// Every retained question line per id, in file order, so an earlier ask
	// survives a later one for the durable store's benefit.
	lines := make(map[string][]Question, len(questionLines))
	askOrder := make([]string, 0, len(questionLines))
	answersByID := make(map[string][]Answer, len(answerLines))
	for _, line := range questionLines {
		q, note, ok := classifyQuestionLine(line)
		switch {
		case note != "":
			conv.Notes = append(conv.Notes, note)
			continue
		case !ok:
			// A retraction: it carries no question text, so it is never an ask.
			if entry, exists := byID[q.ID]; exists {
				entry.Retracted = true
				entry.Reason = q.Reason
			} else {
				conv.Notes = append(conv.Notes, fmt.Sprintf("retraction for unknown question %q ignored", q.ID))
			}
			continue
		}
		asks[q.ID]++
		lines[q.ID] = append(lines[q.ID], q)
		askOrder = append(askOrder, q.ID)
		if entry, ok := byID[q.ID]; ok {
			// A later question line for the same id supersedes the earlier
			// one for THIS entry's state, and revives a retracted question,
			// because re-asking is how the reviewer says the retraction was
			// wrong. For settling it is a new ASK (counted above): it needs an
			// answer stamped with its own ordinal and never inherits one
			// written before it.
			entry.Question = q
			entry.Retracted = false
			continue
		}
		entry := &Entry{Question: q}
		byID[q.ID] = entry
		order = append(order, q.ID)
	}

	for _, line := range answerLines {
		var a Answer
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			conv.Notes = append(conv.Notes, "skipped a malformed answers.ndjson line")
			continue
		}
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" || strings.TrimSpace(a.Answer) == "" {
			conv.Notes = append(conv.Notes, "skipped an answers.ndjson line with no id or no answer")
			continue
		}
		if _, ok := byID[a.ID]; !ok {
			// The writer may be racing a question it has not read yet, or
			// answering something the reviewer withdrew. Recorded, ignored.
			conv.Notes = append(conv.Notes, fmt.Sprintf("answer for unknown question %q ignored", a.ID))
			continue
		}
		answersByID[a.ID] = append(answersByID[a.ID], a)
	}

	// Pair every ask with the answer that settled it, and let the LAST ask of
	// an id be the entry's state: the gate needs the latest, the durable store
	// needs them all, and one rule for both keeps them from disagreeing.
	//
	// A questions.ndjson the scan could not read to the end settles NOTHING:
	// the ask a stamped answer names may be past the seen region, and the last
	// line we saw for an id is not necessarily its last ask, so attaching
	// answers there could report a live question as answered. Fail toward OPEN.
	seen := make(map[string]int, len(lines))
	conv.Asks = make([]Ask, 0, len(askOrder))
	for _, id := range askOrder {
		seen[id]++
		ordinal := seen[id]
		ask := Ask{Ordinal: ordinal, Question: lines[id][seen[id]-1]}
		if !questionsIncomplete {
			ask.Answer = settlingAnswer(answersByID[id], ordinal)
		}
		conv.Asks = append(conv.Asks, ask)
		if ordinal == asks[id] {
			byID[id].Answer = ask.Answer
		}
	}

	conv.Entries = make([]Entry, 0, len(order))
	for _, id := range order {
		conv.Entries = append(conv.Entries, *byID[id])
	}
	if questionsIncomplete || answersIncomplete || droppedAnswerLine {
		conv.Notes = append(conv.Notes, "review conversation file exceeded its size bound; older lines were not read")
	}
	if questionsIncomplete {
		conv.QuestionsIncomplete = true
		conv.Notes = append(conv.Notes, "questions.ndjson could not be read in full; no answer settles a question until it can be")
	}
	return conv, nil
}

func classifyQuestionLine(line string) (Question, string, bool) {
	var q Question
	if err := json.Unmarshal([]byte(line), &q); err != nil {
		return q, "skipped a malformed questions.ndjson line", false
	}
	q.ID = strings.TrimSpace(q.ID)
	if q.ID == "" {
		return q, "skipped a questions.ndjson line with no id", false
	}
	switch strings.TrimSpace(q.Kind) {
	case KindRetract:
		q.Kind = KindRetract
		return q, "", false
	case KindQuestion, "":
		q.Kind = KindQuestion
	default:
		return q, fmt.Sprintf("skipped question %q with unknown kind %q", q.ID, q.Kind), false
	}
	if strings.TrimSpace(q.Question) == "" {
		return q, fmt.Sprintf("skipped question %q with no question text", q.ID), false
	}
	// Routing by weight is the reviewer's own job, so a minor question is
	// never escalated on its behalf: emitting one is the protocol violation,
	// and reporting it keeps that visible instead of parking the run on a
	// question the reviewer was told to decide itself.
	if strings.EqualFold(strings.TrimSpace(q.Weight), WeightMinor) {
		return q, fmt.Sprintf("dropped minor-weight question %q; the reviewer decides minor questions itself", q.ID), false
	}
	return q, "", true
}

func settlingAnswer(answers []Answer, ordinal int) *Answer {
	var settling *Answer
	for _, a := range answers {
		if a.AskOrdinal == ordinal {
			settling = &a
		}
	}
	return settling
}

func AppendAnswer(dir string, a Answer) error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("review answer requires a question id")
	}
	if strings.TrimSpace(a.Answer) == "" {
		return errors.New("review answer requires answer text")
	}
	if strings.TrimSpace(a.AnsweredAt) == "" {
		a.AnsweredAt = time.Now().UTC().Format(time.RFC3339)
	}
	return appendLine(dir, AnswersFile, a)
}

func appendLine(dir, name string, payload any) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("review conversation directory is not set")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode review conversation line: %w", err)
	}
	if len(encoded)+1 > maxLineBytes {
		return fmt.Errorf("review conversation line is %d bytes, over the %d byte limit", len(encoded), maxLineBytes)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create review conversation dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	defer f.Close()
	if _, err := f.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("append %s: %w", name, err)
	}
	return nil
}

func readLines(path string) (kept []string, dropped, incomplete bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, false, nil
		}
		return nil, false, false, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	var lines []string
	read := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for scanner.Scan() {
		read += len(scanner.Bytes()) + 1
		if read > maxFileBytes {
			incomplete = true
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		// A line over the scanner budget, or a torn read while the reviewer is
		// still appending. Keep what parsed rather than losing the file.
		incomplete = true
	}
	if len(lines) > maxLines {
		dropped = true
		lines = lines[len(lines)-maxLines:]
	}
	return lines, dropped, incomplete, nil
}
