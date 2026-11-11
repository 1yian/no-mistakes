package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

var ErrSingletonLockHeld = errors.New("a no-mistakes daemon is already running for this NM_HOME")

const singletonLockRetry = 250 * time.Millisecond

type singletonLock struct {
	file *os.File
}

func acquireSingletonLock(p *paths.Paths) (*singletonLock, error) {
	path := p.LockFile()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock %s: %w", path, err)
	}
	lockErr := tryLockFile(f)
	for deadline := time.Now().Add(singletonLockRetry); lockErr != nil && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		lockErr = tryLockFile(f)
	}
	if lockErr != nil {
		holder := readLockHolder(f)
		f.Close()
		if holder != "" {
			return nil, fmt.Errorf("%w (%s): %w", ErrSingletonLockHeld, holder, lockErr)
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrSingletonLockHeld, path, lockErr)
	}

	// Best-effort diagnostics: record who holds the lock so a rejected
	// second daemon (or an operator) can identify the live owner. This write
	// is not part of the safety mechanism itself - the OS lock already
	// provides that - so a failure here is non-fatal.
	record := lockHolderRecord{PID: os.Getpid(), StartedAt: time.Now().UTC()}
	if data, err := json.Marshal(record); err == nil {
		_ = f.Truncate(0)
		_, _ = f.WriteAt(data, 0)
		_ = f.Sync()
	}

	return &singletonLock{file: f}, nil
}

func (l *singletonLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockFile(l.file)
	_ = l.file.Close()
}

type lockHolderRecord struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

func readLockHolder(f *os.File) string {
	data := make([]byte, 4096)
	n, err := f.ReadAt(data, 0)
	if err != nil && n == 0 {
		return ""
	}
	var record lockHolderRecord
	if err := json.Unmarshal(data[:n], &record); err != nil || record.PID <= 0 {
		return ""
	}
	return fmt.Sprintf("pid %d, started %s", record.PID, record.StartedAt.Format(time.RFC3339))
}
