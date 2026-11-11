package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/types"
	"gopkg.in/yaml.v3"
)

const (
	MaxGates       = 16
	MaxGateNameLen = types.MaxCustomGateLabelLen
)

type Gate struct {
	Name    string         `yaml:"name" json:"name"`
	After   types.StepName `yaml:"after" json:"after"`
	Command string         `yaml:"command" json:"command,omitempty"`
}

func (g *Gate) UnmarshalYAML(value *yaml.Node) error {
	var decoded struct {
		Name         string         `yaml:"name"`
		After        types.StepName `yaml:"after"`
		Command      string         `yaml:"command"`
		Instructions yaml.Node      `yaml:"instructions"`
	}
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	if !decoded.Instructions.IsZero() {
		return fmt.Errorf("instructions: agent gates are not supported; use command")
	}
	*g = Gate{Name: decoded.Name, After: decoded.After, Command: decoded.Command}
	return nil
}

func (g *Gate) UnmarshalJSON(data []byte) error {
	type gatePlain Gate
	var decoded struct {
		gatePlain
		Instructions json.RawMessage `json:"instructions"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if len(decoded.Instructions) != 0 && string(decoded.Instructions) != "null" {
		return fmt.Errorf("instructions: agent gates are not supported; use command")
	}
	*g = Gate(decoded.gatePlain)
	return nil
}

func (g Gate) StepName() types.StepName {
	return types.CustomGateStepName(g.After, g.Name)
}

func validGateName(name string) error {
	if name == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(name) > MaxGateNameLen {
		return fmt.Errorf("is %d characters, at most %d are allowed", len(name), MaxGateNameLen)
	}
	// types.ValidCustomGateLabel is the single owner of the syntax, because the
	// same rule is what makes the derived step name safe to use as a filename.
	if !types.ValidCustomGateLabel(name) {
		return fmt.Errorf("must be lowercase letters, digits, and inner hyphens only")
	}
	return nil
}

func validateGates(gates []Gate) error {
	if len(gates) > MaxGates {
		return fmt.Errorf("gates has %d entries, at most %d are allowed", len(gates), MaxGates)
	}
	seen := make(map[string]int, len(gates))
	for i := range gates {
		raw := gates[i].Name
		gates[i].Name = strings.TrimSpace(raw)
		gate, name := gates[i], gates[i].Name
		if err := validGateName(name); err != nil {
			return fmt.Errorf("gates[%d].name %q %w", i, raw, err)
		}
		if types.IsCoreStepName(types.StepName(name)) {
			return fmt.Errorf("gates[%d].name %q is a core step name; an extra gate must not shadow a core step", i, name)
		}
		if first, dup := seen[name]; dup {
			return fmt.Errorf("gates[%d].name %q duplicates gates[%d]; each gate needs its own name", i, name, first)
		}
		seen[name] = i

		if gate.After == "" {
			return fmt.Errorf("gates[%d] (%q).after must name the core step it runs after", i, name)
		}
		if !gate.After.IsCustomGateAnchor() {
			return fmt.Errorf("gates[%d] (%q).after %q is not an anchorable core step; valid: %s", i, name, gate.After, gateAnchorText())
		}

		if strings.TrimSpace(gate.Command) == "" {
			return fmt.Errorf("gates[%d] (%q).command must not be empty", i, name)
		}
	}
	return nil
}

type GateFixLimits map[string]int

func (g *GateFixLimits) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("auto_fix.gates must map gate names to non-negative integers")
	}
	for i := 1; i < len(node.Content); i += 2 {
		if node.Content[i].Tag != "!!int" {
			return fmt.Errorf("auto_fix.gates.%s must be an integer", node.Content[i-1].Value)
		}
	}
	var decoded map[string]int
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*g = decoded
	return nil
}

func validateGateFixLimits(limits GateFixLimits, gates []Gate) error {
	declared := make(map[string]bool, len(gates))
	for _, gate := range gates {
		declared[gate.Name] = true
	}
	for name, limit := range limits {
		if !declared[name] {
			return fmt.Errorf("auto_fix.gates.%s must name a declared gate", name)
		}
		if limit < 0 {
			return fmt.Errorf("auto_fix.gates.%s must be non-negative", name)
		}
	}
	return nil
}

func gateAnchorText() string {
	anchors := types.CustomGateAnchors()
	names := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		names = append(names, string(anchor))
	}
	return strings.Join(names, ", ")
}

func MarshalGates(gates []Gate) (string, error) {
	if len(gates) == 0 {
		return "", nil
	}
	data, err := json.Marshal(gates)
	if err != nil {
		return "", fmt.Errorf("encode gates: %w", err)
	}
	return string(data), nil
}

func ParseGates(payload string) ([]Gate, error) {
	if strings.TrimSpace(payload) == "" {
		return nil, nil
	}
	var gates []Gate
	if err := json.Unmarshal([]byte(payload), &gates); err != nil {
		return nil, fmt.Errorf("decode gates: %w", err)
	}
	if err := validateGates(gates); err != nil {
		return nil, err
	}
	return gates, nil
}

func copyGates(gates []Gate) []Gate {
	if len(gates) == 0 {
		return nil
	}
	out := make([]Gate, len(gates))
	copy(out, gates)
	return out
}
