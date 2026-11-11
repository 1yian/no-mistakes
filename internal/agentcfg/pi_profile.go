package agentcfg

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
)

type PiProfile struct {
	Model  string `json:"model"`
	Effort Effort `json:"effort"`
}

func (p *PiProfile) ValidateRequest() error {
	if p == nil {
		return nil
	}
	if p.Model == "" && p.Effort == "" {
		return fmt.Errorf("Pi profile requires model or effort")
	}
	if p.Model != "" {
		parts := strings.Split(p.Model, "/")
		if len(parts) < 2 || len(p.Model) > 256 {
			return fmt.Errorf("Pi model must be an exact provider/model ID")
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || part[0] == '-' {
				return fmt.Errorf("Pi model must be an exact provider/model ID")
			}
			for _, c := range part {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
					return fmt.Errorf("Pi model must be an exact provider/model ID (no URL, pattern, or thinking suffix)")
				}
			}
		}
	}
	if p.Effort != "" {
		effort, err := ParseEffort(string(p.Effort))
		if err != nil || effort != p.Effort {
			return fmt.Errorf("invalid Pi effort (valid: %s)", strings.Join(EffortNames(), ", "))
		}
	}
	return nil
}

func (p *PiProfile) Validate() error {
	if p == nil {
		return nil
	}
	if err := p.ValidateRequest(); err != nil {
		return err
	}
	if p.Model == "" || p.Effort == "" {
		return fmt.Errorf("Pi run pin requires both model and effort; supply flags or agent_config.pi defaults")
	}
	return nil
}

func (p *PiProfile) Matches(request *PiProfile) bool {
	return request == nil || p != nil && (request.Model == "" || request.Model == p.Model) && (request.Effort == "" || request.Effort == p.Effort)
}

func (p PiProfile) Value() (driver.Value, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(p)
	return string(b), err
}

func (p *PiProfile) Scan(src any) error {
	var b []byte
	switch v := src.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return fmt.Errorf("invalid persisted Pi profile")
	}
	if err := json.Unmarshal(b, p); err != nil {
		return fmt.Errorf("invalid persisted Pi profile")
	}
	return p.Validate()
}

func OptionalPiProfile(profiles []RunSelection) *PiProfile {
	for _, profile := range profiles {
		if p, ok := profile.(*PiProfile); ok && p != nil {
			return p
		}
	}
	return nil
}

func (p *PiProfile) PinnedBaseArgs(raw []string) []string {
	args, _ := PiSelectionArgs(raw)
	provider, _, _ := strings.Cut(p.Model, "/")
	return append(args, "--provider", provider)
}

func PiSelectionArgs(args []string) (remaining []string, found bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag, _, inline := strings.Cut(arg, "=")
		switch flag {
		case "--model", "--provider", "--thinking", "--models":
			found = true
			if !inline && i+1 < len(args) {
				i++
			}
		default:
			remaining = append(remaining, arg)
		}
	}
	return remaining, found
}
