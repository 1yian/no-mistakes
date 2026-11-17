//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/scm/plugin/fakeplugin"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func runPublishedScenario(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run-published-scenario.yaml")
	content := `actions:
  - match: "Investigate previous review findings"
    text: "fixed unsafe value"
    edits:
      - path: "feature.txt"
        old: "unsafe"
        new: "safe"
    structured:
      summary: "guard unsafe value"
  - match: "Review the code changes and return structured findings"
    text: "review found a warning"
    structured:
      findings:
        - id: "rp-1"
          severity: warning
          file: "feature.txt"
          line: 1
          description: "unsafe value needs validation"
          action: auto-fix
      summary: "found one issue"
      risk_level: medium
      risk_rationale: "the unsafe value needs a guard"
      risk_scope: source-or-external
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no remaining risk"
      risk_scope: source-or-external
      tested: ["fakeagent: focused verification"]
      testing_summary: "simulated tests passed"
      scenarios:
        - name: "fakeagent: simulated end-to-end scenario"
          result: pass
          live: true
          evidence: "fakeagent: simulated test run"
          reason: ""
      verdict: go
      artifacts: []
      title: "feat: run published adoption"
      body: "run published adoption journey"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write run-published scenario: %v", err)
	}
	return path
}

type runPublishedShape struct {
	h         *Harness
	branch    string
	operator  string
	submitted string
	published string
	runID     string
	approve   string
}

func startRunPublishedShape(t *testing.T, h *Harness, branch string) *runPublishedShape {
	t.Helper()
	h.CommitChange("init-run-published", "seed.txt", "seed\n", "seed run-published init")
	initWorktree := h.AddWorktree("init-run-published")
	if out, err := h.RunInDir(initWorktree, "init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	submitted := h.CommitChange(branch, "feature.txt", "unsafe\n", "add unsafe feature")
	h.CommitChange("main", "upstream-advance.txt", "advance\n", "upstream advance")
	if out, err := h.runGit(context.Background(), h.WorkDir, "push", "origin", "main"); err != nil {
		t.Fatalf("advance upstream main: %v\n%s", err, out)
	}
	operator := h.AddWorktree(branch)
	gateOut, err := h.RunInDir(operator, "axi", "run", "--intent", "guard the feature across a moved base")
	if err != nil || !strings.Contains(gateOut, "rp-1") {
		t.Fatalf("review gate: %v\n%s", err, gateOut)
	}
	if out, err := h.RunInDir(operator, "axi", "respond", "--action", "fix", "--findings", "rp-1"); err != nil {
		t.Fatalf("review fix: %v\n%s", err, out)
	}
	approveOut, err := h.RunInDir(operator, "axi", "respond", "--action", "approve")
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, approveOut)
	}
	published := h.UpstreamBranchSHA(branch)
	if published == submitted {
		t.Fatal("pipeline did not publish a rewritten head")
	}
	var runID string
	for _, run := range h.Runs() {
		if run.Branch == branch {
			runID = run.ID
		}
	}
	if runID == "" {
		t.Fatal("no run recorded for the branch")
	}
	return &runPublishedShape{h: h, branch: branch, operator: operator, submitted: submitted, published: published, runID: runID, approve: approveOut}
}

func (s *runPublishedShape) git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := s.h.runGit(context.Background(), s.operator, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (s *runPublishedShape) gitFails(t *testing.T, args ...string) bool {
	t.Helper()
	_, err := s.h.runGit(context.Background(), s.operator, args...)
	return err != nil
}

func (s *runPublishedShape) adoptionRefs() (local, published string) {
	return "refs/no-mistakes/adopt-run-published/" + s.runID + "/local", "refs/no-mistakes/adopt-run-published/" + s.runID + "/published"
}

func (s *runPublishedShape) snapshot(t *testing.T) string {
	t.Helper()
	refs := s.git(t, "for-each-ref", "--format=%(refname) %(objectname)")
	var kept []string
	for _, line := range strings.Split(refs, "\n") {
		if strings.HasPrefix(line, "refs/no-mistakes/sync/") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n") + "\nHEAD " + s.git(t, "rev-parse", "HEAD") + "\n" + s.git(t, "status", "--porcelain")
}

func (s *runPublishedShape) openDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(paths.WithRoot(s.h.NMHome).DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func (s *runPublishedShape) assertNoAdoption(t *testing.T) {
	t.Helper()
	local, published := s.adoptionRefs()
	for _, ref := range []string{local, published} {
		if !s.gitFails(t, "rev-parse", "--verify", "--quiet", ref) {
			t.Errorf("refused sync created adoption ref %s", ref)
		}
	}
}

func assertContainsAll(t *testing.T, label, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing %q:\n%s", label, want, out)
		}
	}
}

func TestAxiSyncAdoptsTheHeadTheRunPublishedAfterRebaseAndFixerRewrite(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: runPublishedScenario(t)})
	s := startRunPublishedShape(t, h, "feature/run-published-adopt")
	upstreamBefore := s.published
	runsBefore := len(h.Runs())

	assertContainsAll(t, "post-push drive result", s.approve, "outcome: passed")

	statusOut, err := h.RunInDir(s.operator, "axi", "status")
	if err != nil {
		t.Fatalf("axi status: %v\n%s", err, statusOut)
	}
	assertContainsAll(t, "cached status", statusOut, "state: diverged", "relation: diverged", "safety: refresh_required", "code: sync")
	if strings.Contains(statusOut, "state: behind") {
		t.Errorf("cached status still claims the branch is behind:\n%s", statusOut)
	}
	if got := strings.TrimSpace(h.WorktreeRefSHA(s.branch)); got != s.submitted {
		t.Fatalf("status moved the branch to %s", got)
	}

	checkOut, err := h.RunInDir(s.operator, "axi", "sync", "--check")
	if err != nil {
		t.Fatalf("sync --check must exit zero for an adoptable run-published head: %v\n%s", err, checkOut)
	}
	assertContainsAll(t, "sync --check", checkOut, "state: diverged", "safety: safe_run_published_advance", "code: sync", "freshness: live")
	if got := strings.TrimSpace(h.WorktreeRefSHA(s.branch)); got != s.submitted {
		t.Fatalf("sync --check moved the branch to %s", got)
	}
	if !s.gitFails(t, "merge-base", "--is-ancestor", s.submitted, s.published) || !s.gitFails(t, "merge-base", "--is-ancestor", s.published, s.submitted) {
		t.Fatal("fixture is not a rewritten divergence: one head is an ancestor of the other")
	}
	if !s.gitFails(t, "merge-tree", "--write-tree", "--merge-base", s.git(t, "merge-base", s.submitted, s.published), s.published, s.submitted) {
		t.Fatal("fixture is content-equivalent: the existing equivalent-diverged route would apply, not the run-published one")
	}
	localRef, publishedRef := s.adoptionRefs()
	s.assertNoAdoption(t)

	syncOut, err := h.RunInDir(s.operator, "axi", "sync")
	if err != nil {
		t.Fatalf("axi sync: %v\n%s", err, syncOut)
	}
	assertContainsAll(t, "axi sync", syncOut,
		"state: synchronized", "changed: true", "safety: already_synchronized", "relation: equal",
		localRef, publishedRef, "Custody and the PR attestation are unchanged", "fresh `no-mistakes axi run`",
	)
	if got := strings.TrimSpace(h.WorktreeRefSHA(s.branch)); got != s.published {
		t.Fatalf("branch after adoption = %s, want published %s", got, s.published)
	}
	if got := s.git(t, "rev-parse", "HEAD"); got != s.published {
		t.Fatalf("HEAD after adoption = %s, want %s", got, s.published)
	}
	if got := s.git(t, "rev-parse", "HEAD^{tree}"); got != s.git(t, "rev-parse", s.published+"^{tree}") {
		t.Fatalf("worktree head tree = %s, want the published tree", got)
	}
	if out := s.git(t, "status", "--porcelain"); out != "" {
		t.Fatalf("worktree not clean after adoption: %q", out)
	}
	if got := s.git(t, "rev-parse", localRef); got != s.submitted {
		t.Fatalf("local anchor = %s, want the pre-adoption head %s", got, s.submitted)
	}
	if got := s.git(t, "rev-parse", publishedRef); got != s.published {
		t.Fatalf("published anchor = %s, want %s", got, s.published)
	}
	feature, readErr := os.ReadFile(filepath.Join(s.operator, "feature.txt"))
	if readErr != nil || strings.TrimSpace(string(feature)) != "safe" {
		t.Fatalf("pipeline fix missing from the adopted worktree: %q (err %v)", string(feature), readErr)
	}
	if _, statErr := os.Stat(filepath.Join(s.operator, "upstream-advance.txt")); statErr != nil {
		t.Fatalf("rebased base missing from the adopted worktree: %v", statErr)
	}

	if got := h.UpstreamBranchSHA(s.branch); got != upstreamBefore {
		t.Fatalf("adoption touched the remote: %s, want %s", got, upstreamBefore)
	}
	if got := len(h.Runs()); got != runsBefore {
		t.Fatalf("adoption changed the run count from %d to %d", runsBefore, got)
	}
	run, err := s.openDB(t).GetRun(s.runID)
	if err != nil || run == nil {
		t.Fatalf("read run: %v", err)
	}
	if run.CustodyReturnedAt != nil {
		t.Fatal("adoption stamped custody return")
	}
	if run.Status != types.RunCompleted || run.HeadSHA != s.published || run.LastPushedSHA == nil || *run.LastPushedSHA != s.published {
		t.Fatalf("adoption altered the run: status=%s head=%s pushed=%v", run.Status, run.HeadSHA, run.LastPushedSHA)
	}

	again, err := h.RunInDir(s.operator, "axi", "sync")
	if err != nil {
		t.Fatalf("repeated sync: %v\n%s", err, again)
	}
	assertContainsAll(t, "repeated sync", again, "state: synchronized", "changed: false")
	if got := s.git(t, "rev-parse", localRef); got != s.submitted {
		t.Fatalf("repeated sync moved the local anchor to %s", got)
	}

	if err := os.WriteFile(filepath.Join(s.operator, "followup.txt"), []byte("reviewer follow-up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.git(t, "add", "followup.txt")
	s.git(t, "commit", "-m", "reviewer follow-up")
	if s.gitFails(t, "merge-base", "--is-ancestor", s.published, "HEAD") {
		t.Fatal("follow-up commit does not build on the published head")
	}
	freshOut, err := h.RunInDir(s.operator, "axi", "run", "--intent", "validate the follow-up on the adopted head")
	if err != nil {
		t.Fatalf("fresh validation after adoption: %v\n%s", err, freshOut)
	}
	if !strings.Contains(freshOut, "gate:") || strings.Contains(freshOut, "fetch first") || strings.Contains(freshOut, "non-fast-forward") {
		t.Fatalf("fresh run on the adopted head did not start cleanly:\n%s", freshOut)
	}
	if got := len(h.Runs()); got != runsBefore+1 {
		t.Fatalf("fresh validation run count = %d, want %d", got, runsBefore+1)
	}
}

func TestAxiSyncAdoptsTheRunPublishedHeadWhileTheCIMonitorWaits(t *testing.T) {
	const (
		pluginHost = "ssm.example.com"
		remoteURL  = "https://" + pluginHost + "/team/repo.git"
		branchName = "feature/run-published-monitor"
	)
	h := NewHarness(t, SetupOpts{
		Agent:    "claude",
		Scenario: runPublishedScenario(t),
		GlobalConfigExtra: "provider_plugins:\n" +
			"  ssm:\n" +
			"    command: " + fakeplugin.ExecutableName + "\n" +
			"    hosts: [" + pluginHost + "]\n" +
			"    timeout: 30s\n",
	})
	configureGitURLRewrite(t, h, remoteURL, h.UpstreamDir)
	if out, err := h.runGit(t.Context(), h.WorkDir, "remote", "set-url", "origin", remoteURL); err != nil {
		t.Fatalf("set plugin-host origin: %v\n%s", err, out)
	}
	stateDir := filepath.Dir(h.AgentLog)
	statePath := filepath.Join(stateDir, "run-published-plugin-state.json")
	requestLog := filepath.Join(stateDir, "run-published-plugin-requests.ndjson")
	if err := fakeplugin.Save(statePath, fakeplugin.State{
		Checks: []fakeplugin.Check{{Name: "build", Bucket: "pass", State: "SUCCESS"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeplugin.EnvState, statePath)
	t.Setenv(fakeplugin.EnvLog, requestLog)

	s := startRunPublishedShape(t, h, branchName)
	assertContainsAll(t, "post-push drive result", s.approve, "outcome: checks-passed")
	run, err := s.openDB(t).GetRun(s.runID)
	if err != nil || run == nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != types.RunRunning || run.CIReadyAt == nil {
		t.Fatalf("run is not a waiting CI monitor: status=%s ci_ready=%v", run.Status, run.CIReadyAt)
	}
	callsBefore, err := fakeplugin.ReadLog(requestLog)
	if err != nil {
		t.Fatal(err)
	}
	pluginStateBefore, err := fakeplugin.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}

	checkOut, err := h.RunInDir(s.operator, "axi", "sync", "--check")
	if err != nil {
		t.Fatalf("sync --check: %v\n%s", err, checkOut)
	}
	assertContainsAll(t, "sync --check", checkOut, "safety: safe_run_published_advance", "status: running")
	syncOut, err := h.RunInDir(s.operator, "axi", "sync")
	if err != nil {
		t.Fatalf("axi sync while the monitor waits: %v\n%s", err, syncOut)
	}
	assertContainsAll(t, "axi sync", syncOut, "state: synchronized", "changed: true", "status: running", "Custody and the PR attestation are unchanged")
	if got := strings.TrimSpace(h.WorktreeRefSHA(s.branch)); got != s.published {
		t.Fatalf("branch after adoption = %s, want published %s", got, s.published)
	}
	localRef, publishedRef := s.adoptionRefs()
	if got := s.git(t, "rev-parse", localRef); got != s.submitted {
		t.Fatalf("local anchor = %s, want %s", got, s.submitted)
	}
	if got := s.git(t, "rev-parse", publishedRef); got != s.published {
		t.Fatalf("published anchor = %s, want %s", got, s.published)
	}

	after, err := s.openDB(t).GetRun(s.runID)
	if err != nil || after == nil {
		t.Fatalf("read run after adoption: %v", err)
	}
	if after.Status != types.RunRunning || after.CIReadyAt == nil || after.CustodyReturnedAt != nil || after.HeadSHA != s.published {
		t.Fatalf("adoption disturbed the monitoring run: status=%s ci_ready=%v custody=%v head=%s", after.Status, after.CIReadyAt, after.CustodyReturnedAt, after.HeadSHA)
	}
	callsAfter, err := fakeplugin.ReadLog(requestLog)
	if err != nil {
		t.Fatal(err)
	}
	pluginStateAfter, err := fakeplugin.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range callsAfter[len(callsBefore):] {
		if call.Command == "pr create" || call.Command == "pr update" || call.Command == "pr edit" {
			t.Errorf("adoption wrote to the PR through the forge: %q", call.Command)
		}
	}
	if len(pluginStateAfter.PRs) != len(pluginStateBefore.PRs) || (len(pluginStateAfter.PRs) == 1 && pluginStateAfter.PRs[0].Body != pluginStateBefore.PRs[0].Body) {
		t.Errorf("adoption changed the PR body or PR set: before %+v after %+v", pluginStateBefore.PRs, pluginStateAfter.PRs)
	}
	if got := h.UpstreamBranchSHA(s.branch); got != s.published {
		t.Fatalf("adoption touched the remote: %s, want %s", got, s.published)
	}
}

func TestAxiSyncRefusesRunPublishedAdoptionOutsideTheRunsOwnHead(t *testing.T) {
	type refusal struct {
		name    string
		prepare func(t *testing.T, s *runPublishedShape)
		wants   []string
		moved   bool
	}
	cases := []refusal{
		{
			name: "local commit after submission",
			prepare: func(t *testing.T, s *runPublishedShape) {
				if err := os.WriteFile(filepath.Join(s.operator, "after-submission.txt"), []byte("local work\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				s.git(t, "add", "after-submission.txt")
				s.git(t, "commit", "-m", "local work after submission")
			},
			wants: []string{"state: diverged", "safety: blocked_diverged", "code: inspect_and_reconcile_manually", "does not apply", "not the head this run was submitted from"},
		},
		{
			name: "dirty worktree",
			prepare: func(t *testing.T, s *runPublishedShape) {
				if err := os.WriteFile(filepath.Join(s.operator, "feature.txt"), []byte("unsafe\nuncommitted edit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wants: []string{"state: dirty", "safety: blocked_dirty"},
		},
		{
			name: "remote rewritten by someone else",
			prepare: func(t *testing.T, s *runPublishedShape) {
				ctx := context.Background()
				if out, err := s.h.runGit(ctx, s.h.WorkDir, "checkout", "-B", "someone-else", s.submitted); err != nil {
					t.Fatalf("checkout rewrite: %v\n%s", err, out)
				}
				s.h.CommitChange("someone-else", "elsewhere.txt", "other author\n", "someone else rewrites the PR branch")
				if out, err := s.h.runGit(ctx, s.h.WorkDir, "push", "--force", "origin", "someone-else:refs/heads/"+s.branch); err != nil {
					t.Fatalf("force-push rewrite: %v\n%s", err, out)
				}
				s.published = s.h.UpstreamBranchSHA(s.branch)
			},
			wants: []string{"safety: blocked_remote_rewritten"},
			moved: true,
		},
		{
			name: "remote advanced by someone else",
			prepare: func(t *testing.T, s *runPublishedShape) {
				ctx := context.Background()
				if out, err := s.h.runGit(ctx, s.h.WorkDir, "fetch", "origin", s.branch); err != nil {
					t.Fatalf("fetch published head: %v\n%s", err, out)
				}
				if out, err := s.h.runGit(ctx, s.h.WorkDir, "checkout", "-B", "someone-else", "FETCH_HEAD"); err != nil {
					t.Fatalf("checkout published head: %v\n%s", err, out)
				}
				s.h.CommitChange("someone-else", "elsewhere.txt", "other author\n", "someone else adds to the PR branch")
				if out, err := s.h.runGit(ctx, s.h.WorkDir, "push", "origin", "someone-else:refs/heads/"+s.branch); err != nil {
					t.Fatalf("push on top of the published head: %v\n%s", err, out)
				}
				s.published = s.h.UpstreamBranchSHA(s.branch)
			},
			wants: []string{"safety: blocked_remote_advanced"},
			moved: true,
		},
		{
			name: "newer run owns the branch",
			prepare: func(t *testing.T, s *runPublishedShape) {
				database := s.openDB(t)
				repo, err := database.GetRepo(s.h.repoID())
				if err != nil || repo == nil {
					t.Fatalf("repo: %v", err)
				}
				time.Sleep(1100 * time.Millisecond)
				if _, err := database.InsertRun(repo.ID, s.branch, s.submitted, s.submitted); err != nil {
					t.Fatalf("seed newer run: %v", err)
				}
			},
			wants: []string{"state: pipeline_owned", "safety: blocked_pipeline_owned"},
		},
		{
			name: "review authority missing",
			prepare: func(t *testing.T, s *runPublishedShape) {
				if err := s.openDB(t).UpdateRunReviewApprovedHeadSHA(s.runID, ""); err != nil {
					t.Fatalf("clear review authority: %v", err)
				}
			},
			wants: []string{"state: diverged", "safety: blocked_diverged", "does not apply", "does not contain the head this run's review approved"},
		},
		{
			name: "custody already returned",
			prepare: func(t *testing.T, s *runPublishedShape) {
				if err := s.openDB(t).SetRunCustodyReturned(s.runID); err != nil {
					t.Fatalf("stamp custody: %v", err)
				}
			},
			wants: []string{"state: diverged", "safety: blocked_diverged", "does not apply", "custody of this run was already returned"},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: runPublishedScenario(t)})
			s := startRunPublishedShape(t, h, "feature/run-published-refuse-"+string(rune('a'+i)))
			tc.prepare(t, s)
			headBefore := s.git(t, "rev-parse", "HEAD")
			before := s.snapshot(t)
			upstreamBefore := h.UpstreamBranchSHA(s.branch)

			out, err := h.RunInDir(s.operator, "axi", "sync")
			if err == nil {
				t.Fatalf("sync must refuse:\n%s", out)
			}
			assertContainsAll(t, "refused sync", out, tc.wants...)
			assertContainsAll(t, "refused sync changed flag", out, "changed: false")
			if got := s.git(t, "rev-parse", "HEAD"); got != headBefore {
				t.Fatalf("refused sync moved HEAD from %s to %s", headBefore, got)
			}
			if after := s.snapshot(t); after != before {
				t.Fatalf("refused sync changed refs or the worktree:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			s.assertNoAdoption(t)
			if got := h.UpstreamBranchSHA(s.branch); got != upstreamBefore {
				t.Fatalf("refused sync touched the remote: %s, want %s", got, upstreamBefore)
			}
		})
	}
}
