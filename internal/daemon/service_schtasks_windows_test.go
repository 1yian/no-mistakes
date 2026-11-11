package daemon

import (
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

func TestInstallWindowsTaskRegistersUserScopedLogonTask(t *testing.T) {
	if testing.Short() {
		t.Skip("registers a real scheduled task")
	}
	if _, err := exec.LookPath("schtasks"); err != nil {
		t.Skipf("schtasks unavailable: %v", err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}

	p := paths.WithRoot(filepath.Join(t.TempDir(), "nm home"))
	taskName := windowsTaskName(p)
	t.Cleanup(func() { _, _ = runServiceCommand("schtasks", "/Delete", "/TN", taskName, "/F") })

	exe := filepath.Join(t.TempDir(), "missing", "no-mistakes.exe")
	if err := installWindowsTask(p, exe); err != nil {
		t.Fatalf("installWindowsTask: %v", err)
	}

	exported, err := runServiceCommand("schtasks", "/Query", "/TN", taskName, "/XML")
	if err != nil {
		t.Fatalf("query registered task: %v", err)
	}
	task := parseWindowsTaskDefinition(t, string(exported))
	assertWindowsTaskAllowsLongRunningOnBattery(t, task)
	if len(task.LogonTriggers) != 1 {
		t.Fatalf("logon triggers = %d, want 1\n%s", len(task.LogonTriggers), exported)
	}
	if got := task.LogonTriggers[0].UserID; !strings.EqualFold(got, current.Username) && !strings.EqualFold(got, current.Uid) {
		t.Fatalf("logon trigger user = %q, want current user %q (%s)", got, current.Username, current.Uid)
	}
	if task.Principal.LogonType != "InteractiveToken" {
		t.Fatalf("principal logon type = %q, want InteractiveToken", task.Principal.LogonType)
	}
	if task.Principal.RunLevel != "" && task.Principal.RunLevel != "LeastPrivilege" {
		t.Fatalf("principal run level = %q, want limited", task.Principal.RunLevel)
	}
	if !serviceDefinitionMatchesRoot(exported, p) {
		t.Fatalf("registered task does not match its root\n%s", exported)
	}
}
