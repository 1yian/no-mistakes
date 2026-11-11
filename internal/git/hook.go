package git

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var runGit = RunBare

const gateConfigStampFile = "no-mistakes-gate-config"
const preservedPreReceiveHook = "pre-receive.no-mistakes-user"

func PreReceiveHookScript() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "no-mistakes"
	}
	return preReceiveHookScript(exe)
}

func preReceiveHookScript(command string) string {
	return `#!/bin/sh
# no-mistakes pre-receive hook
# Authorize the pushing process before any managed gate ref changes.
NM_BIN=` + shellSingleQuote(command) + `
if [ ! -f "$NM_BIN" ]; then
  NM_BIN="$(command -v no-mistakes 2>/dev/null || echo no-mistakes)"
fi
GATE_DIR=$(git rev-parse --absolute-git-dir 2>/dev/null || :)
case "$GATE_DIR" in
  /*) ;;
  *)
    HOOK_PATH=$0
    case "$HOOK_PATH" in
      */*) HOOK_DIR=${HOOK_PATH%/*} ;;
      *) HOOK_DIR=. ;;
    esac
    GATE_DIR=$(cd "$HOOK_DIR/.." 2>/dev/null && (/bin/pwd -P 2>/dev/null || pwd -P) || :)
    ;;
esac
out=$(NM_HOOK_HELPER=1 "$NM_BIN" daemon admit-push --gate "$GATE_DIR" 2>&1)
status=$?
if [ $status -ne 0 ]; then
  printf 'no-mistakes: gate push refused before ref mutation:\n%s\n' "$out" >&2
  exit $status
fi
USER_HOOK="$GATE_DIR/hooks/` + preservedPreReceiveHook + `"
if [ -x "$USER_HOOK" ]; then
  exec "$USER_HOOK"
fi
exit 0
`
}

func isManagedPreReceiveHook(content []byte) bool {
	text := string(content)
	return strings.Contains(text, "# no-mistakes pre-receive hook") && strings.Contains(text, "daemon admit-push")
}

func RefreshManagedPreReceiveHook(bareDir string) (bool, error) {
	hooksDir := filepath.Join(bareDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return false, err
	}
	hookPath := filepath.Join(hooksDir, "pre-receive")
	companion := filepath.Join(hooksDir, preservedPreReceiveHook)
	desired := []byte(PreReceiveHookScript())
	existing, err := os.ReadFile(hookPath)
	if err == nil {
		if string(existing) == string(desired) {
			return false, nil
		}
		if !isManagedPreReceiveHook(existing) {
			if _, companionErr := os.Stat(companion); companionErr == nil {
				return false, fmt.Errorf("preserve pre-receive hook: companion already exists")
			} else if !os.IsNotExist(companionErr) {
				return false, companionErr
			}
			if err := os.Rename(hookPath, companion); err != nil {
				return false, fmt.Errorf("preserve pre-receive hook: %w", err)
			}
			if err := writeGateFileAtomic(hookPath, desired, 0o755, ".pre-receive-*"); err != nil {
				_ = os.Rename(companion, hookPath)
				return false, err
			}
			return true, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := writeGateFileAtomic(hookPath, desired, 0o755, ".pre-receive-*"); err != nil {
		return false, err
	}
	return true, nil
}

func RefreshManagedGateHooks(bareDir string) error {
	if _, err := RefreshManagedPreReceiveHook(bareDir); err != nil {
		return err
	}
	if _, err := RefreshManagedPostReceiveHook(bareDir); err != nil {
		return err
	}
	return nil
}

func PostReceiveHookScript() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "no-mistakes"
	}
	return postReceiveHookScript(exe)
}

func postReceiveHookScript(command string) string {
	return `#!/bin/sh
# no-mistakes post-receive hook
# Notifies the daemon of the push. Non-blocking: post-receive exit code is
# ignored by git, so we never reject the push here. Instead, failures are
# surfaced on stderr (so the pushing client sees them) and appended to
# notify-push.log inside the bare repo for later inspection.
NM_BIN=` + shellSingleQuote(command) + `
if [ ! -f "$NM_BIN" ]; then
  NM_BIN="$(command -v no-mistakes 2>/dev/null || echo no-mistakes)"
fi
# Resolve the bare repo dir explicitly. Git can invoke this hook from a cwd
# whose pwd collapses to "." (issue #269), which would pass "--gate ." and be
# rejected by the daemon ("invalid gate path: ."), so the pipeline never
# starts. Prefer git's own absolute dir query (Git 2.13+, May 2017), then fall
# back to the hook file's location so a poisoned PWD still cannot produce ".".
GATE_DIR=$(git rev-parse --absolute-git-dir 2>/dev/null || :)
case "$GATE_DIR" in
  /*) ;;
  *)
    HOOK_PATH=$0
    case "$HOOK_PATH" in
      */*) HOOK_DIR=${HOOK_PATH%/*} ;;
      *) HOOK_DIR=. ;;
    esac
    GATE_DIR=$(cd "$HOOK_DIR/.." 2>/dev/null && (/bin/pwd -P 2>/dev/null || pwd -P) || :)
    ;;
esac
case "$GATE_DIR" in
  /*) ;;
  *) GATE_DIR=$(/bin/pwd -P 2>/dev/null || pwd -P 2>/dev/null || pwd) ;;
esac
LOG="$GATE_DIR/notify-push.log"
nm_ts() { date '+%Y-%m-%dT%H:%M:%S' 2>/dev/null || echo unknown; }
notify_failed=0
while read oldrev newrev refname; do
	  set -- --gate "$GATE_DIR" \
	    --ref "$refname" \
	    --old "$oldrev" \
	    --new "$newrev"
	  i=0
	  while [ "$i" -lt "${GIT_PUSH_OPTION_COUNT:-0}" ]; do
	    opt=$(printenv "GIT_PUSH_OPTION_$i" 2>/dev/null || :)
	    set -- "$@" --push-option "$opt"
	    i=$((i + 1))
	  done
	  out=$(NM_HOOK_HELPER=1 "$NM_BIN" daemon notify-push "$@" 2>&1)
  status=$?
  if [ $status -ne 0 ]; then
    notify_failed=1
    {
      printf '[%s] notify-push failed for %s (exit %d)\n' "$(nm_ts)" "$refname" "$status"
      printf '%s\n\n' "$out"
    } >> "$LOG"
    {
      printf 'no-mistakes: notify-push failed for %s (exit %d):\n' "$refname" "$status"
      printf '%s\n' "$out"
      printf 'See %s for full history.\n' "$LOG"
    } >&2
  fi
done

if [ "$notify_failed" -eq 0 ]; then
  cat >&2 <<'BANNER'
_  _ ____    _  _ _ ____ ___ ____ _  _ ____ ____
|\ | |  |    |\/| | [__   |  |__| |_/  |___ [__
| \| |__|    |  | | ___]  |  |  | | \_ |___ ___]

  * Pipeline started

  Run no-mistakes to review.

BANNER
fi
exit 0
`
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isManagedPostReceiveHook(content []byte) bool {
	text := string(content)
	return strings.Contains(text, "# no-mistakes post-receive hook") && strings.Contains(text, "daemon notify-push")
}

func InstallPostReceiveHook(bareDir string) error {
	hooksDir := filepath.Join(bareDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	hookPath := filepath.Join(hooksDir, "post-receive")
	return writeHookFileAtomic(hookPath, []byte(PostReceiveHookScript()))
}

func RefreshManagedPostReceiveHook(bareDir string) (bool, error) {
	hooksDir := filepath.Join(bareDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return false, err
	}
	hookPath := filepath.Join(hooksDir, "post-receive")
	desired := []byte(PostReceiveHookScript())
	existing, err := os.ReadFile(hookPath)
	if err == nil {
		if string(existing) == string(desired) {
			return false, nil
		}
		if !isManagedPostReceiveHook(existing) {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := writeHookFileAtomic(hookPath, desired); err != nil {
		return false, err
	}
	return true, nil
}

func writeHookFileAtomic(path string, content []byte) error {
	return writeGateFileAtomic(path, content, 0o755, ".post-receive-*")
}

func writeGateFileAtomic(path string, content []byte, mode os.FileMode, pattern string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func GateConfigCurrent(bareDir string) bool {
	content, err := os.ReadFile(filepath.Join(bareDir, gateConfigStampFile))
	if err != nil || string(content) != gateConfigStampContent() {
		return false
	}
	// Admission is a security boundary, not merely notification. Verify the
	// managed pre-receive bytes on every startup so a stale stamp cannot hide a
	// removed or replaced guard. This remains filesystem-only for current gates.
	preReceive, err := os.ReadFile(filepath.Join(bareDir, "hooks", "pre-receive"))
	return err == nil && string(preReceive) == PreReceiveHookScript()
}

func MarkGateConfigCurrent(bareDir string) error {
	return writeGateFileAtomic(
		filepath.Join(bareDir, gateConfigStampFile),
		[]byte(gateConfigStampContent()),
		0o644,
		".no-mistakes-gate-config-*",
	)
}

func gateConfigStampContent() string {
	sum := sha256.Sum256([]byte("gate-config-v2\x00" + PreReceiveHookScript() + "\x00" + PostReceiveHookScript()))
	return fmt.Sprintf("v2:%x\n", sum)
}

func IsolateHooksPath(ctx context.Context, bareDir string) error {
	_, err := EnsureHooksPathIsolation(ctx, bareDir)
	return err
}

func EnsureHooksPathIsolation(ctx context.Context, bareDir string) (bool, error) {
	verifiedDir, err := bareRepositoryIdentity(ctx, bareDir)
	if err != nil {
		return false, err
	}
	bareDir = verifiedDir
	if _, err := runGit(ctx, bareDir, "config", "--worktree", "--get", "core.hookspath"); err != nil {
		if isWorktreeConfigUnsupported(err) {
			return false, nil
		}
	}
	if _, err := runGit(ctx, bareDir, "config", "extensions.worktreeConfig", "true"); err != nil {
		return false, fmt.Errorf("enable worktree config: %w", err)
	}
	hooksDir, err := filepath.Abs(filepath.Join(bareDir, "hooks"))
	if err != nil {
		return false, fmt.Errorf("resolve hooks dir: %w", err)
	}
	if _, err := runGit(ctx, bareDir, "config", "--worktree", "core.hookspath", hooksDir); err != nil {
		if isWorktreeConfigUnsupported(err) {
			return false, nil
		}
		return false, fmt.Errorf("pin core.hookspath per-worktree: %w", err)
	}
	if err := relocateCoreBareToWorktreeScope(ctx, bareDir); err != nil {
		return false, err
	}
	return true, nil
}

func relocateCoreBareToWorktreeScope(ctx context.Context, bareDir string) error {
	if _, err := runGit(ctx, bareDir, "config", "--worktree", "core.bare", "true"); err != nil {
		if isWorktreeConfigUnsupported(err) {
			return nil
		}
		return fmt.Errorf("pin core.bare per-worktree: %w", err)
	}
	if _, err := runGit(ctx, bareDir, "config", "--local", "--unset", "core.bare"); err != nil {
		if isConfigKeyMissing(err) {
			return nil
		}
		return fmt.Errorf("unset shared core.bare: %w", err)
	}
	return nil
}

func isConfigKeyMissing(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 5
}

func isWorktreeConfigUnsupported(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "unknown option") && strings.Contains(msg, "worktree")
}
