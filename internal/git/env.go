package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/runenv"
)

type environmentContextKey struct{}

func WithEnvironment(ctx context.Context, overlay runenv.Overlay) context.Context {
	if overlay.Empty() {
		return ctx
	}
	return context.WithValue(ctx, environmentContextKey{}, overlay.Clone())
}

func nonInteractiveEnvForContext(ctx context.Context, dir string) []string {
	base := os.Environ()
	if ctx != nil {
		if overlay, ok := ctx.Value(environmentContextKey{}).(runenv.Overlay); ok {
			base = overlay.Apply(base)
		}
	}
	return NonInteractiveEnvFrom(ApplyCommitIdentity(ctx, base), dir)
}

func NonInteractiveEnv(dir string) []string {
	return NonInteractiveEnvFrom(os.Environ(), dir)
}

func NonInteractiveEnvFrom(base []string, dir string) []string {
	if base == nil {
		base = os.Environ()
	}
	env := append(append([]string(nil), base...),
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"GIT_TERMINAL_PROMPT=0",
		// Read-only commands such as status and rev-parse must not refresh the
		// index as a side effect. Mutating commands still take required locks.
		"GIT_OPTIONAL_LOCKS=0",
	)
	// Mirror os/exec, which only injects PWD when Cmd.Env is nil, skips it on
	// these platforms, and absolutizes Cmd.Dir first (go.dev/issue/50599):
	// POSIX defines PWD as "an absolute pathname of the current working
	// directory". Injecting a relative dir verbatim (for example ".") poisons
	// every descendant that trusts PWD — macOS /bin/sh is bash 3.2, whose pwd
	// builtin reports "." when PWD="." leaks through git receive-pack into a
	// hook, which is how the post-receive hook of issue #269 ended up passing
	// `--gate .`.
	if dir != "" && runtime.GOOS != "windows" && runtime.GOOS != "plan9" {
		if abs, err := filepath.Abs(dir); err == nil {
			env = append(env, "PWD="+abs)
		}
	}
	return env
}

func SubprocessEnvironment(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		switch key {
		case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
			"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
			"GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS":
			continue
		}
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}
