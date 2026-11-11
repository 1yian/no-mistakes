package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
)

type RunStatus string

const (
	RunPending              RunStatus = "pending"
	RunRunning              RunStatus = "running"
	RunCompleted            RunStatus = "completed"
	RunFailed               RunStatus = "failed"
	RunCancelled            RunStatus = "cancelled"
	RunChecksPassed         RunStatus = "checks_passed"
	RunHandedOff            RunStatus = "handed_off"
	RunCIMonitorInterrupted RunStatus = "ci_monitor_interrupted"
)

const (
	StepErrorAbortedByUser = "aborted by user"

	RunCancelReasonAbortedByUser  = "cancelled: aborted by user"
	RunCancelReasonSuperseded     = "cancelled: superseded by new push"
	RunCIMonitorInterruptedReason = "ci monitor interrupted by daemon restart; PR remains open"
)

func UserAbortStepError(reason string) string {
	if reason = strings.TrimSpace(reason); reason != "" {
		return StepErrorAbortedByUser + ": " + reason
	}
	return StepErrorAbortedByUser
}

func IsUserAbortStepError(stepError string) bool {
	return stepError == StepErrorAbortedByUser || strings.HasPrefix(stepError, StepErrorAbortedByUser+": ")
}

func (s RunStatus) Terminal() bool {
	switch s {
	case RunCompleted, RunFailed, RunCancelled, RunChecksPassed, RunCIMonitorInterrupted, RunHandedOff:
		return true
	default:
		return false
	}
}

type StepName string

const (
	StepIntent   StepName = "intent"
	StepRebase   StepName = "rebase"
	StepReview   StepName = "review"
	StepTest     StepName = "test"
	StepDocument StepName = "document"
	StepLint     StepName = "lint"
	StepPush     StepName = "push"
	StepPR       StepName = "pr"
	StepCI       StepName = "ci"
)

func normalizeStepName(s StepName) StepName {
	if s == "babysit" {
		return StepCI
	}
	return s
}

func (s *StepName) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = normalizeStepName(StepName(raw))
	return nil
}

func (s *StepName) Scan(src any) error {
	switch v := src.(type) {
	case string:
		*s = normalizeStepName(StepName(v))
		return nil
	case []byte:
		*s = normalizeStepName(StepName(v))
		return nil
	case nil:
		*s = ""
		return nil
	default:
		return fmt.Errorf("scan StepName from %T", src)
	}
}

func (s StepName) Value() (driver.Value, error) {
	return string(s), nil
}

func (s StepName) Order() int {
	if anchor, ok := s.CustomGateAnchor(); ok {
		return anchor.Order()
	}
	switch s {
	case StepIntent:
		return 1
	case StepRebase:
		return 2
	case StepReview:
		return 3
	case StepTest:
		return 4
	case StepDocument:
		return 5
	case StepLint:
		return 6
	case StepPush:
		return 7
	case StepPR:
		return 8
	case StepCI:
		return 9
	default:
		return 0
	}
}

func AllSteps() []StepName {
	return []StepName{StepIntent, StepRebase, StepReview, StepTest, StepDocument, StepLint, StepPush, StepPR, StepCI}
}

func (s StepName) IsCustomGateAnchor() bool {
	switch s {
	case StepRebase, StepReview, StepTest, StepDocument, StepLint:
		return true
	default:
		return false
	}
}

func CustomGateAnchors() []StepName {
	anchors := make([]StepName, 0, 5)
	for _, step := range AllSteps() {
		if step.IsCustomGateAnchor() {
			anchors = append(anchors, step)
		}
	}
	return anchors
}

const (
	CustomGateStepPrefix    = "gate."
	CustomGateStepSeparator = "."
)

const MaxCustomGateLabelLen = 40

func ValidCustomGateLabel(label string) bool {
	if label == "" || len(label) > MaxCustomGateLabelLen {
		return false
	}
	for i, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(label)-1:
		default:
			return false
		}
	}
	return true
}

func CustomGateStepName(anchor StepName, name string) StepName {
	return StepName(CustomGateStepPrefix + string(anchor) + CustomGateStepSeparator + name)
}

func decodeCustomGate(s StepName) (StepName, string, bool) {
	rest, ok := strings.CutPrefix(string(s), CustomGateStepPrefix)
	if !ok {
		return "", "", false
	}
	anchor, label, ok := strings.Cut(rest, CustomGateStepSeparator)
	if !ok || !ValidCustomGateLabel(label) {
		return "", "", false
	}
	if !StepName(anchor).IsCustomGateAnchor() {
		return "", "", false
	}
	return StepName(anchor), label, true
}

func (s StepName) IsCustomGate() bool {
	_, _, ok := decodeCustomGate(s)
	return ok
}

func (s StepName) CustomGateAnchor() (StepName, bool) {
	anchor, _, ok := decodeCustomGate(s)
	return anchor, ok
}

func (s StepName) CustomGateLabel() string {
	_, label, _ := decodeCustomGate(s)
	return label
}

func IsCoreStepName(s StepName) bool {
	for _, step := range AllSteps() {
		if step == s {
			return true
		}
	}
	return false
}

type StepStatus string

const (
	StepStatusPending          StepStatus = "pending"
	StepStatusRunning          StepStatus = "running"
	StepStatusAwaitingApproval StepStatus = "awaiting_approval"
	StepStatusFixing           StepStatus = "fixing"
	StepStatusFixReview        StepStatus = "fix_review"
	StepStatusCompleted        StepStatus = "completed"
	StepStatusSkipped          StepStatus = "skipped"
	StepStatusFailed           StepStatus = "failed"
)

type ApprovalAction string

const (
	ActionApprove          ApprovalAction = "approve"
	ActionFix              ApprovalAction = "fix"
	ActionSkip             ApprovalAction = "skip"
	ActionAbort            ApprovalAction = "abort"
	ActionCompleteCoverage ApprovalAction = "complete-coverage"
	ActionAnswer           ApprovalAction = "answer"
)

type AgentName string

const (
	AgentAuto        AgentName = "auto"
	AgentClaude      AgentName = "claude"
	AgentCodex       AgentName = "codex"
	AgentGrok        AgentName = "grok"
	AgentRovoDev     AgentName = "rovodev"
	AgentOpenCode    AgentName = "opencode"
	AgentPi          AgentName = "pi"
	AgentCopilot     AgentName = "copilot"
	AgentCursor      AgentName = "cursor"
	AgentDevin       AgentName = "devin"
	AgentAntigravity AgentName = "antigravity"
)

type ACPAlias struct {
	Name           AgentName
	Target         string
	DefaultCommand string
}

var acpAliases = []ACPAlias{
	{Name: AgentCursor, Target: "cursor", DefaultCommand: "cursor-agent acp"},
	{Name: AgentDevin, Target: "devin", DefaultCommand: "devin acp"},
}

func ACPAliasFor(name AgentName) (ACPAlias, bool) {
	for _, alias := range acpAliases {
		if alias.Name == name {
			return alias, true
		}
	}
	return ACPAlias{}, false
}

func ACPAliasForTarget(target string) (ACPAlias, bool) {
	for _, alias := range acpAliases {
		if alias.Target == target {
			return alias, true
		}
	}
	return ACPAlias{}, false
}

func ACPAliases() []ACPAlias {
	out := make([]ACPAlias, len(acpAliases))
	copy(out, acpAliases)
	return out
}

func (a ACPAlias) DefaultCommandBinary() string {
	fields := strings.Fields(a.DefaultCommand)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func ACPTargetFor(name AgentName) (string, bool) {
	if alias, ok := ACPAliasFor(name); ok {
		return alias.Target, true
	}
	value := string(name)
	if !strings.HasPrefix(value, "acp:") {
		return "", false
	}
	target := strings.TrimPrefix(value, "acp:")
	if target == "" || strings.ContainsAny(target, " \t\r\n") {
		return "", false
	}
	return target, true
}

func CanonicalAgentName(name AgentName) AgentName {
	if target, ok := ACPTargetFor(name); ok {
		return AgentName("acp:" + target)
	}
	return name
}

func ACPRawCommand(target string, overrides map[string]string) string {
	if override := strings.TrimSpace(overrides[target]); override != "" {
		return override
	}
	if alias, ok := ACPAliasForTarget(target); ok {
		return alias.DefaultCommand
	}
	return ""
}
