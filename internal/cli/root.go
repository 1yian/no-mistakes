package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/charmbracelet/lipgloss"
	"github.com/kunchenguid/no-mistakes/internal/buildinfo"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return ""
}

func (e *exitError) Unwrap() error { return e.err }

func Execute() int {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil {
				fmt.Fprintln(root.ErrOrStderr(), ee.err)
			}
			return ee.code
		}
		fmt.Fprintln(root.ErrOrStderr(), err)
		return 1
	}
	return 0
}

func newRootCmd() *cobra.Command {
	var autoYes bool
	var skipValue string

	cmd := &cobra.Command{
		Use:     "no-mistakes",
		Short:   "Local Git proxy that validates code before pushing to the configured target",
		Version: buildinfo.String(),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			setColorProfileForOutput(cmd.OutOrStdout())
			return guardGateControl(cmd)
		},
		// Silence cobra's default error/usage printing — we handle it ourselves.
		SilenceErrors: true,
		SilenceUsage:  true,
		// When run without a subcommand, attach to the current branch run or
		// route users into the setup wizard when no run is active. The default
		// wizard flow is interactive, while --yes auto-accepts defaults and can
		// still fall back to headless mode when no TTY is available.
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackCommand("root", func() error {
				skipSteps, err := parseSkipSteps(skipValue)
				if err != nil {
					return err
				}
				return attachRun(cmd.Context(), cmd.OutOrStdout(), "", true, autoYes, skipSteps)
			})
		},
	}

	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "run setup wizard and accept defaults automatically")
	cmd.Flags().StringVar(&skipValue, "skip", "", "comma-separated pipeline steps to skip for a new run")

	cmd.AddCommand(newInitCmd())
	cmd.AddCommand(newSkillCmd())
	cmd.AddCommand(newCIWorkflowCmd())
	cmd.AddCommand(newEjectCmd())
	cmd.AddCommand(newUpdateCmd())
	cmd.AddCommand(newDaemonCmd())
	cmd.AddCommand(newAttachCmd())
	cmd.AddCommand(newRerunCmd())
	cmd.AddCommand(newStatusCmd())
	cmd.AddCommand(newSyncCmd())
	cmd.AddCommand(newRunsCmd())
	cmd.AddCommand(newStatsCmd())
	cmd.AddCommand(newExportCmd())
	cmd.AddCommand(newDoctorCmd())
	cmd.AddCommand(newEvalCmd())
	cmd.AddCommand(newAxiCmd())

	return cmd
}

func setColorProfileForOutput(w io.Writer) {
	lipgloss.SetColorProfile(termenv.NewOutput(w).EnvColorProfile())
}

func findRepo(d *db.DB) (*db.Repo, error) {
	gitRoot, err := git.FindGitRoot(".")
	if err != nil {
		return nil, fmt.Errorf("not in a git repository")
	}
	repo, err := d.GetRepoByPath(gitRoot)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if repo != nil {
		return repo, nil
	}
	// Try the main worktree root (handles git worktrees).
	mainRoot, err := git.FindMainRepoRoot(".")
	if err != nil || mainRoot == gitRoot {
		return nil, fmt.Errorf("repo not initialized (run 'no-mistakes init' first)")
	}
	repo, err = d.GetRepoByPath(mainRoot)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return nil, fmt.Errorf("repo not initialized (run 'no-mistakes init' first)")
	}
	return repo, nil
}

func openResources() (*paths.Paths, *db.DB, error) {
	p, err := paths.New()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve paths: %w", err)
	}
	if err := p.EnsureDirs(); err != nil {
		return nil, nil, fmt.Errorf("create directories: %w", err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	return p, d, nil
}
