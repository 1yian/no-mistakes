package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type RespondDispositions struct {
	Fixed   []string
	Ignored []string
	Kept    []string
}

type RespondRefusal struct {
	Message            string
	Missing            []string
	DeclinedEarlierFix []string
	Help               string
}

func (r *RespondRefusal) Error() string {
	if r == nil {
		return ""
	}
	return r.Message
}

const fixResponseHelp = "List every finding the gate shows in --findings or --ignore; a finding an earlier round of this step already decided may be omitted to keep that decision."

const fixResponseRevertHelp = "Reverting an applied fix is out of scope for a gate response: leave those findings out of --ignore to keep the earlier decision, or list them in --findings to have the pipeline fix them again."

const fixResponseUnreadableHelp = "The response was refused rather than validated against unreadable state, and the gate is still parked: inspect it with `no-mistakes axi status`, then retry, or approve or skip the step instead."

func decodeRoundDecision(round *db.StepRound) error {
	if round == nil {
		return nil
	}
	if round.SelectedFindingIDs != nil {
		var ids []string
		if err := json.Unmarshal([]byte(*round.SelectedFindingIDs), &ids); err != nil {
			return fmt.Errorf("round %s selection: %w", round.ID, err)
		}
	}
	for _, field := range []struct {
		name string
		raw  *string
	}{{"findings", round.FindingsJSON}, {"user findings", round.UserFindingsJSON}} {
		if field.raw == nil || strings.TrimSpace(*field.raw) == "" {
			continue
		}
		var findings types.Findings
		if err := json.Unmarshal([]byte(*field.raw), &findings); err != nil {
			return fmt.Errorf("round %s %s: %w", round.ID, field.name, err)
		}
	}
	return nil
}

func splitFixResponse(gateFindingsJSON string, rounds []*db.StepRound, findingIDs, ignoreFindingIDs []string) (RespondDispositions, error) {
	gate, gateParsed := parseGateFindingIDs(gateFindingsJSON)
	inGate := make(map[string]bool, len(gate))
	for _, id := range gate {
		inGate[id] = true
	}

	fixed := uniqueNonEmpty(findingIDs)
	ignored := uniqueNonEmpty(ignoreFindingIDs)

	// An unreadable payload is refused: without the gate's findings there is no
	// way to tell whether every finding was accounted for, and accepting the
	// response would record its omissions as declines off state this code
	// could not read. The gate stays parked, so the operator can inspect it or
	// approve/skip the step instead.
	if !gateParsed {
		return RespondDispositions{}, &RespondRefusal{
			Message: "the gate's findings could not be read, so the response was not recorded",
			Help:    fixResponseUnreadableHelp,
		}
	}
	if unknown := idsOutside(fixed, inGate); len(unknown) > 0 {
		return RespondDispositions{}, &RespondRefusal{
			Message: fmt.Sprintf("%s not shown at this gate", strings.Join(unknown, ",")),
			Help:    fixResponseHelp,
		}
	}
	if unknown := idsOutside(ignored, inGate); len(unknown) > 0 {
		return RespondDispositions{}, &RespondRefusal{
			Message: fmt.Sprintf("%s not shown at this gate", strings.Join(unknown, ",")),
			Help:    fixResponseHelp,
		}
	}

	fixSet := make(map[string]bool, len(fixed))
	for _, id := range fixed {
		fixSet[id] = true
	}
	ignoreSet := make(map[string]bool, len(ignored))
	for _, id := range ignored {
		ignoreSet[id] = true
	}
	var both []string
	for _, id := range gate {
		if fixSet[id] && ignoreSet[id] {
			both = append(both, id)
		}
	}
	if len(both) > 0 {
		return RespondDispositions{}, &RespondRefusal{
			Message: fmt.Sprintf("%s listed in both --findings and --ignore", strings.Join(both, ",")),
			Help:    fixResponseHelp,
		}
	}

	chosenToFix := earlierChosenToFixIDs(rounds, gateFindingsJSON)
	var reversals []string
	for _, id := range gate {
		if ignoreSet[id] && chosenToFix[id] {
			reversals = append(reversals, id)
		}
	}
	if len(reversals) > 0 {
		return RespondDispositions{}, &RespondRefusal{
			Message:            fmt.Sprintf("%s already chosen to fix in an earlier round of this step", strings.Join(reversals, ",")),
			DeclinedEarlierFix: reversals,
			Help:               fixResponseRevertHelp,
		}
	}

	decided := earlierUserDecisionIDs(rounds, gateFindingsJSON)
	var unaccounted []string
	for _, id := range gate {
		if fixSet[id] || ignoreSet[id] || decided[id] {
			continue
		}
		unaccounted = append(unaccounted, id)
	}
	if len(unaccounted) > 0 {
		return RespondDispositions{}, &RespondRefusal{
			Message: fmt.Sprintf("%s not addressed: list them in --findings or --ignore", strings.Join(unaccounted, ",")),
			Missing: unaccounted,
			Help:    fixResponseHelp,
		}
	}

	dispositions := RespondDispositions{
		Fixed:   idsInPayloadOrder(fixed, gate),
		Ignored: idsInPayloadOrder(ignored, gate),
	}
	for _, id := range gate {
		if decided[id] && !fixSet[id] && !ignoreSet[id] {
			dispositions.Kept = append(dispositions.Kept, id)
		}
	}
	return dispositions, nil
}

func FindingDecisionKey(item types.Finding) string {
	encoded, _ := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{findingKey(item)}})
	return encoded
}

func ChosenFindingKeys(round *db.StepRound) map[string]bool {
	keys := make(map[string]bool)
	if !humanDecidedRound(round) {
		return keys
	}
	for _, item := range selectedFindingIdentities([]*db.StepRound{round}) {
		keys[FindingDecisionKey(item)] = true
	}
	return keys
}

func earlierUserDecisionIDs(rounds []*db.StepRound, gate string) map[string]bool {
	keys := make(map[string]bool)
	for _, round := range rounds {
		if !humanDecidedRound(round) {
			continue
		}
		if round.FindingsJSON != nil {
			findings, _ := types.ParseFindingsJSON(*round.FindingsJSON)
			for _, item := range findings.Items {
				keys[FindingDecisionKey(item)] = true
			}
		}
		for key := range ChosenFindingKeys(round) {
			keys[key] = true
		}
	}
	return decisionIDsInGate(keys, gate)
}

func earlierChosenToFixIDs(rounds []*db.StepRound, gate string) map[string]bool {
	keys := make(map[string]bool)
	for _, round := range rounds {
		for key := range ChosenFindingKeys(round) {
			keys[key] = true
		}
	}
	return decisionIDsInGate(keys, gate)
}

func decisionIDsInGate(keys map[string]bool, gate string) map[string]bool {
	ids := make(map[string]bool)
	findings, _ := types.ParseFindingsJSON(gate)
	for _, item := range findings.Items {
		if keys[FindingDecisionKey(item)] {
			ids[item.ID] = true
		}
	}
	return ids
}

func humanDecidedRound(round *db.StepRound) bool {
	if round == nil || round.SelectionSource == nil {
		return false
	}
	switch *round.SelectionSource {
	case db.RoundSelectionSourceUser, db.RoundSelectionSourceUserDeclined:
		return true
	default:
		return false
	}
}

func findingIDsInPayloadOrder(raw string) []string {
	ids, _ := parseGateFindingIDs(raw)
	return ids
}

func parseGateFindingIDs(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	parsed, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return nil, false
	}
	ids := make([]string, 0, len(parsed.Items))
	seen := make(map[string]bool, len(parsed.Items))
	for _, item := range parsed.Items {
		id := item.ID
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, true
}

func uniqueNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func idsOutside(ids []string, inGate map[string]bool) []string {
	var out []string
	for _, id := range ids {
		if !inGate[id] {
			out = append(out, id)
		}
	}
	return out
}

func excludeFindingIDs(ids, excluded []string) []string {
	if len(ids) == 0 || len(excluded) == 0 {
		return ids
	}
	drop := make(map[string]bool, len(excluded))
	for _, id := range excluded {
		drop[id] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !drop[id] {
			out = append(out, id)
		}
	}
	return out
}

func idsInPayloadOrder(ids []string, gate []string) []string {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range gate {
		if set[id] {
			out = append(out, id)
		}
	}
	return out
}
