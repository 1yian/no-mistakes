package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
)

var errOpencodeThinkingToolChoiceConflict = errors.New("opencode provider rejects required tool choice while thinking is enabled")

var errOpencodeForcedToolChoiceUnsupported = errors.New("opencode provider supports only auto tool choice")

var errOpencodeToolsAlreadyRan = errors.New("the failed turn already ran tools")

var errOpencodeToolActivityUnknown = errors.New("could not verify the failed turn ran no tools")

func thinkingConflict(evidence opencodeToolEvidence, cause error) error {
	err := errOpencodeThinkingToolChoiceConflict
	if !evidence.replaySafe() {
		err = fmt.Errorf("%w (%w)", err, evidence.marker())
	}
	if cause != nil {
		return fmt.Errorf("%w: %v", err, cause)
	}
	return err
}

func forcedToolChoiceConflict(evidence opencodeToolEvidence, cause error) error {
	err := errOpencodeForcedToolChoiceUnsupported
	if !evidence.replaySafe() {
		err = fmt.Errorf("%w (%w)", err, evidence.marker())
	}
	if cause != nil {
		return fmt.Errorf("%w: %v", err, cause)
	}
	return err
}

func opencodeFallbackTrigger(err error) bool {
	return errors.Is(err, errOpencodeThinkingToolChoiceConflict) ||
		errors.Is(err, errOpencodeForcedToolChoiceUnsupported)
}

var thinkingToolChoiceConflictPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:(?:required|forced)\s+tool[_ ]choice|tool[_ ]choice\s*(?:is\s*)?["']?(?:required|forced)["']?)\s+(?:is\s+)?(?:incompatible with|cannot be combined with|can't be combined with|cannot be used with|can't be used with|not supported (?:with|when))\s+(?:thinking|reasoning)(?:\s+(?:enabled|mode))?`),
	regexp.MustCompile(`(?i)(?:thinking|reasoning)(?:\s+(?:enabled|mode))?\s+(?:is\s+)?(?:incompatible with|cannot be combined with|can't be combined with|cannot be used with|can't be used with|not supported (?:with|when))\s+(?:(?:a|an|the)\s+)?(?:(?:required|forced)\s+tool[_ ]choice|tool[_ ]choice\s*(?:is\s*)?["']?(?:required|forced)["']?)`),
	regexp.MustCompile(`(?i)(?:thinking|reasoning)\s+may not be enabled when\s+tool[_ ]choice\s+forces\s+tool use`),
}

var forcedToolChoiceUnsupportedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)only\s+["']?\bauto\b["']?\s+(?:is\s+)?supported\s+for\s+["']?tool[_ ]choice["']?`),
	// After "only auto", require a verdict boundary (end / punctuation /
	// is|are) so "tool_choice: only auto scaling is enabled" stays a
	// quota note, not a blanket rejection. "tool_choice: only auto" and
	// "tool_choice: only \"auto\" is supported" still match.
	regexp.MustCompile(`(?i)["']?tool[_ ]choice["']?\s*:?\s*[^.,;:]{0,80}?\bonly\s+["']?\bauto\b["']?(?:\s+(?:is|are)\b|[.,;!?\n"']|$)`),
	regexp.MustCompile(`(?i)tool[_ ]choice\s+must\s+be\s+["']?\bauto\b["']?`),
	regexp.MustCompile(`(?i)unsupported\s+(?:value\s+for\s+)?["']?tool[_ ]choice["']?`),
	regexp.MustCompile(`(?i)["']?tool[_ ]choice["']?\s*(?:value|parameter)?\s*(?:is\s+)?(?:currently\s+)?unsupported\b`),
}

type opencodeAgent struct {
	bin       string
	extraArgs []string
	profile   agentcfg.Profile
	subprocessContext
	mu     sync.Mutex
	server *managedServer
	v2     bool   // protected by mu; selected before starting the managed server
	v2Auth string // private per-server credential, never persisted or passed in argv
}

func (a *opencodeAgent) Name() string { return "opencode" }

func (a *opencodeAgent) ReportsAgentAttempts() bool { return true }

func (a *opencodeAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	return runWithRetry(ctx, "opencode", opts, claudeMaxRetries, classifyOpencodeTransient, a.recoverTransientRetry, func() (*Result, error) {
		return a.runOnce(ctx, opts)
	})
}

func (a *opencodeAgent) recoverTransientRetry(label string) {
	if label != "connection refused" {
		return
	}
	a.mu.Lock()
	srv := a.server
	a.server = nil
	a.mu.Unlock()
	if srv != nil {
		srv.shutdown()
	}
}

func (a *opencodeAgent) runOnce(ctx context.Context, opts RunOpts) (*Result, error) {
	result, err := a.runOnceWithFormat(ctx, opts, true)
	if err == nil || len(opts.JSONSchema) == 0 || !opencodeFallbackTrigger(err) {
		return result, err
	}

	// The fallback is a second attempt in a fresh session, so a turn that
	// already invoked a tool would replay its side effects - and so would one
	// whose tool activity could not be established. Same reasoning as
	// classifyOpencodeTransient, and the same fail-closed answer: report the
	// conflict and let the operator decide.
	if opencodeReplayUnsafe(err) {
		return result, err
	}

	// OpenCode implements json_schema output as a required StructuredOutput
	// tool call. Some thinking-enabled models reject that combination, and
	// some gateways reject every non-auto tool_choice outright. Retry
	// once without the native format, while keeping the schema in the prompt
	// and validating the returned JSON against it in finalizeTextResult.
	emitAgentControl(opts, LifecycleEvent{
		Agent:   a.Name(),
		Phase:   LifecyclePhaseFallback,
		Message: "opencode starting a fresh prompt-only structured output session",
	})
	// Both turns really ran and both cost tokens, so the single row this
	// invocation records must carry both. Each attempt is its own fresh
	// session and opencode never reports cumulatively, so the two are
	// independent deltas that simply add.
	nativeUsage := TokenUsage{}
	if result != nil {
		nativeUsage = result.Usage
	}
	result, fallbackErr := a.runOnceWithFormat(ctx, opts, false)
	if result == nil {
		result = resultFromUsage(nativeUsage)
	} else {
		result.Usage.Add(nativeUsage)
		result.UsageReported = result.Usage.Reported
		result.CacheCreationReported = result.Usage.CacheCreationReported
	}
	if fallbackErr != nil {
		return result, fmt.Errorf("opencode prompt-only structured output fallback: %w", fallbackErr)
	}
	return result, nil
}

func (a *opencodeAgent) runOnceWithFormat(ctx context.Context, opts RunOpts, nativeFormat bool) (*Result, error) {
	// Start server on first invocation (synchronized)
	baseURL, err := a.ensureServer(ctx, opts.CWD, opts.Env)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	v2 := a.v2
	a.mu.Unlock()
	if v2 {
		return a.runV2(ctx, opts, baseURL)
	}

	// Create session with blanket permissions
	sessionID, err := a.createSession(ctx, baseURL, opts.CWD)
	if err != nil {
		return nil, err
	}
	defer a.deleteSession(baseURL, sessionID)

	// Build prompt with schema instructions if provided
	prompt := opts.Prompt
	if len(opts.JSONSchema) > 0 {
		prompt = buildOpencodePrompt(prompt, opts.JSONSchema)
	}

	// Connect to SSE event stream
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	eventBody, err := a.connectEventStream(streamCtx, baseURL)
	if err != nil {
		return nil, err
	}
	defer eventBody.Close()

	// Send message concurrently — blocks until agent completes
	msgCtx, msgCancel := context.WithCancel(ctx)
	defer msgCancel()
	msgCh := make(chan opencodeMessageResult, 1)
	go func() {
		schema := opts.JSONSchema
		if !nativeFormat {
			schema = nil
		}
		resp, err := a.sendMessage(msgCtx, baseURL, sessionID, prompt, schema)
		msgCh <- opencodeMessageResult{resp: resp, err: err, settled: true}
	}()

	// Process SSE events until session.idle
	state := &opencodeStreamState{
		sessionID:  sessionID,
		onChunk:    opts.OnChunk,
		textParts:  make(map[string]*opencodeTextPart),
		usageByMsg: make(map[string]TokenUsage),
	}
	err = parseOpencodeSSE(eventBody, state)
	streamCancel()

	if err != nil {
		// The stream carried the tool events, so with it gone the message
		// response is the remaining record of what the turn ran. Taking only
		// what has already arrived answers "nothing yet" while the request is
		// still in flight, and that is not the same answer as "no tool ran" -
		// so the evidence is resolved here, before anything classifies the
		// failure.
		mr := pollOpencodeMessage(msgCh)
		aborted := false
		if !mr.settled {
			// Aborting is the cleanup this branch already did. Doing it
			// first also ends the turn opencode is still running, which is
			// what makes the in-flight request answer - with the assistant
			// message and its parts. The wait is bounded: a server that is
			// gone never answers, and that turn is simply unverifiable.
			a.abortSession(baseURL, sessionID)
			aborted = true
			mr = awaitOpencodeMessage(msgCh, opencodeEvidenceWait)
		}
		evidence := resolveOpencodeToolEvidence(state, mr, false)
		if mr.settled && mr.err != nil {
			if nativeFormat && isThinkingToolChoiceConflictText(mr.err.Error()) {
				return resultFromUsage(state.usage), thinkingConflict(evidence, mr.err)
			}
			if nativeFormat && isForcedToolChoiceUnsupportedText(mr.err.Error()) {
				return nil, forcedToolChoiceConflict(evidence, mr.err)
			}
			return resultFromUsage(state.usage), opencodeTurnFailure(evidence, fmt.Errorf("opencode message: %w", mr.err))
		}
		if !aborted {
			a.abortSession(baseURL, sessionID)
		}
		if nativeFormat && errors.Is(err, errOpencodeThinkingToolChoiceConflict) {
			return resultFromUsage(state.usage), thinkingConflict(evidence, nil)
		}
		if nativeFormat && errors.Is(err, errOpencodeForcedToolChoiceUnsupported) {
			return nil, forcedToolChoiceConflict(evidence, nil)
		}
		return resultFromUsage(state.usage), opencodeTurnFailure(evidence, fmt.Errorf("opencode events: %w", err))
	}

	// Wait for message response. The stream ran to session.idle, so every
	// tool part of the session crossed it and the evidence is settled however
	// this request ends.
	mr := <-msgCh
	evidence := resolveOpencodeToolEvidence(state, mr, true)
	if mr.err != nil {
		if nativeFormat && isThinkingToolChoiceConflictText(mr.err.Error()) {
			return resultFromUsage(state.usage), thinkingConflict(evidence, mr.err)
		}
		if nativeFormat && isForcedToolChoiceUnsupportedText(mr.err.Error()) {
			return nil, forcedToolChoiceConflict(evidence, mr.err)
		}
		return resultFromUsage(state.usage), opencodeTurnFailure(evidence, fmt.Errorf("opencode message: %w", mr.err))
	}

	// Update usage and text from message response
	responseText := ""
	responseFinalText := ""
	if mr.resp != nil && mr.resp.Info != nil {
		streamedText := state.lastText
		streamedFinalText := state.lastFinalText
		emitResponseChunk := func(chunk string) {
			if opts.OnChunk == nil || chunk == "" {
				return
			}
			state.emitSeparatorIfNeeded()
			opts.OnChunk(chunk)
			state.hasEmittedText = true
		}
		if mr.resp.Info.Role == "assistant" && mr.resp.Info.Tokens != nil {
			state.usageByMsg[mr.resp.Info.ID] = opencodeTokensToUsage(mr.resp.Info.Tokens)
			state.usage = accumulateUsage(state.usageByMsg)
		}
		for _, part := range mr.resp.Parts {
			if part.Type != "text" || strings.TrimSpace(part.Text) == "" {
				continue
			}
			responseText += part.Text
			if part.Metadata != nil && part.Metadata.OpenAI != nil && part.Metadata.OpenAI.Phase == "final_answer" {
				responseFinalText += part.Text
			}
		}
		if responseText != "" {
			state.lastText = responseText
		}
		if responseFinalText != "" {
			state.lastFinalText = responseFinalText
		}
		if responseFinalText != "" {
			responseText = responseFinalText
		}
		if opts.OnChunk != nil && responseText != "" {
			streamedResponseText := streamedText
			if streamedFinalText != "" {
				streamedResponseText = streamedFinalText
			}
			switch {
			case !state.hasEmittedText:
				emitResponseChunk(responseText)
			case streamedResponseText == "":
				emitResponseChunk(responseText)
			case strings.HasPrefix(responseText, streamedResponseText):
				suffix := responseText[len(streamedResponseText):]
				emitResponseChunk(suffix)
			}
		}
	}

	// Prefer structured output from response
	if mr.resp != nil && mr.resp.Info != nil && mr.resp.Info.Structured != nil {
		return &Result{
			Output:                mr.resp.Info.Structured,
			Text:                  state.lastText,
			Usage:                 state.usage,
			UsageReported:         state.usage.Reported,
			CacheCreationReported: state.usage.CacheCreationReported,
		}, nil
	}

	// A thinking model rejecting the forced tool_choice, or a gateway
	// rejecting every non-auto tool_choice, is handled by the
	// prompt-only fallback in runOnce, so both must be recognised before the
	// general failure below claims them.
	if nativeFormat && mr.resp != nil && mr.resp.Info != nil && isThinkingToolChoiceConflict(mr.resp.Info.Error) {
		return resultFromUsage(state.usage), thinkingConflict(evidence, nil)
	}
	if nativeFormat && mr.resp != nil && mr.resp.Info != nil && isForcedToolChoiceUnsupported(mr.resp.Info.Error) {
		return nil, forcedToolChoiceConflict(evidence, nil)
	}

	// A turn that failed reports its cause on info.error rather than on the
	// HTTP status, so the request itself looks successful. Surface that error
	// instead of falling through to the streamed text: opencode leaves no
	// usable text behind a failed turn, so the fallback reports the
	// undiagnosable "opencode returned no text output" and hides causes such
	// as a provider rejecting the forced tool_choice that json_schema output
	// requires, or an expired provider credential. Any prose streamed before
	// the failure is reasoning, not an answer. This supersedes the narrower
	// StructuredOutputError-only branch: opencodeMessageFailure renders that
	// case with the same wording and decodes the nested error payload the
	// flat fields never carried.
	if mr.resp != nil && mr.resp.Info != nil && mr.resp.Info.Error != nil {
		return resultFromUsage(state.usage), newOpencodeMessageFailure(mr.resp.Info.Error, evidence == opencodeToolsRan)
	}

	// Fall back to parsing JSON from text
	outputText := state.lastFinalText
	if outputText == "" {
		outputText = state.lastText
	}
	result, err := finalizeTextResult("opencode", outputText, opts.JSONSchema, state.usage)
	if err != nil {
		// A parse failure quotes the model's own output, so whether it looks
		// transient to the shared classifier is decided by text the model
		// wrote. It takes the same gate as the rest.
		return result, opencodeTurnFailure(evidence, err)
	}
	return result, nil
}

func isThinkingToolChoiceConflict(e *opencodeMessageError) bool {
	for _, text := range e.providerText() {
		if isThinkingToolChoiceConflictText(text) {
			return true
		}
	}
	return false
}

func isThinkingToolChoiceConflictText(text string) bool {
	for _, pattern := range thinkingToolChoiceConflictPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	for _, sentence := range sentenceSplitPattern.Split(text, -1) {
		if !thinkingMentionPattern.MatchString(sentence) {
			continue
		}
		for _, pattern := range forcedToolChoiceUnsupportedPatterns {
			if pattern.MatchString(sentence) {
				return true
			}
		}
	}
	return false
}

var thinkingMentionPattern = regexp.MustCompile(`(?i)\b(?:thinking|reasoning)\b`)

var sentenceSplitPattern = regexp.MustCompile(`[.!?\n]+`)

func isForcedToolChoiceUnsupported(e *opencodeMessageError) bool {
	for _, text := range e.providerText() {
		if isForcedToolChoiceUnsupportedText(text) {
			return true
		}
	}
	return false
}

func isForcedToolChoiceUnsupportedText(text string) bool {
	for _, sentence := range sentenceSplitPattern.Split(text, -1) {
		if thinkingMentionPattern.MatchString(sentence) {
			continue
		}
		for _, pattern := range forcedToolChoiceUnsupportedPatterns {
			if pattern.MatchString(sentence) {
				return true
			}
		}
	}
	return false
}

func (a *opencodeAgent) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.server != nil {
		a.server.shutdown()
		a.server = nil
	}
	return nil
}
