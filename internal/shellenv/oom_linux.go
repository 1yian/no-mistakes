//go:build linux

package shellenv

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

var ErrOutOfMemory = errors.New("ran out of memory")

const stepOOMScoreAdj = "1000"

func OwnOOMScoreScript(script string) string {
	return ownOOMScoreScript("/proc/self/oom_score_adj", script)
}

func ownOOMScoreScript(scorePath, script string) string {
	return "{ printf '%s\\n' " + stepOOMScoreAdj + " > " + scorePath + "; } 2>/dev/null; " + script
}

func RaiseStepOOMScore(pid int) {
	if pid <= 0 {
		return
	}
	_ = os.WriteFile("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", []byte(stepOOMScoreAdj+"\n"), 0o644)
}

func OOMKillBaseline() (uint64, bool) {
	return oomKillCount()
}

func AttributeOOMKill(baseline uint64, haveBaseline bool, err error) error {
	if err == nil || !haveBaseline || !killedBySIGKILL(err) {
		return err
	}
	after, ok := oomKillCount()
	if !ok || after <= baseline {
		return err
	}
	return ErrOutOfMemory
}

func killedBySIGKILL(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee == nil || ee.ProcessState == nil {
		return false
	}
	if ee.ExitCode() == 137 {
		return true
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

func oomKillCount() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, false
	}
	var rel string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// cgroup v2: "0::/path"
		_, rest, ok := strings.Cut(line, "::")
		if !ok || rest == "" {
			return 0, false
		}
		rel = rest
		break
	}
	if rel == "" {
		return 0, false
	}
	events, err := os.ReadFile("/sys/fs/cgroup" + rel + "/memory.events")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(events), "\n") {
		key, val, ok := strings.Cut(line, " ")
		if !ok || key != "oom_kill" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
