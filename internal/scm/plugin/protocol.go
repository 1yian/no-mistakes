package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const ProtocolVersion = 1

const (
	CmdStatus         = "status"
	CmdPRFind         = "pr find"
	CmdPRCreate       = "pr create"
	CmdPRUpdate       = "pr update"
	CmdPRView         = "pr view"
	CmdPRChecks       = "pr checks"
	CmdPRMergeability = "pr mergeability"
	CmdPRCheckLogs    = "pr check-logs"
	CmdPRMerged       = "pr merged"
	CmdPRRetarget     = "pr retarget"
)

const (
	ErrorCodeUnsupported = "unsupported"
	ErrorCodeHeadChanged = "head_changed"
)

var ErrProtocol = errors.New("provider plugin protocol violation")

var ErrTimeout = errors.New("provider plugin timed out")

type Repository struct {
	RemoteURL string
	Host      string
	RawHost   string
	Path      string
}

func contextArgs(plugin string, repo Repository) []string {
	return []string{
		flag("plugin", plugin),
		flag("repo", repo.Path),
		flag("host", repo.Host),
		flag("raw-host", repo.RawHost),
		flag("remote-url", repo.RemoteURL),
	}
}

func flag(name, value string) string { return "--" + name + "=" + value }

func commandWords(command string) []string { return strings.Fields(command) }

type failureDocument struct {
	Error *ResponseError `json:"error"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type statusResult struct {
	ProtocolVersion int `json:"protocol_version"`
	Capabilities    struct {
		MergeableState  bool `json:"mergeable_state"`
		FailedCheckLogs bool `json:"failed_check_logs"`
		MergedProof     bool `json:"merged_proof"`
		SetPRBaseBranch bool `json:"set_pr_base_branch"`
		CheckHeadSHA    bool `json:"check_head_sha"`
	} `json:"capabilities"`
	MaxPRBodyChars *int `json:"max_pr_body_chars"`
}

type wirePR struct {
	Number     prNumber `json:"number"`
	URL        string   `json:"url"`
	HeadBranch string   `json:"head_branch"`
	BaseBranch string   `json:"base_branch"`
	HeadSHA    string   `json:"head_sha"`
}

type prResult struct {
	PR *wirePR `json:"pr"`
}

type findResult struct {
	PR json.RawMessage `json:"pr"`
}

type viewResult struct {
	PR    *wirePR `json:"pr"`
	State string  `json:"state"`
	Title *string `json:"title"`
	Body  *string `json:"body"`
}

type wireCheck struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	HeadSHA     string `json:"head_sha"`
	Bucket      string `json:"bucket"`
	State       string `json:"state"`
	Link        string `json:"link"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	ExecutionID string `json:"execution_id"`
}

type checksResult struct {
	Checks *[]wireCheck `json:"checks"`
}

type mergeabilityResult struct {
	State string `json:"state"`
}

type checkLogsResult struct {
	Logs *string `json:"logs"`
}

type mergedResult struct {
	Merged         bool     `json:"merged"`
	Number         prNumber `json:"number"`
	URL            string   `json:"url"`
	HeadSHA        string   `json:"head_sha"`
	MergeCommitSHA string   `json:"merge_commit_sha"`
	MergedAt       string   `json:"merged_at"`
	MergedBy       string   `json:"merged_by"`
}

type prNumber string

func (n *prNumber) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*n = ""
		return nil
	}
	var text string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
	} else {
		text = string(data)
	}
	value, err := strconv.ParseUint(text, 10, 63)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != text {
		return fmt.Errorf("PR number %s must be a positive base-10 integer", data)
	}
	*n = prNumber(text)
	return nil
}

func decodeResult(stdout []byte, dst any) error {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return errors.New("exited 0 with empty stdout (want one JSON object)")
	}
	var raw json.RawMessage
	if err := decodeSingle(trimmed, &raw); err != nil {
		return fmt.Errorf("exited 0 with invalid JSON output: %w", err)
	}
	if raw[0] != '{' {
		return errors.New("exited 0 but the output is not a JSON object")
	}
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("exited 0 with invalid JSON output: %w", err)
	}
	if len(probe.Error) > 0 && !bytes.Equal(probe.Error, []byte("null")) {
		var reported failureDocument
		message := ""
		if json.Unmarshal(raw, &reported) == nil && reported.Error != nil {
			message = ": " + boundedText(reported.Error.Message)
		}
		return fmt.Errorf("exited 0 but reported an error%s (failures must exit non-zero)", message)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("returned an invalid result: %w", err)
	}
	return nil
}

func decodeFailure(stdout []byte) *ResponseError {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil
	}
	var doc failureDocument
	if err := decodeSingle(trimmed, &doc); err != nil {
		return nil
	}
	return doc.Error
}

func decodeSingle(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case err == nil:
		return errors.New("unexpected data after the JSON document")
	case errors.Is(err, io.EOF):
		return nil
	default:
		return err
	}
}
