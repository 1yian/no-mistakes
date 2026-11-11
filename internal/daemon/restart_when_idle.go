package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// Restart when idle (issue #1691).
//
// `no-mistakes update` replaces the executable file and then has to restart
// the daemon, which until now cancelled every run executing at that moment.
// A scheduled restart instead lets the running daemon - still executing the
// image it was started from - finish those runs, holds new pushes instead of
// starting them, and once nothing is executing stops cleanly and replaces its
// own process image with the executable file, now the new version. Runs parked
// at an approval gate do not hold the restart up: a clean stop already keeps
// them (#1438), so they resume on the new version. Held pushes are recorded in
// HeldPushesFile and started by the restarted daemon.

var ErrRestartForUpdate = errors.New("daemon stopped to restart through its executable for an update")

var errRestartPending = errors.New("the daemon is restarting for an update; try again shortly")

var errReplayDeferred = errors.New("restart scheduled during held push replay")

const defaultRestartIdlePoll = 500 * time.Millisecond

type restartState struct {
	pending        atomic.Bool
	due            atomic.Bool
	replaying      atomic.Bool
	gate           sync.RWMutex
	once           sync.Once
	trigger        func() // stops the daemon; set by the serve loop
	poll           time.Duration
	heldMu         sync.Mutex
	beforeStartRun func()
}

func (m *RunManager) ScheduleRestartWhenIdle() (*ipc.RestartStatusResult, error) {
	if m.shuttingDown.Load() {
		return nil, errors.New("daemon is shutting down")
	}
	if m.restart.trigger == nil {
		return nil, errors.New("this daemon cannot restart itself")
	}
	m.restart.pending.Store(true)
	m.restart.once.Do(func() {
		slog.Info("restart scheduled: waiting for executing runs to finish", "waiting_runs", m.busyRunCount())
		go m.restartWhenIdle()
	})
	return m.RestartStatus(), nil
}

func (m *RunManager) RestartStatus() *ipc.RestartStatusResult {
	status := &ipc.RestartStatusResult{Pending: m.restart.pending.Load()}
	if status.Pending {
		status.WaitingRuns = m.busyRunCount()
	}
	status.HeldPushes = countHeldPushes(m.paths.HeldPushesFile())
	return status
}

func (m *RunManager) restartPending() bool { return m.restart.pending.Load() }

func (m *RunManager) restartWhenIdle() {
	poll := m.restart.poll
	if poll <= 0 {
		poll = defaultRestartIdlePoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if m.shuttingDown.Load() {
			return
		}
		if m.busyRunCount() == 0 && m.markRestartDue() {
			slog.Info("restarting for update: no run is executing")
			m.restart.trigger()
			return
		}
		<-ticker.C
	}
}

func (m *RunManager) markRestartDue() bool {
	m.restart.gate.Lock()
	defer m.restart.gate.Unlock()
	m.restart.due.Store(true)
	if m.busyRunCount() == 0 {
		return true
	}
	m.restart.due.Store(false)
	return false
}

func (m *RunManager) beginGateResponse() (release func(), err error) {
	m.restart.gate.RLock()
	if m.restart.due.Load() {
		m.restart.gate.RUnlock()
		return nil, errRestartPending
	}
	return m.restart.gate.RUnlock, nil
}

func (m *RunManager) busyRunCount() int {
	m.mu.Lock()
	execs := make(map[string]*pipeline.Executor, len(m.executors))
	for id, exec := range m.executors {
		execs[id] = exec
	}
	busy := 0
	for _, qe := range m.runQueue {
		if qe.recovered == nil {
			busy++
		}
	}
	m.mu.Unlock()
	for id, exec := range execs {
		if !exec.AwaitingApproval() {
			busy++
			continue
		}
		run, err := m.db.GetRun(id)
		if err == nil && run != nil && run.Status == types.RunRunning && run.AwaitingAgentSince != nil {
			continue
		}
		busy++
	}
	return busy
}

func (m *RunManager) holdPushIfDeferred(params *ipc.PushReceivedParams, fromReplay bool) (bool, error) {
	m.restart.heldMu.Lock()
	defer m.restart.heldMu.Unlock()
	if !m.restart.pending.Load() && (fromReplay || !m.restart.replaying.Load()) {
		return false, nil
	}
	if fromReplay {
		return true, errReplayDeferred
	}
	if params.LaunchNonce != "" {
		return true, errRestartPending
	}
	if err := m.appendHeldPushLocked(params); err != nil {
		return true, fmt.Errorf("hold push until the daemon restarts: %w", err)
	}
	return true, nil
}

func (m *RunManager) holdPush(params *ipc.PushReceivedParams) error {
	m.restart.heldMu.Lock()
	defer m.restart.heldMu.Unlock()
	return m.appendHeldPushLocked(params)
}

func (m *RunManager) appendHeldPushLocked(params *ipc.PushReceivedParams) error {
	line, err := json.Marshal(params)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(m.paths.HeldPushesFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	slog.Info("push held until the daemon restarts for an update", "ref", params.Ref, "new", params.New, "gate", params.Gate)
	return nil
}

func heldPushesWaiting(heldPath string) bool {
	for _, path := range []string{heldPath, heldPath + ".replay"} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func (m *RunManager) replayHeldPushes(ctx context.Context) {
	path := m.paths.HeldPushesFile()
	replayPath := path + ".replay"
	defer m.restart.replaying.Store(false)
	for ctx.Err() == nil && !m.restart.pending.Load() {
		m.restart.heldMu.Lock()
		if _, err := os.Stat(replayPath); errors.Is(err, fs.ErrNotExist) {
			if err := os.Rename(path, replayPath); err != nil {
				// Nothing is left to replay: fresh pushes start directly from
				// here on, released under the same lock that holds them.
				m.restart.replaying.Store(false)
				m.restart.heldMu.Unlock()
				if !errors.Is(err, fs.ErrNotExist) {
					slog.Error("failed to claim held pushes; they were not started", "path", path, "error", err)
				}
				return
			}
		}
		m.restart.heldMu.Unlock()
		if !m.replayHeldPushFile(ctx, replayPath) {
			return
		}
	}
}

func (m *RunManager) replayHeldPushFile(ctx context.Context, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("failed to read held pushes; they were not started", "path", path, "error", err)
		return false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), ipc.MaxFrameBytes)
	offset := 0
	for scanner.Scan() {
		start := offset
		offset += len(scanner.Bytes()) + 1
		if ctx.Err() != nil {
			return false
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var params ipc.PushReceivedParams
		if err := json.Unmarshal(line, &params); err != nil {
			slog.Error("skipping unreadable held push", "error", err)
			continue
		}
		runID, err := m.handlePushReceived(ctx, &params, true)
		if errors.Is(err, errReplayDeferred) {
			if err := keepUnreplayedPushes(path, data[start:]); err != nil {
				slog.Error("failed to keep the held pushes not yet replayed", "path", path, "error", err)
			} else {
				slog.Info("restart scheduled during held push replay; the rest replay first on the next start", "ref", params.Ref, "new", params.New)
			}
			return false
		}
		if err != nil {
			if ctx.Err() != nil || m.shuttingDown.Load() {
				slog.Warn("daemon stopping during held push replay; the rest replay on the next start", "ref", params.Ref, "new", params.New, "error", err)
				return false
			}
			slog.Error("held push could not be started after restart", "ref", params.Ref, "new", params.New, "error", err)
			continue
		}
		slog.Info("started held push after restart", "ref", params.Ref, "new", params.New, "run_id", runID)
	}
	if err := scanner.Err(); err != nil {
		slog.Error("failed to read held pushes; the rest were not started", "path", path, "error", err)
		return false
	}
	if err := os.Remove(path); err != nil {
		slog.Error("failed to remove replayed held pushes", "path", path, "error", fmt.Errorf("remove: %w", err))
		return false
	}
	return true
}

func keepUnreplayedPushes(path string, rest []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, rest, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *RunManager) gateClaimGuard() (func(), bool) {
	release, err := m.beginGateResponse()
	if err != nil {
		return nil, false
	}
	return release, true
}

func countHeldPushes(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count
}
