package steps

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/config"
)

type pathInstructionBlock struct {
	Path         string
	Instructions string
	Files        []string
}

type pathInstructionMatches struct {
	Blocks       []pathInstructionBlock
	UnmatchedIDs []string
	DuplicateIDs []string
	UnusableIDs  []string
}

func reviewPathsAndInstructions(changed []string, cfg *config.Config, log func(string)) ([]string, string) {
	sources := cfg.Review.PathInstructionSources()
	matches := make([]pathInstructionMatches, len(sources))
	covered := make(map[string]bool)
	for i, source := range sources {
		matches[i] = matchPathInstructions(changed, source.Entries)
		for _, block := range matches[i].Blocks {
			for _, file := range block.Files {
				covered[file] = true
			}
		}
	}
	keep := make(map[string]bool)
	for _, file := range reviewablePaths(changed, cfg.IgnorePatterns) {
		keep[file] = true
	}
	for _, file := range reviewablePaths(changed, cfg.TrustedIgnorePatterns) {
		if covered[file] {
			keep[file] = true
		}
	}
	var reviewable []string
	for _, file := range changed {
		if keep[file] {
			reviewable = append(reviewable, file)
		}
	}
	var instructions string
	for i, source := range sources {
		scoped := matches[i]
		scoped.Blocks = nil
		for _, block := range matches[i].Blocks {
			var files []string
			for _, file := range block.Files {
				if keep[file] {
					files = append(files, file)
				}
			}
			if len(files) > 0 {
				block.Files = files
				scoped.Blocks = append(scoped.Blocks, block)
			}
		}
		logPathInstructions(log, source.Label, scoped)
		instructions += reviewPathInstructionsSection(source.Heading, scoped)
	}
	return reviewable, instructions
}

func matchPathInstructions(changed []string, rules []config.PathInstruction) pathInstructionMatches {
	var out pathInstructionMatches
	if len(rules) == 0 {
		return out
	}
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		pattern := strings.TrimSpace(rule.Path)
		instructions := sanitizePromptMultilineText(rule.Instructions)
		if pattern == "" || instructions == "" {
			out.UnusableIDs = append(out.UnusableIDs, pathInstructionID(rule.Path))
			continue
		}
		key := pattern + "\x00" + instructions
		if seen[key] {
			out.DuplicateIDs = append(out.DuplicateIDs, pattern)
			continue
		}
		seen[key] = true
		files := matchingChangedPaths(changed, pattern)
		if len(files) == 0 {
			out.UnmatchedIDs = append(out.UnmatchedIDs, pattern)
			continue
		}
		out.Blocks = append(out.Blocks, pathInstructionBlock{
			Path:         pattern,
			Instructions: instructions,
			Files:        files,
		})
	}
	return out
}

func pathInstructionID(path string) string {
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		return trimmed
	}
	return "(no path)"
}

func matchingChangedPaths(changed []string, pattern string) []string {
	var files []string
	for _, path := range changed {
		if matchIgnorePattern(path, pattern) {
			files = append(files, path)
		}
	}
	return files
}

func matchedFilesSummary(files []string) string {
	const sep = ", "
	var rendered []string
	shown := 0
	used := 0
	for i, file := range files {
		file = promptPath(file)
		add := len(file)
		if shown > 0 {
			add += len(sep)
		}
		// Keep room for the remaining-count suffix in case this is the last
		// file that fits.
		reserve := 0
		if remaining := len(files) - i - 1; remaining > 0 {
			reserve = len(sep) + len(fmt.Sprintf("+%d more", remaining))
		}
		if used+add+reserve > config.ReviewPathInstructionsMaxFilesBytes {
			break
		}
		shown++
		used += add
		rendered = append(rendered, file)
	}
	summary := strings.Join(rendered, sep)
	if dropped := len(files) - shown; dropped > 0 {
		if summary != "" {
			summary += sep
		}
		summary += fmt.Sprintf("+%d more", dropped)
	}
	return summary
}

func promptPath(path string) string {
	if strings.IndexFunc(path, func(r rune) bool { return !strconv.IsGraphic(r) }) >= 0 {
		return strconv.QuoteToGraphic(path)
	}
	return path
}

func reviewPathInstructionsSection(heading string, matches pathInstructionMatches) string {
	if len(matches.Blocks) == 0 {
		return ""
	}
	rendered := make([]string, 0, len(matches.Blocks))
	for _, block := range matches.Blocks {
		rendered = append(rendered,
			config.ReviewPathInstructionsPathLabel+block.Path+"\n"+
				config.ReviewPathInstructionsFilesLabel+matchedFilesSummary(block.Files)+"\n"+
				config.ReviewPathInstructionsRulesLabel+"\n"+
				block.Instructions)
	}
	return "\n\n" + heading + "\n" + strings.Join(rendered, "\n\n")
}

func logPathInstructions(log func(string), source string, matches pathInstructionMatches) {
	if log == nil {
		return
	}
	if len(matches.Blocks) > 0 {
		scopes := make([]string, 0, len(matches.Blocks))
		for _, block := range matches.Blocks {
			scopes = append(scopes, fmt.Sprintf("%s (%d file(s))", block.Path, len(block.Files)))
		}
		log(fmt.Sprintf("applied %d %s review instruction block(s) for changed paths: %s", len(matches.Blocks), source, strings.Join(scopes, "; ")))
	}
	if len(matches.UnmatchedIDs) > 0 {
		log(fmt.Sprintf("%d %s review instruction rule(s) matched no changed path: %s", len(matches.UnmatchedIDs), source, strings.Join(matches.UnmatchedIDs, ", ")))
	}
	if len(matches.DuplicateIDs) > 0 {
		log(fmt.Sprintf("skipped %d duplicate %s review instruction rule(s): %s", len(matches.DuplicateIDs), source, strings.Join(matches.DuplicateIDs, ", ")))
	}
	if len(matches.UnusableIDs) > 0 {
		log(fmt.Sprintf("skipped %d %s review instruction rule(s) with no usable path or instructions: %s", len(matches.UnusableIDs), source, strings.Join(matches.UnusableIDs, ", ")))
	}
}
