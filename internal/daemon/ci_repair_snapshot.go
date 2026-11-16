package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

func (m *RunManager) snapshotCIRepair(sctx *pipeline.StepContext, body, withdrawn string) (*db.CIRepairObligation, error) {
	if sctx.Run.SubmittedHeadSHA == nil || !validCIRepairSHA(*sctx.Run.SubmittedHeadSHA) {
		return nil, fmt.Errorf("guarded CI repair requires the immutable submitted head")
	}
	root := filepath.Join(m.paths.Root(), "ci-repair", sctx.Run.ID)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, "snapshot-")
	if err != nil {
		return nil, err
	}
	a := &db.CIRepairObligation{RunID: sctx.Run.ID, Owner: m.ciRepairOwner, OriginalHead: sctx.Run.HeadSHA, PRURL: *sctx.Run.PRURL, SnapshotDir: dir, Phase: "withdrawing"}
	ref := "refs/no-mistakes/ci-repair/" + a.RunID + "/published"
	submittedRef := "refs/no-mistakes/ci-repair/" + a.RunID + "/submitted"
	for anchor, head := range map[string]string{ref: a.OriginalHead, submittedRef: *sctx.Run.SubmittedHeadSHA} {
		target, exists, err := git.DirectRefTarget(sctx.Ctx, sctx.GateDir, anchor)
		if err != nil {
			return nil, err
		}
		if exists && target != head {
			return nil, fmt.Errorf("CI repair preservation anchor changed: %s", anchor)
		}
		if !exists {
			if _, err := git.Run(sctx.Ctx, sctx.GateDir, "update-ref", "--no-deref", anchor, head, strings.Repeat("0", 40)); err != nil {
				return nil, err
			}
		}
	}
	if _, err := git.Run(sctx.Ctx, sctx.GateDir, "bundle", "create", filepath.Join(dir, "head.bundle"), ref, submittedRef); err != nil {
		return nil, err
	}
	run, err := m.db.GetRun(a.RunID)
	if err != nil {
		return nil, err
	}
	rows, err := m.db.GetStepsByRun(a.RunID)
	if err != nil {
		return nil, err
	}
	rounds := map[string][]*db.StepRound{}
	for _, row := range rows {
		rounds[row.ID], err = m.db.GetRoundsByStep(row.ID)
		if err != nil {
			return nil, err
		}
	}
	history, err := json.Marshal(struct {
		Run    *db.Run
		Repo   *db.Repo
		Steps  []*db.StepResult
		Rounds map[string][]*db.StepRound
	}{run, sctx.Repo, rows, rounds})
	if err != nil {
		return nil, err
	}
	for name, content := range map[string][]byte{"history.json": history, "pr-before.txt": []byte(body), "pr-withdrawn.txt": []byte(withdrawn)} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			return nil, err
		}
	}
	for source, name := range map[string]string{sctx.EvidenceDir: "evidence", m.paths.RunLogDir(a.RunID): "logs"} {
		if source == "" {
			return nil, fmt.Errorf("missing CI repair preservation source")
		}
		if err := copyCIRepairEvidence(source, filepath.Join(dir, name)); err != nil {
			return nil, err
		}
	}
	a.SnapshotDigest, err = digestCIRepairSnapshot(dir)
	if err != nil {
		return nil, err
	}
	if err := syncCIRepairSnapshot(dir); err != nil {
		return nil, err
	}
	return a, nil
}

func (m *RunManager) verifyCIRepairSnapshot(sctx *pipeline.StepContext, a *db.CIRepairObligation) error {
	root := filepath.Join(m.paths.Root(), "ci-repair", sctx.Run.ID)
	if a.RunID != sctx.Run.ID || a.Owner != m.ciRepairOwner || sctx.Run.PRURL == nil || a.PRURL != *sctx.Run.PRURL || filepath.Dir(a.SnapshotDir) != root || !strings.HasPrefix(filepath.Base(a.SnapshotDir), "snapshot-") {
		return fmt.Errorf("CI repair preservation binding changed")
	}
	info, err := os.Lstat(a.SnapshotDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("CI repair preservation directory missing or replaced")
	}
	for _, name := range []string{"history.json", "pr-before.txt", "pr-withdrawn.txt", "head.bundle", "evidence", "logs"} {
		info, err := os.Lstat(filepath.Join(a.SnapshotDir, name))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("CI repair preservation artifact %s missing or replaced", name)
		}
	}
	digest, err := digestCIRepairSnapshot(a.SnapshotDir)
	if err != nil {
		return err
	}
	if len(a.SnapshotDigest) != 64 || digest != a.SnapshotDigest {
		return fmt.Errorf("CI repair preservation digest changed")
	}
	data, err := os.ReadFile(filepath.Join(a.SnapshotDir, "history.json"))
	if err != nil {
		return err
	}
	var history struct{ Run *db.Run }
	if json.Unmarshal(data, &history) != nil || history.Run == nil || history.Run.ID != a.RunID || history.Run.RepoID != sctx.Run.RepoID || history.Run.Branch != sctx.Run.Branch || history.Run.HeadSHA != a.OriginalHead || history.Run.LastPushedSHA == nil || *history.Run.LastPushedSHA != a.OriginalHead || history.Run.PRURL == nil || *history.Run.PRURL != a.PRURL || history.Run.SubmittedHeadSHA == nil || sctx.Run.SubmittedHeadSHA == nil || *history.Run.SubmittedHeadSHA != *sctx.Run.SubmittedHeadSHA {
		return fmt.Errorf("CI repair history head/identity binding changed")
	}
	ref := "refs/no-mistakes/ci-repair/" + a.RunID + "/published"
	submittedRef := "refs/no-mistakes/ci-repair/" + a.RunID + "/submitted"
	for anchor, head := range map[string]string{ref: a.OriginalHead, submittedRef: *history.Run.SubmittedHeadSHA} {
		target, exists, err := git.DirectRefTarget(sctx.Ctx, sctx.GateDir, anchor)
		if err != nil || !exists || target != head {
			return fmt.Errorf("CI repair preservation anchor missing or changed: %s", anchor)
		}
	}
	if _, err := git.Run(sctx.Ctx, sctx.GateDir, "bundle", "verify", filepath.Join(a.SnapshotDir, "head.bundle")); err != nil {
		return err
	}
	heads, err := git.Run(sctx.Ctx, sctx.GateDir, "bundle", "list-heads", filepath.Join(a.SnapshotDir, "head.bundle"))
	if err != nil {
		return err
	}
	expected := map[string]bool{a.OriginalHead + " " + ref: false, *history.Run.SubmittedHeadSHA + " " + submittedRef: false}
	lines := strings.Split(strings.TrimSpace(heads), "\n")
	if len(lines) != len(expected) {
		return fmt.Errorf("CI repair bundle head count changed")
	}
	for _, line := range lines {
		if _, exists := expected[line]; !exists {
			return fmt.Errorf("CI repair bundle head binding changed")
		}
		expected[line] = true
	}
	for _, found := range expected {
		if !found {
			return fmt.Errorf("CI repair bundle is missing a preserved history")
		}
	}
	return nil
}

func digestCIRepairSnapshot(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("CI repair preservation contains a non-regular artifact")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), info.Size())
		n, err := io.Copy(h, f)
		if err != nil {
			return err
		}
		if n != info.Size() {
			return fmt.Errorf("CI repair preservation moved while reading")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func syncCIRepairSnapshot(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if err := os.Chmod(path, 0600); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		f, err := os.Open(dirs[i])
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	for parent, n := filepath.Dir(root), 0; n < 3; parent, n = filepath.Dir(parent), n+1 {
		f, err := os.Open(parent)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func copyCIRepairEvidence(source, dest string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot preserve non-regular CI repair evidence")
		}
		in, err := root.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}
