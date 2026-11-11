package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

//go:embed pi_output.ts.tmpl
var piOutputExtension string

const piOutputTool = "no_mistakes_output"

const piLifecycleOutputPath = "structured_output"

var piStrictOutputMinVersion = [3]int{0, 82, 0}

var piStrictUnsupported = fmt.Sprintf("Tool %q requires JSON-schema constrained sampling", piOutputTool)

type piOutputSupport struct {
	mu          sync.Mutex
	probed      bool
	version     string
	unsupported bool
}

func (a *piAgent) runStructured(ctx context.Context, opts RunOpts) (*Result, error) {
	if len(opts.JSONSchema) == 0 {
		return a.runOnce(ctx, opts, false)
	}
	strict, reason := a.structuredOutputPath(ctx, opts)
	if !strict {
		emitLifecycle(opts, LifecycleEvent{Agent: "pi", Phase: piLifecycleOutputPath, Message: "pi structured output: prompt-inlined schema (" + reason + ")"})
		return a.runOnce(ctx, opts, false)
	}
	emitLifecycle(opts, LifecycleEvent{Agent: "pi", Phase: piLifecycleOutputPath, Message: "pi structured output: strict schema-constrained " + piOutputTool + " tool"})
	result, err := a.runOnce(ctx, opts, true)
	if err == nil || !strings.Contains(err.Error(), piStrictUnsupported) {
		return result, err
	}
	a.output.mu.Lock()
	a.output.unsupported = true
	a.output.mu.Unlock()
	emitAgentControl(opts, LifecycleEvent{
		Agent:   "pi",
		Phase:   LifecyclePhaseFallback,
		Message: "pi provider/model cannot take strict JSON-schema tools; retrying with the prompt-inlined schema",
	})
	return a.runOnce(ctx, opts, false)
}

func (a *piAgent) structuredOutputPath(ctx context.Context, opts RunOpts) (bool, string) {
	a.output.mu.Lock()
	defer a.output.mu.Unlock()
	if !a.output.probed {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probeCtx, a.bin, "--version")
		cmd.Dir = opts.CWD
		cmd.Env = a.gitSafeEnv(opts.CWD, opts.Env)
		shellenv.ConfigureShellCommand(cmd)
		out, err := shellenv.OutputShellCommand(cmd)
		if err != nil {
			return false, fmt.Sprintf("pi --version failed: %v", err)
		}
		a.output.probed = true
		if fields := strings.Fields(string(out)); len(fields) > 0 {
			a.output.version = fields[0]
		}
	}
	if flag := piToolRestrictionArg(a.extraArgs); flag != "" {
		return false, "configured " + flag + " keeps the output tool from the model"
	}
	if !piVersionSupportsStrictOutput(a.output.version) {
		return false, fmt.Sprintf("pi version %q predates %d.%d.%d", a.output.version, piStrictOutputMinVersion[0], piStrictOutputMinVersion[1], piStrictOutputMinVersion[2])
	}
	if a.output.unsupported {
		return false, "provider/model cannot take strict JSON-schema tools"
	}
	return true, ""
}

func piToolRestrictionArg(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-tools", "-nt", "--tools", "-t":
			return args[i]
		}
		if piArgTakesValue(args[i]) {
			i++
		}
	}
	return ""
}

func piVersionSupportsStrictOutput(version string) bool {
	version, _, _ = strings.Cut(strings.TrimPrefix(version, "v"), "+")
	core, _, prerelease := strings.Cut(version, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return false
		}
		if n != piStrictOutputMinVersion[i] {
			return n > piStrictOutputMinVersion[i]
		}
	}
	return !prerelease
}

var piStrictToolKeywords = map[string]bool{
	"type":        true,
	"properties":  true,
	"items":       true,
	"required":    true,
	"enum":        true,
	"description": true,
}

func piStrictToolSchema(value any) any {
	schema, ok := value.(map[string]any)
	if !ok {
		return value
	}
	declared := make(map[string]any, len(schema))
	for keyword, child := range schema {
		if !piStrictToolKeywords[keyword] {
			continue
		}
		switch keyword {
		case "properties":
			properties, ok := child.(map[string]any)
			if !ok {
				continue
			}
			kept := make(map[string]any, len(properties))
			for name, property := range properties {
				kept[name] = piStrictToolSchema(property)
			}
			declared[keyword] = kept
		case "items":
			declared[keyword] = piStrictToolSchema(child)
		default:
			declared[keyword] = child
		}
	}
	if declared["type"] == "object" {
		declared["additionalProperties"] = false
		if properties, ok := declared["properties"].(map[string]any); ok {
			names := make([]string, 0, len(properties))
			for name := range properties {
				names = append(names, name)
			}
			sort.Strings(names)
			declared["required"] = names
		}
	}
	return declared
}

func preparePiOutput(schema json.RawMessage) (string, func(), error) {
	if len(schema) == 0 {
		return "", func() {}, nil
	}
	var declared any
	if err := json.Unmarshal(schema, &declared); err != nil {
		return "", nil, fmt.Errorf("pi output schema: %w", err)
	}
	// The strict tool must already satisfy the provider's strict function
	// schema: pi-ai only normalizes it from 0.84.4 on, and older strict-capable
	// releases send these parameters verbatim. Keep concrete property types:
	// legacy Pi's tool validation can crash on nullable arrays, so unlike Codex
	// we require real values for optional fields instead of adding null unions.
	strict := piStrictToolSchema(declared)
	parameters, err := json.Marshal(strict)
	if err != nil {
		return "", nil, fmt.Errorf("pi output schema: %w", err)
	}
	f, err := os.CreateTemp("", "no-mistakes-pi-output-*.ts")
	if err != nil {
		return "", nil, fmt.Errorf("pi output extension: %w", err)
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	source := strings.Replace(piOutputExtension, "__NO_MISTAKES_SCHEMA__", string(parameters), 1)
	_, writeErr := f.WriteString(source)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		cleanup()
		if writeErr != nil {
			return "", nil, fmt.Errorf("pi output extension write: %w", writeErr)
		}
		return "", nil, fmt.Errorf("pi output extension close: %w", closeErr)
	}
	return f.Name(), cleanup, nil
}

func (p *piParser) rememberOutputTool(event map[string]any) {
	if event["toolName"] != piOutputTool || event["isError"] != false {
		return
	}
	result, ok := event["result"].(map[string]any)
	if !ok || result["terminate"] != true {
		return
	}
	details, ok := result["details"].(map[string]any)
	if !ok {
		return
	}
	output, exists := details["output"]
	if !exists {
		return
	}
	id, _ := event["toolCallId"].(string)
	if id == "" {
		return
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return
	}
	if p.outputTools == nil {
		p.outputTools = make(map[string]string)
	}
	p.outputTools[id] = string(encoded)
}

func (p *piParser) finalOutputToolText() string {
	if p.finalAssistant == nil || p.finalAssistant["stopReason"] != "toolUse" {
		return ""
	}
	content, _ := p.finalAssistant["content"].([]any)
	var id string
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok || block["type"] != "toolCall" {
			continue
		}
		// A mixed batch has not finished the review when it submits the result.
		// Accept only the terminating call from the final assistant message,
		// never a result emitted before another assistant turn or tool call.
		if block["name"] != piOutputTool || id != "" {
			return ""
		}
		id, _ = block["id"].(string)
		if id == "" {
			return ""
		}
	}
	return p.outputTools[id]
}
