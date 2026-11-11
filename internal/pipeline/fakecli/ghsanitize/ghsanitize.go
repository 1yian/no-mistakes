package ghsanitize

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

func JSON(src []byte) []byte {
	var out bytes.Buffer
	addEscape := false
	for len(src) > 0 {
		r, size := utf8.DecodeRune(src)
		if r == utf8.RuneError && size < 2 {
			// The real sanitizer fails the read on invalid UTF-8.
			out.Write(src[:size])
			src = src[size:]
			continue
		}
		if unicode.IsControl(r) {
			if repl, ok := caret(r); ok {
				out.WriteString(repl)
				src = src[size:]
				continue
			}
		}
		if len(src) >= 6 {
			if repl, ok := jsonCaret(src[:6]); ok {
				if addEscape {
					repl = "\\" + repl
					addEscape = false
				}
				out.WriteString(repl)
				src = src[6:]
				continue
			}
		}
		out.Write(src[:size])
		src = src[size:]
		addEscape = r == '\\' && !addEscape
	}
	return out.Bytes()
}

func Text(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out string
	if err := json.Unmarshal(JSON(encoded), &out); err != nil {
		return s
	}
	return out
}

func sanitized(code int) bool {
	switch {
	case code == 9, code == 10, code == 11, code == 13:
		return false
	case code < 32:
		return true
	case code >= 0x80 && code <= 0x9f:
		return true
	}
	return false
}

func caretFor(code int) string {
	if code >= 0x80 {
		code -= 0x80
	}
	if code == 28 {
		return `^\\`
	}
	return "^" + string(rune('@'+code))
}

func caret(r rune) (string, bool) {
	if !sanitized(int(r)) {
		return "", false
	}
	return caretFor(int(r)), true
}

func jsonCaret(b []byte) (string, bool) {
	if len(b) != 6 || !bytes.HasPrefix(b, []byte(`\u00`)) {
		return "", false
	}
	var code int
	for _, c := range strings.ToLower(string(b[4:])) {
		switch {
		case c >= '0' && c <= '9':
			code = code*16 + int(c-'0')
		case c >= 'a' && c <= 'f':
			code = code*16 + int(c-'a') + 10
		default:
			return "", false
		}
	}
	if !sanitized(code) {
		return "", false
	}
	return caretFor(code), true
}
