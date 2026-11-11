package steps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/safeurl"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/shellenv"
	"github.com/kunchenguid/no-mistakes/internal/winproc"
)

func envValue(env []string, key string) (string, bool) {
	return envValueForOS(env, key, runtime.GOOS)
}

func envValueForOS(env []string, key, goos string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
		if goos == "windows" && len(entry) >= len(prefix) && strings.EqualFold(entry[:len(prefix)], prefix) {
			return entry[len(prefix):], true
		}
	}
	return "", false
}

func envKey(entry string) string {
	key, _, found := strings.Cut(entry, "=")
	if !found {
		key = entry
	}
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func mergeEnv(extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	merged := make([]string, 0, len(os.Environ())+len(extra))
	overrides := make(map[string]string, len(extra))
	for _, entry := range extra {
		overrides[envKey(entry)] = entry
	}
	for _, entry := range os.Environ() {
		key := envKey(entry)
		if override, ok := overrides[key]; ok {
			merged = append(merged, override)
			delete(overrides, key)
			continue
		}
		merged = append(merged, entry)
	}
	for _, entry := range extra {
		key := envKey(entry)
		if override, ok := overrides[key]; ok {
			merged = append(merged, override)
			delete(overrides, key)
		}
	}
	return merged
}

func executableCandidates(name string, env []string) []string {
	return executableCandidatesForOS(runtime.GOOS, name, env)
}

func executableCandidatesForOS(goos, name string, env []string) []string {
	candidates := []string{name}
	if goos != "windows" || filepath.Ext(name) != "" {
		return candidates
	}
	pathExt := ".COM;.EXE;.BAT;.CMD"
	if customPathExt, ok := envValueForOS(env, "PATHEXT", goos); ok {
		pathExt = customPathExt
	}
	for _, ext := range strings.Split(pathExt, ";") {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		candidates = append(candidates, name+ext)
	}
	return candidates
}

func findInCustomPath(workDir string, env []string, name string) string {
	customPath, ok := envValue(env, "PATH")
	if !ok {
		return ""
	}
	for _, dir := range filepath.SplitList(customPath) {
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(workDir, dir)
		}
		for _, candidateName := range executableCandidates(name, env) {
			candidate := filepath.Join(dir, candidateName)
			if fi, err := os.Stat(candidate); err == nil && pathCandidateUsable(runtime.GOOS, candidate, fi) {
				return candidate
			}
		}
	}
	return ""
}

func pathCandidateUsable(goos, path string, fi os.FileInfo) bool {
	if fi.IsDir() {
		return false
	}
	if goos == "windows" {
		return filepath.Ext(path) != ""
	}
	return fi.Mode().Perm()&0o111 != 0
}

func missingFromCustomPath(env []string, name string) string {
	customPath, ok := envValue(env, "PATH")
	if !ok {
		return ""
	}
	missing := filepath.Join(".", executableCandidates(name, env)[0])
	for _, dir := range filepath.SplitList(customPath) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		return filepath.Join(dir, executableCandidates(name, env)[0])
	}
	return missing
}

func stepCmd(sctx *pipeline.StepContext, name string, args ...string) *exec.Cmd {
	return stepCmdContext(sctx, sctx.Ctx, name, args...)
}

func stepCmdContext(sctx *pipeline.StepContext, ctx context.Context, name string, args ...string) *exec.Cmd {
	resolved := name
	missingFromPath := false
	if len(sctx.Env) > 0 && !hasExecutablePathSeparator(name) {
		if candidate := findInCustomPath(sctx.WorkDir, sctx.Env, name); candidate != "" {
			resolved = candidate
		} else if _, ok := envValue(sctx.Env, "PATH"); ok {
			resolved = missingFromCustomPath(sctx.Env, name)
			missingFromPath = true
		}
	}
	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.Dir = sctx.WorkDir
	winproc.Harden(cmd)
	if env := pipelineCommandEnvironment(sctx); env != nil {
		cmd.Env = env
	}
	if missingFromPath {
		cmd.Err = &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	return cmd
}

func pipelineCommandEnvironment(sctx *pipeline.StepContext) []string {
	var env []string
	if len(sctx.Env) > 0 {
		env = mergeEnv(sctx.Env)
	}
	if sctx.ForgeContext != nil && !sctx.ForgeContext.Environment.Empty() {
		env = sctx.ForgeContext.Environment.Apply(env)
	}
	return env
}

func configuredCommandEnvironment(sctx *pipeline.StepContext) []string {
	return git.SubprocessEnvironment(pipelineCommandEnvironment(sctx))
}

func stepGitRun(sctx *pipeline.StepContext, args ...string) (string, error) {
	out, err := stepGitRunRaw(sctx, args...)
	return strings.TrimSpace(out), err
}

func stepGitRunRaw(sctx *pipeline.StepContext, args ...string) (string, error) {
	cmd := stepCmd(sctx, "git", args...)
	cmd.Env = git.NonInteractiveEnvFrom(git.ApplyCommitIdentity(sctx.Ctx, cmd.Env), sctx.WorkDir)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w: %s", safeurl.RedactText(strings.Join(args, " ")), err, safeurl.RedactText(stderr))
	}
	return string(out), nil
}

func stepGitRunTree(sctx *pipeline.StepContext, args ...string) (string, error) {
	cmd := stepCmd(sctx, "git", args...)
	cmd.Env = git.NonInteractiveEnvFrom(git.ApplyCommitIdentity(sctx.Ctx, cmd.Env), sctx.WorkDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	shellenv.ConfigureShellCommand(cmd)
	out, err := shellenv.OutputShellCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", safeurl.RedactText(strings.Join(args, " ")), err, safeurl.RedactText(strings.TrimSpace(stderr.String())))
	}
	return strings.TrimSpace(string(out)), nil
}

func stepGitRunEnv(sctx *pipeline.StepContext, extraEnv []string, args ...string) (string, error) {
	cmd := stepCmd(sctx, "git", args...)
	cmd.Env = append(git.NonInteractiveEnvFrom(git.ApplyCommitIdentity(sctx.Ctx, cmd.Env), sctx.WorkDir), extraEnv...)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w: %s", safeurl.RedactText(strings.Join(args, " ")), err, safeurl.RedactText(stderr))
	}
	return strings.TrimSpace(string(out)), nil
}

func stepGitHeadSHA(sctx *pipeline.StepContext) (string, error) {
	return stepGitRun(sctx, "rev-parse", "HEAD")
}

func stepGitPush(sctx *pipeline.StepContext, remote, ref, expectedSHA string, forceWithLease bool) error {
	args := []string{"push", remote}
	if forceWithLease {
		if expectedSHA != "" {
			args = append(args, fmt.Sprintf("--force-with-lease=%s:%s", ref, expectedSHA))
		} else {
			args = append(args, "--force-with-lease")
		}
	}
	args = append(args, "HEAD:"+ref)
	_, err := stepGitRun(sctx, args...)
	return err
}

func stepGitPushCommit(sctx *pipeline.StepContext, remote, commitSHA, ref, expectedSHA string, forceWithLease bool) error {
	args := []string{"push", remote}
	if forceWithLease {
		if expectedSHA != "" {
			args = append(args, fmt.Sprintf("--force-with-lease=%s:%s", ref, expectedSHA))
		} else {
			args = append(args, "--force-with-lease")
		}
	}
	args = append(args, commitSHA+":"+ref)
	_, err := stepGitRun(sctx, args...)
	return err
}

func stepCLIAvailable(sctx *pipeline.StepContext, provider scm.Provider) bool {
	return stepExecutableAvailable(sctx, provider.CLIName())
}

func stepExecutableAvailable(sctx *pipeline.StepContext, name string) bool {
	if name == "" {
		return false
	}
	if hasExecutablePathSeparator(name) {
		candidate := name
		if !filepath.IsAbs(candidate) && sctx.WorkDir != "" {
			candidate = filepath.Join(sctx.WorkDir, candidate)
		}
		fi, err := os.Stat(candidate)
		return err == nil && pathCandidateUsable(runtime.GOOS, candidate, fi)
	}
	if len(sctx.Env) == 0 {
		_, err := exec.LookPath(name)
		return err == nil
	}
	if candidate := findInCustomPath(sctx.WorkDir, sctx.Env, name); candidate != "" {
		return true
	}
	_, ok := envValue(sctx.Env, "PATH")
	if ok {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func hasExecutablePathSeparator(name string) bool {
	return strings.ContainsRune(name, filepath.Separator) || (filepath.Separator != '/' && strings.ContainsRune(name, '/'))
}

func stepAuthConfigured(sctx *pipeline.StepContext, provider scm.Provider) bool {
	args := provider.AuthCheckCommand()
	if len(args) == 0 {
		return false
	}
	cmd := stepCmd(sctx, args[0], args[1:]...)
	return cmd.Run() == nil
}

func runShellCommand(ctx context.Context, dir, cmdStr string) (string, int, error) {
	return runShellCommandWithEnv(ctx, dir, nil, cmdStr)
}

func runStepShellCommand(sctx *pipeline.StepContext, cmdStr string) (string, int, error) {
	return runShellCommandWithProcessEnv(sctx.Ctx, sctx.WorkDir, configuredCommandEnvironment(sctx), cmdStr)
}

func runShellCommandWithEnv(ctx context.Context, dir string, env []string, cmdStr string) (string, int, error) {
	if len(env) > 0 {
		env = mergeEnv(env)
	}
	return runShellCommandWithProcessEnv(ctx, dir, env, cmdStr)
}

func runShellCommandWithProcessEnv(ctx context.Context, dir string, env []string, cmdStr string) (string, int, error) {
	return runShellCommandWithPriority(ctx, dir, env, cmdStr, 0)
}

func runShellCommandWithPriority(ctx context.Context, dir string, env []string, cmdStr string, nice int) (string, int, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", shellenv.OwnOOMScoreScript(cmdStr))
	}
	if nice != 0 {
		if nice < 0 || nice > 19 || runtime.GOOS == "windows" {
			return "", -1, fmt.Errorf("unsupported command niceness %d on %s", nice, runtime.GOOS)
		}
		if cmd.Err != nil {
			return "", -1, cmd.Err
		}
		// Run the already-resolved shell under nice with the same arguments.
		cmd = exec.CommandContext(ctx, "nice", append([]string{"-n", strconv.Itoa(nice), cmd.Path}, cmd.Args[1:]...)...)
	}
	shellenv.ConfigureCooperativeShellCommand(cmd)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := shellenv.CombinedOutputShellCommand(cmd)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), ee.ExitCode(), nil
		}
		return string(out), -1, fmt.Errorf("run command %q: %w", cmdStr, err)
	}
	return string(out), 0, nil
}
