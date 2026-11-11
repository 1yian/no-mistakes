package steps

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// Published PR text must be a fixed point of the read path. `gh pr view --json`
// (go-gh's asciisanitizer) rewrites C0 and C1 control characters, and the
// six-character JSON escapes that decode to them, into caret notation. The
// pr.template integrity checks compare the body gh reads back with the body that
// was sent, and the appendix digest is stamped over the sent bytes, so any such
// byte makes both fail with a false "edited" refusal. neutralizePRControls runs
// at the publication boundary, before the digest is stamped.

var (
	ansiSequencePattern      = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)
	jsonControlEscapePattern = regexp.MustCompile(`(?i)\\u00(0[0-8cef]|1[0-9a-f]|[89][0-9a-f])`)
)

func neutralizePRControls(s string) string {
	s = ansiSequencePattern.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if rewrittenByGH(r) {
			return '�'
		}
		return r
	}, s)
	return jsonControlEscapePattern.ReplaceAllStringFunc(s, func(m string) string {
		return `\u{` + m[2:] + `}`
	})
}

func rewrittenByGH(r rune) bool {
	switch {
	case r == '\t', r == '\n', r == '\v', r == '\r':
		return false
	case r < 0x20:
		return true
	}
	return r >= 0x80 && r <= 0x9f
}

const maxCaretRecoveryPairs = 12

func caretControlPositions(s string) []int {
	var at []int
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '^' {
			continue
		}
		if c := s[i+1]; c >= '@' && c <= '_' && c != 'I' && c != 'J' && c != 'K' && c != 'M' {
			at = append(at, i)
			i++
		}
	}
	return at
}

func restoreCaretControlsAt(s string, at []int, restore func(k int) bool) string {
	var b strings.Builder
	prev := 0
	for k, i := range at {
		if !restore(k) {
			continue
		}
		b.WriteString(s[prev:i])
		b.WriteByte(s[i+1] - '@')
		prev = i + 2
	}
	b.WriteString(s[prev:])
	return b.String()
}

func restoreCaretControls(s string) string {
	at := caretControlPositions(s)
	return restoreCaretControlsAt(s, at, restoreAll)
}

func restoreAll(int) bool { return true }

func appendixDigestMatches(appendix, digest string) bool {
	matches := func(s string) bool { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) == digest }
	if matches(appendix) {
		return true
	}
	at := caretControlPositions(appendix)
	if len(at) == 0 {
		return false
	}
	if len(at) > maxCaretRecoveryPairs {
		// shortcut: beyond the cap only the all-restored reading is tried; raise the cap if legacy PRs mixing that many literal carets turn up.
		return matches(restoreCaretControlsAt(appendix, at, restoreAll))
	}
	for mask := uint64(1); mask < 1<<uint(len(at)); mask++ {
		if matches(restoreCaretControlsAt(appendix, at, func(k int) bool { return mask&(1<<uint(k)) != 0 })) {
			return true
		}
	}
	return false
}
