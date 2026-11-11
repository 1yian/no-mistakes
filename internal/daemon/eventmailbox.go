package daemon

import (
	"context"
	"sync"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
)

const (
	mailboxMaxEvents   = 64
	mailboxMaxBytes    = 1 << 20
	eventOverheadBytes = 128
)

type eventMailbox struct {
	runID string

	mu     sync.Mutex
	ring   []ipc.Event
	bytes  int
	gap    bool
	gapRev int64
	closed bool

	droppedActivity uint64
	coalescedState  uint64

	signal chan struct{}
}

func newEventMailbox(runID string, startRev int64) *eventMailbox {
	return &eventMailbox{
		runID:  runID,
		ring:   make([]ipc.Event, 0, mailboxMaxEvents),
		signal: make(chan struct{}, 1),
		gap:    true,
		gapRev: startRev,
	}
}

func eventBytes(e ipc.Event) int {
	n := eventOverheadBytes
	if e.Content != nil {
		n += len(*e.Content)
	}
	if e.Findings != nil {
		n += len(*e.Findings)
	}
	if e.Error != nil {
		n += len(*e.Error)
	}
	return n
}

func (mb *eventMailbox) publish(e ipc.Event) {
	mb.mu.Lock()
	if mb.closed {
		mb.mu.Unlock()
		return
	}
	size := eventBytes(e)
	if ipc.ClassOf(e.Type) == ipc.ClassActivity {
		if mb.hasRoomLocked(size) {
			mb.ring = append(mb.ring, e)
			mb.bytes += size
		} else {
			// Drop the newest. Dropping the oldest would rewrite log history
			// the consumer has not read yet; this keeps a contiguous prefix.
			mb.droppedActivity++
		}
		mb.mu.Unlock()
		mb.ping()
		return
	}

	// State (and any unrecognised type, which fails safe to state).
	switch {
	case mb.gap:
		// A gap is already outstanding. Queuing a delta behind it would let
		// a pre-snapshot state regress the post-snapshot render, so fold it
		// in instead. This is what keeps the signal coalescing.
		mb.foldLocked(e)
	default:
		if !mb.hasRoomLocked(size) {
			mb.evictActivityLocked(size)
		}
		if mb.hasRoomLocked(size) {
			mb.ring = append(mb.ring, e)
			mb.bytes += size
		} else {
			mb.foldLocked(e)
		}
	}
	mb.mu.Unlock()
	mb.ping()
}

func (mb *eventMailbox) foldLocked(e ipc.Event) {
	mb.gap = true
	mb.coalescedState++
	if e.StateRev > mb.gapRev {
		mb.gapRev = e.StateRev
	}
}

func (mb *eventMailbox) hasRoomLocked(size int) bool {
	return len(mb.ring) < mailboxMaxEvents && mb.bytes+size <= mailboxMaxBytes
}

func (mb *eventMailbox) evictActivityLocked(size int) {
	for i := 0; i < len(mb.ring); {
		if ipc.ClassOf(mb.ring[i].Type) != ipc.ClassActivity {
			i++
			continue
		}
		mb.bytes -= eventBytes(mb.ring[i])
		mb.ring = append(mb.ring[:i], mb.ring[i+1:]...)
		mb.droppedActivity++
		if mb.hasRoomLocked(size) {
			return
		}
	}
}

func (mb *eventMailbox) ping() {
	select {
	case mb.signal <- struct{}{}:
	default:
	}
}

func (mb *eventMailbox) close() {
	mb.mu.Lock()
	mb.closed = true
	mb.mu.Unlock()
	mb.ping()
}

func (mb *eventMailbox) release() {
	mb.mu.Lock()
	mb.ring = nil
	mb.bytes = 0
	mb.gap = false
	mb.gapRev = 0
	mb.closed = true
	mb.mu.Unlock()
	mb.ping()
}

func (mb *eventMailbox) next(ctx context.Context) (ipc.Event, bool) {
	for {
		mb.mu.Lock()
		if mb.gap {
			mb.gap = false
			rev := mb.gapRev
			mb.gapRev = 0
			mb.mu.Unlock()
			// The gap drains ahead of queued payload on purpose: a coalesced
			// invalidation must reach the consumer before it renders more
			// stale frames, and the revision guard makes the deltas queued
			// behind it harmless no-ops.
			return ipc.Event{Type: ipc.EventStreamGap, RunID: mb.runID, StateRev: rev}, true
		}
		if len(mb.ring) > 0 {
			e := mb.ring[0]
			mb.ring = mb.ring[1:]
			mb.bytes -= eventBytes(e)
			mb.mu.Unlock()
			return e, true
		}
		closed := mb.closed
		mb.mu.Unlock()
		if closed {
			return ipc.Event{}, false
		}
		select {
		case <-mb.signal:
		case <-ctx.Done():
			return ipc.Event{}, false
		}
	}
}

func (mb *eventMailbox) stats() (queued, queuedBytes int, gapPending bool, droppedActivity, coalescedState uint64) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	return len(mb.ring), mb.bytes, mb.gap, mb.droppedActivity, mb.coalescedState
}
