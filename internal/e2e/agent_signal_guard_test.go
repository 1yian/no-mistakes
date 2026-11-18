//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
	"gopkg.in/yaml.v3"
)

type guardVictim struct {
	name string
	pid  int
	pgid int
	dead <-chan struct{}
}

func (v guardVictim) pattern() string {
	return "[" + v.name[:1] + "]" + v.name[1:]
}

func (v guardVictim) alive() bool {
	select {
	case <-v.dead:
		return false
	default:
		return true
	}
}

func startGuardVictim(t *testing.T) guardVictim {
	t.Helper()
	dir := t.TempDir()
	name := fmt.Sprintf("nmguard-%06x", rand.Intn(1<<24))
	victimBin := filepath.Join(dir, name)
	if err := os.WriteFile(victimBin, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	cmd := exec.Command(victimBin)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	dead := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(dead)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-dead
	})
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("victim pgid: %v", err)
	}
	return guardVictim{name: name, pid: cmd.Process.Pid, pgid: pgid, dead: dead}
}

func useRealSignalTools(t *testing.T) {
	t.Helper()
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		head, err := os.ReadFile(filepath.Join(dir, "pkill"))
		if err == nil && strings.HasPrefix(string(head), "#!") {
			continue
		}
		kept = append(kept, dir)
	}
	t.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator)))
}

func guardScenario(t *testing.T, commands []string) string {
	t.Helper()
	structured := func(extra map[string]any) map[string]any {
		out := map[string]any{
			"findings":       []any{},
			"summary":        "no issues found",
			"risk_level":     "low",
			"risk_rationale": "no risks detected in the diff",
			"risk_scope":     "source-or-external",
		}
		for key, value := range extra {
			out[key] = value
		}
		return out
	}
	scenario := map[string]any{"actions": []any{
		map[string]any{
			"match":      "Review the code changes and return structured findings",
			"text":       "no issues found",
			"shell":      commands,
			"structured": structured(nil),
		},
		map[string]any{
			"text": "no issues found",
			"structured": structured(map[string]any{
				"tested":          []string{"fakeagent: simulated test run"},
				"testing_summary": "simulated tests passed",
				"scenarios": []any{map[string]any{
					"name":     "fakeagent: simulated end-to-end scenario",
					"result":   "pass",
					"live":     true,
					"evidence": "fakeagent: simulated test run",
					"reason":   "",
				}},
				"verdict":   "go",
				"artifacts": []any{},
				"title":     "feat: fakeagent change",
				"body":      "## Summary\nfakeagent canned PR body",
			}),
		},
	}}
	data, err := yaml.Marshal(scenario)
	if err != nil {
		t.Fatalf("marshal scenario: %v", err)
	}
	path := filepath.Join(t.TempDir(), "guard-scenario.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

type guardShellResult struct {
	Command string `json:"command"`
	Exit    int    `json:"exit"`
	Output  string `json:"output"`
}

func readGuardShellResults(t *testing.T, path string) map[string]guardShellResult {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open shell log: %v", err)
	}
	defer file.Close()
	results := map[string]guardShellResult{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var result guardShellResult
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			t.Fatalf("parse shell log line %q: %v", scanner.Text(), err)
		}
		results[result.Command] = result
	}
	return results
}

func initGuardRepo(t *testing.T, h *Harness) {
	t.Helper()
	h.CommitChange("init-guard", "seed.txt", "seed\n", "seed guard init")
	initWorktree := h.AddWorktree("init-guard")
	if out, err := h.RunInDir(initWorktree, "init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
}

func requireGuardRefusal(t *testing.T, results map[string]guardShellResult, command string, wants ...string) {
	t.Helper()
	result, ok := results[command]
	if !ok {
		t.Fatalf("agent never ran %q; results: %+v", command, results)
	}
	if result.Exit == 0 {
		t.Fatalf("%q was allowed (exit 0), want refusal; output: %s", command, result.Output)
	}
	for _, want := range wants {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("%q output missing %q:\n%s", command, want, result.Output)
		}
	}
	t.Logf("refused: %s\n  exit=%d\n  %s", command, result.Exit, strings.ReplaceAll(strings.TrimSpace(result.Output), "\n", "\n  "))
}

func TestAgentSignalGuardRefusesNameWideSignalsAndAllowsOwnProcesses(t *testing.T) {
	victim := startGuardVictim(t)
	scratch := t.TempDir()
	orphanPID := filepath.Join(scratch, "orphan.pid")
	shellLog := filepath.Join(scratch, "shell.log")

	commands := []string{
		"pkill -f " + victim.pattern(),
		"killall " + victim.name,
		"pgrep -f " + victim.pattern() + " | xargs kill -TERM",
		"kill -TERM $(pgrep -f " + victim.pattern() + ")",
		"kill -TERM " + strconv.Itoa(victim.pid),
		"kill -CONT -- -1",
		"kill -CONT -- -" + strconv.Itoa(victim.pgid),
		"sleep 300 & p=$!; kill -TERM $p; wait $p; echo own-exit=$?",
		"sleep 300 & echo $! | xargs kill -TERM; wait $!; echo own-xargs-exit=$?",
		"setsid sleep 300 >/dev/null 2>&1 & echo $! > " + orphanPID,
		"kill -TERM $(cat " + orphanPID + ")",
	}
	t.Setenv("FAKEAGENT_SHELL_LOG", shellLog)
	h := NewHarness(t, SetupOpts{Agent: "pi", Scenario: guardScenario(t, commands)})
	initGuardRepo(t, h)

	h.CommitChange("feature/guard", "feature.txt", "value\n", "add feature")
	h.PushToGate("feature/guard")
	run := h.WaitForRun("feature/guard", 90*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run did not complete: status=%s error=%v", run.Status, deref(run.Error))
	}

	results := readGuardShellResults(t, shellLog)
	pid := strconv.Itoa(victim.pid)
	pgid := strconv.Itoa(victim.pgid)
	requireGuardRefusal(t, results, commands[0], "pkill: refused by no-mistakes")
	requireGuardRefusal(t, results, commands[1], "killall: refused by no-mistakes")
	requireGuardRefusal(t, results, commands[2], "kill: refused by no-mistakes: pid "+pid+" is not a process of this run")
	requireGuardRefusal(t, results, commands[3], "kill: refused by no-mistakes: pid "+pid+" is not a process of this run")
	requireGuardRefusal(t, results, commands[4], "kill: refused by no-mistakes: pid "+pid+" is not a process of this run")
	requireGuardRefusal(t, results, commands[5], "pid -1 would signal every process of this account")
	requireGuardRefusal(t, results, commands[6], "process group "+pgid+" includes pid")

	for _, own := range []struct{ command, want string }{
		{commands[7], "own-exit=143"},
		{commands[8], "own-xargs-exit=143"},
		{commands[10], ""},
	} {
		result, ok := results[own.command]
		if !ok {
			t.Fatalf("agent never ran %q", own.command)
		}
		if result.Exit != 0 || strings.Contains(result.Output, "refused") || !strings.Contains(result.Output, own.want) {
			t.Fatalf("own-process command %q was refused or failed (exit %d):\n%s", own.command, result.Exit, result.Output)
		}
		t.Logf("allowed: %s\n  exit=%d output=%q", own.command, result.Exit, strings.TrimSpace(result.Output))
	}

	if !victim.alive() {
		t.Fatalf("another run's process (pid %d) was terminated by the agent's name-wide signals", victim.pid)
	}
}

func TestAgentSignalGuardOffRestoresNameWideSignals(t *testing.T) {
	useRealSignalTools(t)
	victim := startGuardVictim(t)
	shellLog := filepath.Join(t.TempDir(), "shell.log")
	command := "pkill -f " + victim.pattern()
	t.Setenv("FAKEAGENT_SHELL_LOG", shellLog)
	h := NewHarness(t, SetupOpts{
		Agent:             "pi",
		Scenario:          guardScenario(t, []string{command}),
		GlobalConfigExtra: "agent_signal_guard: false",
	})
	initGuardRepo(t, h)

	h.CommitChange("feature/guard-off", "feature.txt", "value\n", "add feature")
	h.PushToGate("feature/guard-off")
	run := h.WaitForRun("feature/guard-off", 90*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run did not complete: status=%s error=%v", run.Status, deref(run.Error))
	}

	results := readGuardShellResults(t, shellLog)
	if result, ok := results[command]; !ok || result.Exit != 0 {
		t.Fatalf("pkill with the guard off should run unrefused, got %+v", result)
	}
	select {
	case <-victim.dead:
	case <-time.After(5 * time.Second):
		t.Fatalf("pkill with agent_signal_guard: false should have ended the victim (pid %d)", victim.pid)
	}
}

func TestAgentSignalGuardKeepsRunCancellationReachingTheAgentTree(t *testing.T) {
	scratch := t.TempDir()
	pidFile := filepath.Join(scratch, "agent-child.pid")
	command := "echo $$ > " + pidFile + "; exec sleep 300"
	t.Setenv("FAKEAGENT_SHELL_LOG", filepath.Join(scratch, "shell.log"))
	h := NewHarness(t, SetupOpts{Agent: "pi", Scenario: guardScenario(t, []string{command})})
	initGuardRepo(t, h)

	h.CommitChange("feature/guard-cancel", "feature.txt", "value\n", "add feature")
	h.PushToGate("feature/guard-cancel")
	active := h.WaitForRunRunning("feature/guard-cancel", 60*time.Second)

	var childPID int
	deadline := time.Now().Add(60 * time.Second)
	for childPID == 0 && time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatalf("the agent's command never started")
	}
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })

	h.CancelRun(active.ID)
	run := h.WaitForRun("feature/guard-cancel", 60*time.Second)
	if run.Status != types.RunCancelled {
		t.Fatalf("run status = %s, want cancelled", run.Status)
	}
	for end := time.Now().Add(15 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if err := syscall.Kill(childPID, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("cancelling the run left the agent's process %d running", childPID)
		}
	}
}
