package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/safeurl"
	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

const (
	refHead          = "head"
	refSourceHead    = "source-head"
	refBase          = "base"
	refTrustedConfig = "trusted-config"
	caseRefNamespace = "refs/no-mistakes/eval/"
)

func caseRefNames() []string {
	return []string{refHead, refSourceHead, refBase, refTrustedConfig}
}

func caseRefPrefix(caseID string) string { return caseRefNamespace + caseID + "/" }

func (s *Store) poolDir(repoFingerprint string) string {
	name := strings.TrimSpace(repoFingerprint)
	if name == "" {
		name = "unknown"
	}
	return filepath.Join(s.root, "pools", name+".git")
}

func initializeObjectPool(ctx context.Context, pool string) error {
	if err := git.InitBare(ctx, pool); err != nil {
		return fmt.Errorf("create eval object pool: %w", err)
	}
	// Case refs intentionally carry both the case ID and a descriptive commit
	// name. In a normal Windows temp or user-data path their lock files can
	// exceed Git for Windows' legacy MAX_PATH handling even though the
	// filesystem supports the path. Keep this repository-local so capture does
	// not depend on or modify the operator's global Git configuration.
	if _, err := git.Run(ctx, pool, "config", "core.longpaths", "true"); err != nil {
		return fmt.Errorf("enable long paths in eval object pool: %w", err)
	}
	return nil
}

func (s *Store) storeCaseObjects(ctx context.Context, gateDir, repoFingerprint, caseID string, refs map[string]string) error {
	pool := s.poolDir(repoFingerprint)
	if err := initializeObjectPool(ctx, pool); err != nil {
		return err
	}
	// The staging clone goes under the eval root rather than the system temp
	// directory because both live on the same filesystem as the gate, which is
	// what lets Git hardlink its objects instead of copying them. Across
	// filesystems that clone would be a full byte-for-byte copy of the
	// repository on every single capture.
	tmp, err := os.MkdirTemp(s.root, ".objects-")
	if err != nil {
		return fmt.Errorf("create temporary capture directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	clone := filepath.Join(tmp, "source.git")
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", "--quiet", gateDir, clone)
	shellenv.ConfigureShellCommand(cmd)
	if err := shellenv.RunShellCommand(cmd); err != nil {
		// This error can carry Git's own output, and the gate's remote may
		// embed a credential, so it is redacted like every other git-run error
		// before it can reach a log.
		return fmt.Errorf("stage source gate objects for capture: %s", safeurl.RedactText(err.Error()))
	}
	prefix := caseRefPrefix(caseID)
	for _, name := range caseRefNames() {
		sha := strings.TrimSpace(refs[name])
		if sha == "" {
			return fmt.Errorf("case %q is missing its %s commit", caseID, name)
		}
		if _, err := git.Run(ctx, clone, "update-ref", prefix+name, sha); err != nil {
			return fmt.Errorf("pin case %s commit: %w", name, err)
		}
	}
	// --no-tags keeps the pool's refs to exactly the case pins. A tag ref
	// would pin objects no case needs and survive every retention pass,
	// quietly making the pool unreclaimable.
	if _, err := git.Run(ctx, pool, "fetch", "--no-tags", clone, "+"+prefix+"*:"+prefix+"*"); err != nil {
		return fmt.Errorf("store case objects: %w", err)
	}
	return nil
}

func restoreCaseObjects(ctx context.Context, poolPath, gateDir, caseID string) error {
	if err := git.ValidateBareRepository(ctx, poolPath); err != nil {
		return fmt.Errorf("case objects are unavailable: %w", err)
	}
	prefix := caseRefPrefix(caseID)
	if _, err := git.Run(ctx, gateDir, "fetch", "--no-tags", poolPath, "+"+prefix+"*:"+prefix+"*"); err != nil {
		return fmt.Errorf("restore case objects: %w", err)
	}
	return nil
}

func dropCaseObjects(ctx context.Context, poolPath, caseID string) error {
	if _, err := os.Stat(poolPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect eval object pool: %w", err)
	}
	if err := git.ValidateBareRepository(ctx, poolPath); err != nil {
		return fmt.Errorf("validate eval object pool: %w", err)
	}
	prefix := caseRefPrefix(caseID)
	var failures []string
	for _, name := range caseRefNames() {
		if _, err := git.Run(ctx, poolPath, "update-ref", "-d", prefix+name); err != nil {
			failures = append(failures, name)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("release case %q objects: %s", caseID, strings.Join(failures, ", "))
	}
	refs, err := git.Run(ctx, poolPath, "for-each-ref", "--format=%(refname)", caseRefNamespace)
	if err != nil {
		return fmt.Errorf("inspect remaining eval pool pins: %w", err)
	}
	if strings.TrimSpace(refs) == "" {
		if err := os.RemoveAll(poolPath); err != nil {
			return fmt.Errorf("remove empty eval object pool: %w", err)
		}
		return nil
	}
	// --auto respects Git's own maintenance thresholds, so a retention pass
	// stays cheap: it schedules repacking when the pool has actually grown
	// instead of paying a full repack for every dropped case.
	if _, err := git.Run(ctx, poolPath, "gc", "--auto", "--quiet"); err != nil {
		return fmt.Errorf("reclaim eval pool space: %w", err)
	}
	return nil
}
