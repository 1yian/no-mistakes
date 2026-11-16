package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/oklog/ulid/v2"
)

type ciRepairRequest struct {
	params ipc.AdmitCIRepairParams
	ctx    context.Context
	result chan ciRepairReply
}

type ciRepairReply struct {
	result *ipc.AdmitCIRepairResult
	err    error
}

func newCIRepairOwner() string { return rand.Text() }

func validCIRepairSHA(head string) bool {
	if len(head) != 40 || head != strings.ToLower(head) {
		return false
	}
	_, err := hex.DecodeString(head)
	return err == nil
}

func (m *RunManager) pendingCIRepair(repoID, branch string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.ciRepairRequests {
		if req.params.RepoID == repoID && req.params.Branch == branch {
			return true
		}
	}
	return false
}

func matchGuardedCIRepair(run *db.Run, p ipc.AdmitCIRepairParams) error {
	if run == nil || run.ID != p.RunID || run.RepoID != p.RepoID || run.Branch != p.Branch || run.Status != types.RunRunning || run.HeadSHA != p.HeadSHA || run.LastPushedSHA == nil || *run.LastPushedSHA != p.HeadSHA || run.PRURL == nil || *run.PRURL != p.PRURL || run.PushActive || run.CustodyReturnedAt != nil {
		return fmt.Errorf("guarded CI repair pinned identity, heads or custody changed")
	}
	return nil
}

func (m *RunManager) HandleAdmitCIRepair(ctx context.Context, p ipc.AdmitCIRepairParams) (*ipc.AdmitCIRepairResult, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("guarded CI repair admission is verified only on Linux")
	}
	if _, err := ulid.ParseStrict(p.RunID); err != nil {
		return nil, fmt.Errorf("guarded CI repair requires an exact run ULID")
	}
	if !validCIRepairSHA(p.HeadSHA) {
		return nil, fmt.Errorf("guarded CI repair requires a full lowercase commit SHA")
	}
	if p.RepoID == "" || p.Branch == "" || p.PRURL == "" {
		return nil, fmt.Errorf("guarded CI repair requires explicit repository, branch and PR pins")
	}
	requestCtx, cancel := handoffContext(ctx)
	defer cancel()
	var result *ipc.AdmitCIRepairResult
	_, err := m.withBranchLock(p.RepoID, p.Branch, func() (string, error) {
		if m.shuttingDown.Load() || m.restartPending() {
			return "", fmt.Errorf("daemon lifecycle transition pending")
		}
		release, ok := m.gateClaimGuard()
		if !ok {
			return "", fmt.Errorf("daemon lifecycle transition pending")
		}
		defer release()
		runs, err := m.db.GetRunsByRepo(p.RepoID)
		if err != nil {
			return "", err
		}
		active := 0
		var run *db.Run
		for _, candidate := range runs {
			if candidate.Branch == p.Branch && !candidate.Status.Terminal() {
				active++
				run = candidate
			}
		}
		if active != 1 {
			return "", fmt.Errorf("guarded CI repair requires exactly one active run on the branch")
		}
		if err := matchGuardedCIRepair(run, p); err != nil {
			return "", err
		}
		prior, err := m.db.CIRepairObligation(p.RunID)
		if err != nil {
			return "", err
		}
		if prior != nil && (prior.Owner != m.ciRepairOwner || prior.OriginalHead != p.HeadSHA || prior.PRURL != p.PRURL || prior.Phase != "withdrawing") {
			return "", fmt.Errorf("guarded CI repair admission already exists or lost continuity; inspect the original run")
		}
		if prior == nil && run.ReviewApprovedHeadSHA == nil {
			return "", fmt.Errorf("guarded CI repair requires existing durable Review authority")
		}
		rows, err := m.db.GetStepsByRun(p.RunID)
		if err != nil {
			return "", err
		}
		fresh := map[types.StepName]bool{}
		seen := map[types.StepName]bool{}
		monitoring := false
		parked := false
		for _, row := range rows {
			if seen[row.StepName] {
				return "", fmt.Errorf("guarded CI repair refuses ambiguous step identities")
			}
			seen[row.StepName] = true
			if row.StepName == types.StepCI {
				monitoring = row.Status == types.StepStatusRunning
				parked = row.Status == types.StepStatusAwaitingApproval || row.Status == types.StepStatusFixReview
				continue
			}
			if row.StepName.Order() >= types.StepReview.Order() && (row.Status != types.StepStatusCompleted || row.CarriedFrom != nil) {
				return "", fmt.Errorf("guarded CI repair requires completed, non-carried preceding phases")
			}
			fresh[row.StepName] = row.Status == types.StepStatusCompleted && row.CarriedFrom == nil
		}
		for _, name := range []types.StepName{types.StepReview, types.StepTest, types.StepDocument, types.StepLint, types.StepPush, types.StepPR} {
			if !fresh[name] {
				return "", fmt.Errorf("guarded CI repair is missing completed %s", name)
			}
		}
		if parked {
			m.mu.Lock()
			executor := m.executors[p.RunID]
			m.mu.Unlock()
			if executor == nil {
				return "", fmt.Errorf("guarded CI repair requires a current executor; legacy and lost-owner adoption are unsupported")
			}
			err := executor.WithCIRepairGateCheckpoint(requestCtx, func(sctx *pipeline.StepContext) error {
				req, _, err := m.registerCIRepairRequest(p, requestCtx, false, executor)
				if err != nil {
					return err
				}
				defer m.clearCIRepairRequest(req)
				result, err = m.admitCIRepairAtCheckpoint(sctx, p, prior)
				return err
			})
			return p.RunID, err
		}
		if !monitoring {
			return "", fmt.Errorf("guarded CI repair requires an idle owned monitor or its real CI approval gate")
		}
		req, done, err := m.registerCIRepairRequest(p, requestCtx, true, nil)
		if err != nil {
			return "", err
		}
		select {
		case reply := <-req.result:
			result = reply.result
			return p.RunID, reply.err
		case <-done:
			return "", fmt.Errorf("guarded CI repair lost monitor continuity; inspect the preserved original run")
		case <-requestCtx.Done():
			return "", fmt.Errorf("guarded CI repair result unconfirmed; inspect the exact original run before retrying: %w", requestCtx.Err())
		}
	})
	return result, err
}

func (m *RunManager) registerCIRepairRequest(p ipc.AdmitCIRepairParams, ctx context.Context, monitor bool, expected *pipeline.Executor) (*ciRepairRequest, chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	executor := m.executors[p.RunID]
	done := m.dones[p.RunID]
	if m.shuttingDown.Load() || executor == nil || !executor.SupportsGuardedCIRepair() || (expected != nil && executor != expected) || m.cancels[p.RunID] == nil || done == nil || m.ciMonitorActive[p.RunID] != monitor || m.handoffRequests[p.RunID] != nil || m.ciRepairRequests[p.RunID] != nil {
		return nil, nil, fmt.Errorf("guarded CI repair requires uncontested current ownership; legacy or lost-monitor adoption is unsupported")
	}
	req := &ciRepairRequest{params: p, ctx: ctx, result: make(chan ciRepairReply, 1)}
	m.ciRepairRequests[p.RunID] = req
	if monitor {
		if wake := m.ciWakes[p.RunID]; wake != nil {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
	return req, done, nil
}

func (m *RunManager) clearCIRepairRequest(req *ciRepairRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ciRepairRequests[req.params.RunID] == req {
		delete(m.ciRepairRequests, req.params.RunID)
	}
}

func (m *RunManager) ciRepairCheckpoint(sctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	for {
		a, err := m.db.CIRepairObligation(sctx.Run.ID)
		if err != nil {
			return nil, err
		}
		if a != nil {
			if a.Owner != m.ciRepairOwner || a.Phase == "held" {
				return nil, fmt.Errorf("guarded CI repair lost monitor continuity; adoption is unsupported")
			}
			sctx.CIRepairOwner = m.ciRepairOwner
			if a.Phase != "withdrawing" {
				run, err := m.db.GetRun(sctx.Run.ID)
				if err != nil {
					return nil, err
				}
				if run == nil || run.Status != types.RunRunning || run.HeadSHA != sctx.Run.HeadSHA || run.PRURL == nil || *run.PRURL != a.PRURL || run.CustodyReturnedAt != nil {
					return nil, fmt.Errorf("guarded CI repair current identity or custody changed")
				}
				if a.Phase == "repair" {
					p := ipc.AdmitCIRepairParams{RunID: run.ID, RepoID: run.RepoID, Branch: run.Branch, HeadSHA: a.OriginalHead, PRURL: a.PRURL}
					if err := matchGuardedCIRepair(run, p); err != nil {
						return nil, err
					}
					if err := m.verifyCIRepairSnapshot(sctx, a); err != nil {
						return nil, err
					}
					withdrawn, err := os.ReadFile(filepath.Join(a.SnapshotDir, "pr-withdrawn.txt"))
					if err != nil {
						return nil, err
					}
					if err := steps.VerifyGuardedCIRepair(sctx, a.OriginalHead, string(withdrawn)); err != nil {
						return nil, err
					}
				} else {
					pending, err := m.db.CIRepairValidationPending(run.ID)
					if err != nil {
						return nil, err
					}
					if !pending {
						if err := steps.VerifyGuardedCIRepairHead(sctx, run.HeadSHA); err != nil {
							return nil, err
						}
					}
				}
				return nil, nil
			}
		}
		m.mu.Lock()
		req := m.ciRepairRequests[sctx.Run.ID]
		m.mu.Unlock()
		if req == nil {
			if a == nil {
				return nil, nil
			}
			select {
			case <-sctx.Ctx.Done():
				return nil, sctx.Ctx.Err()
			case <-sctx.CIWake:
				continue
			}
		}
		callCtx, cancel := context.WithCancel(sctx.Ctx)
		stop := context.AfterFunc(req.ctx, cancel)
		bounded := *sctx
		bounded.Ctx = callCtx
		result, err := m.admitCIRepairAtCheckpoint(&bounded, req.params, a)
		stop()
		cancel()
		m.clearCIRepairRequest(req)
		req.result <- ciRepairReply{result: result, err: err}
		if err == nil {
			return steps.GuardedCIRepairAdmissionOutcome(), nil
		}
	}
}

func (m *RunManager) admitCIRepairAtCheckpoint(sctx *pipeline.StepContext, p ipc.AdmitCIRepairParams, a *db.CIRepairObligation) (*ipc.AdmitCIRepairResult, error) {
	run, err := m.db.GetRun(p.RunID)
	if err != nil {
		return nil, err
	}
	if err := matchGuardedCIRepair(run, p); err != nil {
		return nil, err
	}
	if err := sctx.Ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil {
		original, withdrawn, err := steps.PrepareGuardedCIRepair(sctx)
		if err != nil {
			return nil, err
		}
		a, err = m.snapshotCIRepair(sctx, original, withdrawn)
		if err != nil {
			return nil, err
		}
		if err := sctx.Ctx.Err(); err != nil {
			return nil, err
		}
		if err := m.verifyCIRepairSnapshot(sctx, a); err != nil {
			return nil, err
		}
		if err := m.db.BeginCIRepair(*a); err != nil {
			return nil, err
		}
		sctx.Run.ReviewApprovedHeadSHA = nil
	}
	if err := m.verifyCIRepairSnapshot(sctx, a); err != nil {
		return nil, err
	}
	original, err := os.ReadFile(filepath.Join(a.SnapshotDir, "pr-before.txt"))
	if err != nil {
		return nil, err
	}
	withdrawn, err := os.ReadFile(filepath.Join(a.SnapshotDir, "pr-withdrawn.txt"))
	if err != nil {
		return nil, err
	}
	if err := steps.WithdrawGuardedCIRepair(sctx, string(original), string(withdrawn)); err != nil {
		return nil, err
	}
	if err := sctx.Ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.db.AdmitCIRepair(p.RunID, m.ciRepairOwner); err != nil {
		return nil, err
	}
	return &ipc.AdmitCIRepairResult{RunID: p.RunID, HeadSHA: p.HeadSHA, State: "admitted-unvalidated", SnapshotDir: a.SnapshotDir}, nil
}
