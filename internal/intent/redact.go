package intent

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|secret[_-]?(?:key|token)?|password|passwd|bearer|authorization)\s*[:=]\s*['"]?([A-Za-z0-9_\-./+=]{12,})`),
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`gho_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
}

func RedactSecrets(text string) string {
	for _, pat := range secretPatterns {
		text = pat.ReplaceAllString(text, "[REDACTED]")
	}
	return text
}

func redactSecrets(text string) string { return RedactSecrets(text) }

func clampMessages(msgs []Message, maxBytes int) []Message {
	if maxBytes <= 0 || len(msgs) == 0 {
		return msgs
	}
	total := 0
	for _, m := range msgs {
		total += len(m.Text)
	}
	if total <= maxBytes {
		return msgs
	}

	const omittedMarker = "[... middle messages omitted to fit the context window; the conversation continues below with later messages from the same session ...]"
	// Reserve space for the marker so we don't overshoot the budget after
	// inserting it. The marker is short, so we ignore the case where the
	// budget is too small to even hold the marker.
	budget := maxBytes - len(omittedMarker)
	if budget <= 0 {
		budget = maxBytes
	}

	var frontKeep []Message
	var backKeep []Message
	used := 0
	front, back := 0, len(msgs)-1
	takeFront := true

	for front <= back {
		var size int
		if takeFront {
			size = len(msgs[front].Text)
		} else {
			size = len(msgs[back].Text)
		}
		if used+size > budget {
			break
		}
		if takeFront {
			frontKeep = append(frontKeep, msgs[front])
			front++
		} else {
			backKeep = append([]Message{msgs[back]}, backKeep...)
			back--
		}
		used += size
		takeFront = !takeFront
	}

	// Pathological case: every message individually exceeds the budget.
	// Fall back to the last message, byte-truncated, since the most recent
	// intent is usually the most relevant single signal.
	if len(frontKeep) == 0 && len(backKeep) == 0 {
		last := msgs[len(msgs)-1]
		if len(last.Text) > maxBytes {
			last.Text = last.Text[len(last.Text)-maxBytes:]
		}
		return []Message{last}
	}

	// front > back means we kept everything (no gap created).
	if front > back {
		return append(frontKeep, backKeep...)
	}

	gap := Message{Synthetic: true, Text: omittedMarker}
	return append(append(frontKeep, gap), backKeep...)
}

func StripAdversarial(text string) string {
	repl := strings.NewReplacer(
		"<|", "<<|",
		"|>", "|>>",
		"<system>", "<sys>",
		"</system>", "</sys>",
		"[INST]", "[inst]",
		"[/INST]", "[/inst]",
	)
	return repl.Replace(text)
}

func stripAdversarial(text string) string { return StripAdversarial(text) }
