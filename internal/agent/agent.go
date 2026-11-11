package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type Agent interface {
	Name() string
	Run(ctx context.Context, opts RunOpts) (*Result, error)
	Close() error
}

type RunOpts struct {
	Prompt                   string
	RequireCompletedChildren bool
	Env                      []string
	CWD                      string
	JSONSchema               json.RawMessage      // structured output schema (optional)
	OnChunk                  func(text string)    // streaming text callback (optional)
	OnLifecycle              func(LifecycleEvent) // native agent lifecycle callback (optional)
	Session                  *SessionRef
	SessionFallback          bool
	Purpose                  string
	Round                    int
	SessionFallbackReason    string
	Workload                 *InvocationWorkload
	OnAttempt                func(Attempt)
	ConfiguredModel          string
}

func IsStructuredOutputRejected(err error) bool {
	var rejection interface {
		StructuredOutputRejected() bool
	}
	return errors.As(err, &rejection) && rejection.StructuredOutputRejected()
}

func IsUsageLimitReached(err error) bool {
	var limit interface {
		UsageLimitReached() bool
	}
	return errors.As(err, &limit) && limit.UsageLimitReached()
}

func IsTerminalProviderError(err error) bool {
	var terminal interface {
		TerminalProviderError() bool
	}
	return errors.As(err, &terminal) && terminal.TerminalProviderError()
}

type structuredOutputRejection struct{ err error }

func (e *structuredOutputRejection) Error() string                { return e.err.Error() }
func (e *structuredOutputRejection) Unwrap() error                { return e.err }
func (*structuredOutputRejection) StructuredOutputRejected() bool { return true }

func RejectStructuredOutput(err error) error {
	if err == nil || IsStructuredOutputRejected(err) {
		return err
	}
	return &structuredOutputRejection{err: err}
}

type Attempt struct {
	ConfiguredModel string
	Agent           string
	Result          *Result
	Err             error
	StartedAt       time.Time
	CompletedAt     time.Time
	Session         *SessionRef
	SessionFallback bool
}

type SessionRef struct {
	ID    string
	Agent string
}

type SessionResumer interface {
	SupportsSessionResume() bool
}

func SupportsSessionResume(a Agent) bool {
	r, ok := a.(SessionResumer)
	return ok && r.SupportsSessionResume()
}

type SessionProviderMatcher interface {
	SupportsSessionProvider(string) bool
}

func SupportsSessionProvider(a Agent, provider string) bool {
	if provider == "" {
		return false
	}
	if matcher, ok := a.(SessionProviderMatcher); ok {
		return matcher.SupportsSessionProvider(provider)
	}
	return a != nil && a.Name() == provider && SupportsSessionResume(a)
}

type AttemptReporter interface {
	ReportsAgentAttempts() bool
}

func ReportsAgentAttempts(a Agent) bool {
	r, ok := a.(AttemptReporter)
	return ok && r.ReportsAgentAttempts()
}

type GateInstructionNeutralizer interface {
	NeutralizesGateInstructions() bool
}

func NeutralizesGateInstructions(a Agent) bool {
	if a == nil {
		return false
	}
	n, ok := a.(GateInstructionNeutralizer)
	return ok && n.NeutralizesGateInstructions()
}

func EnsureGateNeutralized(a Agent) error {
	if a == nil {
		return fmt.Errorf("no gate agent configured")
	}
	if NeutralizesGateInstructions(a) {
		return nil
	}
	return fmt.Errorf("gate agent %q does not neutralize the target repository's project "+
		"agent-instruction files (AGENTS.md/CLAUDE.md); refusing to launch it in the target "+
		"checkout. Only codex, claude, pi, and acp:omp have a verified neutralization knob (and only "+
		"when it is not overridden by agent_args_override or an acp_registry_overrides entry); set "+
		"'agent' to codex, claude, pi, or acp:omp in ~/.no-mistakes/config.yaml", a.Name())
}

type LifecycleEvent struct {
	Agent     string
	Phase     string
	PID       int
	Message   string
	NextAgent string
}

type Result struct {
	Output                 json.RawMessage
	Text                   string
	Usage                  TokenUsage
	UsageReported          bool
	SessionID              string
	Resumed                bool
	ResumeRetryable        *bool
	Model                  string
	ModelProvider          string
	Provider               string
	Metrics                *InvocationMetrics
	CacheCreationReported  bool
	SessionUsageCumulative bool
}

type TokenUsage struct {
	InputTokens           int
	OutputTokens          int
	CacheReadTokens       int
	CacheCreationTokens   int
	ReasoningTokens       int
	ReasoningReported     bool
	Reported              bool
	CacheCreationReported bool
}

type InvocationWorkload struct {
	Files int
	Lines int
}

type Options struct {
	ACPRegistryOverrides   map[string]string
	Environment            runenv.Overlay
	DisableProjectSettings bool
	Profile                agentcfg.Profile
}

func resultFromUsage(usage TokenUsage) *Result {
	if !usage.Reported && !usage.CacheCreationReported {
		return nil
	}
	return &Result{
		Usage:                 usage,
		UsageReported:         usage.Reported,
		CacheCreationReported: usage.CacheCreationReported,
	}
}

func failedResult(usage TokenUsage, sessionID string) *Result {
	res := resultFromUsage(usage)
	if sessionID == "" {
		return res
	}
	if res == nil {
		res = &Result{}
	}
	res.SessionID = sessionID
	return res
}

func textResult(text string, usage TokenUsage) *Result {
	return &Result{
		Text:                  text,
		Usage:                 usage,
		UsageReported:         usage.Reported,
		CacheCreationReported: usage.CacheCreationReported,
	}
}

func finalizeTextResult(agentName, text string, schema json.RawMessage, usage TokenUsage) (*Result, error) {
	if text == "" {
		err := fmt.Errorf("%s returned no text output", agentName)
		if len(schema) > 0 {
			return resultFromUsage(usage), RejectStructuredOutput(err)
		}
		return resultFromUsage(usage), err
	}
	if len(schema) == 0 {
		return textResult(text, usage), nil
	}

	output, err := parseStructuredTextOutput(text, schema, strings.HasPrefix(agentName, "acp:"))
	if err != nil {
		return resultFromUsage(usage), RejectStructuredOutput(fmt.Errorf("%s output parse: %w (output snippet: %q)", agentName, err, outputSnippet(text)))
	}

	res := textResult(text, usage)
	res.Output = output
	return res, nil
}

func outputSnippet(text string) string {
	const max = 200
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return trimmed
}

func parseStructuredTextOutput(text string, schema json.RawMessage, preferTerminal bool) (json.RawMessage, error) {
	validationSchema, err := textValidationSchema(schema)
	if err != nil {
		return nil, err
	}

	output, rawErr := parseStructuredCandidate([]byte(text), validationSchema)
	if rawErr == nil {
		return output, nil
	}

	closed, openCands := fencedJSONCandidates(text)

	var candidateErr error
	parseCandidates := func(cands []string) []json.RawMessage {
		var parsed []json.RawMessage
		for _, candidate := range cands {
			fenced, err := parseStructuredCandidate([]byte(candidate), validationSchema)
			if err == nil {
				parsed = append(parsed, fenced)
				continue
			}
			if candidateErr == nil {
				candidateErr = err
			}
		}
		return parsed
	}
	// Closed fences are the canonical structured-output shape and win over
	// open (unclosed, pi JSONL) candidates: prose that quotes ```json as an
	// inline example never shadows a real trailing block, and an unclosed
	// fence never competes with a closed one. Only when no closed fence
	// parses do we consult the open candidates.
	closedParsed := parseCandidates(closed)
	if len(closedParsed) > 1 {
		return nil, fmt.Errorf("multiple JSON code fences found in output")
	}
	openParsed := parseCandidates(openCands)
	if len(openParsed) > 1 {
		return nil, fmt.Errorf("multiple JSON code fences found in output")
	}

	var fenced json.RawMessage
	if len(closedParsed) == 1 {
		fenced = closedParsed[0]
	} else if len(openParsed) == 1 {
		fenced = openParsed[0]
	}

	bareParsed, bareErr := bareJSONObjects(text, validationSchema)
	if len(bareParsed) > 1 {
		if !preferTerminal {
			return nil, fmt.Errorf("multiple bare JSON objects found in output")
		}
		bareParsed = bareParsed[len(bareParsed)-1:]
	}
	if fenced != nil && len(bareParsed) == 1 {
		if !jsonEqual(fenced, bareParsed[0]) {
			return nil, fmt.Errorf("conflicting JSON candidates found in output")
		}
		return fenced, nil
	}
	if fenced != nil {
		return fenced, nil
	}
	if len(bareParsed) == 1 {
		return bareParsed[0], nil
	}
	if candidateErr == nil && bareErr != nil {
		candidateErr = bareErr
	}

	if candidateErr != nil {
		return nil, candidateErr
	}
	if _, err := decodeJSONValue([]byte(text)); err == nil {
		return nil, rawErr
	}

	trimmedText := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmedText, "{") {
		return nil, fmt.Errorf("ended its turn with prose instead of the required JSON object")
	}
	return nil, rawErr
}

func textValidationSchema(schema json.RawMessage) (json.RawMessage, error) {
	if len(schema) == 0 {
		return nil, nil
	}

	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		return nil, err
	}
	allowOptionalSchemaNulls(value)
	return json.Marshal(value)
}

func fencedJSONCandidates(text string) (closed, open []string) {
	rest := text
	for {
		marker := strings.Index(rest, "```")
		if marker < 0 {
			return closed, open
		}
		contentStart, info := fenceContentStart(rest, marker)
		if !strings.EqualFold(strings.TrimSpace(info), "json") {
			// Not a JSON fence: skip it (or its whole block) and keep
			// scanning for a real opener.
			after := skipFenceBlock(rest[contentStart:])
			if after < 0 {
				after = 0
			}
			rest = rest[contentStart+after:]
			continue
		}
		body := rest[contentStart:]
		end, _ := indexJSONFenceClose(body)
		if end < 0 {
			// No closing fence: the JSON body runs to the end of the output
			// (pi JSONL shape). Take everything after the opener as an open
			// candidate; parseStructuredCandidate validates it later and
			// only adopts it when it is actually valid JSON.
			open = append(open, body)
			return closed, open
		}
		closed = append(closed, body[:end])
		// Resume after the opener marker, not after this candidate's closing
		// fence: an unvalidated candidate may span unrelated text, and a
		// later real block must still be discovered on its own.
		rest = rest[marker+3:]
	}
}

func fenceContentStart(text string, fenceStart int) (int, string) {
	after := text[fenceStart+3:]
	lineEnd := strings.IndexByte(after, '\n')
	if lineEnd >= 0 {
		after = after[:lineEnd]
	}
	info, rest := fenceInfoToken(after)
	start := fenceStart + 3 + rest
	if start < len(text) && text[start] == '\n' {
		start++
	}
	return start, info
}

func fenceInfoToken(s string) (string, int) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	start := i
	for i < len(s) {
		c := s[i]
		isWord := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if !isWord {
			break
		}
		i++
	}
	return s[start:i], i
}

func skipFenceBlock(text string) int {
	depth := 1
	for lineStart := 0; lineStart < len(text); {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		line := text[lineStart:lineEnd]
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") {
			if strings.TrimSpace(trimmed[3:]) == "" {
				depth--
				if depth == 0 {
					return lineStart + (len(line) - len(trimmed)) + 3
				}
			} else {
				depth++
			}
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	return -1
}

func indexJSONFenceClose(text string) (int, int) {
	for lineStart := 0; lineStart < len(text); {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		line := text[lineStart:lineEnd]
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") {
			indent := len(line) - len(trimmed)
			return lineStart, lineStart + indent + 3
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	trimmed := strings.TrimRight(text, " \t\r\n")
	if strings.HasSuffix(trimmed, "```") {
		return len(trimmed) - 3, len(trimmed)
	}
	return -1, -1
}

type bareObject struct {
	obj        json.RawMessage
	raw        []byte
	startIndex int
	endIndex   int
}

func bareJSONObjects(text string, schema json.RawMessage) ([]json.RawMessage, error) {
	var valid []bareObject
	var objects []bareObject
	var lastErr error
	for i := 0; i < len(text); i++ {
		if strings.HasPrefix(text[i:], "```") {
			contentStart, _ := fenceContentStart(text, i)
			next := skipFenceBlock(text[contentStart:])
			if next < 0 {
				// An unpaired fence marker (e.g. ```json quoted inside prose)
				// must not abort the scan of the whole output; skip the marker
				// itself and keep looking for a bare object.
				i += 2
				continue
			}
			i = contentStart + next - 1
			continue
		}
		if text[i] != '{' {
			continue
		}
		end, ok := scanBalancedObject(text, i)
		if !ok {
			continue
		}
		candidate := text[i:end]
		entry := bareObject{raw: []byte(candidate), startIndex: i, endIndex: end}
		obj, err := parseStructuredCandidate([]byte(candidate), schema)
		if err == nil {
			entry.obj = obj
			valid = append(valid, entry)
			lastErr = nil
		} else if lastErr == nil {
			lastErr = err
		}
		objects = append(objects, entry)
		i = end - 1
	}
	if len(valid) > 1 {
		parsed := make([]json.RawMessage, 0, len(valid))
		for _, candidate := range valid {
			parsed = append(parsed, candidate.obj)
		}
		return parsed, nil
	}
	if len(valid) == 1 && trailingNonJSONResidue(text[valid[0].endIndex:]) {
		return []json.RawMessage{valid[0].obj}, nil
	}
	if len(valid) == 0 {
		fused, err := fuseAdjacentBareObjects(text, objects, schema)
		if err != nil {
			return nil, err
		}
		if fused != nil {
			return []json.RawMessage{fused}, nil
		}
	}
	return nil, lastErr
}

func trailingNonJSONResidue(rest string) bool {
	trimmed := strings.TrimSpace(rest)
	if trimmed == "" {
		return true
	}
	if strings.Contains(rest, "{") || strings.Contains(rest, "```") {
		return false
	}
	for _, token := range strings.Fields(trimmed) {
		if isProtocolResidueToken(token) {
			continue
		}
		return false
	}
	return true
}

func isProtocolResidueToken(token string) bool {
	rest, sawTag := stripMarkupTags(token)
	if !sawTag {
		return !containsAlphanumeric(token)
	}
	return !containsAlphanumeric(rest)
}

func stripMarkupTags(token string) (string, bool) {
	var rest strings.Builder
	sawTag := false
	for i := 0; i < len(token); {
		if token[i] == '<' {
			if end := strings.IndexByte(token[i:], '>'); end >= 0 {
				sawTag = true
				i += end + 1
				continue
			}
		}
		rest.WriteByte(token[i])
		i++
	}
	return rest.String(), sawTag
}

func containsAlphanumeric(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	})
}

var errSplitBareObjects = errors.New("split bare JSON objects could not be fused into one valid object")

var errDuplicateBareObjectKey = errors.New("adjacent bare JSON objects share a top-level key")

func fuseAdjacentBareObjects(text string, objects []bareObject, schema json.RawMessage) (json.RawMessage, error) {
	for i := 0; i < len(objects); {
		j := i
		for j+1 < len(objects) && isBareObjectSeparator(text[objects[j].endIndex:objects[j+1].startIndex]) {
			j++
		}
		if j > i && trailingNonJSONResidue(text[objects[j].endIndex:]) {
			fused, err := fuseBareObjects(objects[i:j+1], schema)
			if err == nil {
				return fused, nil
			}
			if errors.Is(err, errSplitBareObjects) {
				return nil, errSplitBareObjects
			}
			// Competing values for one key, or a span that is not a JSON object
			// at all: not a recoverable split, so fall through to the generic
			// schema error and stay terminal.
			return nil, nil
		}
		i = j + 1
	}
	return nil, nil
}

func isBareObjectSeparator(gap string) bool {
	trimmed := strings.TrimSpace(gap)
	return trimmed == "" || trimmed == ","
}

func fuseBareObjects(objects []bareObject, schema json.RawMessage) (json.RawMessage, error) {
	merged := make(map[string]json.RawMessage)
	for _, object := range objects {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(object.raw, &fields); err != nil {
			return nil, err
		}
		for key, value := range fields {
			if _, exists := merged[key]; exists {
				return nil, errDuplicateBareObjectKey
			}
			merged[key] = value
		}
	}
	fused, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	out, err := parseStructuredCandidate(fused, schema)
	if err != nil {
		return nil, errSplitBareObjects
	}
	return out, nil
}

func jsonEqual(a, b json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b)) {
		return true
	}
	valA, err := decodeJSONValue(a)
	if err != nil {
		return false
	}
	valB, err := decodeJSONValue(b)
	if err != nil {
		return false
	}
	return jsonValuesEqual(valA, valB)
}

func jsonValuesEqual(a, b any) bool {
	switch valueA := a.(type) {
	case json.Number:
		valueB, ok := b.(json.Number)
		if !ok {
			return false
		}
		return jsonNumbersEqual(valueA, valueB)
	case map[string]any:
		valueB, ok := b.(map[string]any)
		if !ok || len(valueA) != len(valueB) {
			return false
		}
		for key, childA := range valueA {
			childB, ok := valueB[key]
			if !ok || !jsonValuesEqual(childA, childB) {
				return false
			}
		}
		return true
	case []any:
		valueB, ok := b.([]any)
		if !ok || len(valueA) != len(valueB) {
			return false
		}
		for i := range valueA {
			if !jsonValuesEqual(valueA[i], valueB[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

type jsonDecimal struct {
	negative bool
	digits   string
	power    *big.Int
}

func jsonNumbersEqual(a, b json.Number) bool {
	decimalA, ok := parseJSONDecimal(a)
	if !ok {
		return false
	}
	decimalB, ok := parseJSONDecimal(b)
	if !ok {
		return false
	}
	if decimalA.digits == "" || decimalB.digits == "" {
		return decimalA.digits == decimalB.digits
	}
	if decimalA.negative != decimalB.negative {
		return false
	}

	orderA := decimalOrder(decimalA)
	orderB := decimalOrder(decimalB)
	if orderA.Cmp(orderB) != 0 {
		return false
	}

	maxDigits := len(decimalA.digits)
	if len(decimalB.digits) > maxDigits {
		maxDigits = len(decimalB.digits)
	}
	for i := 0; i < maxDigits; i++ {
		digitA, digitB := byte('0'), byte('0')
		if i < len(decimalA.digits) {
			digitA = decimalA.digits[i]
		}
		if i < len(decimalB.digits) {
			digitB = decimalB.digits[i]
		}
		if digitA != digitB {
			return false
		}
	}
	return true
}

func parseJSONDecimal(number json.Number) (jsonDecimal, bool) {
	raw := number.String()
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}

	exponent := new(big.Int)
	mantissa := raw
	if exponentIndex := strings.IndexAny(raw, "eE"); exponentIndex >= 0 {
		mantissa = raw[:exponentIndex]
		exponentText := raw[exponentIndex+1:]
		if strings.HasPrefix(exponentText, "+") {
			exponentText = exponentText[1:]
		}
		if exponentText == "" {
			return jsonDecimal{}, false
		}
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return jsonDecimal{}, false
		}
	}

	fractionalPart := ""
	if dotIndex := strings.IndexByte(mantissa, '.'); dotIndex >= 0 {
		fractionalPart = mantissa[dotIndex+1:]
		mantissa = mantissa[:dotIndex] + fractionalPart
	}

	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return jsonDecimal{}, true
	}
	trimmedDigits := strings.TrimRight(digits, "0")
	trailingZeros := len(digits) - len(trimmedDigits)
	power := new(big.Int).Sub(exponent, big.NewInt(int64(len(fractionalPart))))
	power.Add(power, big.NewInt(int64(trailingZeros)))
	return jsonDecimal{negative: negative, digits: trimmedDigits, power: power}, true
}

func decimalOrder(decimal jsonDecimal) *big.Int {
	order := new(big.Int).Set(decimal.power)
	return order.Add(order, big.NewInt(int64(len(decimal.digits))))
}

func scanBalancedObject(text string, start int) (int, bool) {
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			switch c {
			case '\\':
				escape = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func parseStructuredCandidate(candidate, schema []byte) (json.RawMessage, error) {
	var output json.RawMessage
	if err := json.Unmarshal(candidate, &output); err != nil {
		return nil, err
	}
	if err := validateStructuredOutput(output, schema); err != nil {
		return nil, err
	}
	return output, nil
}

func validateStructuredOutput(output, schema json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}

	var parsedSchema any
	if err := json.Unmarshal(schema, &parsedSchema); err != nil {
		return err
	}

	value, err := decodeJSONValue(output)
	if err != nil {
		return err
	}

	if err := validateJSONValue(value, parsedSchema, "", ""); err != nil {
		return fmt.Errorf("JSON output %w", err)
	}
	return nil
}

func allowOptionalSchemaNulls(value any) {
	schema, ok := value.(map[string]any)
	if !ok {
		return
	}

	required := requiredSet(schema)
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, property := range properties {
			allowOptionalSchemaNulls(property)
			if !required[name] {
				allowSchemaNull(property)
			}
		}
	}
	if items, ok := schema["items"]; ok {
		allowOptionalSchemaNulls(items)
	}
}

func decodeJSONValue(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return value, nil
}

type SchemaViolation struct {
	Field   string
	Message string
}

func (e *SchemaViolation) Error() string { return e.Message }

func schemaViolation(field, format string, args ...any) error {
	return &SchemaViolation{Field: field, Message: fmt.Sprintf(format, args...)}
}

func validateJSONValue(value, schema any, path, field string) error {
	schemaMap, ok := schema.(map[string]any)
	if !ok {
		return nil
	}

	if enum, ok := schemaMap["enum"].([]any); ok && !matchesEnum(value, enum) {
		return schemaViolation(field, "%smust match one of the allowed values", formatJSONPath(path))
	}

	if types, ok := schemaTypes(schemaMap); ok && !matchesAnyType(value, types) {
		return schemaViolation(field, "%smust be %s", formatJSONPath(path), strings.Join(types, " or "))
	}

	if object, ok := value.(map[string]any); ok {
		if err := validateJSONObject(object, schemaMap, path, field); err != nil {
			return err
		}
	}
	if array, ok := value.([]any); ok {
		if err := validateJSONArray(array, schemaMap, path, field); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONObject(object map[string]any, schema map[string]any, path, field string) error {
	required := stringSlice(schema["required"])
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return schemaViolation(joinJSONPath(field, key), "%smissing required field %q", formatJSONPath(path), key)
		}
	}

	// Keys are checked in sorted order so the same output always reports the
	// same violation; map order would let an unchanged retry name a different
	// field and read as a distinct failure.
	properties, _ := schema["properties"].(map[string]any)
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		for _, key := range slices.Sorted(maps.Keys(object)) {
			if _, ok := properties[key]; !ok {
				return schemaViolation(joinJSONPath(field, key), "%scontains unknown field %q", formatJSONPath(path), key)
			}
		}
	}

	for _, key := range slices.Sorted(maps.Keys(properties)) {
		child, ok := object[key]
		if !ok {
			continue
		}
		if err := validateJSONValue(child, properties[key], joinJSONPath(path, key), joinJSONPath(field, key)); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONArray(array []any, schema map[string]any, path, field string) error {
	itemsSchema, ok := schema["items"]
	if !ok {
		return nil
	}
	for i, item := range array {
		if err := validateJSONValue(item, itemsSchema, fmt.Sprintf("%s[%d]", path, i), field); err != nil {
			return err
		}
	}
	return nil
}

func schemaTypes(schema map[string]any) ([]string, bool) {
	switch raw := schema["type"].(type) {
	case string:
		return []string{raw}, true
	case []any:
		types := stringSlice(raw)
		return types, len(types) > 0
	default:
		return nil, false
	}
}

func stringSlice(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		if single, ok := raw.([]string); ok {
			return single
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		str, ok := item.(string)
		if !ok {
			continue
		}
		out = append(out, str)
	}
	return out
}

func matchesAnyType(value any, types []string) bool {
	for _, typ := range types {
		if matchesType(value, typ) {
			return true
		}
	}
	return false
}

func matchesType(value any, typ string) bool {
	switch typ {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		return ok && isJSONInteger(number)
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func isJSONInteger(number json.Number) bool {
	_, err := number.Int64()
	return err == nil
}

func matchesEnum(value any, allowed []any) bool {
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return false
	}
	for _, candidate := range allowed {
		candidateJSON, err := json.Marshal(candidate)
		if err != nil {
			continue
		}
		if bytes.Equal(valueJSON, candidateJSON) {
			return true
		}
	}
	return false
}

func joinJSONPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func formatJSONPath(path string) string {
	if path == "" {
		return ""
	}
	return path + " "
}

func (u TokenUsage) Total() int {
	return u.InputTokens + u.OutputTokens
}

func (u *TokenUsage) Add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.ReasoningReported = u.ReasoningReported || other.ReasoningReported
	u.Reported = u.Reported || other.Reported
	u.CacheCreationReported = u.CacheCreationReported || other.CacheCreationReported
}

func New(name types.AgentName, bin string, extraArgs []string) (Agent, error) {
	return NewWithOptions(name, bin, extraArgs, Options{})
}

func NewWithOptions(name types.AgentName, bin string, extraArgs []string, opts Options) (Agent, error) {
	// Fail closed on a knob this harness cannot express. Config load performs
	// the same check, but this is the funnel every caller reaches - including
	// eval replay and programmatic callers that build a profile directly - so a
	// run can never report a model or effort it did not actually use.
	if err := agentcfg.Validate(name, opts.Profile); err != nil {
		return nil, err
	}
	if target, ok := types.ACPTargetFor(name); ok {
		rawCommand := types.ACPRawCommand(target, opts.ACPRegistryOverrides)
		return &acpxAgent{bin: bin, target: target, rawCommand: rawCommand, model: opts.Profile.Model, disableProjectSettings: opts.DisableProjectSettings, subprocessContext: newSubprocessContext(opts.Environment)}, nil
	}
	// Mapped flags follow the operator's raw agent_args_override flags, so they
	// still precede no-mistakes' managed flags in every adapter's argv. A knob
	// the raw args already pin natively is not emitted at all (see agentcfg),
	// which is what keeps pre-existing configurations byte-identical.
	if mapped := agentcfg.NativeArgs(name, opts.Profile, extraArgs); len(mapped) > 0 {
		merged := make([]string, 0, len(extraArgs)+len(mapped))
		merged = append(merged, extraArgs...)
		merged = append(merged, mapped...)
		extraArgs = merged
	}
	switch name {
	case types.AgentClaude:
		return &claudeAgent{bin: bin, extraArgs: extraArgs, disableProjectSettings: opts.DisableProjectSettings, subprocessContext: newSubprocessContext(opts.Environment)}, nil
	case types.AgentCodex:
		return &codexAgent{bin: bin, extraArgs: extraArgs, disableProjectSettings: opts.DisableProjectSettings, subprocessContext: newSubprocessContext(opts.Environment)}, nil
	case types.AgentGrok:
		return &grokAgent{bin: bin, extraArgs: extraArgs, disableProjectSettings: opts.DisableProjectSettings, subprocessContext: newSubprocessContext(opts.Environment)}, nil
	case types.AgentRovoDev:
		return &rovodevAgent{bin: bin, extraArgs: extraArgs, subprocessContext: newSubprocessContext(opts.Environment)}, nil
	case types.AgentOpenCode:
		return &opencodeAgent{bin: bin, extraArgs: extraArgs, profile: opts.Profile, subprocessContext: newSubprocessContext(opts.Environment)}, nil
	case types.AgentPi:
		return &piAgent{
			bin:                    bin,
			extraArgs:              extraArgs,
			disableProjectSettings: opts.DisableProjectSettings,
			subprocessContext:      newSubprocessContext(opts.Environment),
		}, nil
	case types.AgentCopilot:
		return &copilotAgent{bin: bin, extraArgs: extraArgs, subprocessContext: newSubprocessContext(opts.Environment)}, nil
	case types.AgentAntigravity:
		return &antigravityAgent{bin: bin, extraArgs: extraArgs, subprocessContext: newSubprocessContext(opts.Environment)}, nil
	default:
		return nil, fmt.Errorf("unknown agent %q; valid options: auto, claude, codex, grok, rovodev, opencode, pi, copilot, cursor, devin, antigravity, acp:<target> (set 'agent' in ~/.no-mistakes/config.yaml)", name)
	}
}

func NewNoop() Agent { return &noopAgent{} }

type noopAgent struct{}

func (n *noopAgent) Name() string                                      { return "noop" }
func (n *noopAgent) Run(_ context.Context, _ RunOpts) (*Result, error) { return &Result{}, nil }
func (n *noopAgent) Close() error                                      { return nil }
