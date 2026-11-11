package intent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const CopilotReaderName = "copilot"

type copilotReader struct{}

func NewCopilotReader() Reader { return &copilotReader{} }

func (r *copilotReader) Name() string { return CopilotReaderName }

func (r *copilotReader) Discover(ctx context.Context, opts DiscoverOpts) ([]*Session, error) {
	home, err := resolveHome(opts.HomeDir)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".copilot", "session-state")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read copilot sessions: %w", err)
	}

	matcher := newRepoMatcher(ctx, opts.OriginCWD)
	var out []*Session

	for _, dir := range entries {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if !dir.IsDir() {
			continue
		}
		path := filepath.Join(root, dir.Name(), "events.jsonl")
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		modTime := info.ModTime()
		if !opts.WindowStart.IsZero() && modTime.Before(opts.WindowStart) {
			continue
		}
		if !opts.WindowEnd.IsZero() && modTime.After(opts.WindowEnd.Add(time.Hour)) {
			continue
		}
		meta, err := copilotPeekMetadata(path)
		if err != nil || meta == nil {
			continue
		}
		if !matcher.matches(ctx, meta.cwd) {
			continue
		}
		session := &Session{
			AgentName:     CopilotReaderName,
			SessionID:     dir.Name(),
			CWD:           meta.cwd,
			StartedAt:     meta.startedAt,
			LastActivity:  modTime,
			LastMsgKey:    path + "|" + modTime.UTC().Format(time.RFC3339Nano),
			startedAtPath: path,
		}
		out = append(out, session)
	}
	return out, nil
}

func (r *copilotReader) Load(_ context.Context, s *Session) error {
	if s.startedAtPath == "" {
		return fmt.Errorf("copilot: session has no path")
	}
	f, err := os.Open(s.startedAtPath)
	if err != nil {
		return fmt.Errorf("copilot open: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	var lastID string
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		rec, ok := parseCopilotRecord(line)
		if !ok {
			continue
		}
		if rec.id != "" {
			lastID = rec.id
		}
		if rec.message == nil {
			continue
		}
		s.Messages = append(s.Messages, *rec.message)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("copilot scan: %w", err)
	}
	if lastID != "" {
		s.LastMsgKey = lastID
	}
	return nil
}

type copilotMetadata struct {
	cwd       string
	startedAt time.Time
}

func copilotPeekMetadata(path string) (*copilotMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)

	for scanner.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				StartTime string `json:"startTime"`
				Context   struct {
					CWD string `json:"cwd"`
				} `json:"context"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type != "session.start" || ev.Data.Context.CWD == "" {
			continue
		}
		meta := &copilotMetadata{cwd: ev.Data.Context.CWD}
		if ev.Data.StartTime != "" {
			if t, err := time.Parse(time.RFC3339Nano, ev.Data.StartTime); err == nil {
				meta.startedAt = t
			}
		}
		return meta, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

type copilotRecord struct {
	id      string
	message *Message
}

func parseCopilotRecord(line []byte) (copilotRecord, bool) {
	var raw struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Timestamp string          `json:"timestamp"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return copilotRecord{}, false
	}

	rec := copilotRecord{id: raw.ID}
	ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)

	switch raw.Type {
	case "user.message":
		text := strings.TrimSpace(parseCopilotUserContent(raw.Data))
		if text == "" || isCopilotSyntheticUserText(text) {
			return rec, true
		}
		rec.message = &Message{
			Role:      RoleUser,
			Text:      text,
			FilePaths: scanFilePathsInText(text),
			Timestamp: ts,
		}
	case "assistant.message":
		text, paths := parseCopilotAssistantContent(raw.Data)
		text = strings.TrimSpace(text)
		if text == "" && len(paths) == 0 {
			return rec, true
		}
		rec.message = &Message{
			Role:      RoleAssistant,
			Text:      text,
			FilePaths: paths,
			Timestamp: ts,
		}
	default:
		return rec, true
	}
	return rec, true
}

func parseCopilotUserContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var data struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return ""
	}
	return data.Content
}

func parseCopilotAssistantContent(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 {
		return "", nil
	}
	var data struct {
		Content      string `json:"content"`
		ToolRequests []struct {
			Arguments map[string]any `json:"arguments"`
		} `json:"toolRequests"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", nil
	}
	var paths []string
	for _, req := range data.ToolRequests {
		if req.Arguments != nil {
			paths = append(paths, extractToolPaths(req.Arguments)...)
		}
	}
	return data.Content, paths
}

func isCopilotSyntheticUserText(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "<skill-context")
}
