//go:build unix

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestProtectedPathRefusalRetainsWorktreeButReapsProcessesAndEvidence(t *testing.T) {
	orig := orphanProcessMinAge
	orphanProcessMinAge = 0
	t.Cleanup(func() { orphanProcessMinAge = orig })
	t.Setenv("TMPDIR", t.TempDir())

	for _, placement := range []string{"default", "recorded"} {
		t.Run(placement, func(t *testing.T) {
			p := paths.WithRoot(t.TempDir())
			if err := p.EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			database, err := db.Open(p.DB())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			repo, head := setupTestGitRepo(t, p, database, "protected-reaping")
			run, err := database.InsertRun(repo.ID, "main", head, head)
			if err != nil {
				t.Fatal(err)
			}
			workDir := p.WorktreeDir(repo.ID, run.ID)
			if placement == "recorded" {
				workDir = filepath.Join(t.TempDir(), run.ID)
			}
			if err := database.SetRunWorktreeDir(run.ID, workDir); err != nil {
				t.Fatal(err)
			}
			if err := git.WorktreeAdd(context.Background(), p.RepoDir(repo.ID), workDir, head); err != nil {
				t.Fatal(err)
			}
			if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
				t.Fatal(err)
			}
			sr, err := database.InsertStepResult(run.ID, types.StepPush)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.StartStep(sr.ID); err != nil {
				t.Fatal(err)
			}
			sctx := &pipeline.StepContext{Ctx: context.Background(), WorkDir: workDir, Run: run, DB: database, Config: config.Merge(config.DefaultGlobalConfig(), &config.RepoConfig{}), Log: func(string) {}}
			_, refusal := (protectedPathCommitStep{step: &steps.PushStep{}}).Execute(sctx)
			outcome := pipeline.ProtectedPathOutcome(refusal)
			if outcome == nil {
				t.Fatalf("expected protected-path refusal: %v", refusal)
			}
			if _, err := database.InsertStepRound(sr.ID, 1, "initial", &outcome.Findings, nil, 1); err != nil {
				t.Fatal(err)
			}
			if err := database.ParkStepForApproval(run.ID, sr.ID, types.StepStatusAwaitingApproval, outcome.ExitCode, 1, &outcome.Findings); err != nil {
				t.Fatal(err)
			}
			evidenceDir := filepath.Join(p.EvidenceRoot(""), run.ID)
			if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(evidenceDir, "output.txt"), []byte("test output"), 0o644); err != nil {
				t.Fatal(err)
			}
			expired := time.Now().Add(-config.DefaultEvidenceRetention - time.Hour)
			if err := os.Chtimes(evidenceDir, expired, expired); err != nil {
				t.Fatal(err)
			}
			leakedPID := startOrphanInWorktree(t, workDir)
			gitCmd(t, workDir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unavailable.git"))
			mgr := NewRunManager(database, p, func() []pipeline.Step { return []pipeline.Step{&steps.PushStep{}} })
			run, err = database.GetRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.prepareRecoveredRun(context.Background(), run); err == nil || !strings.Contains(err.Error(), "disable_project_settings") {
				t.Fatalf("recovery must fail closed at trusted config: %v", err)
			}
			layout, err := validatedWorktreeLayout(database, p, config.DefaultGlobalConfig())
			if err != nil {
				t.Fatal(err)
			}
			recoverOnStartup(database, p, mgr, layout)
			run, err = database.GetRun(run.ID)
			if err != nil || run.Status != types.RunFailed || len(mgr.executors) != 0 {
				t.Fatalf("trusted-config recovery did not fail closed: run=%+v err=%v", run, err)
			}
			assertProtectedWorktreePreserved(t, workDir, head)
			if _, err := os.Stat(evidenceDir); !os.IsNotExist(err) {
				t.Errorf("expired evidence of the terminal refused run survived: %v", err)
			}
			if !pidGoneWithin(leakedPID, 10*time.Second) {
				t.Errorf("orphan %d in the terminal refused run survived the startup sweep", leakedPID)
			}
			_, evidenceErr := os.Stat(evidenceDir)
			t.Logf("after failed trusted-config recovery: run=%s executors=%d orphan_alive=%t evidence_exists=%t", run.Status, len(mgr.executors), processIsAlive(leakedPID), !os.IsNotExist(evidenceErr))
		})
	}
}

func TestSweepOrphanRunProcessesReapsFinishedRunAndSparesActiveOne(t *testing.T) {
	// The startup sweep normally refuses to touch anything young, so a run
	// starting concurrently with daemon startup is never mistaken for a leak.
	// These fixtures are seconds old, so the floor is lowered for the test.
	orig := orphanProcessMinAge
	orphanProcessMinAge = 0
	t.Cleanup(func() { orphanProcessMinAge = orig })

	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	repo, err := d.InsertRepoWithID("repo1", "/nonexistent/work", "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}

	activeRun, err := d.InsertRun(repo.ID, "feature", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	if activeRun.Status != types.RunPending {
		t.Fatalf("expected new run to default to pending, got %s", activeRun.Status)
	}
	finishedRun, err := d.InsertRun(repo.ID, "old-branch", "headsha2", "basesha2")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(finishedRun.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}

	activePID := startOrphanInWorktree(t, p.WorktreeDir(repo.ID, activeRun.ID))
	leakedPID := startOrphanInWorktree(t, p.WorktreeDir(repo.ID, finishedRun.ID))

	sweepOrphanRunProcesses(d, p, sweepableWorktrees(leftoverRecordedRunWorktrees(d, p), activeRecordedRunWorktrees(d, p)), nil)

	if !pidGoneWithin(leakedPID, 10*time.Second) {
		t.Fatalf("orphan %d in the finished run's worktree survived the startup sweep", leakedPID)
	}
	if !processIsAlive(activePID) {
		t.Fatalf("orphan %d in an active run's worktree must not be swept", activePID)
	}
}

func TestSweepOrphanRunProcessesSparesWorktreesRetentionIsKeeping(t *testing.T) {
	orig := orphanProcessMinAge
	orphanProcessMinAge = 0
	t.Cleanup(func() { orphanProcessMinAge = orig })

	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	repo, err := d.InsertRepoWithID("repo1", "/nonexistent/work", "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}

	retainedRun, err := d.InsertRun(repo.ID, "retained-branch", "headsha1", "basesha1")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(retainedRun.ID, types.RunCompleted); err != nil {
		t.Fatal(err)
	}
	expiredRun, err := d.InsertRun(repo.ID, "expired-branch", "headsha2", "basesha2")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(expiredRun.ID, types.RunCompleted); err != nil {
		t.Fatal(err)
	}

	retainedDir := p.WorktreeDir(repo.ID, retainedRun.ID)
	expiredDir := p.WorktreeDir(repo.ID, expiredRun.ID)
	retainedPID := startOrphanInWorktree(t, retainedDir)
	expiredPID := startOrphanInWorktree(t, expiredDir)

	fresh := time.Now().Add(-time.Hour)
	if err := os.Chtimes(retainedDir, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(expiredDir, stale, stale); err != nil {
		t.Fatal(err)
	}

	policy := worktreeReapPolicy{Retention: 14 * 24 * time.Hour}
	retained := retainedDefaultTreeRunIDs(d, p, policy, time.Now())
	sweepOrphanRunProcesses(d, p, nil, retained)

	if !processIsAlive(retainedPID) {
		t.Fatalf("orphan %d in a worktree the retention policy is keeping was swept", retainedPID)
	}
	if !pidGoneWithin(expiredPID, 10*time.Second) {
		t.Fatalf("orphan %d in a worktree past the retention window survived the startup sweep", expiredPID)
	}
}

func TestSweepOrphanRunProcessesReachesRecordedWorktreeAndSparesUnclaimedOnes(t *testing.T) {
	orig := orphanProcessMinAge
	orphanProcessMinAge = 0
	t.Cleanup(func() { orphanProcessMinAge = orig })

	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	repo, err := d.InsertRepoWithID("repo1", filepath.Join(t.TempDir(), "checkout"), "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	finishedRun, err := d.InsertRun(repo.ID, "old-branch", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	activeRun, err := d.InsertRun(repo.ID, "feature", "headsha2", "basesha2")
	if err != nil {
		t.Fatal(err)
	}

	// The root lives only on the run records: the config names nothing.
	abandonedRoot := filepath.Join(t.TempDir(), "former-runs")
	finishedWT := filepath.Join(abandonedRoot, finishedRun.ID)
	activeWT := filepath.Join(abandonedRoot, activeRun.ID)
	for _, spec := range []struct {
		id  string
		dir string
	}{{finishedRun.ID, finishedWT}, {activeRun.ID, activeWT}} {
		if err := d.SetRunWorktreeDir(spec.id, spec.dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.UpdateRunStatus(finishedRun.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(activeRun.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}

	leakedPID := startOrphanInWorktree(t, finishedWT)
	activePID := startOrphanInWorktree(t, activeWT)
	operatorPID := startOrphanInWorktree(t, filepath.Join(abandonedRoot, "scratch-checkout"))
	unclaimedPID := startOrphanInWorktree(t, filepath.Join(abandonedRoot, "01JZ8XQ7V6K9M3B0T5N2R4C8YD"))

	sweepOrphanRunProcesses(d, p, sweepableWorktrees(leftoverRecordedRunWorktrees(d, p), activeRecordedRunWorktrees(d, p)), nil)

	if !pidGoneWithin(leakedPID, 10*time.Second) {
		t.Fatalf("orphan %d in a recorded worktree the config no longer names survived the startup sweep", leakedPID)
	}
	if !processIsAlive(activePID) {
		t.Errorf("orphan %d in an active run's worktree must not be swept", activePID)
	}
	if !processIsAlive(operatorPID) {
		t.Errorf("the sweep signalled %d in the operator's own directory", operatorPID)
	}
	if !processIsAlive(unclaimedPID) {
		t.Errorf("the sweep signalled %d in a run-shaped directory no run recorded", unclaimedPID)
	}
}

func TestSweepRunWorktreeProcessesReapsLeakedChildAtRunCleanup(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	m := NewRunManager(nil, p, nil)

	finished := p.WorktreeDir("repo1", "run1")
	other := p.WorktreeDir("repo1", "run2")
	leakedPID := startOrphanInWorktree(t, finished)
	otherPID := startOrphanInWorktree(t, other)

	m.sweepRunWorktreeProcesses("repo1", "run1", finished)

	if !pidGoneWithin(leakedPID, 10*time.Second) {
		t.Fatalf("orphan %d in the finished run's worktree survived run cleanup", leakedPID)
	}
	if !processIsAlive(otherPID) {
		t.Fatalf("run cleanup reached another run's worktree and killed %d", otherPID)
	}
}

func TestSweepRunWorktreeProcessesReapsLeakedChildInTheRunsRecordedRoot(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	m := NewRunManager(nil, p, nil)

	root := filepath.Join(t.TempDir(), "repo-runs")
	const runID = "01JZ8XQ7V6K9M3B0T5N2R4C8YD"
	finished := filepath.Join(root, runID)
	operatorDir := filepath.Join(root, "scratch-checkout")
	unclaimed := filepath.Join(root, "01JZ8XQ7V6K9M3B0T5N2R4C8YF")
	leakedPID := startOrphanInWorktree(t, finished)
	operatorPID := startOrphanInWorktree(t, operatorDir)
	unclaimedPID := startOrphanInWorktree(t, unclaimed)

	m.sweepRunWorktreeProcesses("repo1", runID, finished)

	if !pidGoneWithin(leakedPID, 10*time.Second) {
		t.Fatalf("orphan %d in the finished run's configured worktree survived run cleanup", leakedPID)
	}
	if !processIsAlive(operatorPID) {
		t.Fatalf("run cleanup signalled %d in the operator's own directory", operatorPID)
	}
	if !processIsAlive(unclaimedPID) {
		t.Fatalf("run cleanup signalled %d in a run-shaped directory this run does not own", unclaimedPID)
	}
}

func TestResumeRecoveredRunSweepsWorktreeProcessesBeforeRemoval(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	repo, err := d.InsertRepoWithID("repo1", filepath.Join(t.TempDir(), "checkout"), "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	bindFixtureCommitIdentity(t, d, run.ID)
	if err := d.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}

	// Placed in a directory of the operator's own, the way worktree_roots does.
	root := filepath.Join(t.TempDir(), "repo-runs")
	wtDir := filepath.Join(root, run.ID)
	if err := d.SetRunWorktreeDir(run.ID, wtDir); err != nil {
		t.Fatal(err)
	}
	leakedPID := startOrphanInWorktree(t, wtDir)
	operatorPID := startOrphanInWorktree(t, filepath.Join(root, "scratch-checkout"))

	stored, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := NewRunManager(d, p, nil)
	m.resumeRecoveredRun(recoveredRunPlan{
		run:     stored,
		repo:    repo,
		workDir: wtDir,
		gateDir: p.RepoDir(repo.ID),
		cfg:     config.Merge(config.DefaultGlobalConfig(), &config.RepoConfig{}),
		agent:   agent.NewNoop(),
		steps:   []pipeline.Step{&mockPassStep{name: types.StepReview}},
	})
	m.Shutdown()

	if !pidGoneWithin(leakedPID, 10*time.Second) {
		t.Fatalf("orphan %d in a resumed run's worktree survived its cleanup", leakedPID)
	}
	if !processIsAlive(operatorPID) {
		t.Errorf("cleanup signalled %d in the operator's own directory", operatorPID)
	}
}

func TestRemoveRunWorktreeSweepsBeforeRemoving(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, headSHA := setupTestGitRepo(t, p, d, "repo1")

	root := filepath.Join(t.TempDir(), "repo-runs")
	const runID = "01JZ8XQ7V6K9M3B0T5N2R4C8YD"
	wtDir := filepath.Join(root, runID)
	gitCmd(t, p.RepoDir("repo1"), "worktree", "add", "--detach", wtDir, headSHA)
	leakedPID := startOrphanInWorktree(t, wtDir)
	operatorPID := startOrphanInWorktree(t, filepath.Join(root, "scratch-checkout"))

	NewRunManager(d, p, nil).removeRunWorktree("repo1", runID, p.RepoDir("repo1"), wtDir, "test")

	if !pidGoneWithin(leakedPID, 10*time.Second) {
		t.Fatalf("orphan %d in the removed worktree survived removal", leakedPID)
	}
	if !processIsAlive(operatorPID) {
		t.Errorf("removal signalled %d in the operator's own directory", operatorPID)
	}
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Errorf("worktree directory survived removal, stat err: %v", err)
	}
}

func TestRunSetupFailureSweepsTheWorktreeItRemoves(t *testing.T) {
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&mockPassStep{name: types.StepReview}}
	})

	repo, _ := setupTestGitRepo(t, p, d, "setup-failure-repo")
	root := filepath.Join(t.TempDir(), "repo-runs")
	configureWorktreeRoot(t, p, repo.WorkingPath, root)

	// A post-checkout hook fires inside the new worktree when the daemon creates
	// it, and leaves a process standing there whose parent exits immediately.
	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	gateHooks := filepath.Join(p.RepoDir("setup-failure-repo"), "hooks")
	if err := os.MkdirAll(gateHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\nsleep 300 >/dev/null 2>&1 &\necho $! > " + pidFile + "\n"
	if err := os.WriteFile(filepath.Join(gateHooks, "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	// Setup then fails after the worktree exists: the pushed tree's repo config
	// does not parse.
	headSHA := commitInvalidRepoConfig(t, repo.WorkingPath)

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	pushErr := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("setup-failure-repo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result)
	if pushErr == nil {
		t.Fatal("push with an unparseable repo config created a run")
	}

	leakedPID := readOrphanPID(t, pidFile)
	if leakedPID == 0 {
		t.Skip("post-checkout hook did not run, so no orphan was planted in the worktree")
	}
	if !pidGoneWithin(leakedPID, 10*time.Second) {
		t.Fatalf("orphan %d planted in the worktree survived the setup-failure removal", leakedPID)
	}
}

func commitInvalidRepoConfig(t *testing.T, workDir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workDir, ".no-mistakes.yaml"), []byte("auto_fix: [not, a, mapping\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, workDir, "add", ".no-mistakes.yaml")
	gitCmd(t, workDir, "commit", "-m", "invalid config")
	gitCmd(t, workDir, "push", "gate", "HEAD:refs/heads/main")
	return gitOutput(t, workDir, "rev-parse", "HEAD")
}

func readOrphanPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return 0
}

func startOrphanInWorktree(t *testing.T, dir string) int {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 >/dev/null 2>&1 & echo $!")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("spawn orphan: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		t.Fatalf("orphan pid from %q: %v", out, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !processIsAlive(pid) {
		t.Fatalf("orphan %d was not running", pid)
	}
	return pid
}

func processIsAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func pidGoneWithin(pid int, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if !processIsAlive(pid) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return !processIsAlive(pid)
}
