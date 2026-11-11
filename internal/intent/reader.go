package intent

import (
	"context"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/runenv"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role      Role
	Text      string
	FilePaths []string
	Timestamp time.Time
	Synthetic bool
}

type Session struct {
	AgentName    string
	SessionID    string
	CWD          string
	StartedAt    time.Time
	LastActivity time.Time
	LastMsgKey   string
	Messages     []Message

	startedAtPath string
}

type DiscoverOpts struct {
	TranscriptHomes *runenv.TranscriptHomes
	HomeDir         string
	OriginCWD       string
	WindowStart     time.Time
	WindowEnd       time.Time
}

type Reader interface {
	Name() string
	Discover(ctx context.Context, opts DiscoverOpts) ([]*Session, error)
	Load(ctx context.Context, s *Session) error
}
