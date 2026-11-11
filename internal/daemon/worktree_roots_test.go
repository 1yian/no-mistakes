package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
)

type mockWorkDirStep struct {
	name    types.StepName
	workDir chan string
}

func (s *mockWorkDirStep) Name() types.StepName { return s.name }
func (s *mockWorkDirStep) Execute(sctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	select {
	case s.workDir <- sctx.WorkDir:
	default:
	}
	return &pipeline.StepOutcome{}, nil
}

func yamlPath(path string) string {
	return `"` + strings.ReplaceAll(path, `\`, `\\`) + `"`
}

func namesPath(err error, path string) bool {
	msg := err.Error()
	for _, spelling := range []string{path, worktrees.Canonical(path)} {
		if strings.Contains(msg, spelling) || strings.Contains(msg, strconv.Quote(spelling)) {
			return true
		}
	}
	return false
}

func configureWorktreeRoot(t *testing.T, p *paths.Paths, workingPath, root string) {
	t.Helper()
	existing, err := os.ReadFile(p.ConfigFile())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	updated := string(existing)
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += "worktree_roots:\n  " + yamlPath(workingPath) + ": " + yamlPath(root) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunWorktreeIsCreatedInConfiguredRoot(t *testing.T) {
	step := &mockWorkDirStep{name: types.StepReview, workDir: make(chan string, 1)}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{step}
	})

	repo, headSHA := setupTestGitRepo(t, p, d, "worktree-root-repo")
	root := filepath.Join(t.TempDir(), "repo-runs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "mise.local.toml")
	if err := os.WriteFile(foreign, []byte("[tools]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configureWorktreeRoot(t, p, repo.WorkingPath, root)

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("worktree-root-repo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result); err != nil {
		t.Fatal(err)
	}

	run := waitForRunTerminalState(t, d, result.RunID)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %q, want %q", run.Status, types.RunCompleted)
	}
	var executedIn string
	select {
	case executedIn = <-step.workDir:
	default:
		t.Fatal("step never ran, so no worktree was observed")
	}
	if want := filepath.Join(root, result.RunID); !samePath(executedIn, want) {
		t.Fatalf("step ran in %q, want %q", executedIn, want)
	}
	// The run records where it was placed, so nothing that looks at it later
	// has to ask the configuration again.
	if recorded := run.WorktreePath(); !samePath(recorded, executedIn) {
		t.Fatalf("run recorded worktree %q, want the directory it executed in %q", recorded, executedIn)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("operator file in the configured root must survive the run: %v", err)
	}
}

func TestRunSetupFailureLeavesNoWorktreeBehind(t *testing.T) {
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&mockPassStep{name: types.StepReview}}
	})

	// A gate whose registered checkout no longer exists.
	source := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, source, "init")
	gitCmd(t, source, "config", "user.email", "test@test.com")
	gitCmd(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "test.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, source, "add", ".")
	gitCmd(t, source, "commit", "-m", "initial")
	headSHA := gitOutput(t, source, "rev-parse", "HEAD")
	gateDir := p.RepoDir("gone-checkout-repo")
	gitCmd(t, "", "init", "--bare", gateDir)
	gitCmd(t, source, "remote", "add", "gate", gateDir)
	gitCmd(t, source, "push", "gate", "HEAD:refs/heads/main")
	gitCmd(t, gateDir, "remote", "add", "origin", gateDir)

	missingCheckout := filepath.Join(t.TempDir(), "removed-checkout")
	repo, err := d.InsertRepoWithID("gone-checkout-repo", missingCheckout, "https://example.com/owner/repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "repo-runs")
	configureWorktreeRoot(t, p, repo.WorkingPath, root)

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: gateDir,
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result); err == nil {
		t.Fatal("run setup succeeded although the repository's checkout is gone")
	}

	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("setup failure left %q behind in the operator's worktree root", filepath.Join(root, entry.Name()))
	}
}

func TestRunCreationJudgesOnlyItsOwnPlacement(t *testing.T) {
	step := &mockPassStep{name: types.StepReview}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{step}
	})

	misplaced, misplacedHead := setupTestGitRepo(t, p, d, "misplaced-repo")
	unrelated, unrelatedHead := setupTestGitRepo(t, p, d, "unrelated-repo")
	// Valid at boot, unusable now: the root sits inside a checkout that is
	// registered too.
	configureWorktreeRoot(t, p, misplaced.WorkingPath, filepath.Join(unrelated.WorkingPath, "runs"))

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	push := func(gateID, head, branch string) (ipc.PushReceivedResult, error) {
		var result ipc.PushReceivedResult
		err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
			Gate: p.RepoDir(gateID),
			Ref:  "refs/heads/" + branch,
			Old:  "0000000000000000000000000000000000000000",
			New:  head,
		}, &result)
		return result, err
	}

	if _, err := push("misplaced-repo", misplacedHead, "main"); err == nil {
		t.Error("a run was created for the repository whose own placement is unusable")
	} else if !namesPath(err, misplaced.WorkingPath) {
		t.Errorf("refusal %q does not name the offending entry", err)
	}

	result, err := push("unrelated-repo", unrelatedHead, "main")
	if err != nil {
		t.Fatalf("another repository's bad entry failed this repository's run: %v", err)
	}
	if run := waitForRunTerminalState(t, d, result.RunID); run.Status != types.RunCompleted {
		errText := ""
		if run.Error != nil {
			errText = *run.Error
		}
		t.Fatalf("unrelated run status = %q (error %q), want %q", run.Status, errText, types.RunCompleted)
	}
}

func TestCleanupOrphanWorktrees_ConfiguredRootRemovesOnlyRunDirectories(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	workingPath := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(workingPath, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "repo-runs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("worktree_roots:\n  "+yamlPath(workingPath)+": "+yamlPath(root)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := d.InsertRepoWithID("repo1", workingPath, "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	activeRun, err := d.InsertRun(repo.ID, "feature", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	terminalRun, err := d.InsertRun(repo.ID, "old-branch", "headsha2", "basesha2")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(terminalRun.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}
	// Both runs record where they were placed, the way startRun does.
	for _, run := range []*db.Run{activeRun, terminalRun} {
		if err := d.SetRunWorktreeDir(run.ID, filepath.Join(root, run.ID)); err != nil {
			t.Fatal(err)
		}
	}

	activeWT := filepath.Join(root, activeRun.ID)
	terminalWT := filepath.Join(root, terminalRun.ID)
	operatorDir := filepath.Join(root, "scratch-checkout")
	for _, dir := range []string{activeWT, terminalWT, operatorDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	operatorFile := filepath.Join(root, "mise.local.toml")
	if err := os.WriteFile(operatorFile, []byte("[tools]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanupOrphanWorktrees(d, p, leftoverRecordedRunWorktrees(d, p))

	if _, err := os.Stat(terminalWT); !os.IsNotExist(err) {
		t.Fatalf("terminal run worktree should have been cleaned up, stat err: %v", err)
	}
	for _, keep := range []string{root, activeWT, operatorDir, operatorFile} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("cleanup removed %q from the operator's worktree root: %v", keep, err)
		}
	}
}

func TestCleanupOrphanWorktrees_UnconfiguredRepoUsesDefaultRoot(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	root := filepath.Join(t.TempDir(), "repo-runs")
	other := filepath.Join(t.TempDir(), "other-checkout")
	if err := os.WriteFile(p.ConfigFile(), []byte("worktree_roots:\n  "+yamlPath(other)+": "+yamlPath(root)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := d.InsertRepoWithID("repo1", filepath.Join(t.TempDir(), "checkout"), "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	terminalRun, err := d.InsertRun(repo.ID, "old-branch", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(terminalRun.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}
	terminalWT := p.WorktreeDir(repo.ID, terminalRun.ID)
	if err := os.MkdirAll(terminalWT, 0o755); err != nil {
		t.Fatal(err)
	}

	// The default-tree removal decision belongs to reapWorktrees (retention
	// aware); force every eligible directory past the retention window
	// regardless of its actual mtime, mirroring recoverOnStartup's order.
	future := time.Now().Add(365 * 24 * time.Hour)
	reapWorktrees(d, p, worktreeReapPolicy{Retention: time.Nanosecond}, future)
	cleanupOrphanWorktrees(d, p, leftoverRecordedRunWorktrees(d, p))

	if _, err := os.Stat(terminalWT); !os.IsNotExist(err) {
		t.Fatalf("default-root worktree should have been cleaned up, stat err: %v", err)
	}
}

func TestValidatedWorktreeLayoutRefusesWhenTheCheckoutListIsUnreadable(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}

	victim, err := d.InsertRepoWithID("victimrepo", filepath.Join(t.TempDir(), "victim"), "https://example.com/owner/victim", "main")
	if err != nil {
		t.Fatal(err)
	}
	placed, err := d.InsertRepoWithID("repo1", filepath.Join(t.TempDir(), "checkout"), "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	// Only the repository list reveals that this root sits inside a checkout,
	// so it is unjudgeable without it.
	root := filepath.Join(victim.WorkingPath, "runs")
	configureWorktreeRoot(t, p, placed.WorkingPath, root)
	globalCfg, err := config.LoadGlobal(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}

	// The readable list refuses it, which is what the unreadable one must not
	// quietly reverse.
	if _, err := validatedWorktreeLayout(d, p, globalCfg); err == nil {
		t.Fatal("readable checkout list accepted a root inside a registered checkout")
	}

	// The repository list now cannot be read at all.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	layout, err := validatedWorktreeLayout(d, p, globalCfg)
	if err == nil {
		t.Fatalf("unreadable checkout list validated against nothing and placed runs at %q",
			layout.Dir(placed.ID, placed.WorkingPath, "01M0000000000000000000000"))
	}
	if layout != nil {
		t.Error("refused startup still handed out a worktree layout")
	}
}

func TestDaemonRefusesToStartWithWorktreeRootInsideAnotherRegisteredCheckout(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	victim, err := d.InsertRepoWithID("victimrepo", filepath.Join(t.TempDir(), "victim"), "https://example.com/owner/victim", "main")
	if err != nil {
		t.Fatal(err)
	}
	// The victim has no worktree_roots entry of its own, so only the registered
	// repositories reveal that this root sits inside a checkout.
	placed, err := d.InsertRepoWithID("repo1", filepath.Join(t.TempDir(), "checkout"), "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(victim.WorkingPath, "runs")
	configureWorktreeRoot(t, p, placed.WorkingPath, root)

	err = RunWithResources(p, d)
	if err == nil {
		t.Fatal("daemon started with a worktree root inside another registered checkout")
	}
	if !namesPath(err, root) || !namesPath(err, victim.WorkingPath) {
		t.Errorf("startup failure %q names neither the root nor the checkout it would dirty", err)
	}
	if _, statErr := os.Stat(p.Socket()); statErr == nil {
		t.Error("daemon bound its socket despite refusing the configured placement")
	}
}

func TestDaemonRefusesToStartWithWorktreeRootInsideItsOwnWorktreesDirectory(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// A second repository with a live run, whose worktree the misread would
	// have deleted along with the directory holding it.
	victim, err := d.InsertRepoWithID("victimrepo", filepath.Join(t.TempDir(), "victim"), "https://example.com/owner/victim", "main")
	if err != nil {
		t.Fatal(err)
	}
	liveRun, err := d.InsertRun(victim.ID, "feature", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(liveRun.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	liveWT := p.WorktreeDir(victim.ID, liveRun.ID)
	if err := os.MkdirAll(liveWT, 0o755); err != nil {
		t.Fatal(err)
	}

	misconfigured, err := d.InsertRepoWithID("repo1", filepath.Join(t.TempDir(), "checkout"), "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	configureWorktreeRoot(t, p, misconfigured.WorkingPath, p.WorktreesDir())

	err = RunWithResources(p, d)
	if err == nil {
		t.Fatal("daemon started with a worktree root inside its own worktrees directory")
	}
	if !strings.Contains(err.Error(), "worktree_roots") || !namesPath(err, p.WorktreesDir()) {
		t.Errorf("startup failure %q names neither the setting nor the offending directory", err)
	}
	if _, statErr := os.Stat(liveWT); statErr != nil {
		t.Errorf("another repository's live run worktree was removed: %v", statErr)
	}
	if _, statErr := os.Stat(p.Socket()); statErr == nil {
		t.Error("daemon bound its socket despite refusing the configured placement")
	}
}

func TestCleanupOrphanWorktrees_OperatorRootRemovesOnlyWhatARunRecorded(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	workingPath := filepath.Join(t.TempDir(), "checkout")
	root := filepath.Join(t.TempDir(), "repo-runs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("worktree_roots:\n  "+yamlPath(workingPath)+": "+yamlPath(root)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := d.InsertRepoWithID("repo1", workingPath, "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	ownRun, err := d.InsertRun(repo.ID, "feature", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunWorktreeDir(ownRun.ID, filepath.Join(root, ownRun.ID)); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(ownRun.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}

	// A terminal run of a different repository that recorded a worktree in the
	// same directory, from before the operator reassigned it. Its own record
	// makes it ours to remove.
	otherRepo, err := d.InsertRepoWithID("repo2", filepath.Join(t.TempDir(), "other-checkout"), "https://example.com/owner/repo2", "main")
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := d.InsertRun(otherRepo.ID, "feature", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunWorktreeDir(otherRun.ID, filepath.Join(root, otherRun.ID)); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(otherRun.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}

	ownWT := filepath.Join(root, ownRun.ID)
	otherWT := filepath.Join(root, otherRun.ID)
	// Run-shaped, but no run record names it.
	unclaimedWT := filepath.Join(root, "01JZ8XQ7V6K9M3B0T5N2R4C8YD")
	// A run whose record names another directory entirely must not make this
	// one removable either.
	strayRun, err := d.InsertRun(repo.ID, "stray", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunWorktreeDir(strayRun.ID, filepath.Join(t.TempDir(), "elsewhere", strayRun.ID)); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(strayRun.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}
	strayWT := filepath.Join(root, strayRun.ID)
	for _, dir := range []string{ownWT, otherWT, unclaimedWT, strayWT} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cleanupOrphanWorktrees(d, p, leftoverRecordedRunWorktrees(d, p))

	for _, gone := range []string{ownWT, otherWT} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("recorded terminal run worktree %q should have been cleaned up, stat err: %v", gone, err)
		}
	}
	for _, keep := range []string{root, unclaimedWT, strayWT} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("cleanup removed %q, which no run record names: %v", keep, err)
		}
	}
}

func TestStartupSweepSetIsBoundedByThePresentNotByRunHistory(t *testing.T) {
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
	root := filepath.Join(t.TempDir(), "repo-runs")

	// History: a finished run whose worktree was removed at run end.
	history, err := d.InsertRun(repo.ID, "old", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	historyWT := filepath.Join(root, history.ID)
	if err := d.SetRunWorktreeDir(history.ID, historyWT); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(history.ID, types.RunCompleted); err != nil {
		t.Fatal(err)
	}

	// A leftover: finished, but its directory is still there.
	leftoverRun, err := d.InsertRun(repo.ID, "leftover", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	leftoverWT := filepath.Join(root, leftoverRun.ID)
	if err := os.MkdirAll(leftoverWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunWorktreeDir(leftoverRun.ID, leftoverWT); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(leftoverRun.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}

	// Executing when the daemon started, and its worktree is already gone.
	crashed, err := d.InsertRun(repo.ID, "crashed", "headsha", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	crashedWT := filepath.Join(root, crashed.ID)
	if err := d.SetRunWorktreeDir(crashed.ID, crashedWT); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(crashed.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}

	sweepSet := sweepableWorktrees(leftoverRecordedRunWorktrees(d, p), activeRecordedRunWorktrees(d, p))

	carried := make(map[string]string, len(sweepSet))
	for _, wt := range sweepSet {
		if previous, dup := carried[wt.Dir]; dup {
			t.Errorf("worktree %q carried twice (runs %q and %q)", wt.Dir, previous, wt.RunID)
		}
		carried[wt.Dir] = wt.RunID
	}
	if carried[leftoverWT] != leftoverRun.ID {
		t.Errorf("leftover worktree %q missing from the sweep set: %v", leftoverWT, carried)
	}
	if carried[crashedWT] != crashed.ID {
		t.Errorf("worktree of a run that was executing at startup missing from the sweep set: %v", carried)
	}
	if _, present := carried[historyWT]; present {
		t.Errorf("a finished run whose worktree is gone is still carried: %v", carried)
	}
}

func TestCleanupOrphanWorktrees_ReachesARootTheConfigNoLongerNames(t *testing.T) {
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
	// No worktree_roots entry at all: this placement exists only on the run.
	abandonedRoot := filepath.Join(t.TempDir(), "former-runs")
	recordedWT := filepath.Join(abandonedRoot, run.ID)
	if err := os.MkdirAll(recordedWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunWorktreeDir(run.ID, recordedWT); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(run.ID, types.RunFailed); err != nil {
		t.Fatal(err)
	}

	cleanupOrphanWorktrees(d, p, leftoverRecordedRunWorktrees(d, p))

	if _, err := os.Stat(recordedWT); !os.IsNotExist(err) {
		t.Fatalf("recorded worktree in a root the config no longer names survived cleanup, stat err: %v", err)
	}
	if _, err := os.Stat(abandonedRoot); err != nil {
		t.Errorf("cleanup removed the operator's directory itself: %v", err)
	}
}

func TestStepDiff_ReadsThePlacementItsRunRecorded(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	workingPath := filepath.Join(t.TempDir(), "checkout")
	repo, err := d.InsertRepoWithID("repo1", workingPath, "https://example.com/owner/repo1", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "abc123", "def456")
	if err != nil {
		t.Fatal(err)
	}

	created := filepath.Join(t.TempDir(), "repo-runs", run.ID)
	if err := os.MkdirAll(created, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, created, "init")
	runGit(t, created, "config", "user.email", "test@example.com")
	runGit(t, created, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(created, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, created, "add", "tracked.txt")
	runGit(t, created, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(created, "tracked.txt"), []byte("agent fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunWorktreeDir(run.ID, created); err != nil {
		t.Fatal(err)
	}

	// The operator pastes a different root while the run is parked.
	configureWorktreeRoot(t, p, workingPath, filepath.Join(t.TempDir(), "somewhere-else"))

	diff, truncated, err := NewRunManager(d, p, nil).StepDiff(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("step diff after a mid-run placement edit: %v", err)
	}
	if truncated || !strings.Contains(diff, "agent fix") {
		t.Fatalf("diff = %q (truncated=%v), want the recorded worktree's change", diff, truncated)
	}
}

func TestPrepareRecoveredRun_LocatesThePlacementItsRunRecorded(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	repo, headSHA := setupTestGitRepo(t, p, d, "repo1")
	run, err := d.InsertRun(repo.ID, "feature", headSHA, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunAwaitingAgent(run.ID); err != nil {
		t.Fatal(err)
	}

	created := filepath.Join(t.TempDir(), "repo-runs", run.ID)
	gitCmd(t, p.RepoDir(repo.ID), "worktree", "add", "--detach", created, headSHA)
	if err := d.SetRunWorktreeDir(run.ID, created); err != nil {
		t.Fatal(err)
	}
	// The operator points the checkout at a root this run knows nothing about.
	configureWorktreeRoot(t, p, repo.WorkingPath, filepath.Join(t.TempDir(), "somewhere-else"))

	m := NewRunManager(d, p, nil)
	stored, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.prepareRecoveredRun(context.Background(), stored); err != nil && strings.Contains(err.Error(), "worktree is missing") {
		t.Fatalf("recovery lost the run's recorded worktree %q: %v", created, err)
	}
}

func TestPrepareRecoveredRun_UnrecordedRunKeepsItsDefaultPlacement(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	repo, headSHA := setupTestGitRepo(t, p, d, "repo1")
	run, err := d.InsertRun(repo.ID, "feature", headSHA, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunAwaitingAgent(run.ID); err != nil {
		t.Fatal(err)
	}

	// Written by the previous build: a worktree in the default tree and no
	// recorded placement.
	legacyWT := p.WorktreeDir(repo.ID, run.ID)
	gitCmd(t, p.RepoDir(repo.ID), "worktree", "add", "--detach", legacyWT, headSHA)
	configureWorktreeRoot(t, p, repo.WorkingPath, filepath.Join(t.TempDir(), "repo-runs"))

	stored, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.WorktreePath() != "" {
		t.Fatalf("fixture recorded a placement %q, want the pre-upgrade NULL", stored.WorktreePath())
	}
	if _, err := NewRunManager(d, p, nil).prepareRecoveredRun(context.Background(), stored); err != nil && strings.Contains(err.Error(), "worktree is missing") {
		t.Fatalf("recovery looked past the default placement of a run that recorded none: %v", err)
	}
}

func TestReportUnusableWorktreeRoots_NamesEntriesThatDoNothing(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	registered := filepath.Join(t.TempDir(), "checkout")
	stale := filepath.Join(t.TempDir(), "moved-away")
	if _, err := d.InsertRepoWithID("repo1", registered, "https://example.com/owner/repo1", "main"); err != nil {
		t.Fatal(err)
	}
	configYAML := "worktree_roots:\n" +
		"  " + yamlPath(registered) + ": " + yamlPath(filepath.Join(t.TempDir(), "runs-a")) + "\n" +
		"  " + yamlPath(stale) + ": " + yamlPath(filepath.Join(t.TempDir(), "runs-b")) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	globalCfg, err := config.LoadGlobal(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	layout, err := validatedWorktreeLayout(d, p, globalCfg)
	if err != nil {
		t.Fatalf("startup refused a placement it can host: %v", err)
	}

	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLogger)

	reportUnusableWorktreeRoots(d, layout)

	got := logs.String()
	if !strings.Contains(got, "matches no registered repository") {
		t.Errorf("startup did not report the stale worktree_roots entry, logs:\n%s", got)
	}
	if strings.Contains(got, registered) {
		t.Errorf("a matching entry must not be reported, logs:\n%s", got)
	}
}
