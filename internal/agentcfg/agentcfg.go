package agentcfg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

type Effort string

const (
	EffortMinimal Effort = "minimal"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

var efforts = []Effort{EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

func Efforts() []Effort {
	out := make([]Effort, len(efforts))
	copy(out, efforts)
	return out
}

func EffortNames() []string {
	out := make([]string, 0, len(efforts))
	for _, e := range efforts {
		out = append(out, string(e))
	}
	return out
}

func ParseEffort(raw string) (Effort, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	for _, e := range efforts {
		if string(e) == value {
			return e, nil
		}
	}
	return "", fmt.Errorf("invalid effort %q (valid: %s)", raw, strings.Join(EffortNames(), ", "))
}

type Profile struct {
	Model  string
	Effort Effort
}

func (p Profile) IsZero() bool { return p.Model == "" && p.Effort == "" }

func (p Profile) String() string {
	parts := make([]string, 0, 2)
	if p.Model != "" {
		parts = append(parts, "model="+p.Model)
	}
	if p.Effort != "" {
		parts = append(parts, "effort="+string(p.Effort))
	}
	return strings.Join(parts, ",")
}

type Knob string

const (
	KnobModel  Knob = "model"
	KnobEffort Knob = "effort"
)

type Mechanism string

const (
	MechanismUnsupported Mechanism = "unsupported"
	MechanismArgs        Mechanism = "args"
	MechanismRequest     Mechanism = "request"
)

type knob struct {
	mechanism Mechanism
	args      func(value string) []string
	pinned    func(rawArgs []string) (string, bool)
	validate  func(value string) error
}

type harness struct {
	model  knob
	effort knob
}

func flagArgs(flag string) func(string) []string {
	return func(value string) []string { return []string{flag, value} }
}

func flagPinned(flags ...string) func([]string) (string, bool) {
	return func(rawArgs []string) (string, bool) {
		for i := len(rawArgs) - 1; i >= 0; i-- {
			arg := rawArgs[i]
			for _, flag := range flags {
				if arg == flag {
					if i+1 < len(rawArgs) {
						return rawArgs[i+1], true
					}
					return "", true
				}
				if value, found := strings.CutPrefix(arg, flag+"="); found {
					return value, true
				}
			}
		}
		return "", false
	}
}

func codexConfigArgs(key string) func(string) []string {
	return func(value string) []string { return []string{"-c", key + `="` + value + `"`} }
}

func codexConfigPinned(key string, flags ...string) func([]string) (string, bool) {
	flagMatch := flagPinned(flags...)
	assignmentValue := func(arg string) (string, bool) {
		assignmentKey, value, ok := strings.Cut(strings.TrimSpace(arg), "=")
		if !ok || strings.TrimSpace(assignmentKey) != key {
			return "", false
		}
		return strings.Trim(strings.TrimSpace(value), "\"'"), true
	}
	return func(rawArgs []string) (string, bool) {
		if value, found := flagMatch(rawArgs); len(flags) > 0 && found {
			return value, true
		}
		for i := len(rawArgs) - 1; i >= 0; i-- {
			arg := rawArgs[i]
			if (arg == "-c" || arg == "--config") && i+1 < len(rawArgs) {
				if value, found := assignmentValue(rawArgs[i+1]); found {
					return value, true
				}
			}
			for _, prefix := range []string{"-c ", "--config ", "-c=", "--config="} {
				if assignment, found := strings.CutPrefix(arg, prefix); found {
					if value, found := assignmentValue(assignment); found {
						return value, true
					}
				}
			}
		}
		return "", false
	}
}

func unsupported() knob { return knob{mechanism: MechanismUnsupported} }

func requireProviderModel(value string) error {
	if _, _, ok := SplitProviderModel(value); !ok {
		return fmt.Errorf("model %q must use opencode's provider/model form (for example openai/gpt-5)", value)
	}
	return nil
}

func SplitProviderModel(model string) (provider, id string, ok bool) {
	provider, id, found := strings.Cut(strings.TrimSpace(model), "/")
	provider = strings.TrimSpace(provider)
	id = strings.TrimSpace(id)
	if !found || provider == "" || id == "" {
		return "", "", false
	}
	return provider, id, true
}

func ServedMatchesRequested(requested, served, servedProvider string) bool {
	modelName := func(model string) string {
		model = strings.TrimSpace(model)
		if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
			model = strings.TrimSpace(model[slash+1:])
		}
		return model
	}

	requestedName := modelName(requested)
	servedName := modelName(served)
	return requestedName != "" && requestedName == servedName
}

var harnesses = map[types.AgentName]harness{
	types.AgentClaude: {
		model:  knob{mechanism: MechanismArgs, args: flagArgs("--model"), pinned: flagPinned("--model")},
		effort: knob{mechanism: MechanismArgs, args: flagArgs("--effort"), pinned: flagPinned("--effort")},
	},
	types.AgentCodex: {
		// codex takes the model as a real flag but reasoning depth as a TOML
		// config override, so the two knobs use different shapes.
		model:  knob{mechanism: MechanismArgs, args: flagArgs("-m"), pinned: codexConfigPinned("model", "-m", "--model")},
		effort: knob{mechanism: MechanismArgs, args: codexConfigArgs("model_reasoning_effort"), pinned: codexConfigPinned("model_reasoning_effort")},
	},
	types.AgentGrok: {
		model:  knob{mechanism: MechanismArgs, args: flagArgs("--model"), pinned: flagPinned("--model", "-m")},
		effort: knob{mechanism: MechanismArgs, args: flagArgs("--reasoning-effort"), pinned: flagPinned("--reasoning-effort", "--effort")},
	},
	types.AgentCopilot: {
		model:  knob{mechanism: MechanismArgs, args: flagArgs("--model"), pinned: flagPinned("--model")},
		effort: knob{mechanism: MechanismArgs, args: flagArgs("--effort"), pinned: flagPinned("--effort", "--reasoning-effort")},
	},
	types.AgentPi: {
		model:  knob{mechanism: MechanismArgs, args: flagArgs("--model"), pinned: flagPinned("--model")},
		effort: knob{mechanism: MechanismArgs, args: flagArgs("--thinking"), pinned: flagPinned("--thinking")},
	},
	types.AgentOpenCode: {
		// `opencode serve` rejects model and variant flags outright, so raw
		// args can never reach either knob and there is nothing to defer to.
		// Both ride the session-message body instead.
		model:  knob{mechanism: MechanismRequest, validate: requireProviderModel},
		effort: knob{mechanism: MechanismRequest},
	},
	// rovodev is driven through `acli rovodev serve` plus a REST session API
	// that takes no model parameter, and antigravity's agy CLI parses flags
	// strictly. Neither has a mechanism no-mistakes can set, so both knobs are
	// refused here rather than emitted and silently ignored.
	types.AgentRovoDev:     {model: unsupported(), effort: unsupported()},
	types.AgentAntigravity: {model: unsupported(), effort: unsupported()},
}

var acpHarness = harness{
	model:  knob{mechanism: MechanismArgs, args: flagArgs("--model")},
	effort: unsupported(),
}

func lookup(name types.AgentName) (harness, bool) {
	if _, ok := types.ACPTargetFor(name); ok {
		return acpHarness, true
	}
	h, ok := harnesses[name]
	return h, ok
}

func (h harness) knob(k Knob) knob {
	if k == KnobEffort {
		return h.effort
	}
	return h.model
}

func Agents() []types.AgentName {
	out := make([]types.AgentName, 0, len(harnesses))
	for name := range harnesses {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func Known(name types.AgentName) bool {
	_, ok := lookup(name)
	return ok
}

func MechanismFor(name types.AgentName, k Knob) Mechanism {
	h, ok := lookup(name)
	if !ok {
		return MechanismUnsupported
	}
	return h.knob(k).mechanism
}

func Validate(name types.AgentName, p Profile) error {
	if p.IsZero() {
		return nil
	}
	h, ok := lookup(name)
	if !ok {
		return fmt.Errorf("unknown agent %q", name)
	}
	if p.Effort != "" {
		if _, err := ParseEffort(string(p.Effort)); err != nil {
			return err
		}
	}
	for _, entry := range []struct {
		name  Knob
		value string
		knob  knob
	}{
		{KnobModel, p.Model, h.model},
		{KnobEffort, string(p.Effort), h.effort},
	} {
		if entry.value == "" {
			continue
		}
		if entry.knob.mechanism == MechanismUnsupported {
			return fmt.Errorf("agent %q cannot express %s; no-mistakes has no verified %s mechanism for it (%s)", name, entry.name, entry.name, escapeHatch(name))
		}
		if entry.knob.validate != nil {
			if err := entry.knob.validate(entry.value); err != nil {
				return fmt.Errorf("agent %q %s: %w", name, entry.name, err)
			}
		}
	}
	return nil
}

func escapeHatch(name types.AgentName) string {
	if target, ok := types.ACPTargetFor(name); ok {
		return "bake the flag into acp_registry_overrides." + target + " if the target's own command accepts one"
	}
	return "use agent_args_override." + string(name) + " if your build of the CLI accepts one"
}

func NativeArgs(name types.AgentName, p Profile, rawArgs []string) []string {
	if p.IsZero() {
		return nil
	}
	h, ok := lookup(name)
	if !ok {
		return nil
	}
	var out []string
	for _, entry := range []struct {
		value string
		knob  knob
	}{
		{p.Model, h.model},
		{string(p.Effort), h.effort},
	} {
		if entry.value == "" || entry.knob.mechanism != MechanismArgs || entry.knob.args == nil {
			continue
		}
		if entry.knob.pinned != nil {
			if _, pinned := entry.knob.pinned(rawArgs); pinned {
				continue
			}
		}
		out = append(out, entry.knob.args(entry.value)...)
	}
	return out
}

func EffectiveModel(name types.AgentName, p Profile, rawArgs []string) string {
	h, ok := lookup(name)
	if ok && h.model.mechanism == MechanismArgs && h.model.pinned != nil {
		if value, pinned := h.model.pinned(rawArgs); pinned {
			return value
		}
	}
	return p.Model
}
