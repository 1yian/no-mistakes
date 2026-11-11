package safepath

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

const Placeholder = "~"

var genericHomePattern = regexp.MustCompile(
	`(^|-[A-Za-z]|\\[nrt]|[^A-Za-z0-9_./\\-])((?i:file://)?(?:[A-Za-z]:)?(?:\\\\|[/\\])(?i:home|users)(?:\\\\|[/\\])[^/\\\s"'` + "`" + `<>()\[\]{},;:&|*?]+)`)

func RedactText(text string) string {
	if text == "" {
		return text
	}
	// The account's own home first, because it may sit outside the
	// conventional roots (/root, a container path, a relocated home), then the
	// conventional roots for every other account.
	for _, home := range homeCandidates() {
		text = replaceHomePrefix(text, home)
	}
	return genericHomePattern.ReplaceAllString(text, "${1}"+Placeholder)
}

func homeCandidates() []string {
	var raw []string
	if home, err := os.UserHomeDir(); err == nil {
		raw = append(raw, home)
	}
	raw = append(raw, os.Getenv("HOME"), os.Getenv("USERPROFILE"))

	// Each home's spellings include its resolved target: a macOS home reached
	// through /var reads back as /private/var, and a symlinked home directory
	// reads back as its target. Captured output can carry either spelling.
	seen := map[string]bool{}
	var out []string
	for _, home := range raw {
		for _, candidate := range pathSpellings(home) {
			if !seen[candidate] {
				seen[candidate] = true
				out = append(out, candidate)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func addBothSeparatorSpellings(add func(string), home string) {
	add(home)
	add(strings.ReplaceAll(home, `\`, "/"))
	add(strings.ReplaceAll(home, "/", `\`))
	add(strings.ReplaceAll(home, `\`, `\\`))
}

func usableHomeCandidate(home string) bool {
	if len(home) < 4 {
		return false
	}
	rest, absolute := trimAbsoluteRoot(home)
	if !absolute {
		return false
	}
	// A bare root - "/", a drive letter, a UNC prefix - names no account, and
	// matching it would rewrite every path in the document.
	return strings.Trim(rest, `/\.`) != ""
}

func trimAbsoluteRoot(p string) (rest string, absolute bool) {
	if len(p) > 2 && p[1] == ':' && isASCIILetter(p[0]) && (p[2] == '/' || p[2] == '\\') {
		return strings.TrimLeft(p[2:], `/\`), true
	}
	if p != "" && (p[0] == '/' || p[0] == '\\') {
		return strings.TrimLeft(p, `/\`), true
	}
	return "", false
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func replaceHomePrefix(text, home string) string {
	return replacePathPrefix(text, home, func(string) (int, string) { return 0, Placeholder })
}

func replacePathPrefix(text, prefix string, replace func(rest string) (int, string)) string {
	if prefix == "" || !strings.Contains(text, prefix) {
		return text
	}
	specific := specificHomeCandidate(prefix)
	var b strings.Builder
	for i := 0; i < len(text); {
		offset := strings.Index(text[i:], prefix)
		if offset < 0 {
			b.WriteString(text[i:])
			return b.String()
		}
		start := i + offset
		end := start + len(prefix)
		if boundaryBefore(text, start, specific) && boundaryAfter(text, end) {
			consumed, placeholder := replace(text[end:])
			b.WriteString(text[i:start])
			b.WriteString(placeholder)
			i = end + consumed
			continue
		}
		b.WriteString(text[i : start+1])
		i = start + 1
	}
	return b.String()
}

func specificHomeCandidate(home string) bool {
	rest, _ := trimAbsoluteRoot(home)
	return strings.ContainsAny(rest, `/\`)
}

func boundaryBefore(text string, i int, specific bool) bool {
	if i == 0 {
		return true
	}
	switch c := text[i-1]; {
	case c == '/':
		// The one slash that legitimately precedes an absolute path.
		return strings.HasSuffix(text[:i], "://")
	case c == '\\' || isPathNameByte(c):
		// The letter of a JSON string escape (\n, \r, \t) is the tail of an
		// escape sequence, not a name byte glued to the path - the shape
		// JSON-escaped captured output prints for a line-initial home path.
		if i >= 2 && text[i-2] == '\\' && (c == 'n' || c == 'r' || c == 't') {
			return true
		}
		if i >= 2 && text[i-2] == '-' && isASCIILetter(c) {
			return true
		}
		// Inside a URL that byte is the tail of the host or the previous path
		// segment (https://host/evidence/home/<user>/x.log). Outside one, the
		// same shape is a different path the home is merely a suffix of
		// (/opt/srv/<name>), so it stays a non-boundary.
		return specific && insideURL(text, i)
	default:
		return true
	}
}

func insideURL(text string, i int) bool {
	prefix := text[:i]
	scheme := strings.LastIndex(prefix, "://")
	return scheme >= 0 && !strings.ContainsAny(prefix[scheme:], " \t\r\n")
}

func boundaryAfter(text string, i int) bool {
	if i >= len(text) {
		return true
	}
	c := text[i]
	return c == '/' || c == '\\' || !isPathNameByte(c)
}

func isPathNameByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_' || c == '.' || c == '-':
		return true
	default:
		return false
	}
}
