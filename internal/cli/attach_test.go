package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/gate"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/wizard"
)

func TestRootInteractiveWizardFailsLoudlyWhenRunRegistrationIsSlow(t *testing.T) {
	setupTestRepo(t)
	nmHome := makeSocketSafeTempDir(t)
	t.Setenv("NM_HOME", nmHome)
	p := paths.WithRoot(nmHome)

	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, _, err := gate.Init(context.Background(), d, p, "."); err != nil {
		t.Fatal(err)
	}

	startTestDaemon(t, p, d)

	prevInteractive := terminalInteractive
	terminalInteractive = func() bool { return true }
	defer func() { terminalInteractive = prevInteractive }()

	prevWizardRun := wizardRun
	wizardRun = func(cfg wizard.Config) (wizard.Result, error) {
		if cfg.WaitForRun == nil {
			t.Fatal("expected wait function")
		}
		if err := cfg.WaitForRun(context.Background(), "feat/slow"); err != nil {
			return wizard.Result{}, err
		}
		return wizard.Result{Success: true, Pushed: true, TargetBranch: "feat/slow"}, nil
	}
	defer func() { wizardRun = prevWizardRun }()

	prevRunTUI := runTUI
	runTUI = func(string, *ipc.Client, *ipc.RunInfo, string) error {
		t.Fatal("should not attach when no run is visible yet")
		return nil
	}
	defer func() { runTUI = prevRunTUI }()

	_, err = executeCmd()
	if err == nil {
		t.Fatal("executeCmd() should return an error when the daemon doesn't register a run")
	}
	if !strings.Contains(err.Error(), "feat/slow") {
		t.Errorf("error should name the branch we were waiting for, got: %v", err)
	}
}
