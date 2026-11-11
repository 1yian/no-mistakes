package pipeline

import (
	"sync"

	"github.com/kunchenguid/no-mistakes/internal/db"
)

type PublishedPush struct {
	RunID           string
	RepoID          string
	Binding         db.PushBinding
	PreviousHeadSHA string
}

type HousekeepingLintResult struct {
	FindingsJSON string
	Summary      string
}

type RunShared struct {
	mu               sync.Mutex
	prepared         bool
	housekeepingLint *HousekeepingLintResult
	publishedPush    *PublishedPush
}

func (s *RunShared) SetPublishedPush(result PublishedPush) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishedPush = &result
}

func (s *RunShared) PublishedPush() (PublishedPush, bool) {
	if s == nil {
		return PublishedPush{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publishedPush == nil {
		return PublishedPush{}, false
	}
	return *s.publishedPush, true
}

func (s *RunShared) EnsurePrepared(prepare func() error) error {
	if s == nil {
		return prepare()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prepared {
		return nil
	}
	if err := prepare(); err != nil {
		return err
	}
	s.prepared = true
	return nil
}

func (s *RunShared) SetHousekeepingLint(result HousekeepingLintResult) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.housekeepingLint = &result
}

func (s *RunShared) ClearHousekeepingLint() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.housekeepingLint = nil
}

func (s *RunShared) TakeHousekeepingLint() (HousekeepingLintResult, bool) {
	if s == nil {
		return HousekeepingLintResult{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.housekeepingLint == nil {
		return HousekeepingLintResult{}, false
	}
	result := *s.housekeepingLint
	s.housekeepingLint = nil
	return result, true
}
