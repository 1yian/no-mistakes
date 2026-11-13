package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/branchsync"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/oklog/ulid/v2"
)

const activeRecoveryReason = types.RunCancelReasonRecoveredForEditing

type activeRecoveryRequest struct {
	params ipc.RecoverActiveParams
	ctx    context.Context
	result chan activeRecoveryReply
}

type activeRecoveryReply struct {
	result ipc.RecoverActiveResult
	err    error
}

type activeRecoveryManifest struct {
	Version int                     `json:"version"`
	Request ipc.RecoverActiveParams `json:"request"`
	Result  ipc.RecoverActiveResult `json:"result"`
}

func (m *RunManager) HandleRecoverActive(ctx context.Context, p ipc.RecoverActiveParams) (*ipc.RecoverActiveResult, error) {
	if _, err := ulid.ParseStrict(p.RunID); err != nil {
		return nil, fmt.Errorf("invalid run identity")
	}
	for _, sha := range []string{p.LocalHead, p.PublishedHead} {
		if len(sha) != 40 || strings.ToLower(sha) != sha {
			return nil, fmt.Errorf("recovery requires full lowercase commit SHAs")
		}
		if _, err := hex.DecodeString(sha); err != nil {
			return nil, fmt.Errorf("invalid commit SHA")
		}
	}
	if !filepath.IsAbs(p.WorkDir) {
		return nil, fmt.Errorf("recovery requires an absolute invoking worktree")
	}
	var result ipc.RecoverActiveResult
	_, err := m.withBranchLock(p.RepoID, p.Branch, func() (string, error) {
		if m.restartPending() || m.shuttingDown.Load() {
			return "", fmt.Errorf("daemon lifecycle transition pending")
		}
		run, err := m.db.GetActiveRun(p.RepoID, p.Branch)
		if err != nil {
			return "", err
		}
		if run == nil || run.ID != p.RunID || run.Status != types.RunRunning || run.HeadSHA != p.PublishedHead || run.LastPushedSHA == nil || *run.LastPushedSHA != p.PublishedHead || run.PushActive || run.CustodyReturnedAt != nil {
			return "", fmt.Errorf("active recovery identity or lifecycle changed")
		}
		m.mu.Lock()
		done := m.dones[p.RunID]
		if !m.ciMonitorActive[p.RunID] || m.cancels[p.RunID] == nil || done == nil || m.handoffRequests[p.RunID] != nil || m.activeRecoveries[p.RunID] != nil {
			m.mu.Unlock()
			return "", fmt.Errorf("recovery requires an uncontested active CI monitor")
		}
		req := &activeRecoveryRequest{params: p, ctx: ctx, result: make(chan activeRecoveryReply, 1)}
		m.activeRecoveries[p.RunID] = req
		if wake := m.ciWakes[p.RunID]; wake != nil {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			if m.activeRecoveries[p.RunID] == req {
				delete(m.activeRecoveries, p.RunID)
			}
			m.mu.Unlock()
		}()
		select {
		case reply := <-req.result:
			if reply.err != nil {
				return "", reply.err
			}
			result = reply.result
		case <-done:
			select {
			case reply := <-req.result:
				if reply.err != nil {
					return "", reply.err
				}
				result = reply.result
			default:
				return "", fmt.Errorf("monitor ended before recovery")
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
		select {
		case <-done:
		case <-ctx.Done():
			return "", fmt.Errorf("recovery terminal quiescence unconfirmed: %w", ctx.Err())
		}
		final, err := m.db.GetRun(p.RunID)
		if err != nil {
			return "", err
		}
		if final == nil || final.Status != types.RunCancelled || final.Error == nil || *final.Error != activeRecoveryReason {
			return "", fmt.Errorf("recovery terminal outcome changed; inspect the exact run")
		}
		if err := writeRecoveryJSON(filepath.Join(result.SnapshotDir, "terminal.json"), final); err != nil {
			return "", err
		}
		if err := syncRecoveryDirectories(result.SnapshotDir); err != nil {
			return "", err
		}
		return p.RunID, nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (m *RunManager) activeRecoveryPendingLocked(runID string) bool {
	if m.activeRecoveries[runID] != nil {
		return true
	}
	if m.paths == nil {
		return false
	}
	_, err := os.Lstat(filepath.Join(m.paths.Root(), "recovery-active", runID, "prepared.json"))
	return !os.IsNotExist(err)
}

func (m *RunManager) recoverActiveAtCheckpoint(sctx *pipeline.StepContext) error {
	for {
		m.mu.Lock()
		req := m.activeRecoveries[sctx.Run.ID]
		m.mu.Unlock()
		if req == nil {
			pending := filepath.Join(m.paths.Root(), "recovery-active", sctx.Run.ID, "prepared.json")
			if _, err := os.Lstat(pending); os.IsNotExist(err) {
				return nil
			}
			select {
			case <-sctx.Ctx.Done():
				return sctx.Ctx.Err()
			case <-sctx.CIWake:
				continue
			}
		}
		result, err := m.preserveActiveRecovery(req.ctx, sctx, req.params)
		if err != nil {
			m.mu.Lock()
			if m.activeRecoveries[sctx.Run.ID] == req {
				delete(m.activeRecoveries, sctx.Run.ID)
			}
			m.mu.Unlock()
			req.result <- activeRecoveryReply{err: err}
			continue
		}
		m.mu.Lock()
		cancel := m.cancels[sctx.Run.ID]
		if cancel != nil {
			cancel(errors.New(activeRecoveryReason))
		}
		m.mu.Unlock()
		if cancel == nil {
			req.result <- activeRecoveryReply{err: fmt.Errorf("monitor ownership changed after withdrawal")}
			return fmt.Errorf(activeRecoveryReason)
		}
		req.result <- activeRecoveryReply{result: result}
		return errors.New(activeRecoveryReason)
	}
}

func (m *RunManager) preserveActiveRecovery(ctx context.Context, sctx *pipeline.StepContext, p ipc.RecoverActiveParams) (ipc.RecoverActiveResult, error) {
	var result ipc.RecoverActiveResult
	cfg, err := config.LoadGlobal(m.paths.ConfigFile())
	if err != nil {
		return result, err
	}
	repo, err := m.db.GetRepo(p.RepoID)
	if err != nil || repo == nil {
		return result, fmt.Errorf("registered repository unavailable")
	}
	service := &branchsync.Service{DB: m.db, Repo: repo, WorkDir: p.WorkDir, GateDir: m.paths.RepoDir(repo.ID), Paths: m.paths, RemoteTimeout: cfg.BranchSyncRemoteTimeout}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sctx.Ctx.Err(); err != nil {
			return err
		}
		state := service.Refresh(ctx)
		if state.State != branchsync.StateDiverged || state.Safety != "blocked_diverged" || !state.Local.Clean || state.Local.Branch != p.Branch || state.Local.Head != p.LocalHead || state.Pipeline.RunID != p.RunID || state.Pipeline.Status != string(types.RunRunning) || state.Pipeline.CurrentHead != p.PublishedHead || state.Pipeline.PushedHead != p.PublishedHead || state.Remote.ObservedHead != p.PublishedHead || state.PRState != "open" {
			return fmt.Errorf("recovery proof refused: %s (%s); pinned clean divergence and live published identity required", state.State, state.Safety)
		}
		gateHead, err := git.Run(ctx, service.GateDir, "rev-parse", "refs/heads/"+p.Branch+"^{commit}")
		if err != nil || gateHead != p.PublishedHead {
			return fmt.Errorf("gate head changed")
		}
		head, err := git.HeadSHA(sctx.Ctx, sctx.WorkDir)
		if err != nil || head != p.PublishedHead {
			return fmt.Errorf("managed head changed")
		}
		status, err := git.Run(sctx.Ctx, sctx.WorkDir, "status", "--porcelain")
		if err != nil || status != "" {
			return fmt.Errorf("managed worktree is not clean")
		}
		return nil
	}
	if err := check(); err != nil {
		return result, err
	}
	root := filepath.Join(m.paths.Root(), "recovery-active", p.RunID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return result, err
	}
	var withdrawn *activeRecoveryManifest
	if data, err := os.ReadFile(filepath.Join(root, "withdrawn.json")); err == nil {
		var prior activeRecoveryManifest
		if json.Unmarshal(data, &prior) != nil || prior.Version != 1 || prior.Request != p {
			return result, fmt.Errorf("conflicting withdrawal record")
		}
		withdrawn = &prior
	} else if !os.IsNotExist(err) {
		return result, err
	}
	snapshot, err := os.MkdirTemp(root, "snapshot-")
	if err != nil {
		return result, err
	}
	result = ipc.RecoverActiveResult{RunID: p.RunID, SnapshotDir: snapshot, PublishedRef: "refs/no-mistakes/recover-active/" + p.RunID + "/published"}
	localRef := "refs/no-mistakes/recover-active/" + p.RunID + "/local"
	for ref, sha := range map[string]string{localRef: p.LocalHead, result.PublishedRef: p.PublishedHead} {
		target, exists, err := git.DirectRefTarget(ctx, p.WorkDir, ref)
		if err != nil {
			return result, err
		}
		if exists && target != sha {
			return result, fmt.Errorf("conflicting recovery anchor %s", ref)
		}
		if !exists {
			if _, err := git.Run(ctx, p.WorkDir, "update-ref", "--no-deref", ref, sha, strings.Repeat("0", 40)); err != nil {
				return result, err
			}
		}
	}
	if _, err := git.Run(ctx, p.WorkDir, "bundle", "create", filepath.Join(snapshot, "heads.bundle"), localRef, result.PublishedRef); err != nil {
		return result, err
	}
	if err := syncRecoveryFile(filepath.Join(snapshot, "heads.bundle")); err != nil {
		return result, err
	}
	run, err := m.db.GetRun(p.RunID)
	if err != nil {
		return result, err
	}
	srs, err := m.db.GetStepsByRun(p.RunID)
	if err != nil {
		return result, err
	}
	rounds := make(map[string][]*db.StepRound)
	for _, sr := range srs {
		rounds[sr.ID], err = m.db.GetRoundsByStep(sr.ID)
		if err != nil {
			return result, err
		}
	}
	if err := writeRecoveryJSON(filepath.Join(snapshot, "history.json"), struct {
		Run    *db.Run
		Steps  []*db.StepResult
		Rounds map[string][]*db.StepRound
	}{run, srs, rounds}); err != nil {
		return result, err
	}
	for source, dest := range map[string]string{sctx.EvidenceDir: "evidence", m.paths.RunLogDir(p.RunID): "logs"} {
		if source == "" {
			return result, fmt.Errorf("missing recorded evidence directory")
		}
		if err := copyRecoveryTree(source, filepath.Join(snapshot, dest)); err != nil {
			return result, err
		}
	}
	manifest := activeRecoveryManifest{Version: 1, Request: p, Result: result}
	if err := writeRecoveryJSON(filepath.Join(snapshot, "manifest.json"), manifest); err != nil {
		return result, err
	}
	if err := check(); err != nil {
		return result, err
	}
	if err := steps.WithdrawActiveRecoveryAttestation(sctx, func(body string) error {
		if err := writeRecoveryJSON(filepath.Join(snapshot, "pr-before.json"), body); err != nil {
			return err
		}
		if err := syncRecoveryDirectories(snapshot); err != nil {
			return err
		}
		prepared := filepath.Join(root, "prepared.json")
		if data, err := os.ReadFile(prepared); err == nil {
			var prior activeRecoveryManifest
			if json.Unmarshal(data, &prior) != nil || prior.Version != 1 || prior.Request != p {
				return fmt.Errorf("conflicting prepared recovery; inspect preservation records")
			}
		} else if os.IsNotExist(err) {
			if err := writeRecoveryJSON(prepared, manifest); err != nil {
				return err
			}
		} else {
			return err
		}
		if err := syncRecoveryDirectories(root); err != nil {
			return err
		}
		return m.db.SetRunCIReadyWithReason(p.RunID, false, false)
	}); err != nil {
		return result, err
	}
	if err := check(); err != nil {
		return result, fmt.Errorf("attestation withdrawn but recovery refused: %w", err)
	}
	if withdrawn == nil {
		if err := writeRecoveryJSON(filepath.Join(root, "withdrawn.json"), manifest); err != nil {
			return result, err
		}
	} else {
		result = withdrawn.Result
	}
	if err := syncRecoveryDirectories(root); err != nil {
		return result, err
	}
	if err := check(); err != nil {
		return result, err
	}
	verified := manifest
	if withdrawn != nil {
		verified = *withdrawn
	}
	if err := verifyActiveRecoverySnapshot(ctx, service.GateDir, root, verified); err != nil {
		return result, err
	}
	return result, nil
}

func writeRecoveryJSON(path string, data any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(file).Encode(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func syncRecoveryFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncRecoveryDirectories(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncRecoveryDirectory(directories[i]); err != nil {
			return err
		}
	}
	for parent, n := filepath.Dir(root), 0; n < 3; parent, n = filepath.Dir(parent), n+1 {
		if err := syncRecoveryDirectory(parent); err != nil {
			return err
		}
	}
	return nil
}

func syncRecoveryDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func copyRecoveryTree(source, dest string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("recovery evidence must be a directory")
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		target := filepath.Join(dest, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot preserve non-regular recovery evidence")
		}
		in, err := root.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func verifyActiveRecoverySnapshot(ctx context.Context, gate, root string, manifest activeRecoveryManifest) error {
	snapshot := manifest.Result.SnapshotDir
	if filepath.Dir(snapshot) != root || !strings.HasPrefix(filepath.Base(snapshot), "snapshot-") || manifest.Result.RunID != manifest.Request.RunID || manifest.Result.PublishedRef != "refs/no-mistakes/recover-active/"+manifest.Request.RunID+"/published" {
		return fmt.Errorf("invalid recovery snapshot identity")
	}
	for _, name := range []string{"manifest.json", "history.json", "pr-before.json", "heads.bundle", "evidence", "logs"} {
		info, err := os.Lstat(filepath.Join(snapshot, name))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("missing or replaced recovery artifact %s", name)
		}
	}
	data, err := os.ReadFile(filepath.Join(snapshot, "manifest.json"))
	if err != nil {
		return err
	}
	var recorded activeRecoveryManifest
	if json.Unmarshal(data, &recorded) != nil || recorded != manifest {
		return fmt.Errorf("recovery snapshot binding changed")
	}
	bundle := filepath.Join(snapshot, "heads.bundle")
	if _, err := git.Run(ctx, gate, "bundle", "verify", bundle); err != nil {
		return fmt.Errorf("recovery bundle is unverified: %w", err)
	}
	heads, err := git.Run(ctx, gate, "bundle", "list-heads", bundle)
	if err != nil {
		return err
	}
	for _, binding := range []string{manifest.Request.LocalHead + " refs/no-mistakes/recover-active/" + manifest.Request.RunID + "/local", manifest.Request.PublishedHead + " " + manifest.Result.PublishedRef} {
		found := false
		for _, line := range strings.Split(heads, "\n") {
			if line == binding {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("recovery bundle no longer binds both heads")
		}
	}
	return nil
}

func (m *RunManager) requireFreshActiveRecovery(ctx context.Context, repoID, branch, head string, skips []types.StepName, retry bool) error {
	runs, err := m.db.GetRunsByRepo(repoID)
	if err != nil {
		return err
	}
	var validated []*db.Run
	for _, run := range runs {
		if run.Branch != branch {
			continue
		}
		path := filepath.Join(m.paths.Root(), "recovery-active", run.ID, "withdrawn.json")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			data, err = os.ReadFile(filepath.Join(filepath.Dir(path), "prepared.json"))
		}
		if os.IsNotExist(err) {
			if run.Error != nil && *run.Error == activeRecoveryReason {
				return fmt.Errorf("recovery preservation record is missing; refusing launch")
			}
			if run.CIReadyAt != nil || run.Status == types.RunCompleted {
				steps, err := m.db.GetStepsByRun(run.ID)
				if err != nil {
					return err
				}
				fresh := map[types.StepName]bool{}
				for _, step := range steps {
					if step.Status == types.StepStatusCompleted && step.CarriedFrom == nil {
						fresh[step.StepName] = true
					}
				}
				if fresh[types.StepReview] && fresh[types.StepTest] && fresh[types.StepDocument] && fresh[types.StepLint] && fresh[types.StepPush] && fresh[types.StepPR] {
					validated = append(validated, run)
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		var manifest activeRecoveryManifest
		if json.Unmarshal(data, &manifest) != nil || manifest.Version != 1 || manifest.Request.RunID != run.ID || manifest.Request.RepoID != repoID || manifest.Request.Branch != branch || manifest.Request.PublishedHead != run.HeadSHA {
			return fmt.Errorf("invalid recovery binding; refusing launch")
		}
		if !run.Status.Terminal() {
			return fmt.Errorf("active recovery is pending; finish the pinned recovery before submitting")
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("recovery withdrawal is unconfirmed; refusing launch: %w", err)
		}
		if err := verifyActiveRecoverySnapshot(ctx, m.paths.RepoDir(repoID), filepath.Dir(path), manifest); err != nil {
			return err
		}
		for _, candidate := range validated {
			contained := true
			for _, pair := range [][2]string{{manifest.Request.LocalHead, candidate.HeadSHA}, {manifest.Request.PublishedHead, candidate.HeadSHA}, {candidate.HeadSHA, head}} {
				if _, err := git.Run(ctx, m.paths.RepoDir(repoID), "merge-base", "--is-ancestor", pair[0], pair[1]); err != nil {
					contained = false
					break
				}
			}
			if contained {
				return nil
			}
		}
		if retry || len(skips) > 0 {
			return fmt.Errorf("recovered run requires fresh full validation without retry or skipped steps")
		}
		for _, parent := range []string{manifest.Request.LocalHead, manifest.Request.PublishedHead} {
			if len(parent) != 40 || strings.ToLower(parent) != parent {
				return fmt.Errorf("invalid recovery commit binding")
			}
			if _, err := hex.DecodeString(parent); err != nil {
				return fmt.Errorf("invalid recovery commit binding")
			}
			_, err := git.Run(ctx, m.paths.RepoDir(repoID), "merge-base", "--is-ancestor", parent, head)
			if err != nil {
				return fmt.Errorf("fresh recovery submission must preserve both pinned histories by ancestry; merge the published recovery ref first")
			}
		}
		return nil
	}
	return nil
}
