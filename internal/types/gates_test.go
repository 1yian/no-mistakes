package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomGateStepName_RoundTrips(t *testing.T) {
	name := CustomGateStepName(StepTest, "mutation-budget")
	if name != StepName("gate.test.mutation-budget") {
		t.Fatalf("CustomGateStepName() = %q", name)
	}
	anchor, ok := name.CustomGateAnchor()
	if !ok || anchor != StepTest {
		t.Fatalf("CustomGateAnchor() = %q, %v; want test, true", anchor, ok)
	}
	if label := name.CustomGateLabel(); label != "mutation-budget" {
		t.Fatalf("CustomGateLabel() = %q", label)
	}
	if !name.IsCustomGate() {
		t.Fatal("IsCustomGate() = false, want true")
	}
}

func TestCustomGateStepName_OrdersWithItsAnchor(t *testing.T) {
	if got, want := CustomGateStepName(StepTest, "g").Order(), StepTest.Order(); got != want {
		t.Fatalf("gate order = %d, want the anchor's %d", got, want)
	}
	if got, want := CustomGateStepName(StepReview, "g").Order(), StepReview.Order(); got != want {
		t.Fatalf("gate order = %d, want the anchor's %d", got, want)
	}
}

func TestCustomGateAnchor_RejectsMalformedNames(t *testing.T) {
	for _, name := range []StepName{
		"gate.",
		"gate.test",
		"gate.test.",
		"gate.nope.g",
		"gate..g",
		"gate.intent.g",
		"gate.push.g",
		"gate.pr.g",
		"gate.ci.g",
		StepTest,
	} {
		if _, ok := name.CustomGateAnchor(); ok {
			t.Errorf("CustomGateAnchor(%q) reported a valid gate", name)
		}
	}
}

func TestCustomGateAnchor_RejectsLabelsThatCouldEscapeALogPath(t *testing.T) {
	for _, name := range []StepName{
		"gate.test.../../../etc/passwd",
		`gate.test...\..\windows`,
		"gate.test.a/b",
		"gate.test.a.b",
		StepName("gate.test." + strings.Repeat("x", MaxCustomGateLabelLen+1)),
	} {
		if _, ok := name.CustomGateAnchor(); ok {
			t.Errorf("CustomGateAnchor(%q) reported a valid gate", name)
		}
		if label := name.CustomGateLabel(); label != "" {
			t.Errorf("CustomGateLabel(%q) = %q, want empty for a malformed name", name, label)
		}
	}
}

func TestValidCustomGateLabel(t *testing.T) {
	valid := []string{"a", "arch-fitness", "mutation-budget", "g1", strings.Repeat("x", MaxCustomGateLabelLen)}
	for _, label := range valid {
		if !ValidCustomGateLabel(label) {
			t.Errorf("ValidCustomGateLabel(%q) = false, want true", label)
		}
	}
	invalid := []string{"", "-lead", "trail-", "Upper", "has space", "has.dot", "has/slash", strings.Repeat("x", MaxCustomGateLabelLen+1)}
	for _, label := range invalid {
		if ValidCustomGateLabel(label) {
			t.Errorf("ValidCustomGateLabel(%q) = true, want false", label)
		}
	}
}

func TestCustomGateStepName_IsUsableAsAStepLogFilename(t *testing.T) {
	filename := string(CustomGateStepName(StepTest, "mutation-budget")) + ".log"

	if strings.ContainsAny(filename, `<>:"/\|?*`) {
		t.Fatalf("step log filename %q contains a character Windows reserves in a path", filename)
	}
	for _, r := range filename {
		if r < 0x20 {
			t.Fatalf("step log filename %q contains a control character", filename)
		}
	}

	path := filepath.Join(t.TempDir(), filename)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create step log file %q: %v", filename, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close step log file: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat step log file: %v", err)
	}
}

func TestIsCoreStepName(t *testing.T) {
	for _, step := range AllSteps() {
		if !IsCoreStepName(step) {
			t.Errorf("IsCoreStepName(%q) = false", step)
		}
	}
	if IsCoreStepName(CustomGateStepName(StepTest, "g")) {
		t.Error("a custom gate reported as a core step")
	}
}
