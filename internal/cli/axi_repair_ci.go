package cli

import (
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/spf13/cobra"
	toon "github.com/toon-format/toon-go"
)

func newAxiRepairCICmd() *cobra.Command {
	var expectedRun, expectedHead, expectedPR string
	cmd := &cobra.Command{
		Use:           "repair-ci",
		Short:         "Admit guarded same-run CI repair with mandatory fresh Review onward",
		Long:          "Explicitly admit an absent or genuinely failing CI monitor to its normal fixer gate. Requires exact original run, published head and PR pins on the caller's current repository branch. Preserves custody, withdraws old validation and requires an additive repair, fresh Review onward, and genuine exact-head CI. Admission is not validation. Failed withdrawal remains held unvalidated. Only continuously owned current-version Linux monitors are supported; legacy adoption and lost-owner recovery are refused. This command never starts, replaces or stops a daemon, changes the caller's checkout, or hands custody out for editing. An unresolved obligation refuses cancellation, skips and replacement runs.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(expectedRun) == "" || strings.TrimSpace(expectedHead) == "" || strings.TrimSpace(expectedPR) == "" {
				return emitError(cmd, 2, "--expect-run, --expect-head and --expect-pr are required")
			}
			env, err := openAxiEnvWithOptions(axiEnvOptions{readOnlyDB: true})
			if err != nil {
				return emitError(cmd, 1, err.Error())
			}
			defer env.close()
			branch, err := git.CurrentBranch(cmd.Context(), ".")
			if err != nil {
				return emitError(cmd, 1, fmt.Sprintf("get current branch: %v", err))
			}
			env.client, err = ipc.Dial(env.p.Socket())
			if err != nil {
				return emitError(cmd, 1, "guarded repair requires an already running compatible daemon: "+err.Error())
			}
			params := ipc.AdmitCIRepairParams{RunID: expectedRun, RepoID: env.repo.ID, Branch: branch, HeadSHA: expectedHead, PRURL: expectedPR}
			var result ipc.AdmitCIRepairResult
			if err := env.client.Call(ipc.MethodAdmitCIRepair, &params, &result); err != nil {
				return emitError(cmd, 1, "guarded CI repair: "+err.Error(), "Inspect the exact original run with axi status before retrying; an ambiguous withdrawal remains held unvalidated")
			}
			if result.RunID != expectedRun || result.HeadSHA != expectedHead || result.State != "admitted-unvalidated" || result.SnapshotDir == "" {
				return emitError(cmd, 1, "guarded CI repair returned an unsupported or unconfirmed capability result; inspect the original run")
			}
			emitDoc(cmd, toon.Field{Key: "run", Value: result.RunID}, toon.Field{Key: "state", Value: result.State}, toon.Field{Key: "validated", Value: false}, toon.Field{Key: "snapshot", Value: result.SnapshotDir}, toon.Field{Key: "help", Value: []string{"Admission is not a pass or a gate response. Run `no-mistakes axi run` to attach to the original run's real approval gate, then select fix for its displayed CI finding IDs.", "The normal fixer must create genuine CI or repair the actual failure. Fresh Review onward and genuine repaired-head CI are mandatory; no skips, no_ci declaration or synthetic success can discharge the obligation."}})
			return nil
		},
	}
	cmd.Flags().StringVar(&expectedRun, "expect-run", "", "exact original run ID, used only as a current-branch identity guard")
	cmd.Flags().StringVar(&expectedHead, "expect-head", "", "full lowercase published commit SHA to preserve")
	cmd.Flags().StringVar(&expectedPR, "expect-pr", "", "exact held PR URL")
	return cmd
}
