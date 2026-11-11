package agent

import (
	"fmt"
	"sync"
	"time"
)

const (
	LifecyclePhaseStart       = "start"
	LifecyclePhaseExit        = "exit"
	LifecyclePhaseRetry       = "retry"
	LifecyclePhaseFallback    = "fallback"
	LifecyclePhaseQuotaSwitch = "quota_switch"
	LifecyclePhaseActivity    = "activity"
)

const nativeAgentActivityInterval = 5 * time.Second

func emitAgentStarted(opts RunOpts, name string, pid int) {
	emitLifecycle(opts, LifecycleEvent{
		Agent:   name,
		Phase:   LifecyclePhaseStart,
		PID:     pid,
		Message: fmt.Sprintf("%s started pid=%d", name, pid),
	})
}

func emitAgentExited(opts RunOpts, name string, pid int, err error) {
	message := fmt.Sprintf("%s exited pid=%d status=success", name, pid)
	if err != nil {
		message = fmt.Sprintf("%s exited pid=%d error=%s", name, pid, err.Error())
	}
	emitLifecycle(opts, LifecycleEvent{
		Agent:   name,
		Phase:   LifecyclePhaseExit,
		PID:     pid,
		Message: message,
	})
}

func nativeAgentActivityObserver(opts RunOpts, name string) func() {
	if opts.OnLifecycle == nil {
		return nil
	}
	var (
		mu       sync.Mutex
		lastEmit time.Time
	)
	return func() {
		now := time.Now()
		mu.Lock()
		if !lastEmit.IsZero() && now.Sub(lastEmit) < nativeAgentActivityInterval {
			mu.Unlock()
			return
		}
		lastEmit = now
		mu.Unlock()
		emitLifecycle(opts, LifecycleEvent{
			Agent:   name,
			Phase:   LifecyclePhaseActivity,
			Message: fmt.Sprintf("%s producing output", name),
		})
	}
}

func emitAgentRetry(opts RunOpts, name string, label string, attempt, max int) {
	message := fmt.Sprintf("%s retrying after transient error %q (attempt %d/%d)", name, label, attempt, max)
	emitAgentControl(opts, LifecycleEvent{
		Agent:   name,
		Phase:   LifecyclePhaseRetry,
		Message: message,
	})
}

func emitAgentFallback(opts RunOpts, current, next string, err error) {
	emitAgentControl(opts, LifecycleEvent{
		Agent:   current,
		Phase:   LifecyclePhaseFallback,
		Message: fmt.Sprintf("agent %s failed (%s); falling back to %s", current, fallbackReason(err), next),
	})
}

func emitAgentQuotaSwitch(opts RunOpts, current, next string, err error) {
	emitAgentControl(opts, LifecycleEvent{
		Agent:     current,
		Phase:     LifecyclePhaseQuotaSwitch,
		NextAgent: next,
		Message:   fmt.Sprintf("agent %s exhausted its quota (%s); switching to %s with a fresh session", current, fallbackReason(err), next),
	})
}

func emitAgentControl(opts RunOpts, event LifecycleEvent) {
	if opts.OnLifecycle != nil {
		emitLifecycle(opts, event)
		return
	}
	if opts.OnChunk != nil {
		opts.OnChunk(event.Message)
	}
}

func emitLifecycle(opts RunOpts, event LifecycleEvent) {
	if opts.OnLifecycle != nil {
		opts.OnLifecycle(event)
	}
}
