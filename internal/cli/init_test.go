package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
)

func TestResolveWorktreeRoot(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	workDir := t.TempDir()

	if _, err := resolveWorktreeRoot(p, nil, workDir, "   "); err == nil {
		t.Fatal("expected error for empty --worktree-root")
	}
	abs := filepath.Join(t.TempDir(), "runs")
	got, err := resolveWorktreeRoot(p, nil, workDir, abs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != abs {
		t.Errorf("resolveWorktreeRoot(%q) = %q, want %q", abs, got, abs)
	}
	// A relative value never reaches the config, where the daemon's own
	// working directory would decide what it means.
	relative, err := resolveWorktreeRoot(p, nil, workDir, "runs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(relative) {
		t.Errorf("resolveWorktreeRoot(%q) = %q, want an absolute path", "runs", relative)
	}
}

func TestResolveWorktreeRootRejectsUnusablePlacements(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	repoDir := setupTestRepo(t)

	for name, root := range map[string]string{
		"the default worktrees directory": filepath.Join(p.WorktreesDir(), "runs"),
		"the run log directory":           p.LogsDir(),
		"the gates directory":             p.ReposDir(),
		"NM_HOME itself":                  p.Root(),
	} {
		if _, err := resolveWorktreeRoot(p, nil, repoDir, root); err == nil {
			t.Errorf("expected error for a root inside %s (%q)", name, root)
		}
	}
	if _, err := resolveWorktreeRoot(p, nil, repoDir, filepath.Join(repoDir, "runs")); err == nil {
		t.Error("expected error for a root inside the repository being initialized")
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorktreeRoot(p, nil, repoDir, file); err == nil {
		t.Error("expected error for a root that is not a directory")
	}
}

func TestResolveWorktreeRootRefusesRootInsideAnotherConfiguredCheckout(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	repoDir := setupTestRepo(t)
	otherCheckout := filepath.Join(t.TempDir(), "other-checkout")
	configYAML := "worktree_roots:\n  " + yamlPath(otherCheckout) + ": " + yamlPath(filepath.Join(t.TempDir(), "other-runs")) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(otherCheckout, "runs")
	_, err := resolveWorktreeRoot(p, nil, repoDir, root)
	if err == nil {
		t.Fatal("expected a refusal for a root inside another configured checkout")
	}
	if !namesPath(err, otherCheckout) {
		t.Errorf("refusal %q does not name the checkout the root sits in", err)
	}

	// A directory next to that checkout is the normal case.
	if _, err := resolveWorktreeRoot(p, nil, repoDir, filepath.Join(t.TempDir(), "runs")); err != nil {
		t.Errorf("root outside every checkout refused: %v", err)
	}
}

func TestResolveWorktreeRootRefusesRootInsideAnotherRegisteredCheckout(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	repoDir := setupTestRepo(t)
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Registered, with no entry in the config at all.
	otherCheckout := filepath.Join(t.TempDir(), "other-checkout")
	if _, err := d.InsertRepoWithID("otherrepo", otherCheckout, "https://example.com/owner/other", "main"); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(otherCheckout, "runs")
	_, err = resolveWorktreeRoot(p, d, repoDir, root)
	if err == nil {
		t.Fatal("expected a refusal for a root inside another registered checkout")
	}
	if !namesPath(err, otherCheckout) {
		t.Errorf("refusal %q does not name the checkout the root sits in", err)
	}

	if _, err := resolveWorktreeRoot(p, d, repoDir, filepath.Join(t.TempDir(), "runs")); err != nil {
		t.Errorf("root outside every checkout refused: %v", err)
	}
}

func TestResolveWorktreeRootRefusesRootClaimedByAnotherCheckout(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	repoDir := setupTestRepo(t)
	root := filepath.Join(t.TempDir(), "shared-runs")
	otherCheckout := filepath.Join(t.TempDir(), "other-checkout")
	configYAML := "worktree_roots:\n  " + yamlPath(otherCheckout) + ": " + yamlPath(root) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := resolveWorktreeRoot(p, nil, repoDir, root)
	if err == nil {
		t.Fatal("expected a refusal for a root another checkout already claims")
	}
	if !namesPath(err, otherCheckout) {
		t.Errorf("refusal %q does not name the checkout that claims the root", err)
	}

	// The same checkout re-initializing with the root it already uses is not a
	// conflict with itself.
	selfConfig := "worktree_roots:\n  " + yamlPath(repoDir) + ": " + yamlPath(root) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(selfConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorktreeRoot(p, nil, repoDir, root); err != nil {
		t.Errorf("re-initializing the checkout that already uses this root was refused: %v", err)
	}
}

func TestInitRefusesToRegisterACheckoutHoldingAConfiguredWorktreeRoot(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	repoDir := setupTestRepo(t)

	// A directory inside the checkout about to be registered, placing the runs
	// of an unrelated checkout.
	otherCheckout := filepath.Join(t.TempDir(), "other-checkout")
	insideConfig := "worktree_roots:\n  " + yamlPath(otherCheckout) + ": " + yamlPath(filepath.Join(repoDir, "runs")) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(insideConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	err := assertCheckoutHoldsNoConfiguredWorktreeRoot(p, repoDir)
	if err == nil {
		t.Fatal("expected a refusal for a checkout that contains a configured worktree root")
	}
	if !namesPath(err, otherCheckout) {
		t.Errorf("refusal %q does not name the entry that becomes unusable", err)
	}

	// ... and refusing is not a taste: the daemon's own startup gate, given the
	// same registration, refuses to come up at all.
	cfg, cfgErr := config.LoadGlobal(p.ConfigFile())
	if cfgErr != nil {
		t.Fatal(cfgErr)
	}
	if err := worktrees.New(p, cfg.WorktreeRoots).Validate(repoDir); err == nil {
		t.Error("the daemon's startup gate accepts this registration; init must not be stricter than it")
	}

	// The same checkout placing its own runs inside itself is refused too.
	selfConfig := "worktree_roots:\n  " + yamlPath(repoDir) + ": " + yamlPath(filepath.Join(repoDir, "runs")) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(selfConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assertCheckoutHoldsNoConfiguredWorktreeRoot(p, repoDir); err == nil {
		t.Error("expected a refusal for a checkout that contains its own configured worktree root")
	}

	// A configuration whose roots are all outside this checkout registers
	// normally: init refuses only what this registration itself breaks.
	outsideConfig := "worktree_roots:\n  " + yamlPath(otherCheckout) + ": " + yamlPath(filepath.Join(t.TempDir(), "runs")) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(outsideConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assertCheckoutHoldsNoConfiguredWorktreeRoot(p, repoDir); err != nil {
		t.Errorf("registration of a checkout that holds no configured root refused: %v", err)
	}
}

func TestInitRefusesToRegisterWhileTheGlobalConfigDoesNotLoad(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	repoDir := setupTestRepo(t)

	otherCheckout := filepath.Join(t.TempDir(), "other-checkout")
	unrelatedCheckout := filepath.Join(t.TempDir(), "unrelated-checkout")
	unloadable := "worktree_roots:\n" +
		"  " + yamlPath(otherCheckout) + ": " + yamlPath(filepath.Join(repoDir, "runs")) + "\n" +
		"  " + yamlPath(unrelatedCheckout) + ": relative-runs\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(unloadable), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgErr := func() error {
		_, err := config.LoadGlobal(p.ConfigFile())
		return err
	}()
	if cfgErr == nil {
		t.Fatal("fixture config loads; it must be the parseable-but-unloadable shape")
	}

	err := assertCheckoutHoldsNoConfiguredWorktreeRoot(p, repoDir)
	if err == nil {
		t.Fatal("expected a refusal while the global config does not load")
	}
	if !strings.Contains(err.Error(), p.ConfigFile()) {
		t.Errorf("refusal %q does not name the config that has to be repaired", err)
	}

	// Repairing the relative value is what would otherwise have taken the CLI
	// down, and the placement is then refused on its own terms.
	repaired := "worktree_roots:\n" +
		"  " + yamlPath(otherCheckout) + ": " + yamlPath(filepath.Join(repoDir, "runs")) + "\n" +
		"  " + yamlPath(unrelatedCheckout) + ": " + yamlPath(filepath.Join(t.TempDir(), "unrelated-runs")) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(repaired), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := assertCheckoutHoldsNoConfiguredWorktreeRoot(p, repoDir); err == nil {
		t.Error("expected the placement refusal once the config loads")
	}
}

func TestPrintWorktreeRootGuidancePrintsConfigEntry(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")
	root := filepath.Join(dir, "work", "repo1-runs")
	printWorktreeRootGuidance(&out, p, checkout, root)

	got := out.String()
	for _, want := range []string{"worktree_roots:", checkout + ": " + root, p.ConfigFile()} {
		if !strings.Contains(got, want) {
			t.Errorf("guidance missing %q, got:\n%s", want, got)
		}
	}
}

func TestPrintWorktreeRootGuidanceReportsExistingEntry(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")
	root := filepath.Join(dir, "work", "repo1-runs")
	configYAML := "worktree_roots:\n  " + yamlPath(checkout) + ": " + yamlPath(root) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	// The same directory spelled differently is the same entry.
	printWorktreeRootGuidance(&out, p, checkout+string(filepath.Separator), root)

	got := out.String()
	if !strings.Contains(got, "already configured") {
		t.Errorf("guidance should report the entry is in effect, got:\n%s", got)
	}
	if strings.Contains(got, "worktree_roots:") {
		t.Errorf("guidance should not repeat an entry that is already in effect, got:\n%s", got)
	}
}

func TestPrintWorktreeRootGuidanceMergesIntoAnExistingBlock(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	existingCheckout := filepath.Join(dir, "src", "repo1")
	existingRoot := filepath.Join(dir, "work", "repo1-runs")
	block := "worktree_roots:\n  " + yamlPath(existingCheckout) + ": " + yamlPath(existingRoot) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}

	checkout := filepath.Join(dir, "src", "repo2")
	root := filepath.Join(dir, "work", "repo2-runs")
	var out bytes.Buffer
	printWorktreeRootGuidance(&out, p, checkout, root)

	got := out.String()
	entry := "  " + checkout + ": " + root
	if !containsYAMLLine(got, strings.TrimSpace(entry)) {
		t.Errorf("guidance missing the entry %q, got:\n%s", entry, got)
	}
	if containsYAMLLine(got, "worktree_roots:") {
		t.Errorf("guidance repeats the top-level key that already exists, got:\n%s", got)
	}

	// The merge the guidance describes must load, with both repositories placed.
	merged := block + entry + "\n"
	cfg, err := config.LoadGlobal(writeConfig(t, p, merged))
	if err != nil {
		t.Fatalf("config built by following the guidance does not load: %v", err)
	}
	layout := worktrees.New(p, cfg.WorktreeRoots)
	for checkoutPath, wantRoot := range map[string]string{existingCheckout: existingRoot, checkout: root} {
		if configured, ok := layout.CustomRoot(checkoutPath); !ok || configured != wantRoot {
			t.Errorf("merged config places %q at (%q, %v), want %q", checkoutPath, configured, ok, wantRoot)
		}
	}

	// ... and the duplicate block is why: it makes the config unloadable.
	duplicate := block + "worktree_roots:\n" + entry + "\n"
	if _, err := config.LoadGlobal(writeConfig(t, p, duplicate)); err == nil {
		t.Error("a duplicate worktree_roots block loaded; the guidance's merge shape would then be a matter of taste")
	}
}

func TestPrintWorktreeRootGuidanceReplacesThisCheckoutsEntry(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")
	oldRoot := filepath.Join(dir, "work", "repo1-runs")
	newRoot := filepath.Join(dir, "work", "repo1-runs-v2")
	oldEntry := "  " + checkout + ": " + oldRoot
	newEntry := "  " + checkout + ": " + newRoot
	block := "worktree_roots:\n" + oldEntry + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	printWorktreeRootGuidance(&out, p, checkout, newRoot)

	got := out.String()
	for _, want := range []string{strings.TrimSpace(oldEntry), strings.TrimSpace(newEntry)} {
		if !containsYAMLLine(got, want) {
			t.Errorf("guidance missing the line %q, so the operator cannot see what to replace, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Add this") {
		t.Errorf("guidance says to add an entry for a checkout the block already has, got:\n%s", got)
	}

	// The replacement the guidance describes loads and re-points the checkout.
	replaced := "worktree_roots:\n" + newEntry + "\n"
	cfg, err := config.LoadGlobal(writeConfig(t, p, replaced))
	if err != nil {
		t.Fatalf("config built by following the guidance does not load: %v", err)
	}
	if configured, ok := worktrees.New(p, cfg.WorktreeRoots).CustomRoot(checkout); !ok || configured != newRoot {
		t.Errorf("replaced config places %q at (%q, %v), want %q", checkout, configured, ok, newRoot)
	}

	// ... and appending instead is why it must be a replacement.
	if _, err := config.LoadGlobal(writeConfig(t, p, block+newEntry+"\n")); err == nil {
		t.Error("a second entry for the same checkout loaded; the guidance's replace shape would then be a matter of taste")
	}

	var same bytes.Buffer
	printWorktreeRootGuidance(&same, p, checkout, oldRoot)
	if !strings.Contains(same.String(), "already configured") {
		t.Errorf("guidance for the configured root should report it is in effect, got:\n%s", same.String())
	}
}

func TestPrintWorktreeRootGuidanceNamesTheEntryAsTheConfigSpellsIt(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")
	configuredKey := checkout + string(filepath.Separator)
	oldRoot := filepath.Join(dir, "work", "repo1-runs")
	newRoot := filepath.Join(dir, "work", "repo1-runs-v2")
	block := "worktree_roots:\n  " + yamlPath(configuredKey) + ": " + yamlPath(oldRoot) + "\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	printWorktreeRootGuidance(&out, p, checkout, newRoot)

	got := out.String()
	if !containsYAMLLine(got, configuredKey+": "+oldRoot) {
		t.Errorf("guidance does not name the line the config actually contains, got:\n%s", got)
	}
	if containsYAMLLine(got, checkout+": "+newRoot) {
		t.Errorf("guidance rewrote the key, which would leave two keys naming one checkout, got:\n%s", got)
	}
}

func TestPrintWorktreeRootGuidanceMatchesTheBlocksIndentation(t *testing.T) {
	dir := t.TempDir()
	existingCheckout := filepath.Join(dir, "src", "repo1")
	existingRoot := filepath.Join(dir, "work", "repo1-runs")
	checkout := filepath.Join(dir, "src", "repo2")
	root := filepath.Join(dir, "work", "repo2-runs")
	block := "worktree_roots:\n    " + existingCheckout + ": " + existingRoot + "\n"

	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	printWorktreeRootGuidance(&out, p, checkout, root)
	entry := indentedYAMLLine(t, out.String(), checkout+": "+root)
	merged := block + entry + "\n"
	cfg, err := config.LoadGlobal(writeConfig(t, p, merged))
	if err != nil {
		t.Fatalf("config built by following the guidance does not load: %v\nmerged:\n%s", err, merged)
	}
	layout := worktrees.New(p, cfg.WorktreeRoots)
	for checkoutPath, wantRoot := range map[string]string{existingCheckout: existingRoot, checkout: root} {
		if configured, ok := layout.CustomRoot(checkoutPath); !ok || configured != wantRoot {
			t.Errorf("merged config places %q at (%q, %v), want %q", checkoutPath, configured, ok, wantRoot)
		}
	}

	// ... and an entry at the wrong indentation is why the guidance has to match:
	// it is not another entry of that mapping at all.
	if _, err := config.LoadGlobal(writeConfig(t, p, block+"  "+checkout+": "+root+"\n")); err == nil {
		t.Error("an entry indented differently from its siblings loaded; matching the block would then be cosmetic")
	}

	var repoint bytes.Buffer
	printWorktreeRootGuidance(&repoint, p, existingCheckout, root)
	if named := indentedYAMLLine(t, repoint.String(), existingCheckout+": "+existingRoot); named != "    "+existingCheckout+": "+existingRoot {
		t.Errorf("guidance named %q for replacement, which is not the line the config contains", named)
	}
	replacement := indentedYAMLLine(t, repoint.String(), existingCheckout+": "+root)
	cfg, err = config.LoadGlobal(writeConfig(t, p, "worktree_roots:\n"+replacement+"\n"))
	if err != nil {
		t.Fatalf("re-pointed config does not load: %v", err)
	}
	if configured, ok := worktrees.New(p, cfg.WorktreeRoots).CustomRoot(existingCheckout); !ok || configured != root {
		t.Errorf("re-pointed config places %q at (%q, %v), want %q", existingCheckout, configured, ok, root)
	}
}

func indentedYAMLLine(t *testing.T, rendered, want string) string {
	t.Helper()
	for _, raw := range strings.Split(ansiEscape.ReplaceAllString(rendered, ""), "\n") {
		line := strings.TrimPrefix(raw, "  ")
		if strings.TrimSpace(line) == want {
			return line
		}
	}
	t.Fatalf("guidance never printed %q as a line of its own, got:\n%s", want, rendered)
	return ""
}

func TestPrintWorktreeRootGuidanceReplacesAKeyWithNoBlockToAddTo(t *testing.T) {
	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")
	root := filepath.Join(dir, "work", "repo1-runs")
	otherCheckout := filepath.Join(dir, "src", "repo2")
	otherRoot := filepath.Join(dir, "work", "repo2-runs")

	for name, tc := range map[string]struct {
		document string
		placed   map[string]string
	}{
		"a key set to an empty map": {
			document: "worktree_roots: {}\n",
			placed:   map[string]string{checkout: root},
		},
		"a key with no value": {
			document: "worktree_roots:\n",
			placed:   map[string]string{checkout: root},
		},
		"an inline mapping another checkout uses": {
			document: "worktree_roots: {" + otherCheckout + ": " + otherRoot + "}\n",
			placed:   map[string]string{checkout: root, otherCheckout: otherRoot},
		},
	} {
		p := paths.WithRoot(t.TempDir())
		if err := p.EnsureDirs(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.ConfigFile(), []byte(tc.document), 0o644); err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		printWorktreeRootGuidance(&out, p, checkout, root)
		got := out.String()

		// The line to replace is named as the file spells it.
		if !containsYAMLLine(got, strings.TrimSpace(tc.document)) {
			t.Errorf("%s: guidance does not name the line to replace, got:\n%s", name, got)
		}

		// Following it produces a config that loads and places everything.
		pasted := pastedWorktreeRootsBlock(t, got)
		cfg, err := config.LoadGlobal(writeConfig(t, p, pasted))
		if err != nil {
			t.Errorf("%s: config built by following the guidance does not load: %v\npasted:\n%s", name, err, pasted)
			continue
		}
		layout := worktrees.New(p, cfg.WorktreeRoots)
		for checkoutPath, wantRoot := range tc.placed {
			if configured, ok := layout.CustomRoot(checkoutPath); !ok || configured != wantRoot {
				t.Errorf("%s: config places %q at (%q, %v), want %q\npasted:\n%s", name, checkoutPath, configured, ok, wantRoot, pasted)
			}
		}

		// ... and adding an entry under the key as written is why it must be a
		// replacement.
		appended := tc.document + "  " + checkout + ": " + root + "\n"
		if _, err := config.LoadGlobal(writeConfig(t, p, appended)); err == nil && strings.Contains(tc.document, "{") {
			t.Errorf("%s: appending under a flow mapping loaded; the guidance's shape would then be a matter of taste", name)
		}
	}
}

func TestPrintWorktreeRootGuidanceRepointsAnInlineEntry(t *testing.T) {
	dir := t.TempDir()
	checkout := filepath.Join(dir, "src", "repo1")
	oldRoot := filepath.Join(dir, "work", "repo1-runs")
	newRoot := filepath.Join(dir, "work", "repo1-runs-v2")
	document := "worktree_roots: {" + checkout + ": " + oldRoot + "}\n"

	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	printWorktreeRootGuidance(&out, p, checkout, newRoot)
	got := out.String()

	if !containsYAMLLine(got, strings.TrimSpace(document)) {
		t.Errorf("guidance does not name the line the config contains, got:\n%s", got)
	}
	pasted := pastedWorktreeRootsBlock(t, got)
	cfg, err := config.LoadGlobal(writeConfig(t, p, pasted))
	if err != nil {
		t.Fatalf("config built by following the guidance does not load: %v\npasted:\n%s", err, pasted)
	}
	if configured, ok := worktrees.New(p, cfg.WorktreeRoots).CustomRoot(checkout); !ok || configured != newRoot {
		t.Errorf("config places %q at (%q, %v), want %q\npasted:\n%s", checkout, configured, ok, newRoot, pasted)
	}
	if containsYAMLLine(pasted, checkout+": "+oldRoot) {
		t.Errorf("the replacement still carries the old entry, so the checkout would have two:\n%s", pasted)
	}
}

func pastedWorktreeRootsBlock(t *testing.T, rendered string) string {
	t.Helper()
	var block []string
	for _, raw := range strings.Split(ansiEscape.ReplaceAllString(rendered, ""), "\n") {
		line := strings.TrimPrefix(raw, "  ")
		switch {
		case strings.TrimSpace(line) == "worktree_roots:":
			block = []string{line}
		case len(block) > 0 && strings.TrimSpace(line) != "":
			block = append(block, line)
		}
	}
	if len(block) < 2 {
		t.Fatalf("guidance printed no worktree_roots block to paste:\n%s", rendered)
	}
	return strings.Join(block, "\n") + "\n"
}

func containsYAMLLine(rendered, want string) bool {
	for _, line := range strings.Split(ansiEscape.ReplaceAllString(rendered, ""), "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func writeConfig(t *testing.T, p *paths.Paths, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func yamlPath(path string) string {
	return `"` + strings.ReplaceAll(path, `\`, `\\`) + `"`
}

func namesPath(err error, path string) bool {
	msg := err.Error()
	for _, spelling := range []string{path, worktrees.Canonical(path)} {
		if strings.Contains(msg, spelling) || strings.Contains(msg, strconv.Quote(spelling)) {
			return true
		}
	}
	return false
}
