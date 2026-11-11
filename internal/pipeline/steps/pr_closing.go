package steps

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/closingissues"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/scm/github"
	"github.com/kunchenguid/no-mistakes/internal/scm/gitlab"
)

// The pipeline adds closing references to a PR body from exactly one place:
// explicit `axi run --closes` values, persisted on the run and claimed once by
// the PR step (sctx.ClosingIssueRefs). They render in one stable `## Issues`
// section. An author-preserving (pr.template) body keeps its author text
// verbatim, so only the references the author text does not already close
// are added to its generated appendix. Closure is never inferred from intent,
// commits, or branch names: pipeline-generated text is published with its
// closing references neutralized by the provider's closingGrammar.

const issuesSectionHeading = "## Issues"

type closingGrammar struct {
	keywordLine *regexp.Regexp
	reference   *regexp.Regexp
	neutralize  func(string) string
	codeAware   bool
}

const githubClosingKeywordPattern = `(?:close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved):?\s+(?:#[1-9][0-9]*|[A-Za-z0-9-]+/[A-Za-z0-9._-]+#[1-9][0-9]*)`

var closingKeywordLinePattern = regexp.MustCompile(`(?i)^(?:(?:[-*+]|[0-9]+[.)])\s+)?` + githubClosingKeywordPattern + `(?:\s*,\s*` + githubClosingKeywordPattern + `)*[.;!]?$`)

var closingReferencePattern = regexp.MustCompile(`(?i)(?:[A-Za-z0-9-]+/[A-Za-z0-9._-]+#[1-9][0-9]*|#[1-9][0-9]*)`)

var closingReferenceInTextPattern = regexp.MustCompile(`(?i)\b((?:close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved):?\s+)((?:[A-Za-z0-9-]+/[A-Za-z0-9._-]+)?#[1-9][0-9]*|https?://[A-Za-z0-9.-]+(?::[0-9]+)?/[A-Za-z0-9-]+/[A-Za-z0-9._-]+/(?:issues|pull)/[1-9][0-9]*)\b`)

const gitlabClosingKeywordPattern = `(?:[Cc]los(?:e[sd]?|ing)|[Ff]ix(?:e[sd]|ing)?|[Rr]esolv(?:e[sd]?|ing)|[Ii]mplement(?:s|ed|ing)?)`

const gitlabClosingTargetPattern = `(?:[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*#[1-9][0-9]*|#[1-9][0-9]*)`

const gitlabClosingReferenceList = gitlabClosingTargetPattern + `(?:(?: *, *(?:and +)?| +and +)` + gitlabClosingTargetPattern + `)*`

const gitlabClosingSeparator = `(?::? +(?:issues? +)?)`

var gitlabClosingKeywordLinePattern = regexp.MustCompile(`^(?:(?:[-*+]|[0-9]+[.)])[ \t]+)?` + gitlabClosingKeywordPattern + gitlabClosingSeparator + gitlabClosingReferenceList + `[.;!]?$`)

var gitlabClosingReferencePatternCompiled = regexp.MustCompile(gitlabClosingTargetPattern)

var gitlabClosingKeywordInTextPattern = regexp.MustCompile(`\b` + gitlabClosingKeywordPattern + `:? +`)

var (
	githubClosingGrammar = closingGrammar{
		keywordLine: closingKeywordLinePattern,
		reference:   closingReferencePattern,
		neutralize:  neutralizeGitHubClosingReferences,
		codeAware:   true,
	}
	gitlabClosingGrammar = closingGrammar{
		keywordLine: gitlabClosingKeywordLinePattern,
		reference:   gitlabClosingReferencePatternCompiled,
		neutralize:  neutralizeGitLabClosingReferences,
		codeAware:   false,
	}
)

func closingGrammarFor(provider scm.Provider) closingGrammar {
	if provider == scm.ProviderGitLab {
		return gitlabClosingGrammar
	}
	return githubClosingGrammar
}

func extractClosingKeywordLines(body string, grammar closingGrammar) []string {
	seen := map[string]struct{}{}
	var lines []string
	var fence markdownFence
	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if grammar.codeAware {
			inFence := fence.marker != 0
			fence.consume(raw)
			if inFence || fence.marker != 0 || strings.HasPrefix(raw, "\t") || strings.HasPrefix(raw, "    ") {
				continue
			}
		}
		line := strings.TrimSpace(raw)
		if !grammar.keywordLine.MatchString(line) {
			continue
		}
		key := strings.ToLower(line)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		lines = append(lines, line)
	}
	return lines
}

func neutralizeGitHubClosingReferences(s string) string {
	if !closingReferenceInTextPattern.MatchString(s) {
		return s
	}
	lines := strings.Split(s, "\n")
	var fence markdownFence
	var htmlCode string
	for i, raw := range lines {
		inFence := fence.marker != 0
		fence.consume(raw)
		if inFence || fence.marker != 0 || strings.HasPrefix(raw, "\t") || strings.HasPrefix(raw, "    ") {
			continue
		}
		lines[i] = outsideInlineCode(raw, func(text string) string {
			return outsideHTMLCode(text, &htmlCode, func(text string) string {
				return closingReferenceInTextPattern.ReplaceAllString(text, "${1}`${2}`")
			})
		})
	}
	return strings.Join(lines, "\n")
}

func neutralizeGitLabClosingReferences(s string) string {
	return gitlabClosingKeywordInTextPattern.ReplaceAllStringFunc(s, func(match string) string {
		return strings.Replace(match, " ", "&#32;", 1)
	})
}

var htmlCodeTagPattern = regexp.MustCompile(`(?i)<(/?)(code|pre)\b[^>]*>`)

func outsideHTMLCode(text string, open *string, rewrite func(string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range htmlCodeTagPattern.FindAllStringSubmatchIndex(text, -1) {
		closing := m[3] > m[2]
		name := strings.ToLower(text[m[4]:m[5]])
		switch {
		case *open == "" && !closing:
			b.WriteString(rewrite(text[last:m[0]]))
			*open = name
		case *open == name && closing:
			b.WriteString(text[last:m[0]])
			*open = ""
		default:
			continue
		}
		b.WriteString(text[m[0]:m[1]])
		last = m[1]
	}
	if *open == "" {
		b.WriteString(rewrite(text[last:]))
	} else {
		b.WriteString(text[last:])
	}
	return b.String()
}

func outsideInlineCode(line string, rewrite func(string) string) string {
	var b strings.Builder
	text := 0
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		n := 1
		for i+n < len(line) && line[i+n] == '`' {
			n++
		}
		closing := -1
		for j := i + n; j < len(line); {
			if line[j] != '`' {
				j++
				continue
			}
			m := 1
			for j+m < len(line) && line[j+m] == '`' {
				m++
			}
			if m == n {
				closing = j
				break
			}
			j += m
		}
		if closing < 0 {
			i += n
			continue
		}
		b.WriteString(rewrite(line[text:i]))
		b.WriteString(line[i : closing+n])
		i = closing + n
		text = i
	}
	b.WriteString(rewrite(line[text:]))
	return b.String()
}

func closingTargets(lines []string, repo string, grammar closingGrammar) map[string]struct{} {
	targets := map[string]struct{}{}
	for _, line := range lines {
		for _, target := range grammar.reference.FindAllString(line, -1) {
			target = closingissues.Localize(strings.TrimPrefix(target, "#"), repo)
			targets[strings.ToLower(target)] = struct{}{}
		}
	}
	return targets
}

func prRepository(sctx *pipeline.StepContext, provider scm.Provider) string {
	if sctx == nil {
		return ""
	}
	if sctx.Repo != nil {
		if repo := projectPathForProvider(sctx.Repo.UpstreamURL, provider); repo != "" {
			return repo
		}
	}
	if sctx.Run != nil && sctx.Run.PRURL != nil {
		return projectPathForProvider(*sctx.Run.PRURL, provider)
	}
	return ""
}

func projectPathForProvider(raw string, provider scm.Provider) string {
	if provider == scm.ProviderGitLab {
		// A merge request URL needs its own accessor: ProjectPath reads a
		// repository remote and would leave the /-/merge_requests/<n> tail on.
		if project := gitlab.ProjectPathFromMRURL(raw); project != "" {
			return project
		}
		return gitlab.ProjectPath(raw)
	}
	return github.RepoSlug(raw)
}

func closingLine(ref string) string {
	return "Closes " + closingissues.Target(ref)
}

func issuesSection(sctx *pipeline.StepContext, authorText string) string {
	if sctx == nil {
		return ""
	}
	provider := resolvedProviderForBody(sctx)
	grammar := closingGrammarFor(provider)
	var lines []string
	present := closingTargets(extractClosingKeywordLines(authorText, grammar), prRepository(sctx, provider), grammar)
	for _, ref := range sctx.ClosingIssueRefs {
		key := strings.ToLower(ref)
		if _, exists := present[key]; exists {
			continue
		}
		present[key] = struct{}{}
		lines = append(lines, closingLine(ref))
	}
	if len(lines) == 0 {
		return ""
	}
	return issuesSectionHeading + "\n\n" + strings.Join(lines, "\n")
}

func appendIssuesSection(body, section string) string {
	if section == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return section
	}
	return body + "\n\n" + section
}

func claimClosingIssueRefs(sctx *pipeline.StepContext, host scm.Host, provider scm.Provider) error {
	if sctx.DB == nil {
		return nil
	}
	refs, err := sctx.DB.ClaimClosingIssueRefsForPRBody(sctx.Run.ID)
	if err != nil {
		return fmt.Errorf("resolve closing issue references: %w", err)
	}
	// The PR's own repository names an issue one way, so `95` and
	// `owner/repo#95` render once.
	repo := prRepository(sctx, provider)
	for i, ref := range refs {
		refs[i] = closingissues.Localize(ref, repo)
	}
	refs, err = closingissues.Normalize(refs)
	if err != nil {
		return fmt.Errorf("resolve closing issue references: %w", err)
	}
	if len(refs) == 0 {
		sctx.ClosingIssueRefs = nil
		return nil
	}
	// A nested project path is a GitLab subgroup reference. Publishing one for
	// another forge would hand it text it reads as a *different* repository's
	// owner/repo reference, so it fails closed instead of being silently
	// mistranslated.
	if provider != scm.ProviderGitLab {
		for _, ref := range refs {
			if strings.Count(ref, "/") > 1 {
				return fmt.Errorf("render closing issues: %s is a GitLab subgroup reference, and this repository's provider (%s) names issues as owner/repository#number", ref, provider)
			}
		}
	}
	// A provider that does not close issues from the pull request body would
	// publish a reference that silently closes nothing, leaving the requested
	// issue open after merge.
	if !host.Capabilities().ClosingReferences {
		return fmt.Errorf("render closing issues: provider %s does not support closing references from the pull request body", provider)
	}
	if _, ok := host.(scm.PRContentReader); !ok {
		return fmt.Errorf("verify closing issues: provider cannot read the current pull request body")
	}
	// Assigned only once every gate has passed, so a refusal never leaves the
	// step holding references it will not publish.
	sctx.ClosingIssueRefs = refs
	return nil
}

func refuseSkipWithClosingIssues(sctx *pipeline.StepContext, reason string) error {
	if sctx.DB == nil {
		return nil
	}
	refs, err := sctx.DB.ClaimClosingIssueRefsForPRBody(sctx.Run.ID)
	if err != nil {
		return fmt.Errorf("resolve closing issue references: %w", err)
	}
	if len(refs) == 0 {
		return nil
	}
	return fmt.Errorf("render closing issues: --closes requires publishing a pull request, but PR creation is unavailable: %s", reason)
}

func verifyClosingIssues(ctx context.Context, host scm.Host, pr *scm.PR, sctx *pipeline.StepContext) error {
	if sctx == nil || len(sctx.ClosingIssueRefs) == 0 {
		return nil
	}
	reader, ok := host.(scm.PRContentReader)
	if !ok {
		return fmt.Errorf("verify closing issues: provider cannot read the current pull request body")
	}
	if pr == nil {
		return fmt.Errorf("verify closing issues: pull request identity is unavailable")
	}
	content, err := reader.GetPRContent(ctx, pr)
	if err != nil {
		return fmt.Errorf("verify closing issues: %w", err)
	}
	return verifyClosingIssuesInBody(content.Body, sctx)
}

func verifyClosingIssuesInBody(body string, sctx *pipeline.StepContext) error {
	if sctx == nil {
		return nil
	}
	provider := resolvedProviderForBody(sctx)
	grammar := closingGrammarFor(provider)
	targets := closingTargets(extractClosingKeywordLines(body, grammar), prRepository(sctx, provider), grammar)
	for _, ref := range sctx.ClosingIssueRefs {
		if _, ok := targets[strings.ToLower(ref)]; !ok {
			return fmt.Errorf("verify closing issues: pull request body is missing %s", closingLine(ref))
		}
	}
	return nil
}

func assembleDraftPRBody(sctx *pipeline.StepContext, whatChanged, riskLine, testingMD, pipelineMD, evidenceMD string, bodyLimit int, provider scm.Provider) string {
	section := joinBlocks(neutralizeAttestationMarkers(provider, evidenceMD), issuesSection(sctx, ""))
	reserve := 0
	if section != "" {
		reserve = len("\n\n" + section)
	}
	if bodyLimit > 0 {
		if section != "" {
			reserve = scm.PRBodyLen("\n\n" + section)
		}
		if bodyLimit-reserve <= 0 {
			if evidenceMD != "" {
				return appendIssuesSection(assemblePRBody(sctx, whatChanged, riskLine, testingMD, pipelineMD, bodyLimit, provider), section)
			}
			// The section alone does not fit. Omit it rather than pass a
			// non-positive (unlimited) budget; the pre-publication check then
			// refuses a body that would drop a closing reference.
			return assemblePRBody(sctx, whatChanged, riskLine, testingMD, pipelineMD, bodyLimit, provider)
		}
		return appendIssuesSection(assemblePRBody(sctx, whatChanged, riskLine, testingMD, pipelineMD, bodyLimit-reserve, provider), section)
	}
	return appendIssuesSection(buildPRBodyWithin(whatChanged, riskLine, testingMD, pipelineMD, sctx, provider, maxPullRequestBodyBytes-reserve), section)
}
