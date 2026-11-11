package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type codexMetricsAccumulator struct {
	modelRoundtrips  int
	toolCalls        int
	categories       ToolCategoryCounts
	subprocessWaitMS int64
	starts           map[string]time.Time
}

func newCodexMetricsAccumulator() *codexMetricsAccumulator {
	return &codexMetricsAccumulator{starts: map[string]time.Time{}}
}

func (m *codexMetricsAccumulator) onItem(eventType string, item *codexItem, at time.Time) {
	if m == nil || item == nil {
		return
	}
	isTool, defaultCat, classify := codexToolItemKind(item.Type)
	switch eventType {
	case "item.started":
		if isTool {
			m.starts[item.ID] = at
		}
	case "item.completed":
		if !isTool && item.Type != "agent_message" {
			return
		}
		m.modelRoundtrips++
		if !isTool {
			return
		}
		m.toolCalls++
		if classify {
			cats := ClassifyToolCommand(item.Command)
			if len(cats) == 0 {
				m.categories.Add(ToolOther)
			}
			for _, cat := range cats {
				m.categories.Add(cat)
			}
		} else {
			m.categories.Add(defaultCat)
		}
		if start, ok := m.starts[item.ID]; ok {
			if d := at.Sub(start).Milliseconds(); d > 0 {
				m.subprocessWaitMS += d
			}
			delete(m.starts, item.ID)
		}
	}
}

func (m *codexMetricsAccumulator) metrics() InvocationMetrics {
	return InvocationMetrics{
		ModelRoundtrips:  m.modelRoundtrips,
		ToolCalls:        m.toolCalls,
		ToolCategories:   m.categories,
		SubprocessWaitMS: m.subprocessWaitMS,
	}
}

func codexToolItemKind(itemType string) (isTool bool, defaultCategory ToolCategory, classifyCommand bool) {
	switch itemType {
	case "command_execution", "local_shell_call", "exec_command":
		return true, ToolOther, true
	case "file_change", "patch", "apply_patch":
		return true, ToolEdit, false
	case "mcp_tool_call", "web_search", "web_fetch", "custom_tool_call":
		return true, ToolOther, false
	default:
		return false, ToolOther, false
	}
}

func resolveCodexModel(threadID string, now time.Time) (model, provider string) {
	if threadID == "" {
		return "", ""
	}
	path := findCodexRollout(codexSessionsDir(), threadID, now)
	if path == "" {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	return parseCodexRolloutModel(f)
}

func codexSessionsDir() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(userHome, ".codex")
	}
	return filepath.Join(home, "sessions")
}

func findCodexRollout(sessionsDir, threadID string, now time.Time) string {
	if sessionsDir == "" {
		return ""
	}
	for offset := 0; offset < 3; offset++ {
		day := now.AddDate(0, 0, -offset)
		dir := filepath.Join(sessionsDir, day.Format("2006"), day.Format("01"), day.Format("02"))
		matches, err := filepath.Glob(filepath.Join(dir, "rollout-*"+threadID+"*.jsonl"))
		if err == nil && len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

func parseCodexRolloutModel(r io.Reader) (model, provider string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for lines := 0; lines < 200 && scanner.Scan(); lines++ {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				Model         string `json:"model"`
				ModelProvider string `json:"model_provider"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "turn_context":
			if ev.Payload.Model != "" {
				model = ev.Payload.Model
			}
		case "session_meta":
			if ev.Payload.ModelProvider != "" {
				provider = ev.Payload.ModelProvider
			}
		}
		if model != "" && provider != "" {
			break
		}
	}
	return sanitizeModelToken(model), sanitizeModelToken(provider)
}

func sanitizeModelToken(s string) string {
	// A model identity is a single token (e.g. "gpt-5.6-sol"); take the first
	// whitespace-delimited field so a malformed multi-word rollout value cannot
	// smuggle text into telemetry.
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 64 {
		s = s[:64]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ':', r == '/':
			b.WriteRune(r)
		}
	}
	return b.String()
}
