package scm

import "strings"

const prBodyTruncationMarker = "\n\n…(description truncated)"

func PRBodyLen(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func MaxPRBodyChars(p Provider) int {
	switch p {
	case ProviderAzureDevOps:
		return 4000
	default:
		return 0
	}
}

type PRBodyLimiter interface {
	MaxPRBodyChars() int
}

func HostMaxPRBodyChars(host Host) int {
	if host == nil {
		return 0
	}
	if limiter, ok := host.(PRBodyLimiter); ok {
		return limiter.MaxPRBodyChars()
	}
	return MaxPRBodyChars(host.Provider())
}

func ClampPRBody(body string, max int) string {
	if max <= 0 || PRBodyLen(body) <= max {
		return body
	}
	budget := max - PRBodyLen(prBodyTruncationMarker)
	if budget < 0 {
		budget = 0
	}
	var b strings.Builder
	used := 0
	for _, r := range body {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteString(prBodyTruncationMarker)
	return b.String()
}
