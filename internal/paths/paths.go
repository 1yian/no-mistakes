package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type Paths struct {
	root string
}

func New() (*Paths, error) {
	if env := os.Getenv("NM_HOME"); env != "" {
		return &Paths{root: env}, nil
	}
	if testing.Testing() && os.Getenv("NO_MISTAKES_ALLOW_DEFAULT_ROOT_IN_TESTS") != "1" {
		return nil, fmt.Errorf("NM_HOME must be set under go test to avoid touching the real no-mistakes daemon root")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Paths{root: filepath.Join(home, ".no-mistakes")}, nil
}

func ForGate(gatePath string) (*Paths, error) {
	clean := filepath.Clean(gatePath)
	if !strings.HasSuffix(clean, ".git") {
		return nil, fmt.Errorf("cannot derive the gate home that owns %q: not a managed gate path (expected <root>/repos/<id>.git)", gatePath)
	}
	reposDir := filepath.Dir(clean)
	if filepath.Base(reposDir) != "repos" {
		return nil, fmt.Errorf("cannot derive the gate home that owns %q: not under a repos directory (expected <root>/repos/<id>.git)", gatePath)
	}
	// filepath.Dir("/repos") is "/", so a gate directly under the filesystem
	// root resolves to "/" rather than an empty root.
	return &Paths{root: filepath.Dir(reposDir)}, nil
}

func WithRoot(root string) *Paths {
	return &Paths{root: root}
}

func (p *Paths) Root() string       { return p.root }
func (p *Paths) DB() string         { return filepath.Join(p.root, "state.sqlite") }
func (p *Paths) Socket() string     { return filepath.Join(p.root, "socket") }
func (p *Paths) PIDFile() string    { return filepath.Join(p.root, "daemon.pid") }
func (p *Paths) ConfigFile() string { return filepath.Join(p.root, "config.yaml") }

func (p *Paths) HeldPushesFile() string { return filepath.Join(p.root, "held-pushes.jsonl") }

func (p *Paths) RunInputsDir() string { return filepath.Join(p.root, "run-inputs") }

func (p *Paths) LockFile() string { return filepath.Join(p.root, "daemon.lock") }
func (p *Paths) UpdateCheckFile() string {
	return filepath.Join(p.root, "update-check.json")
}

func (p *Paths) EvalDir() string { return filepath.Join(p.root, "eval") }

func (p *Paths) EvidenceDir() string { return filepath.Join(p.root, "evidence") }

func (p *Paths) EvidenceRoot(configured string) string {
	if c := strings.TrimSpace(configured); c != "" && filepath.IsAbs(c) {
		return filepath.Clean(c)
	}
	return p.EvidenceDir()
}

func (p *Paths) ValidateEvidenceRoot(configured string) error {
	root := p.EvidenceRoot(configured)
	worktrees := filepath.Clean(p.WorktreesDir())
	rel, err := filepath.Rel(worktrees, root)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return fmt.Errorf("test.evidence.local_root %q must not be inside managed worktrees %q", root, worktrees)
	}
	return nil
}

func (p *Paths) RunEvidenceDir(configured, runID string) string {
	return filepath.Join(p.EvidenceRoot(configured), runID)
}

func (p *Paths) ResolveEvidenceRoot(evidenceDir, localRoot string) (root, warning string) {
	defaultRoot := p.EvidenceDir()
	configured := strings.TrimSpace(evidenceDir)
	if configured == "" {
		return p.EvidenceRoot(localRoot), ""
	}
	expanded := expandEvidenceHome(configured)
	if !filepath.IsAbs(expanded) {
		return defaultRoot, fmt.Sprintf("evidence_dir %q is not an absolute path; using %s", configured, defaultRoot)
	}
	clean := filepath.Clean(expanded)
	if err := writableEvidenceDir(clean); err != nil {
		return defaultRoot, fmt.Sprintf("evidence_dir %q is missing or not writable (%v); using %s", clean, err, defaultRoot)
	}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	if p.evidenceRootInsideWorktrees(clean) {
		return defaultRoot, fmt.Sprintf("evidence_dir %q is inside managed worktrees %q; using %s", clean, p.WorktreesDir(), defaultRoot)
	}
	return clean, ""
}

func (p *Paths) evidenceRootInsideWorktrees(root string) bool {
	worktrees := filepath.Clean(p.WorktreesDir())
	rel, err := filepath.Rel(worktrees, filepath.Clean(root))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func expandEvidenceHome(path string) string {
	path = strings.TrimSpace(path)
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~\\") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func writableEvidenceDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	f, err := os.CreateTemp(path, ".nm-evidence-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

func (p *Paths) ReposDir() string { return filepath.Join(p.root, "repos") }
func (p *Paths) RepoDir(repoID string) string {
	return filepath.Join(p.root, "repos", repoID+".git")
}

func (p *Paths) WorktreesDir() string { return filepath.Join(p.root, "worktrees") }
func (p *Paths) WorktreeDir(repoID, runID string) string {
	return filepath.Join(p.root, "worktrees", repoID, runID)
}

func (p *Paths) LogsDir() string { return filepath.Join(p.root, "logs") }
func (p *Paths) RunLogDir(runID string) string {
	return filepath.Join(p.root, "logs", runID)
}
func (p *Paths) DaemonLog() string { return filepath.Join(p.root, "logs", "daemon.log") }

func (p *Paths) DaemonBootstrapLog() string {
	return filepath.Join(p.root, "logs", "daemon-bootstrap.log")
}

func (p *Paths) ManagedServerLog() string {
	return filepath.Join(p.root, "logs", "managed-server.log")
}
func (p *Paths) CLILog() string { return filepath.Join(p.root, "logs", "cli.log") }

func (p *Paths) ServerPIDsDir() string { return filepath.Join(p.root, "servers") }

func (p *Paths) EnsureDirs() error {
	dirs := []string{
		p.root,
		p.ReposDir(),
		p.WorktreesDir(),
		p.LogsDir(),
		p.ServerPIDsDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
