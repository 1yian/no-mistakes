package steps

import (
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

const MaxRunPRBodyBytes = 49122

func NormalizeRunPRTitle(title string) (string, error) {
	if title == "" {
		return "", nil
	}
	if !utf8.ValidString(title) {
		return "", fmt.Errorf("must be valid UTF-8")
	}
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", fmt.Errorf("must not be empty or whitespace-only")
	}
	if len(trimmed) > config.MaxPRTitleBytes {
		return "", fmt.Errorf("must not exceed %d bytes", config.MaxPRTitleBytes)
	}
	if config.ContainsUnsafeTitleRune(trimmed) {
		return "", fmt.Errorf("must be one line without control, bidi, or invisible format characters")
	}
	return trimmed, nil
}

func ValidateRunPRBody(body string) error {
	if body == "" {
		return nil
	}
	if len(body) > MaxRunPRBodyBytes {
		return fmt.Errorf("exceeds the %d-byte transport limit", MaxRunPRBodyBytes)
	}
	if !utf8.ValidString(body) {
		return fmt.Errorf("must be valid UTF-8")
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("must not be empty or whitespace-only")
	}
	for _, r := range body {
		switch {
		case r == '\t', r == '\n', r == '\r':
		case unicode.IsControl(r):
			return fmt.Errorf("must not contain control characters other than tab, newline, and carriage return")
		case r >= '‪' && r <= '‮', r >= '⁦' && r <= '⁩':
			return fmt.Errorf("must not contain bidi override or isolate characters")
		}
	}
	if hasPRAppendixMarkers(body) || strings.Contains(body, pipelineAttestationCommentPrefix) {
		return fmt.Errorf("must not contain reserved no-mistakes publication markers")
	}
	return nil
}

func suppliedPRTitle(sctx *pipeline.StepContext) string {
	if sctx == nil || sctx.Run == nil || sctx.Run.PRTitle == nil {
		return ""
	}
	return *sctx.Run.PRTitle
}

func suppliedPRBody(sctx *pipeline.StepContext) string {
	if sctx == nil || sctx.Run == nil || sctx.Run.PRBody == nil {
		return ""
	}
	return *sctx.Run.PRBody
}

func logSuppliedPRTextUnused(sctx *pipeline.StepContext, reason string) {
	var parts []string
	if suppliedPRTitle(sctx) != "" {
		parts = append(parts, "title")
	}
	if suppliedPRBody(sctx) != "" {
		parts = append(parts, "description")
	}
	if len(parts) == 0 {
		return
	}
	sctx.Log(fmt.Sprintf("supplied PR %s not applied: %s", strings.Join(parts, " and "), reason))
}

func renderSuppliedPRTitle(sctx *pipeline.StepContext, title string) (string, error) {
	if sctx.Config == nil || sctx.Config.PR.TitleFormat == "" {
		return title, nil
	}
	return renderPRTitle(sctx, title)
}

func (s *PRStep) buildSuppliedPRContent(sctx *pipeline.StepContext, branch, baseBranch, baseSHA, template string, provider scm.Provider, bodyLimit int) (prContent, error) {
	body := suppliedPRBody(sctx)
	if template != "" {
		if heading, missing := missingTemplateHeading(template, body); missing {
			return prContent{}, fmt.Errorf("supplied PR description must keep every pr.template top-level # heading with the same text and order; %q is missing, changed or out of order", heading)
		}
	}
	var title string
	var err error
	if supplied := suppliedPRTitle(sctx); supplied != "" {
		title, err = renderSuppliedPRTitle(sctx, supplied)
		if err != nil {
			return prContent{}, err
		}
		if needsPRIntentBrief(sctx) {
			if err := s.draftPRIntentBrief(sctx, branch, baseBranch, baseSHA); err != nil {
				return prContent{}, err
			}
		}
	} else {
		title, err = s.draftConfiguredPRTitle(sctx, branch, baseBranch, baseSHA)
		if err != nil {
			slog.Warn("agent failed for supplied-description PR title, using fallback", "error", err)
			if needsPRIntentBrief(sctx) && strings.TrimSpace(sctx.PublicIntentBrief) == "" {
				return prContent{}, fmt.Errorf("cannot publish configured public Intent brief without a valid draft")
			}
			title, err = renderPRTitle(sctx, "update pull request")
			if err != nil {
				return prContent{}, err
			}
		}
	}
	appendix, err := s.buildPRAppendix(sctx, provider)
	if err != nil {
		return prContent{}, err
	}
	narrative := neutralizeAttestationMarkers(provider, body)
	appendix = appendIssuesSection(appendix, issuesSection(sctx, narrative))
	return composeOwnedPRContent(prOwnedBody{before: narrative}, title, appendix, bodyLimit, sctx.WorkDir, nmWorktreesRoot())
}

func applySuppliedPRTitle(sctx *pipeline.StepContext, content prContent) (prContent, error) {
	supplied := suppliedPRTitle(sctx)
	if supplied == "" {
		return content, nil
	}
	title, err := renderSuppliedPRTitle(sctx, supplied)
	if err != nil {
		return prContent{}, err
	}
	content.Title = redactPRContent(prContent{Title: title}, sctx.WorkDir, nmWorktreesRoot()).Title
	return content, nil
}
