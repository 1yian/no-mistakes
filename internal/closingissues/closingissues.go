package closingissues

import (
	"fmt"
	"sort"
	"strings"
)

func Normalize(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	refs := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("closing issue reference must not be empty")
		}
		ref, err := normalize(value)
		if err != nil {
			return nil, err
		}
		if ref == "" {
			continue
		}
		key := strings.ToLower(ref)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return compare(refs[i], refs[j]) < 0 })
	return refs, nil
}

func Encode(refs []string) (string, error) {
	normalized, err := Normalize(refs)
	if err != nil {
		return "", err
	}
	return strings.Join(normalized, "\n"), nil
}

func Decode(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return Normalize(strings.Split(value, "\n"))
}

func Covers(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, ref := range have {
		set[strings.ToLower(ref)] = struct{}{}
	}
	for _, ref := range want {
		if _, ok := set[strings.ToLower(ref)]; !ok {
			return false
		}
	}
	return true
}

func Localize(ref, repo string) string {
	prefix, number, qualified := strings.Cut(ref, "#")
	if qualified && repo != "" && strings.EqualFold(prefix, repo) {
		return number
	}
	return ref
}

func Target(ref string) string {
	if strings.Contains(ref, "#") {
		return ref
	}
	return "#" + ref
}

func Parts(ref string) (owner, repository, issue, target string) {
	prefix, issue, qualified := strings.Cut(ref, "#")
	if !qualified {
		return "", "", ref, Target(ref)
	}
	owner, repository, _ = strings.Cut(prefix, "/")
	return owner, repository, issue, Target(ref)
}

func normalize(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	prefix, number, qualified := strings.Cut(value, "#")
	if !qualified {
		number = value
		prefix = ""
	} else if !validProjectPath(prefix) {
		return "", fmt.Errorf("invalid closing issue reference %q: expected a number or project#number", value)
	}
	if !positiveDecimal(number) {
		return "", fmt.Errorf("invalid closing issue reference %q: issue number must be a positive decimal number", value)
	}
	if prefix == "" {
		return number, nil
	}
	return strings.ToLower(prefix) + "#" + number, nil
}

func validProjectPath(prefix string) bool {
	segments := strings.Split(prefix, "/")
	if len(segments) < 2 {
		return false
	}
	for i, segment := range segments {
		if i < len(segments)-1 && (strings.HasPrefix(segment, "-") || strings.HasSuffix(segment, "-")) {
			return false
		}
		if !validProjectSegment(segment) {
			return false
		}
	}
	return true
}

func validProjectSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if !asciiLetterOrDigit(r) && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func positiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func asciiLetterOrDigit(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func compare(a, b string) int {
	aPrefix, aNumber, aQualified := strings.Cut(a, "#")
	bPrefix, bNumber, bQualified := strings.Cut(b, "#")
	if !aQualified {
		aNumber, aPrefix = a, ""
	}
	if !bQualified {
		bNumber, bPrefix = b, ""
	}
	if c := strings.Compare(strings.ToLower(aPrefix), strings.ToLower(bPrefix)); c != 0 {
		return c
	}
	if len(aNumber) != len(bNumber) {
		return len(aNumber) - len(bNumber)
	}
	return strings.Compare(aNumber, bNumber)
}
