package evidence

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/safepath"
	"github.com/kunchenguid/no-mistakes/internal/safeurl"
)

const (
	maxFiles      = 500
	maxTotalBytes = int64(256 << 20)
	maxFileBytes  = int64(64 << 20)

	fetchRef = "refs/no-mistakes/evidence-fetch"

	scratchIndexName = "no-mistakes-evidence-index"
)

type Request struct {
	RepoDir           string
	PushURL           string
	Branch            string
	Dir               string
	Segments          []string
	SourceDir         string
	ExcludeDirs       []string
	RunWorktree       string
	WorktreesRoot     string
	Message           string
	ForbiddenBranches []string
}

type Result struct {
	Branch    string
	CommitSHA string
	Dir       string
	Files     []string
}

func Publish(ctx context.Context, req Request) (*Result, error) {
	branch, err := NormalizeBranch(req.Branch)
	if err != nil {
		return nil, err
	}
	for _, forbidden := range req.ForbiddenBranches {
		if strings.TrimSpace(forbidden) != "" && strings.EqualFold(strings.TrimSpace(forbidden), branch) {
			return nil, fmt.Errorf("evidence branch %q is also a code branch of this repository; configure a different test.evidence.branch", branch)
		}
	}
	if strings.TrimSpace(req.PushURL) == "" {
		return nil, fmt.Errorf("no push target for evidence branch %q", branch)
	}

	dir := normalizeInBranchDir(req.Dir, req.Segments)
	files, err := collectFiles(req.SourceDir, req.ExcludeDirs)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		result, err := publishOnce(ctx, req, branch, dir, files)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isPushRace(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func publishOnce(ctx context.Context, req Request, branch, dir string, files []collectedFile) (*Result, error) {
	tip, err := remoteTip(ctx, req.RepoDir, req.PushURL, branch)
	if err != nil {
		return nil, err
	}
	if tip != "" {
		if err := assertEvidenceBranch(ctx, req.RepoDir, tip, branch); err != nil {
			return nil, err
		}
	}

	indexDir, err := os.MkdirTemp("", scratchIndexName)
	if err != nil {
		return nil, fmt.Errorf("create evidence index: %w", err)
	}
	defer os.RemoveAll(indexDir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(indexDir, "index")}

	if tip != "" {
		if _, err := git.RunWithEnv(ctx, req.RepoDir, env, "read-tree", tip); err != nil {
			return nil, fmt.Errorf("read evidence branch tree: %w", err)
		}
	}
	if err := addMarker(ctx, req.RepoDir, env, indexDir); err != nil {
		return nil, err
	}

	published := make([]string, 0, len(files))
	for _, file := range files {
		inBranch := path.Join(dir, file.Rel)
		blob, err := hashEvidenceFile(ctx, req, env, file.Abs)
		if err != nil {
			return nil, fmt.Errorf("hash evidence file %s: %w", file.Rel, err)
		}
		if err := addToIndex(ctx, req.RepoDir, env, file.Mode, blob, inBranch); err != nil {
			return nil, err
		}
		published = append(published, file.Rel)
	}

	tree, err := git.RunWithEnv(ctx, req.RepoDir, env, "write-tree")
	if err != nil {
		return nil, fmt.Errorf("write evidence tree: %w", err)
	}

	if tip != "" {
		tipTree, err := git.Run(ctx, req.RepoDir, "rev-parse", "--verify", tip+"^{tree}")
		if err == nil && tipTree == tree {
			// The branch already holds exactly these bytes (a re-run of the
			// same step). Link against the existing commit instead of pushing
			// an empty one.
			return &Result{Branch: branch, CommitSHA: tip, Dir: dir, Files: published}, nil
		}
	}

	commit, err := commitTree(ctx, req.RepoDir, tree, tip, req.Message)
	if err != nil {
		return nil, err
	}
	if _, err := git.Run(ctx, req.RepoDir, "push", req.PushURL, commit+":refs/heads/"+branch); err != nil {
		return nil, fmt.Errorf("push evidence branch %s to %s: %w", branch, safeurl.Redact(req.PushURL), err)
	}
	return &Result{Branch: branch, CommitSHA: commit, Dir: dir, Files: published}, nil
}

func hashEvidenceFile(ctx context.Context, req Request, env []string, abs string) (string, error) {
	content, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if utf8.Valid(content) && !bytes.Contains(content, []byte{0}) {
		redacted := safepath.RedactText(safepath.RedactWorktrees(string(content), req.RunWorktree, req.WorktreesRoot))
		if redacted != string(content) {
			return git.RunWithInput(ctx, req.RepoDir, redacted, "hash-object", "-w", "--stdin")
		}
	}
	return git.RunWithEnv(ctx, req.RepoDir, env, "hash-object", "-w", "--", abs)
}

func remoteTip(ctx context.Context, repoDir, pushURL, branch string) (string, error) {
	out, err := git.Run(ctx, repoDir, "ls-remote", "--heads", pushURL, "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("read evidence branch %s on %s: %w", branch, safeurl.Redact(pushURL), err)
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	if _, err := git.Run(ctx, repoDir, "fetch", "--no-tags", "--force", pushURL, "+refs/heads/"+branch+":"+fetchRef); err != nil {
		return "", fmt.Errorf("fetch evidence branch %s: %w", branch, err)
	}
	sha, err := git.Run(ctx, repoDir, "rev-parse", "--verify", fetchRef+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve fetched evidence branch %s: %w", branch, err)
	}
	return sha, nil
}

func assertEvidenceBranch(ctx context.Context, repoDir, tip, branch string) error {
	content, err := git.RunRaw(ctx, repoDir, "cat-file", "blob", tip+":"+MarkerPath)
	if err != nil || string(content) != MarkerContent {
		return fmt.Errorf("refusing to publish evidence: branch %q already exists and is not a no-mistakes evidence branch (invalid %s marker at its tip)", branch, MarkerPath)
	}
	return nil
}

func addMarker(ctx context.Context, repoDir string, env []string, indexDir string) error {
	markerFile := filepath.Join(indexDir, "marker")
	if err := os.WriteFile(markerFile, []byte(MarkerContent), 0o644); err != nil {
		return fmt.Errorf("write evidence marker: %w", err)
	}
	blob, err := git.RunWithEnv(ctx, repoDir, env, "hash-object", "-w", "--", markerFile)
	if err != nil {
		return fmt.Errorf("hash evidence marker: %w", err)
	}
	return addToIndex(ctx, repoDir, env, "100644", blob, MarkerPath)
}

func addToIndex(ctx context.Context, repoDir string, env []string, mode, blob, inBranchPath string) error {
	if _, err := git.RunWithEnv(ctx, repoDir, env, "update-index", "--add", "--cacheinfo", mode+","+blob+","+inBranchPath); err != nil {
		return fmt.Errorf("add %s to evidence index: %w", inBranchPath, err)
	}
	return nil
}

func commitTree(ctx context.Context, repoDir, tree, parent, message string) (string, error) {
	var env []string
	if identity, bound := git.CommitIdentityFromContext(ctx); bound {
		if err := identity.Validate(); err != nil {
			return "", err
		}
	} else {
		name := configuredIdentity(ctx, repoDir, "user.name", "no-mistakes")
		email := configuredIdentity(ctx, repoDir, "user.email", "no-mistakes@localhost")
		env = []string{
			"GIT_AUTHOR_NAME=" + name,
			"GIT_AUTHOR_EMAIL=" + email,
			"GIT_COMMITTER_NAME=" + name,
			"GIT_COMMITTER_EMAIL=" + email,
		}
	}
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	if strings.TrimSpace(message) == "" {
		message = "no-mistakes: test evidence"
	}
	args = append(args, "-m", message)
	commit, err := git.RunWithEnv(ctx, repoDir, env, args...)
	if err != nil {
		return "", fmt.Errorf("create evidence commit: %w", err)
	}
	return commit, nil
}

func configuredIdentity(ctx context.Context, repoDir, key, fallback string) string {
	value, err := git.Run(ctx, repoDir, "config", "--get", "--default", "", key)
	if err != nil || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func isPushRace(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "non-fast-forward") ||
		strings.Contains(text, "fetch first") ||
		strings.Contains(text, "stale info")
}

type collectedFile struct {
	Abs  string
	Rel  string
	Mode string
}

func collectFiles(root string, exclude []string) ([]collectedFile, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	excluded := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		if name = strings.Trim(strings.TrimSpace(name), "/"); name != "" {
			excluded[name] = true
		}
	}
	var files []collectedFile
	var total int64
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if rel, relErr := filepath.Rel(root, p); relErr == nil && excluded[filepath.ToSlash(rel)] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxFileBytes {
			return fmt.Errorf("evidence file %s is %d bytes, over the %d byte limit", d.Name(), info.Size(), maxFileBytes)
		}
		total += info.Size()
		if total > maxTotalBytes {
			return fmt.Errorf("evidence exceeds the %d byte publication limit", maxTotalBytes)
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		slashed := filepath.ToSlash(rel)
		if !safeInBranchPath(slashed) {
			return nil
		}
		mode := "100644"
		if info.Mode()&0o111 != 0 {
			mode = "100755"
		}
		files = append(files, collectedFile{Abs: p, Rel: slashed, Mode: mode})
		if len(files) > maxFiles {
			return fmt.Errorf("evidence has more than %d files", maxFiles)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, nil
}

func safeInBranchPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, component := range strings.Split(p, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func normalizeInBranchDir(dir string, segments []string) string {
	var parts []string
	for _, raw := range append(strings.Split(filepath.ToSlash(strings.TrimSpace(dir)), "/"), segments...) {
		component := strings.TrimSpace(raw)
		if component == "" || component == "." || component == ".." || strings.Contains(component, "/") {
			continue
		}
		parts = append(parts, component)
	}
	joined := path.Join(parts...)
	if joined == "." || !safeInBranchPath(joined) {
		return ""
	}
	return joined
}
