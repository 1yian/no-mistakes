package evidence

import (
	"fmt"
	"strings"
)

const DefaultBranch = "no-mistakes/evidence"

const MarkerPath = ".no-mistakes-evidence"

const MarkerContent = `This branch holds test evidence published by no-mistakes.

It is an orphan branch: it shares no history with the repository's code
branches, and nothing here is part of any release. Pull request bodies link
into it by commit sha so links stay stable.
`

func NormalizeBranch(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return DefaultBranch, nil
	}
	if err := validateBranchName(trimmed); err != nil {
		return "", fmt.Errorf("invalid evidence branch name %q: %w", trimmed, err)
	}
	return trimmed, nil
}

func validateBranchName(name string) error {
	if strings.HasPrefix(name, "refs/") {
		return fmt.Errorf("give a branch name, not a full ref")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("must not start with %q", "-")
	}
	if name == "@" || name == "HEAD" {
		return fmt.Errorf("%q is reserved by Git", name)
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("must not start or end with %q", "/")
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("must not end with %q", ".")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("must not contain %q", "..")
	}
	if strings.Contains(name, "@{") {
		return fmt.Errorf("must not contain %q", "@{")
	}
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("must not contain control characters")
		case r == ' ':
			return fmt.Errorf("must not contain spaces")
		case strings.ContainsRune("~^:?*[\\", r):
			return fmt.Errorf("must not contain %q", string(r))
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" {
			return fmt.Errorf("must not contain an empty path component")
		}
		if strings.HasPrefix(component, ".") {
			return fmt.Errorf("path component %q must not start with %q", component, ".")
		}
		if strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("path component %q must not end with %q", component, ".lock")
		}
	}
	return nil
}
