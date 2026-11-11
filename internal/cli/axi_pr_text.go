package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps"
	"github.com/spf13/cobra"
)

const (
	prTitlePushOptionPrefix = "no-mistakes.pr-title="
	prBodyPushOptionPrefix  = "no-mistakes.pr-body="
)

const maxAxiRunPRBodyBytes = (65516 - len(prBodyPushOptionPrefix)) / 4 * 3

func bindPRTextFlags(cmd *cobra.Command, title, bodyFile *string) {
	cmd.Flags().StringVar(title, "pr-title", "", "PR title to use verbatim when this run creates the PR (pr.title_format still applies)")
	cmd.Flags().StringVar(bodyFile, "pr-body-file", "", "read the PR description to use verbatim when this run creates the PR from this file; '-' reads stdin to EOF")
}

func resolvePRTextFlags(cmd *cobra.Command, title, bodyFile string, intentReadsStdin bool) (db.RunPRText, error) {
	var text db.RunPRText
	if cmd.Flags().Changed("pr-title") {
		normalized, err := steps.NormalizeRunPRTitle(title)
		if err == nil && normalized == "" {
			err = errors.New("must not be empty or whitespace-only")
		}
		if err != nil {
			return db.RunPRText{}, fmt.Errorf("--pr-title %w", err)
		}
		text.Title = normalized
	}
	if cmd.Flags().Changed("pr-body-file") {
		var data []byte
		var err error
		switch bodyFile {
		case "":
			return db.RunPRText{}, errors.New("--pr-body-file requires a file path, or - for stdin")
		case "-":
			if intentReadsStdin {
				return db.RunPRText{}, errors.New("--pr-body-file - and --intent - cannot both read stdin; pass one of them as a file")
			}
			data, err = readAxiPRBody(cmd.InOrStdin())
		default:
			data, err = readAxiRegularFile(bodyFile, "--pr-body-file", readAxiPRBody)
		}
		if err != nil {
			return db.RunPRText{}, fmt.Errorf("read --pr-body-file: %w", err)
		}
		body := string(data)
		err = steps.ValidateRunPRBody(body)
		if err == nil && body == "" {
			err = errors.New("must not be empty or whitespace-only")
		}
		if err != nil {
			return db.RunPRText{}, fmt.Errorf("--pr-body-file %w", err)
		}
		text.Body = body
	}
	return text, nil
}

func readAxiPRBody(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(maxAxiRunPRBodyBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAxiRunPRBodyBytes {
		return nil, fmt.Errorf("input exceeds the %d-byte PR description transport limit", maxAxiRunPRBodyBytes)
	}
	return data, nil
}

func hasRunPRText(text db.RunPRText) bool {
	return text.Title != "" || text.Body != ""
}

func formatPRTextPushOptions(text db.RunPRText) []string {
	var options []string
	if opt := formatOpaquePushOption(prTitlePushOptionPrefix, text.Title); opt != "" {
		options = append(options, opt)
	}
	if opt := formatOpaquePushOption(prBodyPushOptionPrefix, text.Body); opt != "" {
		options = append(options, opt)
	}
	return options
}

func parsePRTextPushOptions(options []string) (db.RunPRText, error) {
	title, err := parseOpaquePushOptions(options, prTitlePushOptionPrefix, "pr title")
	if err != nil {
		return db.RunPRText{}, err
	}
	body, err := parseOpaquePushOptions(options, prBodyPushOptionPrefix, "pr body")
	if err != nil {
		return db.RunPRText{}, err
	}
	return db.RunPRText{Title: title, Body: body}, nil
}

func requireDaemonHonorsRunPRText(client closingIssueRefsUpdateClient, text db.RunPRText) error {
	if !hasRunPRText(text) {
		return nil
	}
	var result ipc.ProbeRunPRTextResult
	err := client.Call(ipc.MethodProbeRunPRText, &ipc.ProbeRunPRTextParams{}, &result)
	if err == nil && !result.OK {
		err = errors.New("daemon declined the supplied PR text capability")
	}
	if err != nil {
		return fmt.Errorf("the running daemon is too old to honor --pr-title or --pr-body-file (%v); restart it with `no-mistakes daemon restart` so the current binary serves it", err)
	}
	return nil
}

func conflictingActiveRunPRText(run *ipc.RunInfo, text db.RunPRText) error {
	if run == nil {
		return nil
	}
	if text.Title != "" && run.PRTitleDigest != db.PRTextDigest(text.Title, "") {
		return fmt.Errorf("active run %s is already in progress without this --pr-title", run.ID)
	}
	if text.Body != "" && run.PRBodyDigest != db.PRTextDigest("", text.Body) {
		return fmt.Errorf("active run %s is already in progress without this --pr-body-file description", run.ID)
	}
	return nil
}
