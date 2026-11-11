package agent

import (
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
)

const GateRoleEnvVar = "NO_MISTAKES_GATE"

const CompactAdviserDisableEnvVar = "COMPACT_ADVISER_DISABLE"

type subprocessContext struct {
	environment runenv.Overlay
}

func newSubprocessContext(environment runenv.Overlay) subprocessContext {
	return subprocessContext{environment: environment.Clone()}
}

func (c subprocessContext) gitSafeEnv(dir string, extra ...[]string) []string {
	return gitSafeEnvWithOverlay(dir, c.environment, extra...)
}

func (c subprocessContext) overlay() runenv.Overlay {
	return c.environment.Clone()
}

func gitSafeEnv(dir string, extra ...[]string) []string {
	return gitSafeEnvWithOverlay(dir, runenv.Overlay{}, extra...)
}

func gitSafeEnvWithOverlay(dir string, overlay runenv.Overlay, extra ...[]string) []string {
	base := overlay.Apply(nil)
	env := git.NonInteractiveEnvFrom(base, dir)
	if len(extra) > 0 {
		env = append(env, extra[0]...)
	}
	return append(git.SubprocessEnvironment(env), GateRoleEnvVar+"=1", CompactAdviserDisableEnvVar+"=1")
}
