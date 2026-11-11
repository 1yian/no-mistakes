package db

import (
	"encoding/json"
	"fmt"
	"time"
)

const MaxBranchReviewAnswers = 40

type ReviewAnswer struct {
	RepoID     string
	Branch     string
	QuestionID string
	RunID      string
	AskOrdinal int
	Question   string
	Options    []string
	File       string
	Line       int
	Answer     string
	AnsweredBy string
	AnsweredAt string
	UpdatedAt  int64
}

func (d *DB) RecordReviewAnswer(a ReviewAnswer) error {
	if a.RepoID == "" || a.Branch == "" || a.QuestionID == "" {
		return fmt.Errorf("record review answer: repo, branch and question id are required")
	}
	// The ask ordinal is part of the key, so a caller with no ordinal to give
	// is refused rather than coerced to 1: coercing it would key the row as the
	// FIRST ask and let the ON CONFLICT clause overwrite that ask's recorded
	// human decision, which is the failure the ordinal joined the key to
	// prevent. Every accepted ask is 1-based (reviewqa.Conversation.Asks).
	if a.AskOrdinal < 1 {
		return fmt.Errorf("record review answer: ask ordinal must be 1 or greater, got %d", a.AskOrdinal)
	}
	var optionsJSON *string
	if len(a.Options) > 0 {
		encoded, err := json.Marshal(a.Options)
		if err != nil {
			return fmt.Errorf("record review answer: encode options: %w", err)
		}
		s := string(encoded)
		optionsJSON = &s
	}
	now := time.Now().Unix()
	_, err := d.sql.Exec(
		`INSERT INTO review_questions
		    (repo_id, branch, question_id, run_id, ask_ordinal, question, options_json, file, line,
		     answer, answered_by, answered_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (repo_id, branch, question_id, run_id, ask_ordinal) DO UPDATE SET
		    question = excluded.question,
		    options_json = excluded.options_json,
		    file = excluded.file,
		    line = excluded.line,
		    answer = excluded.answer,
		    answered_by = excluded.answered_by,
		    answered_at = excluded.answered_at,
		    updated_at = excluded.updated_at`,
		a.RepoID, a.Branch, a.QuestionID, a.RunID, a.AskOrdinal, a.Question, optionsJSON,
		nullableText(a.File), nullableInt(a.Line),
		a.Answer, nullableText(a.AnsweredBy), nullableText(a.AnsweredAt), now, now,
	)
	if err != nil {
		return fmt.Errorf("record review answer: %w", err)
	}
	return nil
}

func (d *DB) GetBranchReviewAnswers(repoID, branch string, limit int) ([]ReviewAnswer, bool, error) {
	if limit <= 0 {
		limit = MaxBranchReviewAnswers
	}
	rows, err := d.sql.Query(
		`SELECT repo_id, branch, question_id, run_id, ask_ordinal, question, options_json, file, line,
		        answer, answered_by, answered_at, updated_at
		   FROM review_questions
		  WHERE repo_id = ? AND branch = ?
		  -- rowid breaks the tie, because updated_at cannot: every ask settled
		  -- in one run is re-upserted by each later review round and so shares
		  -- one second, and a question_id tie-break orders q1, q10, q11, q2 -
		  -- id order, not ask order. The table is not WITHOUT ROWID, so its
		  -- implicit rowid is a monotonic insertion key that an upsert leaves
		  -- alone, which is the recency both callers' comments claim to render
		  -- and what the row limit must keep.
		  ORDER BY updated_at DESC, rowid DESC
		  LIMIT ?`,
		repoID, branch, limit+1,
	)
	if err != nil {
		return nil, false, fmt.Errorf("get branch review answers: %w", err)
	}
	defer rows.Close()

	var answers []ReviewAnswer
	for rows.Next() {
		var a ReviewAnswer
		var optionsJSON, file, answeredBy, answeredAt *string
		var line *int64
		if err := rows.Scan(
			&a.RepoID, &a.Branch, &a.QuestionID, &a.RunID, &a.AskOrdinal, &a.Question, &optionsJSON,
			&file, &line, &a.Answer, &answeredBy, &answeredAt, &a.UpdatedAt,
		); err != nil {
			return nil, false, fmt.Errorf("scan branch review answer: %w", err)
		}
		if optionsJSON != nil {
			_ = json.Unmarshal([]byte(*optionsJSON), &a.Options)
		}
		if file != nil {
			a.File = *file
		}
		if line != nil {
			a.Line = int(*line)
		}
		if answeredBy != nil {
			a.AnsweredBy = *answeredBy
		}
		if answeredAt != nil {
			a.AnsweredAt = *answeredAt
		}
		answers = append(answers, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(answers) > limit
	if truncated {
		answers = answers[:limit]
	}
	return answers, truncated, nil
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}
