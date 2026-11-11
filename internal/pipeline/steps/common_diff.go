package steps

import (
	"context"
	"path"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
)

func isTestFile(path string) bool {
	base := filepath.Base(path)
	if base == "" {
		return false
	}

	// Go: *_test.go
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	// Rust: *_test.rs
	if strings.HasSuffix(base, "_test.rs") {
		return true
	}
	// Python: test_*.py or *_test.py
	if strings.HasSuffix(base, ".py") {
		name := strings.TrimSuffix(base, ".py")
		if strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test") {
			return true
		}
	}
	// Ruby: test_*.rb
	if strings.HasSuffix(base, ".rb") && strings.HasPrefix(filepath.Base(path), "test_") {
		return true
	}
	// Java: *Test.java or *Tests.java
	if strings.HasSuffix(base, "Test.java") || strings.HasSuffix(base, "Tests.java") {
		return true
	}
	// JS/TS: *.test.{js,ts,jsx,tsx} or *.spec.{js,ts,jsx,tsx}
	for _, ext := range []string{".js", ".ts", ".jsx", ".tsx"} {
		if strings.HasSuffix(base, ".test"+ext) || strings.HasSuffix(base, ".spec"+ext) {
			return true
		}
	}
	return false
}

func detectNewTestFiles(ctx context.Context, dir string) []string {
	out, err := git.Run(ctx, dir, "status", "--porcelain")
	if err != nil || out == "" {
		return nil
	}
	var testFiles []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		// Porcelain format: XY <path> where XY is a 2-char status code + space
		status := line[:2]
		path := strings.TrimSpace(line[3:])
		// New files: untracked (??) or staged add (A ) or staged add with modifications (AM)
		if status == "??" || status[0] == 'A' {
			if isTestFile(path) {
				testFiles = append(testFiles, path)
			}
		}
	}
	return testFiles
}

func matchIgnorePattern(file, pattern string) bool {
	// "vendor/**" → matches anything under "vendor/"
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return file == prefix || strings.HasPrefix(file, prefix+"/")
	}
	// No slash in pattern → match against basename only
	if !strings.Contains(pattern, "/") {
		matched, _ := path.Match(pattern, path.Base(file))
		return matched
	}
	// Full path match
	matched, _ := path.Match(pattern, file)
	return matched
}

func changedPathList(changedFiles string) []string {
	paths := strings.Split(changedFiles, "\x00")
	if paths[len(paths)-1] == "" {
		paths = paths[:len(paths)-1]
	}
	return paths
}

func reviewablePaths(changed []string, ignorePatterns []string) []string {
	var paths []string
	for _, file := range changed {
		ignored := false
		for _, pattern := range ignorePatterns {
			if matchIgnorePattern(file, pattern) {
				ignored = true
				break
			}
		}
		if !ignored {
			paths = append(paths, file)
		}
	}
	return paths
}

func filterDiff(diff string, patterns []string) string {
	if len(patterns) == 0 || diff == "" {
		return diff
	}

	lines := strings.Split(diff, "\n")
	var result []string
	skip := false

	for _, line := range lines {
		// Detect start of a new file section
		if strings.HasPrefix(line, "diff --git ") {
			// Extract path from "diff --git a/<path> b/<path>"
			path := extractDiffPath(line)
			skip = false
			for _, p := range patterns {
				if matchIgnorePattern(path, p) {
					skip = true
					break
				}
			}
		}
		if !skip {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

func extractDiffPath(diffLine string) string {
	const prefix = "diff --git a/"
	rest := strings.TrimPrefix(diffLine, prefix)
	if rest == diffLine {
		return ""
	}
	// Non-rename: rest is "<path> b/<path>" where both paths are equal.
	// Total length = 2*pathLen + len(" b/") = 2*pathLen + 3.
	pathLen := (len(rest) - 3) / 2
	if pathLen > 0 && pathLen+3 <= len(rest) && rest[pathLen:pathLen+3] == " b/" {
		return rest[:pathLen]
	}
	// Fallback for renames or unexpected format: split on first " b/".
	parts := strings.SplitN(rest, " b/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}
