package agent

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type opencodeMessageFailure struct {
	name       string
	message    string
	statusCode int
	retries    int
	retryable  bool
	structured bool
	terminal   bool

	toolActivity bool
}

func newOpencodeMessageFailure(e *opencodeMessageError, toolActivity bool) error {
	if e == nil {
		return nil
	}
	return &opencodeMessageFailure{
		name:         e.Name,
		message:      e.message(),
		statusCode:   e.statusCode(),
		retries:      e.retries(),
		retryable:    e.retryable(),
		structured:   e.IsStructuredOutput(),
		terminal:     isTerminalRetryError(strings.ToLower(strings.Join(e.providerText(), "\n"))),
		toolActivity: toolActivity,
	}
}

func (e *opencodeMessageFailure) StructuredOutputRejected() bool { return e.structured }

func (e *opencodeMessageFailure) TerminalProviderError() bool { return e.terminal }

func (e *opencodeMessageFailure) Error() string {
	// StructuredOutputError keeps its own wording: the actionable fact is
	// that opencode already spent its internal retries trying to make the
	// model call the StructuredOutput tool.
	if e.structured {
		return fmt.Sprintf("opencode structured output failed after %d internal retries: %s",
			e.retries, e.detail())
	}
	name := e.name
	if name == "" {
		name = "error"
	}
	msg := fmt.Sprintf("opencode %s: %s", name, e.detail())
	if e.statusCode != 0 {
		msg = fmt.Sprintf("opencode %s (status %d): %s", name, e.statusCode, e.detail())
	}
	// Without this clause a withheld retry is indistinguishable from a
	// failure opencode called non-retryable, so an operator reading a 503
	// would expect the attempts the run did not spend.
	if e.retryable && e.toolActivity {
		msg += " (not retried: the failed turn already ran tools)"
	}
	return msg
}

func (e *opencodeMessageFailure) detail() string {
	if msg := strings.TrimSpace(e.message); msg != "" {
		return msg
	}
	return "no detail reported"
}

func (e *opencodeMessageFailure) label() string {
	name := e.name
	if name == "" {
		name = "message error"
	}
	if e.statusCode != 0 {
		return fmt.Sprintf("opencode %s %d", name, e.statusCode)
	}
	return "opencode " + name
}

func classifyOpencodeTransient(err error) (string, bool) {
	var failure *opencodeMessageFailure
	if errors.As(err, &failure) {
		// A retry starts a FRESH opencode session (runOnce always calls
		// createSession), so it replays the whole prompt with no memory of
		// the tools the failed attempt already executed - a second commit, a
		// second file write, a second posted comment. Nothing in the wire
		// protocol says which of those were idempotent, so a turn that got
		// as far as running a tool fails closed and the operator decides.
		// The failure this retry exists for - a provider blip that kills the
		// turn before the model acts - is untouched by the gate.
		// Resuming the failed session instead is not a fix: opencode's
		// message endpoint only appends another user message, so whether the
		// model re-runs the tool stays its judgement rather than a guarantee.
		if failure.retryable && !failure.terminal && !failure.toolActivity {
			return failure.label(), true
		}
		return "", false
	}
	// Everything else - a dropped SSE stream, a failed message request, an
	// unparseable turn, a thinking conflict - reaches the shared substring
	// classifier with no verdict from opencode, and that classifier reads
	// only text. The marker is the one place the tool evidence survives, so
	// it is checked before the fall-through rather than at each call site.
	if opencodeReplayUnsafe(err) {
		return "", false
	}
	return classifyTransient(err)
}

func isOpencodeToolPart(partType string) bool {
	return partType == "tool" || strings.HasPrefix(partType, "tool-")
}

type opencodeToolEvidence int

const (
	opencodeToolsUnknown opencodeToolEvidence = iota
	opencodeToolsNone
	opencodeToolsRan
)

func (e opencodeToolEvidence) replaySafe() bool { return e == opencodeToolsNone }

func (e opencodeToolEvidence) String() string {
	switch e {
	case opencodeToolsRan:
		return "ran tools"
	case opencodeToolsNone:
		return "no tools"
	default:
		return "unknown"
	}
}

func (e opencodeToolEvidence) reason() string {
	if e == opencodeToolsRan {
		return "the failed turn already ran tools"
	}
	return "could not verify the failed turn ran no tools"
}

func (e opencodeToolEvidence) marker() error {
	if e == opencodeToolsRan {
		return errOpencodeToolsAlreadyRan
	}
	return errOpencodeToolActivityUnknown
}

func opencodeReplayUnsafe(err error) bool {
	var failure *opencodeMessageFailure
	return (errors.As(err, &failure) && failure.toolActivity) ||
		errors.Is(err, errOpencodeToolsAlreadyRan) || errors.Is(err, errOpencodeToolActivityUnknown)
}

func resolveOpencodeToolEvidence(state *opencodeStreamState, mr opencodeMessageResult, streamComplete bool) opencodeToolEvidence {
	if state != nil && state.toolInvoked {
		return opencodeToolsRan
	}
	if mr.resp != nil {
		for _, part := range mr.resp.Parts {
			if isOpencodeToolPart(part.Type) {
				return opencodeToolsRan
			}
		}
		return opencodeToolsNone
	}
	if streamComplete {
		return opencodeToolsNone
	}
	if mr.settled && requestNeverReachedOpencode(mr.err) {
		return opencodeToolsNone
	}
	return opencodeToolsUnknown
}

func requestNeverReachedOpencode(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

type opencodeMessageResult struct {
	resp    *opencodeMessageResponse
	err     error
	settled bool
}

var opencodeEvidenceWait = 10 * time.Second

func pollOpencodeMessage(ch <-chan opencodeMessageResult) opencodeMessageResult {
	select {
	case mr := <-ch:
		return mr
	default:
		return opencodeMessageResult{}
	}
}

func awaitOpencodeMessage(ch <-chan opencodeMessageResult, d time.Duration) opencodeMessageResult {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case mr := <-ch:
		return mr
	case <-timer.C:
		return opencodeMessageResult{}
	}
}

type opencodeToolActivityFailure struct {
	err      error
	evidence opencodeToolEvidence
}

func (e *opencodeToolActivityFailure) Unwrap() []error {
	return []error{e.err, e.evidence.marker()}
}

func (e *opencodeToolActivityFailure) Error() string {
	msg := e.err.Error()
	// Same convention as opencodeMessageFailure: name the withheld retry
	// only where there would have been one, so it never reads as an
	// explanation for a failure that was never going to be retried.
	// Classifying the cause here cannot recurse - the wrapper is not part of
	// it.
	if _, retryable := classifyTransient(e.err); retryable {
		msg += " (not retried: " + e.evidence.reason() + ")"
	}
	return msg
}

func opencodeTurnFailure(evidence opencodeToolEvidence, err error) error {
	if err == nil || evidence.replaySafe() {
		return err
	}
	return &opencodeToolActivityFailure{err: err, evidence: evidence}
}
