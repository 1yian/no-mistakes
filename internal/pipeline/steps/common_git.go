package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/safeurl"
)

func reviewWorkload(ctx context.Context, workDir, base, head string) *agent.InvocationWorkload {
	files, lines, err := git.DiffStat(ctx, workDir, base, head)
	if err != nil {
		return nil
	}
	return &agent.InvocationWorkload{Files: files, Lines: lines}
}

func resolveBaseSHA(ctx context.Context, workDir, baseSHA, defaultBranch string) string {
	if !git.IsZeroSHA(baseSHA) {
		return baseSHA
	}
	if mb := mergeBaseWithDefaultBranch(ctx, workDir, defaultBranch); mb != "" {
		return mb
	}
	return git.EmptyTreeSHA
}

func resolveBranchBaseSHA(ctx context.Context, sctx *pipeline.StepContext, fallbackBaseSHA, defaultBranch string) (string, error) {
	// Eval replay has no upstream to fetch: it pins origin/<base> to the
	// captured commit in its isolated worktree, which is the base to use.
	if strings.TrimSpace(defaultBranch) != "" && !sctx.EvalReplay {
		if err := fetchRunUpstreamBranch(ctx, sctx, defaultBranch); err != nil {
			return "", fmt.Errorf("fetch default branch %q to resolve branch base: %w", defaultBranch, err)
		}
	}
	baseSHA := mergeBaseWithDefaultBranch(ctx, sctx.WorkDir, defaultBranch)
	if baseSHA == "" {
		baseSHA = resolveBaseSHA(ctx, sctx.WorkDir, fallbackBaseSHA, defaultBranch)
	}
	if err := sctx.CheckAgentCrashRetryBase(baseSHA); err != nil {
		return "", err
	}
	return baseSHA, nil
}

func resolveDefaultBranchTipSHA(ctx context.Context, workDir, upstreamURL, fallbackBaseSHA, defaultBranch string) string {
	sha, _ := resolveDefaultBranchTip(ctx, workDir, upstreamURL, fallbackBaseSHA, defaultBranch)
	return sha
}

func resolveRunDefaultBranchTip(ctx context.Context, sctx *pipeline.StepContext, fallbackBaseSHA, defaultBranch string) (string, bool) {
	if strings.TrimSpace(defaultBranch) != "" {
		if err := fetchRunUpstreamBranch(ctx, sctx, defaultBranch); err != nil {
			return unresolvedDefaultBranchTip(ctx, sctx.WorkDir, fallbackBaseSHA, defaultBranch), false
		}
		sha, err := git.Run(ctx, sctx.WorkDir, "rev-parse", "--verify", "origin/"+defaultBranch)
		if err == nil && strings.TrimSpace(sha) != "" {
			return strings.TrimSpace(sha), true
		}
	}
	return resolveBaseSHA(ctx, sctx.WorkDir, fallbackBaseSHA, defaultBranch), false
}

func resolveDefaultBranchTip(ctx context.Context, workDir, upstreamURL, fallbackBaseSHA, defaultBranch string) (string, bool) {
	if strings.TrimSpace(defaultBranch) != "" {
		remoteName := resolveUpstreamRemoteName(ctx, workDir, upstreamURL)
		if err := git.FetchRemoteBranch(ctx, workDir, remoteName, defaultBranch); err != nil {
			return unresolvedDefaultBranchTip(ctx, workDir, fallbackBaseSHA, defaultBranch), false
		}
		for _, ref := range []string{remoteName + "/" + defaultBranch, defaultBranch} {
			sha, err := git.Run(ctx, workDir, "rev-parse", "--verify", ref)
			if err == nil && strings.TrimSpace(sha) != "" {
				return strings.TrimSpace(sha), true
			}
		}
	}
	return resolveBaseSHA(ctx, workDir, fallbackBaseSHA, defaultBranch), false
}

func unresolvedDefaultBranchTip(ctx context.Context, workDir, fallbackBaseSHA, defaultBranch string) string {
	if !git.IsZeroSHA(fallbackBaseSHA) {
		return fallbackBaseSHA
	}
	sha, localErr := git.Run(ctx, workDir, "rev-parse", "--verify", defaultBranch)
	if localErr == nil && strings.TrimSpace(sha) != "" {
		return strings.TrimSpace(sha)
	}
	return git.EmptyTreeSHA
}

func resolveUpstreamRemoteName(ctx context.Context, workDir, upstreamURL string) string {
	if strings.TrimSpace(upstreamURL) == "" {
		return "origin"
	}
	remotes, err := git.Run(ctx, workDir, "remote")
	if err != nil {
		return "origin"
	}
	for _, remote := range strings.Fields(remotes) {
		url, urlErr := git.GetRemoteURL(ctx, workDir, remote)
		if urlErr == nil && strings.TrimSpace(url) == strings.TrimSpace(upstreamURL) {
			return remote
		}
	}
	return "origin"
}

func mergeBaseWithDefaultBranch(ctx context.Context, workDir, defaultBranch string) string {
	if strings.TrimSpace(defaultBranch) == "" {
		return ""
	}
	for _, ref := range []string{"origin/" + defaultBranch, defaultBranch} {
		mb, err := git.Run(ctx, workDir, "merge-base", "HEAD", ref)
		if err == nil && strings.TrimSpace(mb) != "" {
			return strings.TrimSpace(mb)
		}
	}
	return ""
}

func lastFetchedBranchTip(ctx context.Context, workDir, branch string, fork bool) string {
	trackingRef := "refs/remotes/origin/" + branch
	if fork {
		trackingRef = forkBranchTrackingRef(branch)
	}
	sha, err := git.Run(ctx, workDir, "rev-parse", "--verify", "--quiet", trackingRef+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sha)
}

func normalizedBranchRef(ref string) string {
	if !strings.HasPrefix(ref, "refs/") {
		return "refs/heads/" + ref
	}
	return ref
}

func resolveUpstreamURL(sctx *pipeline.StepContext) string {
	if url, err := git.GetRemoteURL(sctx.Ctx, sctx.WorkDir, "origin"); err == nil && strings.TrimSpace(url) != "" {
		// A matching redacted value means origin may carry credentials that the
		// database intentionally omits. A different registration was refreshed
		// from the working clone at run start, so prefer it without rewriting
		// either clone or gate remote configuration.
		if sctx.Repo == nil || !sctx.Repo.URLsVerified || safeurl.Redact(url) == sctx.Repo.UpstreamURL {
			return url
		}
	}
	return sctx.Repo.UpstreamURL
}

var fetchUpstreamTimeout = 120 * time.Second

var ErrFetchTimeout = errors.New("upstream fetch timed out")

func fetchRunUpstreamBranch(ctx context.Context, sctx *pipeline.StepContext, branch string) error {
	// Respect a deadline the caller already set rather than extending it.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, fetchUpstreamTimeout, ErrFetchTimeout)
		defer cancel()
	}

	err := fetchRunUpstreamBranchInner(ctx, sctx, branch)
	if err != nil && errors.Is(context.Cause(ctx), ErrFetchTimeout) {
		return fmt.Errorf("%w after %s: %v", ErrFetchTimeout, fetchUpstreamTimeout, err)
	}
	return err
}

func fetchRunUpstreamBranchInner(ctx context.Context, sctx *pipeline.StepContext, branch string) error {
	upstreamURL := resolveUpstreamURL(sctx)
	originURL, err := git.GetRemoteURL(ctx, sctx.WorkDir, "origin")
	if err == nil && upstreamURL == originURL {
		return git.FetchRemoteBranch(ctx, sctx.WorkDir, "origin", branch)
	}
	return git.FetchRemoteBranchToRef(ctx, sctx.WorkDir, upstreamURL, branch, "refs/remotes/origin/"+branch)
}

func resolvePushURL(sctx *pipeline.StepContext) string {
	if sctx.Repo != nil && strings.TrimSpace(sctx.Repo.ForkURL) != "" {
		return sctx.Repo.ForkURL
	}
	return resolveUpstreamURL(sctx)
}
