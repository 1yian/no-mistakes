package pipeline

import (
	"encoding/json"
	"path"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func findingIDsJSON(raw string) string {
	if raw == "" {
		return ""
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return ""
	}
	ids := make([]string, 0, len(findings.Items))
	for _, item := range findings.Items {
		if item.ID == "" {
			continue
		}
		ids = append(ids, item.ID)
	}
	return marshalFindingIDs(ids)
}

func findingIDList(raw string) []string {
	if raw == "" {
		return nil
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(findings.Items))
	for _, item := range findings.Items {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func marshalFindingIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func findingKey(item types.Finding) types.Finding {
	item.ID = ""
	item.Action = ""
	item.Source = ""
	item.UserInstructions = ""
	item.DriverInstructions = ""
	return item
}

func SameFinding(a, b types.Finding) bool {
	return findingKey(a) == findingKey(b)
}

func findingFingerprint(item types.Finding) types.Finding {
	item = findingKey(item)
	item.Line = 0
	return item
}

func countFindingFingerprints(items []types.Finding) map[types.Finding]int {
	counts := make(map[types.Finding]int, len(items))
	for _, item := range items {
		counts[findingFingerprint(item)]++
	}
	return counts
}

func hasFindingMatch(item types.Finding, exact map[types.Finding]bool, itemCounts, candidateCounts map[types.Finding]int) bool {
	if exact[findingKey(item)] {
		return true
	}
	fingerprint := findingFingerprint(item)
	return itemCounts[fingerprint] == 1 && candidateCounts[fingerprint] == 1
}

func normalizeFindingsJSON(raw string, prefix string) string {
	if raw == "" {
		return ""
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	normalized := types.NormalizeFindings(findings, prefix)
	normalizedRaw, err := types.MarshalFindingsJSON(normalized)
	if err != nil {
		return raw
	}
	return normalizedRaw
}

func excludeFindingsJSON(raw string, ids []string) string {
	if raw == "" || len(ids) == 0 {
		return raw
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	excluded := types.ExcludeFindings(findings, ids)
	if len(excluded.Items) == 0 {
		return ""
	}
	excludedRaw, err := types.MarshalFindingsJSON(excluded)
	if err != nil {
		return raw
	}
	return excludedRaw
}

func mergeFindingsJSON(existingRaw, additionalRaw string) string {
	if existingRaw == "" {
		return additionalRaw
	}
	if additionalRaw == "" {
		return existingRaw
	}
	existing, err := types.ParseFindingsJSON(existingRaw)
	if err != nil {
		return additionalRaw
	}
	additional, err := types.ParseFindingsJSON(additionalRaw)
	if err != nil {
		return existingRaw
	}
	seen := make(map[types.Finding]bool, len(existing.Items)+len(additional.Items))
	existingCounts := countFindingFingerprints(existing.Items)
	additionalCounts := countFindingFingerprints(additional.Items)
	merged := types.FindingsMetadata(existing)
	if additional.RiskLevel != "" {
		merged.RiskLevel = additional.RiskLevel
		merged.RiskRationale = additional.RiskRationale
		merged.RiskScope = additional.RiskScope
	}
	for _, item := range existing.Items {
		merged.Items = append(merged.Items, item)
		seen[findingKey(item)] = true
	}
additions:
	for _, item := range additional.Items {
		if item.Source == types.FindingSourceUser {
			// A gate selection names the user's ID. Content deduplication must
			// not discard that record (including its action and instructions).
			for i, previous := range merged.Items {
				matches := findingKey(previous) == findingKey(item) ||
					(additionalCounts[findingFingerprint(item)] == 1 && existingCounts[findingFingerprint(item)] == 1 &&
						findingFingerprint(previous) == findingFingerprint(item))
				if !matches {
					continue
				}
				if previous.Source == types.FindingSourceUser && previous.ID == item.ID {
					continue additions
				}
				if previous.Source != types.FindingSourceUser && previous.Action == types.ActionNoOp {
					merged.Items[i] = item
					seen[findingKey(item)] = true
					continue additions
				}
			}
			// Keep independently selected user records and blocking findings
			// alongside each other rather than losing either identity.
			merged.Items = append(merged.Items, item)
			seen[findingKey(item)] = true
			continue
		}
		if hasFindingMatch(item, seen, additionalCounts, existingCounts) {
			continue
		}
		key := findingKey(item)
		if seen[key] {
			continue
		}
		merged.Items = append(merged.Items, item)
		seen[key] = true
	}
	if len(merged.Items) == 0 {
		return ""
	}
	mergedRaw, err := types.MarshalFindingsJSON(merged)
	if err != nil {
		return existingRaw
	}
	return mergedRaw
}

func retainMatchingFindingsJSON(existingRaw, keepRaw string) string {
	if existingRaw == "" || keepRaw == "" {
		return ""
	}
	existing, err := types.ParseFindingsJSON(existingRaw)
	if err != nil {
		return ""
	}
	keep, err := types.ParseFindingsJSON(keepRaw)
	if err != nil {
		return ""
	}
	allowed := make(map[types.Finding]bool, len(keep.Items))
	existingCounts := countFindingFingerprints(existing.Items)
	keepCounts := countFindingFingerprints(keep.Items)
	for _, item := range keep.Items {
		allowed[findingKey(item)] = true
	}
	filtered := types.FindingsMetadata(existing)
	for _, item := range existing.Items {
		if !hasFindingMatch(item, allowed, existingCounts, keepCounts) {
			continue
		}
		filtered.Items = append(filtered.Items, item)
	}
	if len(filtered.Items) == 0 {
		return ""
	}
	filteredRaw, err := types.MarshalFindingsJSON(filtered)
	if err != nil {
		return ""
	}
	return filteredRaw
}

func autoFixableFindingsJSON(raw string) string {
	if raw == "" {
		return ""
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	fixable := types.AutoFixableFindings(findings)
	if len(fixable.Items) == 0 {
		return ""
	}
	fixableRaw, err := types.MarshalFindingsJSON(fixable)
	if err != nil {
		return raw
	}
	return fixableRaw
}

func gateAutoFixEligible(current, previous, deferred string, fixing bool) bool {
	findings, err := types.ParseFindingsJSON(current)
	if err != nil || types.HasAskUserFindings(findings) || len(types.AutoFixableFindings(findings).Items) == 0 {
		return false
	}
	if !fixing {
		return true
	}
	prior, err := types.ParseFindingsJSON(previous)
	if err != nil {
		return false
	}
	priorCount := len(prior.Items)
	if deferred != "" {
		unselected, err := types.ParseFindingsJSON(deferred)
		if err != nil {
			return false
		}
		priorCount += len(unselected.Items)
	}
	return len(findings.Items) < priorCount
}

func hasAskUserFindingsJSON(raw string) bool {
	if raw == "" {
		return false
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return false
	}
	return types.HasAskUserFindings(findings)
}

func hasBlockingFindingsJSON(raw string) bool {
	if raw == "" {
		return false
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return true
	}
	for _, item := range findings.Items {
		if item.Severity == types.FindingSeverityError || item.Severity == types.FindingSeverityWarning {
			return true
		}
	}
	return false
}

func findingIDsFromSelectionJSON(raw string) []string {
	if raw == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}

func combineFindingIDLists(existing, additional []string) []string {
	result := append([]string(nil), existing...)
	seen := make(map[string]bool, len(result))
	for _, id := range result {
		if id != "" {
			seen[id] = true
		}
	}
	for _, id := range additional {
		if id != "" && !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	return result
}

func retainFindingIDs(raw string, ids []string) []string {
	if len(ids) == 0 || raw == "" {
		return nil
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return append([]string(nil), ids...)
	}
	present := make(map[string]bool, len(findings.Items))
	for _, item := range findings.Items {
		if item.ID != "" {
			present[item.ID] = true
		}
	}
	retained := make([]string, 0, len(ids))
	for _, id := range ids {
		if present[id] {
			retained = append(retained, id)
		}
	}
	return retained
}

func selectedFindingIdentities(rounds []*db.StepRound) map[string]types.Finding {
	identity := make(map[string]types.Finding)
	for _, round := range rounds {
		if round.SelectedFindingIDs == nil {
			continue
		}
		ids := findingIDsFromSelectionJSON(*round.SelectedFindingIDs)
		if len(ids) == 0 {
			continue
		}

		byID := make(map[string]types.Finding)
		if round.FindingsJSON != nil {
			if findings, err := types.ParseFindingsJSON(*round.FindingsJSON); err == nil {
				for _, item := range findings.Items {
					if item.ID != "" {
						byID[item.ID] = item
					}
				}
			}
		}
		if round.UserFindingsJSON != nil {
			if findings, err := types.ParseFindingsJSON(*round.UserFindingsJSON); err == nil {
				for _, item := range findings.Items {
					if item.ID != "" {
						if _, exists := byID[item.ID]; !exists {
							byID[item.ID] = item
						}
					}
				}
			}
		}
		for _, id := range ids {
			if item, ok := byID[id]; ok {
				identity[id] = item
			}
		}
	}
	return identity
}

func retainFindingIDsByIdentity(latestRaw string, ids []string, identity map[string]types.Finding) []string {
	if len(ids) == 0 || latestRaw == "" {
		return nil
	}
	latest, err := types.ParseFindingsJSON(latestRaw)
	if err != nil {
		return nil
	}
	byID := make(map[string]types.Finding, len(latest.Items))
	for _, item := range latest.Items {
		if item.ID != "" {
			byID[item.ID] = item
		}
	}
	retained := make([]string, 0, len(ids))
	for _, id := range ids {
		current, ok := byID[id]
		if !ok {
			continue
		}
		want, known := identity[id]
		if !known || findingKey(current) != findingKey(want) {
			continue
		}
		retained = append(retained, id)
	}
	return retained
}

func hasSelectedFindingsJSON(raw string, ids []string) bool {
	if len(ids) == 0 || raw == "" {
		return false
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return true
	}
	present := make(map[string]bool, len(findings.Items))
	for _, item := range findings.Items {
		if item.ID != "" {
			present[item.ID] = true
		}
	}
	for _, id := range ids {
		if present[id] {
			return true
		}
	}
	return false
}

func remapFindingIDsJSON(mergedRaw, selectedRaw string) string {
	if mergedRaw == "" || selectedRaw == "" {
		return selectedRaw
	}
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		return selectedRaw
	}
	selected, err := types.ParseFindingsJSON(selectedRaw)
	if err != nil {
		return selectedRaw
	}
	mergedCounts := countFindingFingerprints(merged.Items)
	selectedCounts := countFindingFingerprints(selected.Items)
	used := make(map[int]bool, len(selected.Items))
	matched := make([]bool, len(selected.Items))
	// Reserve every selected item's exact identity before content matching.
	// Otherwise an earlier blocking item can consume the ID of a later user
	// addition that has the same content, swapping their identities on recovery.
	for i := range selected.Items {
		for j := range merged.Items {
			if !used[j] && selected.Items[i].ID != "" && selected.Items[i].ID == merged.Items[j].ID && SameFinding(selected.Items[i], merged.Items[j]) {
				used[j] = true
				matched[i] = true
				break
			}
		}
	}
	for i := range selected.Items {
		if matched[i] {
			continue
		}
		contentMatched := false
		for j := range merged.Items {
			if used[j] || findingKey(selected.Items[i]) != findingKey(merged.Items[j]) {
				continue
			}
			selected.Items[i].ID = merged.Items[j].ID
			used[j] = true
			contentMatched = true
			break
		}
		if contentMatched {
			continue
		}
		fingerprint := findingFingerprint(selected.Items[i])
		if selectedCounts[fingerprint] != 1 || mergedCounts[fingerprint] != 1 {
			continue
		}
		for j := range merged.Items {
			if !used[j] && findingFingerprint(merged.Items[j]) == fingerprint {
				selected.Items[i].ID = merged.Items[j].ID
				used[j] = true
				break
			}
		}
	}
	remapped, err := types.MarshalFindingsJSON(selected)
	if err != nil {
		return selectedRaw
	}
	return remapped
}

func normalizeCoveredPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func resolveVerifiedFindingsJSON(outstandingRaw string, pendingIDs []string, reviewedPaths, reviewablePaths []string, thisRoundRaw string) string {
	if outstandingRaw == "" || len(pendingIDs) == 0 || len(reviewedPaths) == 0 {
		return outstandingRaw
	}
	outstanding, err := types.ParseFindingsJSON(outstandingRaw)
	if err != nil {
		return outstandingRaw
	}
	pending := make(map[string]bool, len(pendingIDs))
	for _, id := range pendingIDs {
		if id != "" {
			pending[id] = true
		}
	}
	if len(pending) == 0 {
		return outstandingRaw
	}
	reviewable := make(map[string]bool, len(reviewablePaths))
	for _, candidate := range reviewablePaths {
		if normalized := normalizeCoveredPath(candidate); normalized != "" {
			reviewable[normalized] = true
		}
	}
	if len(reviewable) == 0 {
		return outstandingRaw
	}
	covered := make(map[string]bool, len(reviewedPaths))
	for _, reviewed := range reviewedPaths {
		normalized := normalizeCoveredPath(reviewed)
		if normalized == "" || !reviewable[normalized] {
			return outstandingRaw
		}
		covered[normalized] = true
	}
	if len(covered) == 0 {
		return outstandingRaw
	}
	fullyCovered := len(covered) == len(reviewable)
	thisRound, _ := types.ParseFindingsJSON(thisRoundRaw)
	reported := make(map[types.Finding]bool, len(thisRound.Items))
	reportedFiles := make(map[string]bool, len(thisRound.Items))
	hasUnanchoredFinding := false
	for _, item := range thisRound.Items {
		reported[findingKey(item)] = true
		if normalized := normalizeCoveredPath(item.File); normalized != "" {
			reportedFiles[normalized] = true
		} else {
			hasUnanchoredFinding = true
		}
	}
	outstandingCounts := countFindingFingerprints(outstanding.Items)
	thisRoundCounts := countFindingFingerprints(thisRound.Items)
	result := types.FindingsMetadata(outstanding)
	for _, item := range outstanding.Items {
		file := normalizeCoveredPath(item.File)
		if pending[item.ID] && !hasUnanchoredFinding && (covered[file] || file == "" && fullyCovered) && !hasFindingMatch(item, reported, outstandingCounts, thisRoundCounts) && !reportedFiles[file] {
			continue
		}
		result.Items = append(result.Items, item)
	}
	if len(result.Items) == len(outstanding.Items) {
		return outstandingRaw
	}
	if len(result.Items) == 0 {
		return ""
	}
	encoded, err := types.MarshalFindingsJSON(result)
	if err != nil {
		return outstandingRaw
	}
	return encoded
}

func mergeOutstandingFindingsJSON(existingRaw, additionalRaw string, reviewedPaths []string) string {
	if additionalRaw == "" {
		if existingRaw == "" {
			return ""
		}
		findings, err := types.ParseFindingsJSON(existingRaw)
		if err != nil {
			return existingRaw
		}
		findings.ReviewedPaths = append([]string(nil), reviewedPaths...)
		findings.WithdrawnFindings = nil
		encoded, err := types.MarshalFindingsJSON(findings)
		if err != nil {
			return existingRaw
		}
		return encoded
	}
	mergedRaw := mergeFindingsJSON(existingRaw, additionalRaw)
	current, currentErr := types.ParseFindingsJSON(additionalRaw)
	if mergedRaw == "" && currentErr == nil {
		// Empty finding sets still carry the current round's positive coverage
		// and provenance or an explicit refusal. Preserve the current envelope.
		mergedRaw = additionalRaw
	}
	if mergedRaw == "" {
		return ""
	}
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		return mergedRaw
	}
	merged.ReviewedPaths = append([]string(nil), reviewedPaths...)
	if currentErr == nil {
		merged.ReviewCoverageScope = current.ReviewCoverageScope
	}
	merged.WithdrawnFindings = nil
	seen := make(map[string]bool, len(merged.Items))
	for i := range merged.Items {
		id := merged.Items[i].ID
		if id != "" && !seen[id] {
			seen[id] = true
			continue
		}
		merged.Items[i].ID = nextFreeReviewFindingID(seen)
		seen[merged.Items[i].ID] = true
	}
	encoded, err := types.MarshalFindingsJSON(merged)
	if err != nil {
		return mergedRaw
	}
	return encoded
}

func nextFreeReviewFindingID(seen map[string]bool) string {
	for i := 1; ; i++ {
		id := "review-" + strconv.Itoa(i)
		if !seen[id] {
			return id
		}
	}
}

func combineSelectedFindingIDs(selected []string, mergedFindings string) []string {
	if mergedFindings == "" {
		return selected
	}
	merged, err := types.ParseFindingsJSON(mergedFindings)
	if err != nil {
		return selected
	}
	seen := make(map[string]bool, len(selected))
	for _, id := range selected {
		if id != "" {
			seen[id] = true
		}
	}
	result := append([]string(nil), selected...)
	for _, item := range merged.Items {
		if item.ID == "" || seen[item.ID] {
			continue
		}
		result = append(result, item.ID)
		seen[item.ID] = true
	}
	return result
}

func resolveAddedFindingIDs(added []types.Finding, reserved ...string) []types.Finding {
	gate := types.Findings{}
	for _, raw := range reserved {
		findings, _ := types.ParseFindingsJSON(raw)
		gate.Items = append(gate.Items, findings.Items...)
	}
	merged := types.MergeUserOverrides(gate, nil, added)
	return merged.Items[len(gate.Items):]
}

func mergeUserOverridesJSON(raw string, instructions, driverInstructions map[string]string, added []types.Finding) string {
	if len(instructions) == 0 && len(driverInstructions) == 0 && len(added) == 0 {
		return raw
	}
	base, err := types.ParseFindingsJSON(raw)
	if err != nil {
		base = types.Findings{}
	}
	merged := types.MergeUserOverrides(base, instructions, added)
	for i := range merged.Items[:len(base.Items)] {
		if note, ok := driverInstructions[merged.Items[i].ID]; ok {
			merged.Items[i].DriverInstructions = note
		}
	}
	encoded, err := types.MarshalFindingsJSON(merged)
	if err != nil {
		return raw
	}
	return encoded
}

func filterFindingsJSON(raw string, ids []string) string {
	if raw == "" {
		return raw
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	filtered := types.FilterFindings(findings, ids)
	if len(ids) == 0 {
		filtered = types.FindingsMetadata(findings)
		filtered.Summary = "0 selected findings"
	}
	filteredRaw, err := types.MarshalFindingsJSON(filtered)
	if err != nil {
		return raw
	}
	return filteredRaw
}

func dropReviewQuestionFindingsJSON(raw string) string {
	if raw == "" {
		return raw
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	kept := make([]types.Finding, 0, len(findings.Items))
	for _, item := range findings.Items {
		if item.Category == types.FindingCategoryReviewQuestion || item.ID == ReviewQuestionsUnreadableFindingID {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == len(findings.Items) {
		return raw
	}
	if len(kept) == 0 {
		return ""
	}
	findings.Items = kept
	encoded, err := types.MarshalFindingsJSON(findings)
	if err != nil {
		return raw
	}
	return encoded
}

func dropWithdrawnFindingsJSON(raw string, withdrawn []types.WithdrawnFinding) (string, []types.WithdrawnFinding) {
	if raw == "" || len(withdrawn) == 0 {
		return raw, nil
	}
	reasons := make(map[string]string, len(withdrawn))
	for _, w := range withdrawn {
		if w.ID != "" {
			reasons[w.ID] = w.Reason
		}
	}
	if len(reasons) == 0 {
		return raw, nil
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw, nil
	}
	var applied []types.WithdrawnFinding
	kept := make([]types.Finding, 0, len(findings.Items))
	for _, item := range findings.Items {
		if reason, named := reasons[item.ID]; named && item.Source != types.FindingSourceUser {
			applied = append(applied, types.WithdrawnFinding{ID: item.ID, Reason: reason})
			continue
		}
		kept = append(kept, item)
	}
	if len(applied) == 0 {
		return raw, nil
	}
	if len(kept) == 0 {
		return "", applied
	}
	findings.Items = kept
	encoded, err := types.MarshalFindingsJSON(findings)
	if err != nil {
		return raw, nil
	}
	return encoded, applied
}

func recordWithdrawnFindingsJSON(raw string, withdrawn []types.WithdrawnFinding) string {
	if raw == "" || len(withdrawn) == 0 {
		return raw
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	findings.WithdrawnFindings = withdrawn
	encoded, err := types.MarshalFindingsJSON(findings)
	if err != nil {
		return raw
	}
	return encoded
}
