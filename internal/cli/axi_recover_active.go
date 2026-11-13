package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/spf13/cobra"
)

func newAxiRecoverActiveCmd() *cobra.Command {
	var runID, local, published string
	cmd := &cobra.Command{
		Use:   "recover-active",
		Short: "Preserve a divergent active run and withdraw validation for editing",
		Long:  "Preserves the pinned local and published heads, run history, logs and evidence, then withdraws the PR attestation and cancels the exact CI monitor. The local branch and remote are never moved. Merge the returned published ref additively, correct the change, and use axi run for fresh full validation. Recovery is not validation or a green handoff. All three identity flags are required. The running daemon must support this command; it is never restarted by this command.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(runID) == "" || local == "" || published == "" {
				return emitError(cmd, 2, "--run, --local-head and --published-head are required")
			}
			env, err := openAxiEnvWithOptions(axiEnvOptions{readOnlyDB: true})
			if err != nil {
				return emitError(cmd, 1, err.Error())
			}
			defer env.close()
			client, err := ipc.Dial(env.p.Socket())
			if err != nil {
				return emitError(cmd, 1, fmt.Sprintf("connect to existing daemon: %v", err))
			}
			defer client.Close()
			branch, err := git.CurrentBranch(cmd.Context(), ".")
			if err != nil {
				return emitError(cmd, 1, err.Error())
			}
			dir, err := os.Getwd()
			if err != nil {
				return emitError(cmd, 1, err.Error())
			}
			var result ipc.RecoverActiveResult
			err = client.CallWithTimeout(ipc.MethodRecoverActive, ipc.RecoverActiveParams{RepoID: env.repo.ID, Branch: branch, RunID: runID, WorkDir: dir, LocalHead: local, PublishedHead: published}, &result, 5*time.Minute)
			if err != nil {
				return emitError(cmd, 1, fmt.Sprintf("active recovery not confirmed: %v; inspect the exact run before retrying", err))
			}
			emitDoc(cmd,
				toon.Field{Key: "state", Value: "recovered-for-editing"},
				toon.Field{Key: "validated", Value: false},
				toon.Field{Key: "run_id", Value: result.RunID},
				toon.Field{Key: "snapshot_dir", Value: result.SnapshotDir},
				toon.Field{Key: "published_ref", Value: result.PublishedRef},
				toon.Field{Key: "help", Value: []string{"Merge the published_ref into this branch with git merge --no-ff; resolve conflicts without dropping either history.", "Correct the change, commit it, then run no-mistakes axi run --intent <complete acceptance criteria> without --skip. Never use retry or handoff as revalidation.", "The PR attestation is withdrawn. No previous validation is fresh; recovery does not validate or publish changes."}},
			)
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "exact active run identity to recover")
	cmd.Flags().StringVar(&local, "local-head", "", "expected clean local full commit SHA")
	cmd.Flags().StringVar(&published, "published-head", "", "expected current and published full commit SHA")
	return cmd
}
