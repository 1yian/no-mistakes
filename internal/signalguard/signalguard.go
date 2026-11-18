//go:build unix

package signalguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/runenv"
)

const (
	SpawnerEnv  = "NO_MISTAKES_SIGNAL_SPAWNER"
	WorktreeEnv = "NO_MISTAKES_SIGNAL_WORKTREE"

	helperArg    = "__signal-guard"
	bashKillName = "BASH_FUNC_kill%%"
)

var nameWideTools = []string{"pkill", "killall", "killall5", "skill"}

type Guard struct {
	dir string
}

func Install(dir string) (*Guard, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate no-mistakes executable: %w", err)
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for _, name := range append([]string{"kill"}, nameWideTools...) {
		script := "#!/bin/sh\nexec " + shellQuote(exe) + " " + helperArg + " " + shellQuote(dir) + " " + name + " \"$@\"\n"
		if err := writeScript(filepath.Join(dir, name), script); err != nil {
			return nil, err
		}
	}
	return &Guard{dir: dir}, nil
}

func (g *Guard) Apply(base runenv.Overlay, worktree string) runenv.Overlay {
	if g == nil {
		return base
	}
	out := base.Clone()
	if out.Set == nil {
		out.Set = make(map[string]string)
	}
	out.Set[SpawnerEnv] = strconv.Itoa(os.Getpid())
	out.Set[WorktreeEnv] = worktree
	out.Set[bashKillName] = "() { case \"$*\" in *%*) builtin kill \"$@\";; *) " + shellQuote(filepath.Join(g.dir, "kill")) + " \"$@\";; esac; }"
	out.PathPrepend = append([]string{g.dir}, out.PathPrepend...)
	return out
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func writeScript(path, content string) error {
	if current, err := os.ReadFile(path); err == nil && string(current) == content {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm() == 0o755 {
			return nil
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".guard-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
