package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/daemon"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/gate"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/safeurl"
	"github.com/kunchenguid/no-mistakes/internal/skill"
	"github.com/kunchenguid/no-mistakes/internal/worktrees"
	"github.com/spf13/cobra"
)

const banner = `_  _ ____    _  _ _ ____ ___ ____ _  _ ____ ____
|\ | |  |    |\/| | [__   |  |__| |_/  |___ [__
| \| |__|    |  | | ___]  |  |  | | \_ |___ ___]`

func newInitCmd() *cobra.Command {
	var forkURL string
	var worktreeRoot string
	var skipUserSkill bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize no-mistakes gate for the current repository",
		Long: "Sets up or refreshes a local bare repo as a gate, installs a post-receive hook,\n" +
			"best-effort isolates the gate hook path from shared local git config writes when Git supports `config --worktree`,\n" +
			"adds or repairs the \"no-mistakes\" git remote, and records the repo in the database.\n\n" +
			"Run this from inside a git repository that has an \"origin\" remote.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackCommand("init", func() error {
				p, d, err := openResources()
				if err != nil {
					return err
				}
				defer d.Close()

				if cmd.Flags().Changed("fork-url") && strings.TrimSpace(forkURL) == "" {
					return fmt.Errorf("init: --fork-url must not be empty")
				}
				if err := assertCheckoutHoldsNoConfiguredWorktreeRoot(p, "."); err != nil {
					return err
				}
				resolvedWorktreeRoot := ""
				if cmd.Flags().Changed("worktree-root") {
					resolvedWorktreeRoot, err = resolveWorktreeRoot(p, d, ".", worktreeRoot)
					if err != nil {
						return err
					}
				}
				repo, created, err := gate.InitWithFork(cmd.Context(), d, p, ".", forkURL)
				if err != nil {
					return fmt.Errorf("init: %w", err)
				}
				if err := daemon.EnsureDaemon(p); err != nil {
					// Only roll back a gate we created in this run; a re-init
					// must never eject a user's pre-existing gate.
					if created {
						if _, ejectErr := gate.Eject(cmd.Context(), d, p, "."); ejectErr != nil {
							return fmt.Errorf("start daemon: %w, rollback init: %v", err, ejectErr)
						}
					}
					return fmt.Errorf("start daemon: %w", err)
				}

				var skillErr error
				if !skipUserSkill {
					_, skillErr = installUserSkill()
				}

				w := cmd.OutOrStdout()
				fmt.Fprintln(w, sCyan.Render(banner))
				fmt.Fprintln(w)
				headline := "Gate initialized"
				if !created {
					headline = "Gate already initialized (refreshed)"
				}
				fmt.Fprintf(w, "  %s %s\n", sGreen.Render("✓"), headline)
				fmt.Fprintln(w)
				fmt.Fprintf(w, "  %s  %s\n", sDim.Render("  repo"), repo.WorkingPath)
				fmt.Fprintf(w, "  %s  no-mistakes → %s\n", sDim.Render("  gate"), p.RepoDir(repo.ID))
				remoteURL := repo.UpstreamURL
				if repo.ForkURL != "" {
					remoteURL = safeurl.Redact(remoteURL)
				}
				fmt.Fprintf(w, "  %s  %s\n", sDim.Render("remote"), remoteURL)
				if repo.ForkURL != "" {
					fmt.Fprintf(w, "  %s  %s\n", sDim.Render("  fork"), safeurl.Redact(repo.ForkURL))
				}
				if skipUserSkill {
					fmt.Fprintf(w, "  %s  %s\n", sDim.Render(" skill"), sDim.Render("skipped: --skip-user-skill"))
				} else if skillErr != nil {
					fmt.Fprintf(w, "  %s  %s\n", sDim.Render(" skill"), sYellow.Render("skipped: "+skillErr.Error()))
				} else {
					fmt.Fprintf(w, "  %s  %s %s\n", sDim.Render(" skill"), sGreen.Render("/no-mistakes"), sDim.Render("installed for agents at user level"))
				}
				if resolvedWorktreeRoot != "" {
					printWorktreeRootGuidance(w, p, repo.WorkingPath, resolvedWorktreeRoot)
				}
				if legacy := skill.Vendored(repo.WorkingPath); len(legacy) > 0 {
					fmt.Fprintf(w, "  %s  %s\n", sDim.Render("  note"), sDim.Render("vendored skill copy ("+strings.Join(legacy, ", ")+") is no longer needed and can be removed"))
				}
				fmt.Fprintln(w)
				fmt.Fprintf(w, "  %s\n", sDim.Render("Push through the gate with:"))
				fmt.Fprintf(w, "  %s\n", sBold.Render("git push no-mistakes <branch>"))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&forkURL, "fork-url", "", "GitHub fork remote URL to push branches to while opening PRs against origin")
	cmd.Flags().StringVar(&worktreeRoot, "worktree-root", "", "Directory to create this repository's run worktrees in, so directory-scoped toolchain config (mise, direnv) reaches them; prints the worktree_roots entry to add to the global config")
	cmd.Flags().BoolVar(&skipUserSkill, "skip-user-skill", false, "Do not install or refresh the /no-mistakes agent skill at user level (~/.claude/skills and ~/.agents/skills)")
	return cmd
}

func resolveWorktreeRoot(p *paths.Paths, d *db.DB, workDir, root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("init: --worktree-root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("init: resolve --worktree-root: %w", err)
	}
	abs = filepath.Clean(abs)
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		return "", fmt.Errorf("init: --worktree-root %s is not a directory", abs)
	}
	gitRoot, _ := git.FindMainRepoRoot(workDir)
	if err := worktrees.CheckPlacement(p, gitRoot, abs, knownCheckouts(p, d)...); err != nil {
		return "", fmt.Errorf("init: --worktree-root %w", err)
	}
	if claimant, claimed := checkoutClaimingWorktreeRoot(p, gitRoot, abs); claimed {
		return "", fmt.Errorf("init: --worktree-root %s is already the worktree root of %s; each checkout needs its own root", abs, claimant)
	}
	return abs, nil
}

func assertCheckoutHoldsNoConfiguredWorktreeRoot(p *paths.Paths, workDir string) error {
	checkout, err := git.FindMainRepoRoot(workDir)
	if err != nil || strings.TrimSpace(checkout) == "" {
		return nil
	}
	cfg, err := config.LoadGlobal(p.ConfigFile())
	if err != nil {
		return fmt.Errorf("init: %s must load before a repository can be registered: %w", p.ConfigFile(), err)
	}
	layout := worktrees.New(p, cfg.WorktreeRoots)
	configured := layout.Checkouts()
	sort.Strings(configured)
	for _, other := range configured {
		root, ok := layout.CustomRoot(other)
		if !ok || !worktrees.Contains(checkout, root) {
			continue
		}
		if err := layout.ValidateCheckout(other, checkout); err != nil {
			return fmt.Errorf("init: registering %s makes the configured worktree placement unusable: %w", checkout, err)
		}
	}
	return nil
}

func knownCheckouts(p *paths.Paths, d *db.DB) []string {
	var out []string
	if cfg, err := config.LoadGlobal(p.ConfigFile()); err == nil {
		out = append(out, worktrees.New(p, cfg.WorktreeRoots).Checkouts()...)
	}
	if d != nil {
		if registered, err := d.RepoWorkingPaths(); err == nil {
			out = append(out, registered...)
		}
	}
	return out
}

func checkoutClaimingWorktreeRoot(p *paths.Paths, checkout, root string) (string, bool) {
	cfg, err := config.LoadGlobal(p.ConfigFile())
	if err != nil {
		return "", false
	}
	layout := worktrees.New(p, cfg.WorktreeRoots)
	self := worktrees.Canonical(checkout)
	for _, configured := range layout.Checkouts() {
		if checkout != "" && configured == self {
			continue
		}
		if claimed, ok := layout.CustomRoot(configured); ok && worktrees.Canonical(claimed) == worktrees.Canonical(root) {
			return configured, true
		}
	}
	return "", false
}

func printWorktreeRootGuidance(w io.Writer, p *paths.Paths, workingPath, root string) {
	shape := config.InspectGlobalConfigMapping(p.ConfigFile(), "worktree_roots")
	configuredKey, configuredRoot, configured := configuredWorktreeRootEntry(shape, workingPath)
	if configured && worktrees.Canonical(configuredRoot) == worktrees.Canonical(root) {
		fmt.Fprintf(w, "  %s  %s %s\n", sDim.Render("  runs"), sGreen.Render(root), sDim.Render("(already configured)"))
		return
	}
	fmt.Fprintf(w, "  %s  %s\n", sDim.Render("  runs"), root)
	fmt.Fprintln(w)

	indent := worktreeRootsEntryIndent(shape)
	switch {
	case !shape.Present:
		fmt.Fprintf(w, "  %s\n", sDim.Render("Add this to "+p.ConfigFile()+" so runs are created there:"))
		fmt.Fprintf(w, "  %s\n", sBold.Render("worktree_roots:"))
		fmt.Fprintf(w, "  %s\n", sBold.Render(indent+workingPath+": "+root))
	case !shape.AppendableBlock:
		printWorktreeRootsBlockReplacement(w, p, shape, workingPath, configuredKey, root)
	case configured:
		fmt.Fprintf(w, "  %s\n", sDim.Render("Replace this line in "+p.ConfigFile()+" so runs are created there:"))
		fmt.Fprintf(w, "  %s\n", sBold.Render(indent+configuredKey+": "+configuredRoot))
		fmt.Fprintf(w, "  %s\n", sDim.Render("with:"))
		fmt.Fprintf(w, "  %s\n", sBold.Render(indent+configuredKey+": "+root))
	default:
		fmt.Fprintf(w, "  %s\n", sDim.Render("Add this under the existing worktree_roots: in "+p.ConfigFile()+" so runs are created there:"))
		fmt.Fprintf(w, "  %s\n", sBold.Render(indent+workingPath+": "+root))
	}
}

func worktreeRootsEntryIndent(shape config.GlobalConfigMapping) string {
	if shape.EntryIndent > 0 {
		return strings.Repeat(" ", shape.EntryIndent)
	}
	return "  "
}

func printWorktreeRootsBlockReplacement(w io.Writer, p *paths.Paths, shape config.GlobalConfigMapping, workingPath, configuredKey, root string) {
	if shape.Line != "" {
		fmt.Fprintf(w, "  %s\n", sDim.Render("Replace this line in "+p.ConfigFile()+" so runs are created there:"))
		fmt.Fprintf(w, "  %s\n", sBold.Render(shape.Line))
		fmt.Fprintf(w, "  %s\n", sDim.Render("with:"))
	} else {
		fmt.Fprintf(w, "  %s\n", sDim.Render("Replace the worktree_roots: entry in "+p.ConfigFile()+" so runs are created there:"))
	}
	fmt.Fprintf(w, "  %s\n", sBold.Render("worktree_roots:"))
	repointed := false
	for _, existing := range shape.Entries {
		value := existing.Value
		if configuredKey != "" && existing.Key == configuredKey {
			value = root
			repointed = true
		}
		fmt.Fprintf(w, "  %s\n", sBold.Render("  "+existing.Key+": "+value))
	}
	if !repointed {
		fmt.Fprintf(w, "  %s\n", sBold.Render("  "+workingPath+": "+root))
	}
}

func configuredWorktreeRootEntry(shape config.GlobalConfigMapping, workingPath string) (key, root string, ok bool) {
	want := worktrees.Canonical(workingPath)
	for _, entry := range shape.Entries {
		if strings.TrimSpace(entry.Key) == "" {
			continue
		}
		if worktrees.Canonical(entry.Key) == want {
			return entry.Key, entry.Value, true
		}
	}
	return "", "", false
}
