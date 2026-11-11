//go:build unix

package shellenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultWaitDelay = 5 * time.Second

const terminateGrace = 3 * time.Second

func ConfigureShellCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Install the WaitDelay backstop unless the caller picked one explicitly
	// (the short login-shell probe uses a tighter bound of its own).
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = defaultWaitDelay
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		pgid := cmd.Process.Pid
		err := syscall.Kill(-pgid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return err
		}
		// Escalate out of band. cmd.Cancel runs on the goroutine that owns
		// cmd.Wait, so blocking here for the grace period would delay every
		// cancellation by that long even when the group exits immediately.
		go func() {
			if groupGoneWithin(pgid, terminateGrace) {
				return
			}
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}()
		return nil
	}
}

func ConfigureCooperativeShellCommand(cmd *exec.Cmd) {
	ConfigureShellCommand(cmd)
}

func StartShellCommand(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	// The parent raises the child's score (inherited by grandchildren) so a later
	// cgroup OOM prefers this step over the daemon. Linux only; a no-op elsewhere.
	if cmd.Process != nil {
		RaiseStepOOMScore(cmd.Process.Pid)
	}
	return nil
}

func TerminateShellCommandGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Negative PID targets the whole group (Setpgid made the leader's PID the
	// group ID). errors.Is(ESRCH) is the expected, benign "no survivors" case.
	pgid := cmd.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return
	}
	if groupGoneWithin(pgid, terminateGrace) {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func ShellCommandGroupActive(cmd *exec.Cmd, window time.Duration) (active bool, detail string, err error) {
	return shellCommandGroupActive(cmd, window, func(ctx context.Context) ([]byte, error) {
		return shellCommandGroupPS(ctx, cmd.Process.Pid)
	})
}

func shellCommandGroupActive(cmd *exec.Cmd, window time.Duration, ps func(context.Context) ([]byte, error)) (active bool, detail string, err error) {
	if cmd == nil || cmd.Process == nil {
		return false, "", errors.New("command was not started")
	}
	deadline := time.Now().Add(window)
	var lastMembers []groupMember
	for first := true; ; first = false {
		timeout := min(defaultWaitDelay, time.Until(deadline))
		if first && timeout <= 0 {
			// A zero-window call still needs one verifiable snapshot.
			timeout = 100 * time.Millisecond
		}
		live, members, err := probeShellCommandGroup(cmd.Process.Pid, timeout, ps)
		if len(members) > 0 {
			lastMembers = members
		}
		if err != nil {
			return live, formatGroupMembers(lastMembers), err
		}
		if !live || !time.Now().Before(deadline) {
			return live, formatGroupMembers(members), nil
		}
		time.Sleep(min(groupProbeInterval, time.Until(deadline)))
		if !time.Now().Before(deadline) {
			return live, formatGroupMembers(members), nil
		}
	}
}

func probeShellCommandGroup(pgid int, timeout time.Duration, ps func(context.Context) ([]byte, error)) (bool, []groupMember, error) {
	err := syscall.Kill(-pgid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil, nil
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false, nil, err
	}
	// A zombie still occupies its process group but has finished execution.
	// Linux's signal probe succeeds for it; macOS can return EPERM instead.
	// Inspect states with the same portable ps fields used by procreap.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := ps(ctx)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return false, nil, fmt.Errorf("inspect process group states: %w", err)
	}
	var members []groupMember
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			return false, nil, fmt.Errorf("invalid process group state: %q", line)
		}
		memberPGID, err := strconv.Atoi(fields[0])
		if err != nil {
			return false, nil, fmt.Errorf("parse process group: %w", err)
		}
		memberPID, err := strconv.Atoi(fields[2])
		if err != nil {
			return false, nil, fmt.Errorf("parse process id: %w", err)
		}
		if memberPGID == pgid && !strings.HasPrefix(fields[1], "Z") {
			members = append(members, groupMember{pid: memberPID, command: strings.Join(fields[3:], " ")})
		}
	}
	return len(members) > 0, members, nil
}

func shellCommandGroupPS(ctx context.Context, pgid int) ([]byte, error) {
	args := []string{"-eo", "pgid=,stat=,pid=,command="}
	if runtime.GOOS == "darwin" {
		// Reading every host process's command can stall on unrelated
		// processes and exhaust the completion window after our children
		// already exited. Darwin can select the group before reading commands.
		// Its legacy mode ignores -g, so explicitly select POSIX semantics.
		args = []string{"-g", strconv.Itoa(pgid), "-o", "pgid=,stat=,pid=,command="}
	}
	probe := exec.CommandContext(ctx, "ps", args...)
	if runtime.GOOS == "darwin" {
		probe.Env = append(os.Environ(), "COMMAND_MODE=unix2003")
	}
	ConfigureShellCommand(probe)
	out, err := OutputShellCommand(probe)
	var exitErr *exec.ExitError
	if runtime.GOOS == "darwin" && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(strings.TrimSpace(string(out))) == 0 {
		// A group may empty between kill(0) and ps. Darwin returns 1 for
		// no matches. Confirm absence independently before accepting that
		// exit, since inspection errors must still refuse completion.
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return out, nil
		}
	}
	return out, err
}

func groupGoneWithin(pgid int, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func detachFromTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
