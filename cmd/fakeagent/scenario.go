package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Scenario struct {
	Actions []Action `yaml:"actions"`
}

type Action struct {
	Match string `yaml:"match"`

	Structured map[string]any `yaml:"structured,omitempty"`

	StructuredRaw string `yaml:"structured_raw,omitempty"`

	Text string `yaml:"text,omitempty"`

	Edits []Edit `yaml:"edits,omitempty"`

	Stage []string `yaml:"stage,omitempty"`

	Git [][]string `yaml:"git,omitempty"`

	DelayMS int `yaml:"delay_ms,omitempty"`

	AskQuestions []string `yaml:"ask_questions,omitempty"`

	WriteEvidence []EvidenceFile `yaml:"write_evidence,omitempty"`
}

type EvidenceFile struct {
	Path    string `yaml:"path"`
	Content string `yaml:"content"`
}

type Edit struct {
	Path string `yaml:"path"`
	Old  string `yaml:"old,omitempty"`
	New  string `yaml:"new,omitempty"`
}

func loadScenario(path string) (*Scenario, error) {
	if path == "" {
		return defaultScenario(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario %q: %w", path, err)
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse scenario %q: %w", path, err)
	}
	return &s, nil
}

func defaultScenario() *Scenario {
	return &Scenario{
		Actions: []Action{{
			Text: "no issues found",
			Structured: map[string]any{
				"findings":        []any{},
				"summary":         "no issues found",
				"risk_level":      "low",
				"risk_rationale":  "no risks detected in the diff",
				"risk_scope":      "source-or-external",
				"tested":          []string{"fakeagent: simulated test run"},
				"testing_summary": "simulated tests passed",
				"artifacts":       []any{},
				"scenarios": []any{map[string]any{
					"name":     "fakeagent: simulated end-to-end scenario",
					"result":   "pass",
					"live":     true,
					"evidence": "fakeagent: simulated test run",
					// Present-but-empty rather than omitted: the codex adapter
					// rewrites every schema property as required-and-nullable,
					// so a canned response that omits an optional field fails
					// validation on that backend alone.
					"reason": "",
				}},
				"verdict": "go",
				"title":   "feat: fakeagent change",
				"body":    "## Summary\nfakeagent canned PR body",
			},
		}},
	}
}

func (s *Scenario) Match(prompt string) Action {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return s.MatchInDir(wd, prompt)
}

func (s *Scenario) MatchInDir(wd, prompt string) Action {
	for _, a := range s.Actions {
		if a.Match == "" || strings.Contains(prompt, a.Match) {
			return withReviewCoverage(wd, prompt, a)
		}
	}
	return Action{Text: "no matching scenario"}
}

const reviewPromptMarker = "Review the code changes and return structured findings"

func withReviewCoverage(wd, prompt string, a Action) Action {
	if !strings.Contains(prompt, reviewPromptMarker) || a.StructuredRaw != "" {
		return a
	}
	if a.Structured != nil {
		if _, present := a.Structured["reviewed_paths"]; present {
			return a
		}
	}
	paths, err := reviewCoverageForPrompt(wd, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeagent: review coverage: %v\n", err)
		return a
	}
	structured := make(map[string]any, len(a.Structured)+1)
	for k, v := range a.Structured {
		structured[k] = v
	}
	structured["reviewed_paths"] = paths
	a.Structured = structured
	return a
}

func reviewCoverageForPrompt(wd, prompt string) ([]string, error) {
	if _, tail, present := strings.Cut(prompt, "\nChanged files this review is held to ("); present {
		_, tail, _ = strings.Cut(tail, "\n")
		paths := []string{}
		for _, line := range strings.Split(tail, "\n") {
			if strings.HasPrefix(line, "- A complete review pass ") {
				return paths, nil
			}
			file, ok := strings.CutPrefix(line, "- ")
			if !ok || file == "" {
				return nil, errors.New("malformed review coverage checklist")
			}
			paths = append(paths, file)
		}
		return nil, errors.New("unterminated review coverage checklist")
	}
	base := promptContextValue(prompt, "base commit")
	if base == "" {
		return nil, errors.New("review prompt has no base commit line")
	}
	var ignore []string
	if raw := promptContextValue(prompt, "ignore patterns"); raw != "" && raw != "none" {
		for _, pattern := range strings.Split(raw, ",") {
			if pattern = strings.TrimSpace(pattern); pattern != "" {
				ignore = append(ignore, pattern)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "-z", "--no-renames", base)
	cmd.Dir = wd
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s in %s: %w", base, wd, err)
	}
	paths := []string{}
	for _, file := range strings.Split(string(out), "\x00") {
		if file == "" || ignoredByPatterns(file, ignore) {
			continue
		}
		paths = append(paths, file)
	}
	return paths, nil
}

func promptContextValue(prompt, key string) string {
	prefix := "- " + key + ": "
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func ignoredByPatterns(file string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if file == prefix || strings.HasPrefix(file, prefix+"/") {
				return true
			}
			continue
		}
		if !strings.Contains(pattern, "/") {
			if matched, _ := path.Match(pattern, path.Base(file)); matched {
				return true
			}
			continue
		}
		if matched, _ := path.Match(pattern, file); matched {
			return true
		}
	}
	return false
}

// applyEdits mutates files under CWD (which is the worktree no-mistakes
// pointed the agent at). Errors are logged to stderr but not fatal so a
// scenario with a stale path doesn't kill the whole run.

func applyEdits(edits []Edit) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	return applyEditsInDir(wd, edits)
}

func applyAction(action Action) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	return applyActionInDir(wd, action)
}

func applyActionInDir(wd string, action Action) error {
	if action.DelayMS > 0 {
		time.Sleep(time.Duration(action.DelayMS) * time.Millisecond)
	}
	if err := applyEditsInDir(wd, action.Edits); err != nil {
		return err
	}
	if err := stageFilesInDir(wd, action.Stage); err != nil {
		return err
	}
	return runGitInDir(wd, action.Git)
}

func runGitInDir(wd string, invocations [][]string) error {
	for _, args := range invocations {
		if len(args) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

func applyEditsInDir(wd string, edits []Edit) error {
	wd, err := filepath.Abs(wd)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	var errs []error
	for _, e := range edits {
		if e.Path == "" {
			continue
		}
		path, err := scenarioEditPath(wd, e.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakeagent: %v\n", err)
			errs = append(errs, err)
			continue
		}
		if e.Old == "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				err = fmt.Errorf("mkdir %s: %w", e.Path, err)
				fmt.Fprintf(os.Stderr, "fakeagent: %v\n", err)
				errs = append(errs, err)
				continue
			}
			if err := os.WriteFile(path, []byte(e.New), 0o644); err != nil {
				err = fmt.Errorf("write %s: %w", e.Path, err)
				fmt.Fprintf(os.Stderr, "fakeagent: %v\n", err)
				errs = append(errs, err)
			}
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			err = fmt.Errorf("read %s: %w", e.Path, err)
			fmt.Fprintf(os.Stderr, "fakeagent: %v\n", err)
			errs = append(errs, err)
			continue
		}
		if !strings.Contains(string(data), e.Old) {
			err = fmt.Errorf("replace %s: old text not found", e.Path)
			fmt.Fprintf(os.Stderr, "fakeagent: %v\n", err)
			errs = append(errs, err)
			continue
		}
		updated := strings.Replace(string(data), e.Old, e.New, 1)
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			err = fmt.Errorf("write %s: %w", e.Path, err)
			fmt.Fprintf(os.Stderr, "fakeagent: %v\n", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func stageFilesInDir(wd string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	relPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		full, err := scenarioEditPath(wd, path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(wd, full)
		if err != nil {
			return fmt.Errorf("stage %q: %w", path, err)
		}
		relPaths = append(relPaths, rel)
	}
	if len(relPaths) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := append([]string{"add", "--"}, relPaths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add staged files: %w: %s", err, out)
	}
	return nil
}

func scenarioEditPath(wd, path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q must stay under working directory", path)
	}
	clean := filepath.Clean(path)
	full := filepath.Join(wd, clean)
	rel, err := filepath.Rel(wd, full)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q must stay under working directory", path)
	}
	base, err := scenarioExistingBasePath(full)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	if err := scenarioPathWithinWorkingDirectory(wd, base); err != nil {
		return "", fmt.Errorf("path %q must stay under working directory", path)
	}
	return full, nil
}

func scenarioExistingBasePath(path string) (string, error) {
	current := path
	for {
		if _, err := os.Lstat(current); err == nil {
			return filepath.EvalSymlinks(current)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(current)
		if next == current {
			return "", fmt.Errorf("no existing path for %q", path)
		}
		current = next
	}
}

func scenarioPathWithinWorkingDirectory(wd, path string) error {
	resolvedWD, err := filepath.EvalSymlinks(wd)
	if err != nil {
		resolvedWD = wd
	}
	rel, err := filepath.Rel(resolvedWD, path)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes working directory")
	}
	return nil
}

func (a Action) structuredJSON() []byte {
	if a.StructuredRaw != "" {
		return []byte(a.StructuredRaw)
	}
	if a.Structured == nil {
		return []byte("{}")
	}
	data, err := json.Marshal(a.Structured)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func (a Action) hasStructuredOutput() bool {
	return a.Structured != nil || a.StructuredRaw != ""
}

func (a Action) textOrDefault() string {
	if a.Text != "" {
		return a.Text
	}
	return "ok"
}
