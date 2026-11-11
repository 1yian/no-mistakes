package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/safeurl"
	"github.com/kunchenguid/no-mistakes/internal/shellenv"
	"github.com/kunchenguid/no-mistakes/internal/winproc"
)

const EmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func IsZeroSHA(sha string) bool {
	return sha == "0000000000000000000000000000000000000000"
}

func Run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := RunRaw(ctx, dir, args...)
	return strings.TrimSpace(string(out)), err
}

func RunRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if isBareGitDir(dir) {
		return runInDirWithEnvRaw(ctx, dir, nil, append([]string{"--git-dir=" + dir}, args...)...)
	}
	return runInDirWithEnvRaw(ctx, dir, nil, args...)
}

func RunWithInput(ctx context.Context, dir, input string, args ...string) (string, error) {
	return RunWithEnvAndInput(ctx, dir, nil, input, args...)
}

func RunBare(ctx context.Context, bareDir string, args ...string) (string, error) {
	if bareDir == "" {
		return "", fmt.Errorf("bare git directory is empty")
	}
	return runInDir(ctx, bareDir, append([]string{"--git-dir=" + bareDir}, args...)...)
}

func RunWithEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	return RunWithEnvAndInput(ctx, dir, extraEnv, "", args...)
}

func RunWithEnvAndInput(ctx context.Context, dir string, extraEnv []string, input string, args ...string) (string, error) {
	if isBareGitDir(dir) {
		return runInDirWithEnvAndInput(ctx, dir, extraEnv, input, append([]string{"--git-dir=" + dir}, args...)...)
	}
	return runInDirWithEnvAndInput(ctx, dir, extraEnv, input, args...)
}

func runInDir(ctx context.Context, dir string, args ...string) (string, error) {
	return runInDirWithEnv(ctx, dir, nil, args...)
}

func runInDirWithEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	return runInDirWithEnvAndInput(ctx, dir, extraEnv, "", args...)
}

func runInDirWithEnvAndInput(ctx context.Context, dir string, extraEnv []string, input string, args ...string) (string, error) {
	out, err := runInDirWithEnvAndInputRaw(ctx, dir, extraEnv, input, args...)
	return strings.TrimSpace(string(out)), err
}

func runInDirWithEnvRaw(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
	return runInDirWithEnvAndInputRaw(ctx, dir, extraEnv, "", args...)
}

func runInDirWithEnvAndInputRaw(ctx context.Context, dir string, extraEnv []string, input string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(nonInteractiveEnvForContext(ctx, dir), extraEnv...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	winproc.Harden(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	shellenv.ConfigureShellCommand(cmd)
	out, err := shellenv.OutputShellCommand(cmd)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = fmt.Errorf("%w (%v)", ctxErr, err)
		}
		return nil, fmt.Errorf("git %s: %w: %s", safeurl.RedactText(strings.Join(args, " ")), err, safeurl.RedactText(strings.TrimSpace(stderr.String())))
	}
	return out, nil
}

func StablePatchID(ctx context.Context, dir, from, to, path string) (string, error) {
	diff, err := RunRaw(ctx, dir, "diff", "--no-ext-diff", "--binary", from, to, "--", ":(literal)"+path)
	if err != nil {
		return "", err
	}
	if len(diff) == 0 {
		return "", nil
	}
	args := []string{"patch-id", "--stable"}
	if isBareGitDir(dir) {
		args = append([]string{"--git-dir=" + dir}, args...)
	}
	out, err := runInDirWithEnvAndInputRaw(ctx, dir, nil, string(diff), args...)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("git patch-id --stable returned no identity")
	}
	return fields[0], nil
}

func ValidateBareRepository(ctx context.Context, bareDir string) error {
	if !isBareGitDir(bareDir) {
		return fmt.Errorf("not a structurally valid bare repository: %s", bareDir)
	}
	out, err := RunBare(ctx, bareDir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return fmt.Errorf("validate bare repository: %w", err)
	}
	if strings.TrimSpace(out) != "true" {
		return fmt.Errorf("repository is not bare: %s", bareDir)
	}
	return nil
}

func LooksLikeBareRepository(dir string) bool {
	return isBareGitDir(dir)
}

func isBareGitDir(dir string) bool {
	if dir == "" {
		return false
	}
	root, err := os.Lstat(dir)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return false
	}
	if fi, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || fi.IsDir() {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, "objects"))
	return err == nil && fi.IsDir()
}

func bareRepositoryIdentity(ctx context.Context, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("intended bare repository is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid intended bare repository %q: %w", path, err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("bare repository identity: intended %q, resolved %q: %w", path, "unavailable", err)
	}
	resolved, err := Run(ctx, canonical, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("bare repository identity: intended %q, resolved %q: %w", path, "unavailable", err)
	}
	resolvedCanonical, err := filepath.EvalSymlinks(resolved)
	if err != nil || resolvedCanonical != canonical {
		return "", fmt.Errorf("bare repository identity mismatch: intended %q, resolved %q", path, resolved)
	}
	if err := ValidateBareRepository(ctx, canonical); err != nil {
		return "", fmt.Errorf("bare repository identity: intended %q, resolved %q: %w", path, resolved, err)
	}
	return abs, nil
}

func InitBare(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("intended bare repository is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid intended bare repository %q: %w", path, err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect intended bare repository %q: %w", path, err)
	}
	if len(entries) > 0 {
		if _, err := bareRepositoryIdentity(ctx, path); err != nil {
			return err
		}
	}
	// Pin even a new target explicitly. Never let cwd or GIT_DIR choose which
	// repository receives init's config writes.
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+abs, "init", "--bare", abs)
	cmd.Env = nonInteractiveEnvForContext(ctx, "")
	winproc.Harden(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git init --bare: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func AddRemote(ctx context.Context, dir, name, url string) error {
	_, err := Run(ctx, dir, "remote", "add", name, url)
	return err
}

func EnsureRemote(ctx context.Context, dir, name, url string) error {
	if _, err := GetRemoteURL(ctx, dir, name); err == nil {
		_, err := Run(ctx, dir, "remote", "set-url", name, url)
		return err
	}
	return AddRemote(ctx, dir, name, url)
}

func RemoveRemote(ctx context.Context, dir, name string) error {
	_, err := Run(ctx, dir, "remote", "remove", name)
	return err
}

func GetRemoteURL(ctx context.Context, dir, name string) (string, error) {
	return Run(ctx, dir, "remote", "get-url", name)
}

func GetConfiguredRemoteURL(ctx context.Context, dir, name string) (string, error) {
	return Run(ctx, dir, "config", "--get", "remote."+name+".url")
}

func GetConfiguredRemoteURLs(ctx context.Context, dir, name string) ([]string, error) {
	out, err := Run(ctx, dir, "config", "--null", "--get-all", "remote."+name+".url")
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00"), nil
}

func HasRemote(ctx context.Context, dir, name string) (bool, error) {
	out, err := Run(ctx, dir, "remote")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

func FindGitRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = abs
	winproc.Harden(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", abs)
	}
	root := strings.TrimSpace(string(out))
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return root, nil
	}
	return resolved, nil
}

func FindMainRepoRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	// Resolve the git common dir.
	commonDirCmd := exec.Command("git", "rev-parse", "--git-common-dir")
	commonDirCmd.Dir = abs
	winproc.Harden(commonDirCmd)
	commonDirOut, err := commonDirCmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", abs)
	}
	commonDir := strings.TrimSpace(string(commonDirOut))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(abs, commonDir)
	}

	// Branch 1: regular repo or linked worktree. Linked worktrees share
	// the main repo's <root>/.git, so the common dir's basename is still
	// ".git" and its parent is the main working tree.
	if filepath.Base(commonDir) == ".git" {
		return resolveMainRoot(filepath.Dir(commonDir))
	}

	// Branch 2: detached git dir (absorbed submodule). Ask the git dir
	// itself for its core.worktree, which git writes when it absorbs a
	// submodule's git dir. The value is typically relative (e.g.
	// "../../../sub"); resolve it against the common dir.
	worktreeCmd := exec.Command("git", "--git-dir", commonDir, "config", "--get", "core.worktree")
	winproc.Harden(worktreeCmd)
	if worktreeOut, err := worktreeCmd.Output(); err == nil {
		worktree := strings.TrimSpace(string(worktreeOut))
		if worktree != "" {
			if !filepath.IsAbs(worktree) {
				worktree = filepath.Join(commonDir, worktree)
			}
			return resolveMainRoot(worktree)
		}
	}

	// Branch 3: exotic GIT_DIR without a usable core.worktree. Defer to
	// `git rev-parse --show-toplevel` from the original path.
	topCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	topCmd.Dir = abs
	winproc.Harden(topCmd)
	topOut, err := topCmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", abs)
	}
	return resolveMainRoot(strings.TrimSpace(string(topOut)))
}

func resolveMainRoot(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path, nil
	}
	return resolved, nil
}

func Diff(ctx context.Context, dir, base, head string) (string, error) {
	return Run(ctx, dir, "diff", base+".."+head)
}

func DiffNameOnly(ctx context.Context, dir, base, head string) ([]string, error) {
	out, err := Run(ctx, dir, "diff", "--name-only", base+".."+head)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			files = append(files, trimmed)
		}
	}
	return files, nil
}

func DiffStat(ctx context.Context, dir, base, head string) (files, lines int, err error) {
	out, err := Run(ctx, dir, "diff", "--numstat", base+".."+head)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		files++
		added, aerr := strconv.Atoi(fields[0])
		deleted, derr := strconv.Atoi(fields[1])
		if aerr == nil {
			lines += added
		}
		if derr == nil {
			lines += deleted
		}
	}
	return files, lines, nil
}

func CommitTime(ctx context.Context, dir, sha string) (time.Time, error) {
	out, err := Run(ctx, dir, "show", "-s", "--format=%ct", sha)
	if err != nil {
		return time.Time{}, err
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse commit time %q: %w", out, err)
	}
	return time.Unix(secs, 0).UTC(), nil
}

func CommitAuthorEmail(ctx context.Context, dir, sha string) (string, error) {
	return Run(ctx, dir, "show", "-s", "--format=%ae", sha)
}

func DiffHead(ctx context.Context, dir string) (string, error) {
	return Run(ctx, dir, "diff", "HEAD")
}

func Log(ctx context.Context, dir, base, head string) (string, error) {
	return Run(ctx, dir, "log", "--oneline", base+".."+head)
}

func HeadSHA(ctx context.Context, dir string) (string, error) {
	return Run(ctx, dir, "rev-parse", "HEAD")
}

func CurrentBranch(ctx context.Context, dir string) (string, error) {
	return Run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

func IsDetachedHEAD(ctx context.Context, dir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "-q", "HEAD")
	cmd.Dir = dir
	winproc.Harden(cmd)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			// Exit 1 means HEAD is not a symbolic ref — detached.
			if ee.ExitCode() == 1 {
				return true, nil
			}
		}
		return false, fmt.Errorf("git symbolic-ref: %w", err)
	}
	return false, nil
}

func DefaultBranch(ctx context.Context, dir, remote string) string {
	out, err := Run(ctx, dir, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "main"
	}
	// Output format: "ref: refs/heads/main\tHEAD\n<sha>\tHEAD\n"
	// Fields splits: ["ref:", "refs/heads/main", "HEAD"]
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "ref: refs/heads/") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return strings.TrimPrefix(parts[1], "refs/heads/")
			}
		}
	}
	return "main"
}

func FetchRemoteBranch(ctx context.Context, dir, remote, branch string) error {
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, remote, branch)
	_, err := Run(ctx, dir, "fetch", "--no-tags", remote, refspec)
	return err
}

func FetchRemoteBranchToRef(ctx context.Context, dir, remote, branch, localRef string) error {
	refspec := fmt.Sprintf("+refs/heads/%s:%s", branch, localRef)
	_, err := Run(ctx, dir, "fetch", "--no-tags", remote, refspec)
	return err
}

func FetchRemoteBranchToPrivateRef(ctx context.Context, dir, remote, branch, localRef string) error {
	refspec := fmt.Sprintf("+refs/heads/%s:%s", branch, localRef)
	_, err := Run(ctx, dir, "fetch", "--no-tags", "--no-write-fetch-head", "--refmap=", remote, refspec)
	return err
}

func FetchRemoteRef(ctx context.Context, dir, remote, remoteRef, expectedCommit string) error {
	temporaryRef := fmt.Sprintf("refs/no-mistakes/fetch/%d-%d", os.Getpid(), time.Now().UnixNano())
	defer func() {
		_, _ = Run(context.WithoutCancel(ctx), dir, "update-ref", "--no-deref", "-d", temporaryRef)
	}()
	refspec := fmt.Sprintf("+%s:%s", remoteRef, temporaryRef)
	if _, err := Run(ctx, dir, "fetch", "--no-tags", "--no-write-fetch-head", remote, refspec); err != nil {
		return err
	}
	fetched, err := Run(ctx, dir, "rev-parse", "--verify", temporaryRef+"^{commit}")
	if err != nil {
		return fmt.Errorf("fetched ref %s is not a commit: %w", remoteRef, err)
	}
	if fetched != strings.TrimSpace(expectedCommit) {
		return fmt.Errorf("fetched ref %s moved to %s, expected %s", remoteRef, fetched, expectedCommit)
	}
	return nil
}

func Push(ctx context.Context, dir, remote, ref, expectedSHA string, forceWithLease bool) error {
	return PushWithOptions(ctx, dir, remote, ref, expectedSHA, forceWithLease, nil)
}

func PushCommit(ctx context.Context, dir, remote, commitSHA, ref, expectedSHA string, forceWithLease bool) error {
	return pushSourceWithOptions(ctx, dir, remote, commitSHA, ref, expectedSHA, forceWithLease, nil, false)
}

func PushCommitWithOptions(ctx context.Context, dir, remote, commitSHA, ref, expectedSHA string, forceWithLease bool, pushOptions []string) error {
	return pushSourceWithOptions(ctx, dir, remote, commitSHA, ref, expectedSHA, forceWithLease, pushOptions, false)
}

func PushCommitWithOptionsSkippingHooks(ctx context.Context, dir, remote, commitSHA, ref, expectedSHA string, forceWithLease bool, pushOptions []string) error {
	return pushSourceWithOptions(ctx, dir, remote, commitSHA, ref, expectedSHA, forceWithLease, pushOptions, true)
}

func PushWithOptions(ctx context.Context, dir, remote, ref, expectedSHA string, forceWithLease bool, pushOptions []string) error {
	return pushSourceWithOptions(ctx, dir, remote, "HEAD", ref, expectedSHA, forceWithLease, pushOptions, false)
}

func PushWithOptionsSkippingHooks(ctx context.Context, dir, remote, ref, expectedSHA string, forceWithLease bool, pushOptions []string) error {
	return pushSourceWithOptions(ctx, dir, remote, "HEAD", ref, expectedSHA, forceWithLease, pushOptions, true)
}

func pushSourceWithOptions(ctx context.Context, dir, remote, source, ref, expectedSHA string, forceWithLease bool, pushOptions []string, skipHooks bool) error {
	// On an up-to-date push, send-pack sends no ref update but still writes the
	// push options and a closing flush; receive-pack exits on the empty update
	// list without reading them, so that write can kill git with SIGPIPE. Git
	// reports up-to-date before any lease check and runs no hook, so skipping
	// the push gives the same result without the race.
	if len(pushOptions) > 0 && remoteRefAt(ctx, dir, remote, ref, source) {
		return nil
	}
	args := []string{"push"}
	if skipHooks {
		args = append(args, "--no-verify")
	}
	for _, option := range pushOptions {
		args = append(args, "-o", option)
	}
	args = append(args, remote)
	if forceWithLease {
		if expectedSHA != "" {
			args = append(args, fmt.Sprintf("--force-with-lease=%s:%s", ref, expectedSHA))
		} else {
			args = append(args, "--force-with-lease")
		}
	}
	args = append(args, source+":"+ref)
	_, err := Run(ctx, dir, args...)
	return err
}

func remoteRefAt(ctx context.Context, dir, remote, ref, source string) bool {
	commit, err := Run(ctx, dir, "rev-parse", "--verify", source+"^{commit}")
	if err != nil {
		return false
	}
	out, err := Run(ctx, dir, "ls-remote", remote, ref)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if sha, name, ok := strings.Cut(line, "\t"); ok && name == ref {
			return sha == commit
		}
	}
	return false
}

func LsRemote(ctx context.Context, dir, remote, ref string) (string, error) {
	out, err := Run(ctx, dir, "ls-remote", remote, ref)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", nil
	}
	// Output format: "<sha>\t<ref>"
	parts := strings.Fields(out)
	if len(parts) < 1 {
		return "", nil
	}
	return parts[0], nil
}

func HasUncommittedChanges(ctx context.Context, dir string) (bool, error) {
	out, err := Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func UntrackedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := RunRaw(ctx, dir, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range strings.Split(string(out), "\x00") {
		if len(entry) < 3 {
			continue
		}
		// Porcelain format: XY <path>\0 where XY is a 2-char status code + space.
		if entry[:2] == "??" {
			files = append(files, entry[3:])
		}
	}
	return files, nil
}

func CreateBranch(ctx context.Context, dir, name string) error {
	_, err := Run(ctx, dir, "checkout", "-b", name)
	return err
}

func CommitAll(ctx context.Context, dir, message string) error {
	if _, err := Run(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	dirty, err := HasUncommittedChanges(ctx, dir)
	if err != nil {
		return err
	}
	if !dirty {
		return fmt.Errorf("no changes to commit")
	}
	_, err = Run(ctx, dir, "commit", "-m", message)
	return err
}

func CopyLocalUserIdentity(ctx context.Context, dstDir string, identity CommitIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	values := map[string]string{
		"user.name": identity.CommitterName, "user.email": identity.CommitterEmail,
		"author.name": identity.AuthorName, "author.email": identity.AuthorEmail,
		"committer.name": identity.CommitterName, "committer.email": identity.CommitterEmail,
	}
	isolated, err := Run(ctx, dstDir, "config", "--local", "--type=bool", "--get", "--default", "false", "extensions.worktreeConfig")
	if err != nil {
		return err
	}
	scope := []string{"--local"}
	if isolated == "true" {
		path, err := Run(ctx, dstDir, "rev-parse", "--git-path", "config.worktree")
		if err != nil {
			return err
		}
		scope = []string{"--file", path}
	}
	for key, value := range values {
		args := append([]string{"config"}, scope...)
		if _, err := Run(ctx, dstDir, append(args, key, value)...); err != nil {
			return err
		}
	}
	return nil
}

func WorktreeAdd(ctx context.Context, repoDir, wtPath, sha string) error {
	_, err := Run(ctx, repoDir, "worktree", "add", "--detach", wtPath, sha)
	return err
}

func WorktreeRemove(ctx context.Context, repoDir, wtPath string) error {
	_, err := Run(ctx, repoDir, "worktree", "remove", "--force", wtPath)
	return err
}

func ResolveRef(ctx context.Context, dir, ref string) (string, error) {
	out, err := Run(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve ref %s: %w", ref, err)
	}
	return out, nil
}

func DirectRefTarget(ctx context.Context, dir, ref string) (string, bool, error) {
	target, err := Run(ctx, dir, "symbolic-ref", "--quiet", "--no-recurse", ref)
	if err == nil {
		return "", false, fmt.Errorf("ref %s is symbolic (target %s)", ref, target)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return "", false, err
	}
	out, err := Run(ctx, dir, "for-each-ref", "--format=%(refname) %(objectname) %(symref)", ref)
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != ref {
			continue
		}
		if len(fields) > 2 {
			return "", false, fmt.Errorf("ref %s is symbolic (target %s)", ref, fields[2])
		}
		if len(fields) != 2 {
			return "", false, fmt.Errorf("ref %s has no direct object target", ref)
		}
		return fields[1], true, nil
	}
	return "", false, nil
}

func ExactRefTarget(ctx context.Context, dir, ref string) (string, bool, error) {
	out, err := Run(ctx, dir, "for-each-ref", "--format=%(refname) %(objectname)", ref)
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		name, target, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && name == ref {
			return strings.TrimSpace(target), true, nil
		}
	}
	return "", false, nil
}

func RefExists(ctx context.Context, dir, ref string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	cmd.Env = nonInteractiveEnvForContext(ctx, dir)
	winproc.Harden(cmd)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return true, nil
}

func ShowFile(ctx context.Context, dir, ref, path string) (string, error) {
	out, err := Run(ctx, dir, "show", fmt.Sprintf("%s:%s", ref, path))
	if err != nil {
		return "", err
	}
	return out, nil
}
