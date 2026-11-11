package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	baseAttributionMaxListed    = 20
	baseAttributionMaxLineRunes = 300
)

var testFailureLine = regexp.MustCompile(`^(?:--- FAIL: |FAILED |not ok |[✕✗×] )|\.\.\. FAILED$`)

var goPackageResult = regexp.MustCompile(`^(?:FAIL|ok)\s*\t(\S+)`)

var (
	ansiEscape         = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
	testDurationSuffix = regexp.MustCompile(`\s*\(\d+(?:\.\d+)?\s*(?:s|ms|µs|us)\)|\s+\d+(?:\.\d+)?s$|\s*# time=\d+(?:\.\d+)?m?s$`)
	tapOrdinal         = regexp.MustCompile(`^not ok \d+`)
)

type baseAttribution struct {
	baseSHA      string
	baseExitCode int
	introduced   []string
	preexisting  []string
	ambiguous    []string
	unattributed []string
	unavailable  string
}

func attributeTestFailures(sctx *pipeline.StepContext, testCmd, baseSHA, headOutput string) (baseAttribution, error) {
	attribution := baseAttribution{baseSHA: baseSHA}
	if strings.TrimSpace(baseSHA) == "" || baseSHA == git.EmptyTreeSHA || git.IsZeroSHA(baseSHA) {
		attribution.unavailable = "the run has no base commit"
		return attribution, nil
	}
	sctx.Log(fmt.Sprintf("configured test command failed; re-running it on base commit %s to attribute failures...", shortSHA(baseSHA)))
	baseOutput, baseExitCode, err := runTestCommandOnBase(sctx, testCmd, baseSHA)
	if ctxErr := sctx.Ctx.Err(); ctxErr != nil {
		return attribution, ctxErr
	}
	if err != nil {
		sctx.Log(fmt.Sprintf("warning: base attribution unavailable: %v", err))
		attribution.unavailable = err.Error()
		return attribution, nil
	}
	attribution.baseExitCode = baseExitCode
	attribution.classify(headOutput, baseOutput)
	return attribution, nil
}

func (a *baseAttribution) classify(headOutput, baseOutput string) {
	baseFailures := map[string]bool{}
	baseUnattributed := map[string]bool{}
	if a.baseExitCode != 0 {
		lines, _, unattributed := testFailureLines(baseOutput)
		if len(lines) == 0 {
			return
		}
		for _, line := range lines {
			baseFailures[line] = true
		}
		for _, line := range unattributed {
			baseUnattributed[line] = true
		}
	}
	// A key carries its package or file in its own text, so a head line that
	// is qualified matches only an identically qualified base line.
	headLines, qualified, headUnattributed := testFailureLines(headOutput)
	for _, line := range headUnattributed {
		if !baseUnattributed[line] {
			a.unattributed = append(a.unattributed, line)
		}
	}
	for _, line := range headLines {
		switch {
		case !baseFailures[line]:
			a.introduced = append(a.introduced, line)
		case !qualified[line]:
			a.ambiguous = append(a.ambiguous, line)
		default:
			a.preexisting = append(a.preexisting, line)
		}
	}
}

func runTestCommandOnBase(sctx *pipeline.StepContext, testCmd, baseSHA string) (string, int, error) {
	scratch, err := os.MkdirTemp("", "no-mistakes-test-base-")
	if err != nil {
		return "", 0, fmt.Errorf("create base checkout directory: %w", err)
	}
	defer os.RemoveAll(scratch)
	checkout := filepath.Join(scratch, "base")
	source, err := filepath.Abs(sctx.WorkDir)
	if err != nil {
		return "", 0, fmt.Errorf("resolve run worktree: %w", err)
	}
	if _, err := stepGitRun(sctx, "clone", "--quiet", "--shared", "--no-checkout", source, checkout); err != nil {
		return "", 0, fmt.Errorf("clone base checkout: %w", err)
	}
	if _, err := stepGitRun(sctx, "-C", checkout, "checkout", "--quiet", "--detach", baseSHA); err != nil {
		return "", 0, fmt.Errorf("check out base commit %s: %w", shortSHA(baseSHA), err)
	}
	ctx, cancel, budget := testAgentContext(sctx)
	defer cancel()
	exceeded := func() error {
		if ctx.Err() != nil && sctx.Ctx.Err() == nil {
			return fmt.Errorf("base run exceeded %s", budget)
		}
		return nil
	}
	env := configuredCommandEnvironment(sctx)
	if prepareCmd := strings.TrimSpace(sctx.Config.Commands.Prepare); prepareCmd != "" {
		output, exitCode, err := runShellCommandWithPriority(ctx, checkout, env, prepareCmd, sctx.Config.CommandOverrides["prepare"].Nice)
		if err := exceeded(); err != nil {
			return "", 0, err
		}
		if err != nil {
			return "", 0, fmt.Errorf("prepare base checkout: %w", err)
		}
		if output != "" {
			logCommandOutput(sctx, output, "Prepare (base)", types.StepTest)
		}
		if exitCode != 0 {
			return "", 0, fmt.Errorf("prepare command exited with code %d on the base checkout", exitCode)
		}
		if err := cleanupPreparationChanges(ctx, checkout, baseSHA, nil); err != nil {
			if err := exceeded(); err != nil {
				return "", 0, err
			}
			return "", 0, fmt.Errorf("clean base preparation changes: %w", err)
		}
	}
	output, exitCode, err := runShellCommandWithPriority(ctx, checkout, env, testCmd, sctx.Config.CommandOverrides["test"].Nice)
	if err := exceeded(); err != nil {
		return "", 0, err
	}
	if err != nil {
		return "", 0, fmt.Errorf("run test command on base: %w", err)
	}
	logCommandOutput(sctx, output, "Test (base)", types.StepTest)
	// The shell's own "cannot execute" and "not found" codes mean the suite
	// never ran on the base (typically an ignored dependency the fresh clone
	// lacks), which is not a base test failure.
	if exitCode == 126 || exitCode == 127 {
		return "", 0, fmt.Errorf("test command could not run on the base checkout (exit code %d)", exitCode)
	}
	return output, exitCode, nil
}

func testFailureLines(output string) (lines []string, qualified map[string]bool, unattributed []string) {
	var failures []string
	qualified = map[string]bool{}
	var pendingGo []int
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(ansiEscape.ReplaceAllString(raw, ""))
		if m := goPackageResult.FindStringSubmatch(line); m != nil {
			if len(pendingGo) == 0 && strings.HasPrefix(line, "FAIL") {
				result := strings.Join(strings.Fields(testDurationSuffix.ReplaceAllString(line, "")), " ")
				if !slices.Contains(unattributed, result) {
					unattributed = append(unattributed, result)
				}
			}
			for _, i := range pendingGo {
				failures[i] = m[1] + ": " + failures[i]
				qualified[failures[i]] = true
			}
			pendingGo = nil
			continue
		}
		if !testFailureLine.MatchString(line) {
			continue
		}
		line = tapOrdinal.ReplaceAllString(line, "not ok")
		line = strings.TrimSpace(testDurationSuffix.ReplaceAllString(line, ""))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "--- FAIL: ") {
			pendingGo = append(pendingGo, len(failures))
		}
		if strings.HasPrefix(line, "FAILED ") {
			line = pytestTestID(line)
			if strings.Contains(line, "::") {
				qualified[line] = true
			}
		}
		failures = append(failures, line)
	}
	seen := map[string]bool{}
	for _, line := range failures {
		if !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	return lines, qualified, unattributed
}

func pytestTestID(line string) string {
	depth := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case ' ':
			if depth == 0 && strings.HasPrefix(line[i:], " - ") {
				return line[:i]
			}
		}
	}
	return line
}

func (a baseAttribution) render() string {
	if a.unavailable != "" {
		return "Base attribution unavailable (" + a.unavailable + "); every failure is attributed to this change."
	}
	base := shortSHA(a.baseSHA)
	if a.baseExitCode == 0 {
		return "Base attribution: commands.test passes on base commit " + base + ", so this change introduced the failure." + renderAttributedList("Introduced by this change", a.introduced) + renderAttributedList(unattributedLabel, a.unattributed)
	}
	header := fmt.Sprintf("Base attribution: commands.test also fails on base commit %s (exit code %d).", base, a.baseExitCode)
	if len(a.introduced) == 0 && len(a.preexisting) == 0 && len(a.ambiguous) == 0 && len(a.unattributed) == 0 {
		return header + " Individual failures were not recognized in both outputs, so introduced and pre-existing failures could not be separated."
	}
	return header + renderAttributedList("Introduced by this change", a.introduced) + renderAttributedList("Pre-existing on the base commit", a.preexisting) + renderAttributedList("Ambiguous, could not attribute", a.ambiguous) + renderAttributedList(unattributedLabel, a.unattributed)
}

const unattributedLabel = "Failures without a per-test line (could not be attributed)"

func renderAttributedList(label string, lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s (%d):", label, len(lines))
	for i, line := range lines {
		if i == baseAttributionMaxListed {
			fmt.Fprintf(&b, "\n- ... and %d more (see the step log)", len(lines)-i)
			break
		}
		if runes := []rune(line); len(runes) > baseAttributionMaxLineRunes {
			line = string(runes[:baseAttributionMaxLineRunes]) + "..."
		}
		b.WriteString("\n- " + line)
	}
	return b.String()
}
