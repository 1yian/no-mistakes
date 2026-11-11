//go:build linux

package memscopetest

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func RequireDelegatedMemoryLimit(t *testing.T, mib int) {
	t.Helper()
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip(err)
	}
	probe := exec.Command("systemd-run", "--user", "--scope", "--quiet",
		"-p", "MemoryMax="+strconv.Itoa(mib)+"M",
		"-p", "MemorySwapMax=0",
		"--",
		"sh", "-c", `cat "/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)/memory.max"`)
	out, err := probe.CombinedOutput()
	if err != nil {
		t.Skipf("no usable systemd user scope with memory.max: %v\n%s", err, out)
	}
	want := strconv.Itoa(mib * 1024 * 1024)
	if got := strings.TrimSpace(string(out)); got != want {
		t.Skipf("scope memory.max = %q, want %s", got, want)
	}
}
