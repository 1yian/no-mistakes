package steps

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// This file owns how the Test step's live-validation contract - the derived
// scenarios and the run's verdict - reaches a human reviewer.
//
// The contract exists because "tests passed" answered a question nobody asked:
// a reviewer wants to know which end-user scenarios were driven against the
// real product, which were only asserted against stubs, and which could not be
// driven here at all. So every surface renders the same three facts per
// scenario (what was exercised, what happened, whether it was live) plus the
// step's own verdict, and the machine-readable half of the same answer rides
// the PR attestation as live_validation for a consumer that must decide
// whether this change was live validated without reading prose.

func collectTestingScenarios(sr *db.StepResult, rounds []*db.StepRound) []types.TestScenario {
	for _, raw := range testingEvidenceFindingsJSON(sr, rounds) {
		if raw == nil || strings.TrimSpace(*raw) == "" {
			continue
		}
		findings, err := types.ParseFindingsJSON(*raw)
		if err != nil {
			continue
		}
		if len(findings.Scenarios) > 0 {
			return findings.Scenarios
		}
	}
	return nil
}

func collectTestingVerdict(sr *db.StepResult, rounds []*db.StepRound) string {
	for _, raw := range testingEvidenceFindingsJSON(sr, rounds) {
		if raw == nil || strings.TrimSpace(*raw) == "" {
			continue
		}
		findings, err := types.ParseFindingsJSON(*raw)
		if err != nil {
			continue
		}
		if types.IsKnownTestVerdict(findings.Verdict) {
			return findings.Verdict
		}
	}
	return ""
}

func renderLiveValidationLine(scenarios []types.TestScenario, verdict string) string {
	live, total := types.LiveScenarioCounts(scenarios)
	if !types.IsKnownTestVerdict(verdict) && total == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Live validation: ")
	if types.IsKnownTestVerdict(verdict) {
		b.WriteString(verdictEmoji(verdict))
		b.WriteString(" ")
		b.WriteString(verdict)
	} else {
		b.WriteString("no verdict recorded")
	}
	if total > 0 {
		b.WriteString(fmt.Sprintf(" - %d of %d scenarios driven live against the product", live, total))
	}
	return b.String()
}

func verdictEmoji(verdict string) string {
	switch verdict {
	case types.TestVerdictGo:
		return "✅"
	case types.TestVerdictNoGo:
		return "❌"
	default:
		return "⚠️"
	}
}

func scenarioResultEmoji(result string) string {
	switch result {
	case types.ScenarioResultPass:
		return "✅"
	case types.ScenarioResultFail:
		return "❌"
	default:
		return "⏸️"
	}
}

func renderScenarioTable(scenarios []types.TestScenario, opts testingSummaryOptions) string {
	flavor := opts.flavor
	if len(scenarios) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| Scenario | Result | Live | Evidence |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	var names []string
	if opts.evidenceLinkBase != "" && opts.evidenceRoot != "" {
		names = evidenceFileNames(opts.evidenceRoot)
	}
	rows := 0
	for _, scenario := range scenarios {
		name := scenarioCell(scenario.Name, flavor)
		if name == "" {
			continue
		}
		result := scenario.Result
		live := "no"
		if scenario.Live {
			live = "live"
		}
		basis := strings.TrimSpace(scenario.Evidence)
		if result == types.ScenarioResultUntested && strings.TrimSpace(scenario.Reason) != "" {
			basis = strings.TrimSpace(scenario.Reason)
		}
		b.WriteString(fmt.Sprintf("| %s | %s %s | %s | %s |\n",
			name,
			scenarioResultEmoji(result),
			result,
			live,
			scenarioEvidenceCell(basis, opts, names),
		))
		rows++
	}
	if rows == 0 {
		return ""
	}
	return b.String()
}

func scenarioCell(text string, flavor prBodyFlavor) string {
	clean := sanitizePromptText(text)
	if clean == "" {
		return ""
	}
	clean = truncateScenarioCell(clean)
	clean = escapePRText(clean, flavor)
	return strings.ReplaceAll(clean, "|", "\\|")
}

const maxScenarioCellRunes = 200

func truncateScenarioCell(text string) string {
	runes := []rune(text)
	if len(runes) <= maxScenarioCellRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:maxScenarioCellRunes])) + "…"
}

func scenarioEvidenceCell(text string, opts testingSummaryOptions, names []string) string {
	if opts.evidenceLinkBase == "" || opts.evidenceRoot == "" {
		return scenarioCell(text, opts.flavor)
	}
	clean := truncateScenarioCell(sanitizePromptText(text))
	rendered, _ := renderScenarioEvidenceReferences(clean, opts, names, true)
	return escapePipelineFoldMarkers(rendered)
}

func renderScenarioEvidenceReferences(clean string, opts testingSummaryOptions, names []string, codeSpans bool) (string, bool) {
	linked := false
	plain := func(s string) string { return strings.ReplaceAll(escapePRText(s, opts.flavor), "|", "\\|") }
	var b, prose strings.Builder
	rest := clean
	for len(rest) > 0 {
		if codeSpans {
			if content, n := scenarioCodeSpan(rest); n > 0 {
				trimmed := strings.TrimSpace(content)
				if _, _, m := matchScenarioEvidenceReference(trimmed, opts.evidenceRoot, names); m > 0 && m == len(trimmed) {
					content = trimmed
				}
				if rendered, found := renderScenarioEvidenceReferences(content, opts, names, false); found {
					b.WriteString(plain(prose.String()))
					prose.Reset()
					b.WriteString(rendered)
					linked = true
				} else {
					prose.WriteString(rest[:n])
				}
				rest = rest[n:]
				continue
			}
		}
		// A reference the cell already carries as a link is complete:
		// rewriting a filename inside it would break the link.
		if n := scenarioExistingReferenceSpan(rest); n > 0 {
			prose.WriteString(rest[:n])
			rest = rest[n:]
			continue
		}
		if scenarioReferenceCanStart(clean, len(clean)-len(rest)) {
			if label, path, n := matchScenarioEvidenceReference(rest, opts.evidenceRoot, names); n > 0 {
				if link := renderEvidenceFileLink(opts.evidenceLinkBase, opts.evidenceRoot, path, label); link != "" {
					b.WriteString(plain(prose.String()))
					prose.Reset()
					b.WriteString(strings.ReplaceAll(link, "|", "\\|"))
					linked = true
					rest = rest[n:]
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(rest)
		prose.WriteRune(r)
		rest = rest[size:]
	}
	b.WriteString(plain(prose.String()))
	return b.String(), linked
}

func scenarioExistingReferenceSpan(rest string) int {
	text := rest
	if strings.HasPrefix(text, "![") {
		text = text[1:]
	}
	if strings.HasPrefix(text, "[") {
		if close := scenarioBracketSpan(text); close > 0 && close < len(text) && text[close] == '(' {
			if n := scenarioInlineLinkSuffixSpan(text[close+1:]); n > 0 {
				return len(rest) - len(text) + close + 1 + n
			}
		}
	}
	if strings.HasPrefix(rest, "<") {
		if end := strings.IndexByte(rest, '>'); end > 0 && scenarioStartsWithURL(rest[1:end]) {
			return end + 1
		}
	}
	if scenarioStartsWithURL(rest) {
		if end := strings.IndexFunc(rest, unicode.IsSpace); end >= 0 {
			return end
		}
		return len(rest)
	}
	return 0
}

func scenarioBracketSpan(text string) int {
	depth := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return 0
}

func scenarioCodeSpan(rest string) (content string, n int) {
	if !strings.HasPrefix(rest, "`") {
		return "", 0
	}
	run := 0
	for run < len(rest) && rest[run] == '`' {
		run++
	}
	for i := run; i < len(rest); {
		if rest[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(rest) && rest[j] == '`' {
			j++
		}
		if j-i == run {
			return rest[run:i], j
		}
		i = j
	}
	return "", 0
}

func scenarioInlineLinkSuffixSpan(text string) int {
	i := 0
	space := func() {
		for i < len(text) && strings.ContainsRune(" \t\r\n", rune(text[i])) {
			i++
		}
	}
	space()
	if i < len(text) && text[i] == '<' {
		i++
		for i < len(text) && text[i] != '>' {
			if text[i] == '<' || text[i] == '\n' || text[i] == '\r' {
				return 0
			}
			if text[i] == '\\' && i+1 < len(text) {
				i++
			}
			i++
		}
		if i == len(text) {
			return 0
		}
		i++
	} else {
		depth := 0
		for i < len(text) {
			c := text[i]
			if c == '\\' && i+1 < len(text) {
				i += 2
				continue
			}
			if strings.ContainsRune(" \t\r\n", rune(c)) || (c == ')' && depth == 0) {
				break
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
			}
			i++
		}
		if depth != 0 {
			return 0
		}
	}
	endDestination := i
	space()
	if i > endDestination && i < len(text) && strings.ContainsRune("\"'(", rune(text[i])) {
		close := text[i]
		if close == '(' {
			close = ')'
		}
		i++
		for i < len(text) && text[i] != close {
			if close == ')' && text[i] == '(' {
				return 0
			}
			if text[i] == '\\' && i+1 < len(text) {
				i++
			}
			i++
		}
		if i == len(text) {
			return 0
		}
		i++
		space()
	}
	if i < len(text) && text[i] == ')' {
		return i + 1
	}
	return 0
}

func scenarioStartsWithURL(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 || i > 16 {
		return false
	}
	for _, r := range s[:i] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func matchScenarioEvidenceReference(rest, root string, names []string) (label, path string, n int) {
	reference := rest
	var quote byte
	if len(reference) > 0 && strings.ContainsRune("\"'", rune(reference[0])) {
		quote = reference[0]
		reference = reference[1:]
	}
	offsets := []int{0}
	if absoluteRoot, err := filepath.Abs(root); err == nil {
		for _, dir := range []string{absoluteRoot, resolveArtifactPathSymlinks(absoluteRoot)} {
			prefix := strings.TrimRight(dir, string(filepath.Separator)) + string(filepath.Separator)
			if strings.HasPrefix(reference, prefix) {
				offsets = append(offsets, len(prefix))
			}
		}
	}
	for _, name := range names {
		for _, offset := range offsets {
			candidate := name
			if offset > 0 {
				candidate = filepath.FromSlash(name)
			}
			if !strings.HasPrefix(reference[offset:], candidate) {
				continue
			}
			end := offset + len(candidate)
			if quote != 0 {
				if end >= len(reference) || reference[end] != quote {
					continue
				}
				end++
			}
			if scenarioReferenceCanEnd(reference[end:]) {
				if quote != 0 {
					end++
				}
				return name, filepath.Join(root, filepath.FromSlash(name)), end
			}
		}
	}
	return "", "", 0
}

func scenarioReferenceCanStart(text string, pos int) bool {
	if pos == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:pos])
	return unicode.IsSpace(r) || strings.ContainsRune(";,([|`\"'", r)
}

func scenarioReferenceCanEnd(after string) bool {
	if after == "" {
		return true
	}
	r, size := utf8.DecodeRuneInString(after)
	if unicode.IsSpace(r) || strings.ContainsRune(";,)]|`\"'", r) {
		return true
	}
	if r != '.' {
		return false
	}
	next := after[size:]
	if next == "" {
		return true
	}
	r, _ = utf8.DecodeRuneInString(next)
	return unicode.IsSpace(r)
}
