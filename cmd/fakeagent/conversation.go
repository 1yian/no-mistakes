package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	questionChannelMarker  = "question channel: "
	questionChannelTrailer = "/questions.ndjson"
)

func reviewConversationDirFromPrompt(prompt string) string {
	i := strings.Index(prompt, questionChannelMarker)
	if i < 0 {
		return ""
	}
	rest := prompt[i+len(questionChannelMarker):]
	j := strings.Index(rest, questionChannelTrailer)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func askQuestions(prompt string, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	dir := reviewConversationDirFromPrompt(prompt)
	if dir == "" {
		return fmt.Errorf("scenario asks a review question but the prompt names no question channel")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create review conversation dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "questions.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open questions file: %w", err)
	}
	defer f.Close()
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, err := f.WriteString(strings.TrimRight(line, "\n") + "\n"); err != nil {
			return fmt.Errorf("append question: %w", err)
		}
	}
	return nil
}

const evidenceDirMarker = "evidence files into this evidence directory, never into the worktree"

func evidenceDirFromPrompt(prompt string) string {
	i := strings.Index(prompt, evidenceDirMarker)
	if i < 0 {
		return ""
	}
	line := prompt[i:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	k := strings.LastIndex(line, ": ")
	if k < 0 {
		return ""
	}
	return strings.TrimSpace(line[k+2:])
}

func writeEvidence(prompt string, files []EvidenceFile) error {
	if len(files) == 0 {
		return nil
	}
	dir := evidenceDirFromPrompt(prompt)
	if dir == "" {
		return fmt.Errorf("scenario writes test evidence but the prompt names no evidence directory")
	}
	for _, f := range files {
		if strings.TrimSpace(f.Path) == "" {
			continue
		}
		full := filepath.Join(dir, filepath.Clean(f.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("create evidence dir: %w", err)
		}
		if err := os.WriteFile(full, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("write evidence %s: %w", f.Path, err)
		}
	}
	return nil
}
