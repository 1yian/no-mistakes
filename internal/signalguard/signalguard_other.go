//go:build !unix

package signalguard

import "github.com/kunchenguid/no-mistakes/internal/runenv"

type Guard struct{}

func Install(dir string) (*Guard, error) { return nil, nil }

func (g *Guard) Apply(base runenv.Overlay, worktree string) runenv.Overlay { return base }

func RunHelper(args []string) (bool, int) { return false, 0 }
