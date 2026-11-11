//go:build !linux

package shellenv

import "errors"

var ErrOutOfMemory = errors.New("ran out of memory")

func OwnOOMScoreScript(script string) string { return script }

func RaiseStepOOMScore(int) {}

func OOMKillBaseline() (uint64, bool) { return 0, false }

func AttributeOOMKill(_ uint64, _ bool, err error) error { return err }
