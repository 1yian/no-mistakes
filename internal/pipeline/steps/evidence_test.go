package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

func TestTestEvidenceDir_ReadsTheExecutorResolvedDirectory(t *testing.T) {
	t.Parallel()
	want := filepath.Join(t.TempDir(), "evidence", "run-123")
	got := testEvidenceDir(&pipeline.StepContext{EvidenceDir: want})
	if got != want {
		t.Errorf("evidence dir = %q, want %q", got, want)
	}
}

func TestTestEvidenceDir_DefaultResolutionStaysUnderTheAppRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NM_HOME", root)
	p, err := paths.New()
	if err != nil {
		t.Fatal(err)
	}

	got := testEvidenceDir(&pipeline.StepContext{EvidenceDir: p.RunEvidenceDir("", "run-123")})

	want := filepath.Join(root, "evidence", "run-123")
	if got != want {
		t.Errorf("evidence dir = %q, want %q", got, want)
	}
	legacy := filepath.Join(os.TempDir(), "no-mistakes-evidence")
	if strings.HasPrefix(got, legacy) {
		t.Errorf("evidence dir %q is still under the legacy shared temp root %q", got, legacy)
	}
}

func TestEvidenceBranchSlug_KeepsBranchStructureAndDropsUnsafeSegments(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"feature/add-login":  "feature/add-login",
		"../../etc/pa ss~wd": "etc/pa-ss-wd",
		"///":                "",
	}
	for branch, want := range cases {
		got := strings.Join(evidenceBranchSlug(branch), "/")
		if got != want {
			t.Errorf("slug(%q) = %q, want %q", branch, got, want)
		}
	}
}
