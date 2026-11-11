package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const goldSourceCIFalseNegative = "recorded-ci-false-negative"

func isCIFalseNegativeCategory(category string) bool {
	switch category {
	case types.FindingCategoryCICheck, types.FindingCategoryCIReviewBot:
		return true
	default:
		return false
	}
}

func CIFalseNegativesFromRun(database *db.DB, runID string) ([]FindingGold, error) {
	if database == nil {
		return nil, fmt.Errorf("ci false-negative ingest requires a database")
	}
	runID = strings.TrimSpace(runID)
	run, err := database.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("read source run: %w", err)
	}
	// Both success statuses qualify: a run that ended because the PR merged or
	// closed, and one that reached its checks-passed verdict and released the
	// lane while the PR stayed open. A checks_passed run is precisely the run
	// whose CI readiness is the recorded verdict.
	if run == nil || (run.Status != types.RunCompleted && run.Status != types.RunChecksPassed) || run.CIReadyAt == nil || run.CIReadyNoCI {
		return nil, nil
	}
	steps, err := database.GetStepsByRun(runID)
	if err != nil {
		return nil, fmt.Errorf("read source steps: %w", err)
	}
	var ciStep *db.StepResult
	for _, step := range steps {
		if step.StepName == types.StepCI {
			ciStep = step
			break
		}
	}
	if ciStep == nil || ciStep.Status != types.StepStatusCompleted {
		return nil, nil
	}
	if ciStep.OverrideReason != nil && strings.TrimSpace(*ciStep.OverrideReason) != "" {
		return nil, nil
	}
	rounds, err := database.GetRoundsByStep(ciStep.ID)
	if err != nil {
		return nil, fmt.Errorf("read CI rounds: %w", err)
	}
	var gold []FindingGold
	seen := map[string]bool{}
	for i, round := range rounds {
		if round.FindingsJSON == nil || round.SelectedFindingIDs == nil || !repairLandedAfter(rounds, i) {
			continue
		}
		if round.SelectionSource == nil || (*round.SelectionSource != db.RoundSelectionSourceAutoFix && *round.SelectionSource != db.RoundSelectionSourceUser) {
			continue
		}
		selected := parseSelectedFindingIDs(*round.SelectedFindingIDs)
		if len(selected) == 0 {
			continue
		}
		findings, err := types.ParseFindingsJSON(*round.FindingsJSON)
		if err != nil {
			continue
		}
		for _, finding := range findings.Items {
			if !isCIFalseNegativeCategory(finding.Category) {
				continue
			}
			id := strings.TrimSpace(finding.ID)
			if id == "" || !selected[id] {
				continue
			}
			g := ciFindingGold(finding)
			if seen[g.ID] {
				continue
			}
			seen[g.ID] = true
			gold = append(gold, g)
		}
	}
	return gold, nil
}

func repairLandedAfter(rounds []*db.StepRound, selectedIndex int) bool {
	if selectedIndex+1 >= len(rounds) {
		return false
	}
	repair := rounds[selectedIndex+1]
	return repair.IsFixRound() && repair.RepairPublished
}

func AutoIngestCIFalseNegatives(ctx context.Context, p *paths.Paths, database *db.DB, runID string) (IngestResult, bool, error) {
	if p == nil || database == nil {
		return IngestResult{}, false, fmt.Errorf("eval ci false-negative ingest requires paths and a database")
	}
	misses, err := CIFalseNegativesFromRun(database, runID)
	if err != nil {
		return IngestResult{}, false, err
	}
	if len(misses) == 0 {
		return IngestResult{}, true, nil
	}
	store, err := Open(p.EvalDir())
	if err != nil {
		return IngestResult{}, false, err
	}
	defer store.Close()

	result, err := IngestPostPRMiss(ctx, store, p, database, runID, misses)
	if err != nil {
		// A run whose review did not pass green, or has no capturable review,
		// has nowhere to attach these misses: skip it rather than fault.
		if errors.Is(err, ErrReviewDidNotPassGreen) || errors.Is(err, ErrNoCapturableReview) {
			return IngestResult{}, true, nil
		}
		return IngestResult{}, false, err
	}
	return result, false, nil
}

func ciFindingGold(finding types.Finding) FindingGold {
	severity := types.NormalizeFindingSeverity(finding.Severity)
	if !types.IsKnownFindingSeverity(severity) {
		severity = types.FindingSeverityError
	}
	return FindingGold{
		ID:          ciFalseNegativeID(finding),
		Kind:        GoldFalseNegative,
		Source:      goldSourceCIFalseNegative,
		File:        strings.TrimSpace(finding.File),
		Line:        finding.Line,
		Description: strings.TrimSpace(finding.Description),
		Severity:    severity,
	}
}

func ciFalseNegativeID(finding types.Finding) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		finding.Category,
		strings.TrimSpace(finding.CheckID),
		strings.TrimSpace(finding.Check),
		strings.TrimSpace(finding.File),
		strconv.Itoa(finding.Line),
		strings.TrimSpace(finding.Description),
	}, "\x00")))
	return "ci-fn-" + hex.EncodeToString(h[:6])
}

func parseSelectedFindingIDs(raw string) map[string]bool {
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			selected[id] = true
		}
	}
	return selected
}
