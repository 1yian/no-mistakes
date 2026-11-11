package types

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	ActionNoOp    = "no-op"
	ActionAutoFix = "auto-fix"
	ActionAskUser = "ask-user"
)

const (
	FindingSeverityError   = "error"
	FindingSeverityWarning = "warning"
	FindingSeverityInfo    = "info"
)

var (
	knownFindingSeverities = []string{FindingSeverityError, FindingSeverityWarning, FindingSeverityInfo}
	knownFindingActions    = []string{ActionAutoFix, ActionAskUser, ActionNoOp}
)

func NormalizeFindingSeverity(severity string) string {
	return strings.ToLower(strings.TrimSpace(severity))
}

func NormalizeFindingAction(action string) string {
	return strings.ToLower(strings.TrimSpace(action))
}

func IsKnownFindingSeverity(severity string) bool {
	return slices.Contains(knownFindingSeverities, NormalizeFindingSeverity(severity))
}

func IsKnownFindingAction(action string) bool {
	return slices.Contains(knownFindingActions, NormalizeFindingAction(action))
}

func KnownFindingSeverities() []string { return slices.Clone(knownFindingSeverities) }

func KnownFindingActions() []string { return slices.Clone(knownFindingActions) }

const (
	FindingSourceAgent = "agent"
	FindingSourceUser  = "user"
)

const (
	FindingReviewScopeSource                = "source"
	FindingReviewScopePipelineOwnedDelivery = "pipeline-owned-delivery"
	FindingReviewScopeExternalDelivery      = "external-delivery"
)

const (
	FindingsRiskScopeSourceOrExternal      = "source-or-external"
	FindingsRiskScopePipelineOwnedDelivery = "pipeline-owned-delivery"
)

const (
	FindingCategoryDocumentation = "documentation"
	FindingCategoryLint          = "lint"
)

const (
	FindingCategoryCICheck         = "ci-check"
	FindingCategoryCIMergeConflict = "ci-merge-conflict"
	FindingCategoryCIBehindBase    = "ci-behind-base"
	FindingCategoryCITransient     = "ci-transient"
	FindingCategoryCIReviewBot     = "ci-review-bot"
)

const FindingCategoryReviewQuestion = "review-question"

const FindingCategoryTestCommand = "test-command"

const FindingCategoryTestVerdict = "test-verdict"

const FindingIDTestAgentTimeout = "test-agent-timeout"

const FindingIDTestAgentUnvalidatedWork = "test-agent-unvalidated-work"

const FindingIDReviewFixRoundsExhausted = "review-fix-rounds-exhausted"

const (
	ScenarioResultPass     = "pass"
	ScenarioResultFail     = "fail"
	ScenarioResultUntested = "untested"
)

const (
	TestVerdictGo           = "go"
	TestVerdictNoGo         = "no-go"
	TestVerdictInconclusive = "inconclusive"
	TestVerdictNoSurface    = "no-surface"
)

var (
	knownScenarioResults = []string{ScenarioResultPass, ScenarioResultFail, ScenarioResultUntested}
	knownTestVerdicts    = []string{TestVerdictGo, TestVerdictNoGo, TestVerdictInconclusive, TestVerdictNoSurface}
)

func IsKnownScenarioResult(result string) bool {
	return slices.Contains(knownScenarioResults, result)
}

func IsKnownTestVerdict(verdict string) bool {
	return slices.Contains(knownTestVerdicts, verdict)
}

func KnownScenarioResults() []string { return slices.Clone(knownScenarioResults) }

func KnownTestVerdicts() []string { return slices.Clone(knownTestVerdicts) }

type Finding struct {
	ID                 string `json:"id,omitempty"`
	Severity           string `json:"severity"`
	File               string `json:"file,omitempty"`
	Line               int    `json:"line,omitempty"`
	Description        string `json:"description"`
	Action             string `json:"action"`
	Source             string `json:"source,omitempty"`
	UserInstructions   string `json:"user_instructions,omitempty"`
	DriverInstructions string `json:"driver_instructions,omitempty"`
	ReviewScope        string `json:"review_scope,omitempty"`
	Category           string `json:"category,omitempty"`
	Check              string `json:"check,omitempty"`
	CheckID            string `json:"check_id,omitempty"`
}

type TestScenario struct {
	Name     string `json:"name"`
	Result   string `json:"result"`
	Live     bool   `json:"live"`
	Evidence string `json:"evidence"`
	Reason   string `json:"reason"`
}

func LiveScenarioCounts(scenarios []TestScenario) (live, total int) {
	for _, s := range scenarios {
		total++
		if s.Live {
			live++
		}
	}
	return live, total
}

func NoLiveExercisableScenarios(scenarios []TestScenario) bool {
	if len(scenarios) == 0 {
		return false
	}
	for _, s := range scenarios {
		if s.Live || s.Result != ScenarioResultUntested {
			return false
		}
	}
	return true
}

type TestArtifact struct {
	Kind    string `json:"kind,omitempty"`
	Label   string `json:"label"`
	Path    string `json:"path,omitempty"`
	URL     string `json:"url,omitempty"`
	Content string `json:"content,omitempty"`
}

type findingWire struct {
	ID                  string `json:"id,omitempty"`
	Severity            string `json:"severity"`
	File                string `json:"file,omitempty"`
	Line                int    `json:"line,omitempty"`
	Description         string `json:"description"`
	Action              string `json:"action"`
	Source              string `json:"source,omitempty"`
	UserInstructions    string `json:"user_instructions,omitempty"`
	DriverInstructions  string `json:"driver_instructions,omitempty"`
	ReviewScope         string `json:"review_scope,omitempty"`
	Category            string `json:"category,omitempty"`
	Check               string `json:"check,omitempty"`
	CheckID             string `json:"check_id,omitempty"`
	RequiresHumanReview *bool  `json:"requires_human_review,omitempty"`
}

type WithdrawnFinding struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

type ReviewCoverageScope struct {
	Version          int      `json:"version"`
	RunID            string   `json:"run_id"`
	StepResultID     string   `json:"step_result_id"`
	HeadSHA          string   `json:"head_sha"`
	RunBaseSHA       string   `json:"run_base_sha"`
	BaseSHA          string   `json:"base_sha"`
	TrustedConfigSHA string   `json:"trusted_config_sha"`
	Paths            []string `json:"paths"`
	SourceRoundID    string   `json:"source_round_id,omitempty"`
}

type Findings struct {
	Items               []Finding            `json:"findings"`
	Summary             string               `json:"summary"`
	ReviewedPaths       []string             `json:"reviewed_paths,omitempty"`
	ReviewCoverageScope *ReviewCoverageScope `json:"review_coverage_scope,omitempty"`
	WithdrawnFindings   []WithdrawnFinding   `json:"withdrawn_findings,omitempty"`
	Tested              []string             `json:"tested,omitempty"`
	TestingSummary      string               `json:"testing_summary,omitempty"`
	Artifacts           []TestArtifact       `json:"artifacts,omitempty"`
	Scenarios           []TestScenario       `json:"scenarios,omitempty"`
	Verdict             string               `json:"verdict,omitempty"`
	TestedHeadSHA       string               `json:"tested_head_sha,omitempty"`
	UnvalidatedSinceSHA string               `json:"unvalidated_since_sha,omitempty"`
	RiskLevel           string               `json:"risk_level"`
	RiskRationale       string               `json:"risk_rationale"`
	RiskScope           string               `json:"risk_scope,omitempty"`
}

type findingsWire struct {
	Items               []Finding            `json:"findings"`
	Legacy              []Finding            `json:"items"`
	Summary             string               `json:"summary"`
	ReviewedPaths       []string             `json:"reviewed_paths"`
	ReviewCoverageScope *ReviewCoverageScope `json:"review_coverage_scope"`
	WithdrawnFindings   []WithdrawnFinding   `json:"withdrawn_findings"`
	Tested              []string             `json:"tested"`
	TestingSummary      string               `json:"testing_summary"`
	Artifacts           []TestArtifact       `json:"artifacts"`
	Scenarios           []TestScenario       `json:"scenarios"`
	Verdict             string               `json:"verdict"`
	TestedHeadSHA       string               `json:"tested_head_sha"`
	UnvalidatedSinceSHA string               `json:"unvalidated_since_sha"`
	RiskLevel           string               `json:"risk_level"`
	RiskRationale       string               `json:"risk_rationale"`
	RiskScope           string               `json:"risk_scope"`
}

func ParseFindingsJSON(raw string) (Findings, error) {
	var wire findingsWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Findings{}, err
	}
	items := wire.Items
	if len(items) == 0 && len(wire.Legacy) > 0 {
		items = wire.Legacy
	}
	return Findings{
		Items:               items,
		Summary:             wire.Summary,
		ReviewedPaths:       wire.ReviewedPaths,
		ReviewCoverageScope: wire.ReviewCoverageScope,
		WithdrawnFindings:   wire.WithdrawnFindings,
		Tested:              wire.Tested,
		TestingSummary:      wire.TestingSummary,
		Artifacts:           wire.Artifacts,
		Scenarios:           wire.Scenarios,
		Verdict:             wire.Verdict,
		TestedHeadSHA:       wire.TestedHeadSHA,
		UnvalidatedSinceSHA: wire.UnvalidatedSinceSHA,
		RiskLevel:           wire.RiskLevel,
		RiskRationale:       wire.RiskRationale,
		RiskScope:           wire.RiskScope,
	}, nil
}

func FindingsMetadata(findings Findings) Findings {
	findings.Items = nil
	return findings
}

func NormalizeFindings(findings Findings, prefix string) Findings {
	used := make(map[string]bool, len(findings.Items))
	for i := range findings.Items {
		findings.Items[i].ID = strings.TrimSpace(findings.Items[i].ID)
		if used[findings.Items[i].ID] {
			findings.Items[i].ID = ""
			continue
		}
		used[findings.Items[i].ID] = true
	}
	for i := range findings.Items {
		if findings.Items[i].ID != "" {
			continue
		}
		id := prefix + "-" + itoa(i+1)
		for next := i + 2; used[id]; next++ {
			id = prefix + "-" + itoa(next)
		}
		findings.Items[i].ID = id
		used[id] = true
	}
	return findings
}

func FilterFindings(findings Findings, ids []string) Findings {
	if len(ids) == 0 {
		return findings
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	filtered := FindingsMetadata(findings)
	for _, item := range findings.Items {
		if selected[item.ID] {
			filtered.Items = append(filtered.Items, item)
		}
	}
	if len(filtered.Items) != len(findings.Items) {
		filtered.Summary = summarizeSelectedFindings(len(filtered.Items))
	}
	return filtered
}

func ExcludeFindings(findings Findings, ids []string) Findings {
	if len(ids) == 0 {
		return findings
	}
	excluded := make(map[string]bool, len(ids))
	for _, id := range ids {
		excluded[id] = true
	}
	result := FindingsMetadata(findings)
	for _, item := range findings.Items {
		if !excluded[item.ID] {
			result.Items = append(result.Items, item)
		}
	}
	return result
}

func AutoFixableFindings(findings Findings) Findings {
	result := FindingsMetadata(findings)
	for _, item := range findings.Items {
		if item.ActionOrDefault() == ActionAutoFix {
			result.Items = append(result.Items, item)
		}
	}
	return result
}

func MergeUserOverrides(findings Findings, instructions map[string]string, added []Finding) Findings {
	result := FindingsMetadata(findings)
	if len(findings.Items) > 0 {
		result.Items = make([]Finding, len(findings.Items))
		copy(result.Items, findings.Items)
	}
	for i := range result.Items {
		if note, ok := instructions[result.Items[i].ID]; ok {
			result.Items[i].UserInstructions = note
		}
	}
	used := make(map[string]bool, len(result.Items)+len(added))
	for _, item := range result.Items {
		if item.ID != "" {
			used[item.ID] = true
		}
	}
	counter := 0
	appended := false
	for _, item := range added {
		item.Source = FindingSourceUser
		if item.Action == "" {
			item.Action = ActionAutoFix
		}
		// The added ID is trimmed for the same reason the producer's is: the
		// identity a later round attributes by must not carry whitespace, and a
		// padded collision must still be seen as a collision.
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" || used[item.ID] {
			item.ID, counter = nextUserFindingID(used, counter)
		} else {
			used[item.ID] = true
		}
		result.Items = append(result.Items, item)
		appended = true
	}
	if appended && isSelectedFindingsSummary(result.Summary) {
		result.Summary = summarizeSelectedFindings(len(result.Items))
	}
	return result
}

func HasAskUserFindings(findings Findings) bool {
	for _, item := range findings.Items {
		if item.ActionOrDefault() == ActionAskUser {
			return true
		}
	}
	return false
}

func HasActionableFindings(findings Findings) bool {
	for _, item := range findings.Items {
		if item.ActionOrDefault() != ActionNoOp {
			return true
		}
	}
	return false
}

func HasReviewQuestion(findings Findings) bool {
	for _, item := range findings.Items {
		if item.Category == FindingCategoryReviewQuestion {
			return true
		}
	}
	return false
}

func summarizeSelectedFindings(count int) string {
	switch count {
	case 0:
		return "0 selected findings"
	case 1:
		return "1 selected finding"
	default:
		return fmt.Sprintf("%d selected findings", count)
	}
}

func nextUserFindingID(used map[string]bool, counter int) (string, int) {
	for {
		counter++
		candidate := "user-" + itoa(counter)
		if used[candidate] {
			continue
		}
		used[candidate] = true
		return candidate, counter
	}
}

func isSelectedFindingsSummary(summary string) bool {
	if summary == "0 selected findings" || summary == "1 selected finding" {
		return true
	}
	if !strings.HasSuffix(summary, " selected findings") {
		return false
	}
	count := strings.TrimSuffix(summary, " selected findings")
	if count == "" {
		return false
	}
	for _, r := range count {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func MarshalFindingsJSON(findings Findings) (string, error) {
	raw, err := json.Marshal(findings)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func (f *Finding) UnmarshalJSON(data []byte) error {
	var wire findingWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	f.ID = wire.ID
	f.Severity = wire.Severity
	f.File = wire.File
	f.Line = wire.Line
	f.Description = wire.Description
	f.Action = wire.Action
	f.Source = wire.Source
	f.UserInstructions = wire.UserInstructions
	f.DriverInstructions = wire.DriverInstructions
	f.ReviewScope = wire.ReviewScope
	f.Category = wire.Category
	f.Check = wire.Check
	f.CheckID = wire.CheckID
	if f.Action == "" && wire.RequiresHumanReview != nil {
		if *wire.RequiresHumanReview {
			f.Action = ActionAskUser
		} else {
			f.Action = ActionAutoFix
		}
	}
	return nil
}

func (f Finding) ActionOrDefault() string {
	if f.Action == "" {
		return ActionAskUser
	}
	return f.Action
}
