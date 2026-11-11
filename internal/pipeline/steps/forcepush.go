package steps

import (
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
)

type gitRunner func(args ...string) (string, error)

type forcePushDecision struct {
	remoteSHA   string // current remote head; the lease anchor for a force-push
	newBranch   bool   // the branch does not exist on the remote -> plain push
	upToDate    bool   // the remote already points at the head -> no push needed
	fastForward bool
}

type forcePushWouldDiscardError struct {
	ref       string
	remoteSHA string
	dropped   []string
}

func (e *forcePushWouldDiscardError) Error() string {
	sample := e.dropped
	if len(sample) > 5 {
		sample = sample[:5]
	}
	return fmt.Sprintf(
		"refusing to force-push %s: remote head %s carries %d commit(s) the pipeline never incorporated (e.g. %s); pushing would discard upstream work. Re-fetch and rebase onto the current remote, or push manually if this overwrite is intended.",
		e.ref, shortSHA(e.remoteSHA), len(e.dropped), strings.Join(shortSHAs(sample), ", "),
	)
}

func resolveForcePushDecision(gitRun gitRunner, pushURL, ref, newHeadSHA, lastSeenSHA, baseSHA string) (forcePushDecision, error) {
	current, err := lsRemoteSHA(gitRun, pushURL, ref)
	if err != nil {
		return forcePushDecision{}, fmt.Errorf("resolve remote head for %s: %w", ref, err)
	}
	if current == "" {
		return forcePushDecision{newBranch: true}, nil
	}
	if current == newHeadSHA {
		return forcePushDecision{remoteSHA: current, upToDate: true}, nil
	}
	// A fast-forward discards nothing by construction, so it needs neither the
	// lease nor --force. The ancestry test is local: when the remote head is a
	// commit we do not have (it arrived out of band), git errors and we fall
	// through to the guarded path below, which fetches it properly.
	if _, err := gitRun("merge-base", "--is-ancestor", current, newHeadSHA); err == nil {
		return forcePushDecision{remoteSHA: current, fastForward: true}, nil
	}
	if lastSeenSHA != "" && current == lastSeenSHA {
		// Remote unchanged since the pipeline last observed it: the force-push
		// only rewrites history we built on or last produced ourselves.
		return forcePushDecision{remoteSHA: current}, nil
	}
	// The remote moved to a commit we did not produce. Allow the push only if
	// everything now on the remote is already represented in what we are about
	// to push (or in the history the run is knowingly rewriting); otherwise
	// refuse rather than discard it.
	dropped, err := remoteCommitsNotIncorporated(gitRun, pushURL, ref, newHeadSHA, current, baseSHA)
	if err != nil {
		return forcePushDecision{}, fmt.Errorf("verify force-push safety for %s: %w", ref, err)
	}
	if len(dropped) == 0 {
		return forcePushDecision{remoteSHA: current}, nil
	}
	return forcePushDecision{}, &forcePushWouldDiscardError{ref: ref, remoteSHA: current, dropped: dropped}
}

func lsRemoteSHA(gitRun gitRunner, remote, ref string) (string, error) {
	out, err := gitRun("ls-remote", remote, ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func remoteCommitsNotIncorporated(gitRun gitRunner, pushURL, ref, newHeadSHA, remoteSHA, baseSHA string) ([]string, error) {
	branch := strings.TrimPrefix(ref, "refs/heads/")
	if _, err := gitRun("fetch", "--no-tags", pushURL, "refs/heads/"+branch); err != nil {
		return nil, fmt.Errorf("fetch remote branch: %w", err)
	}
	args := []string{"rev-list", "--cherry-pick", "--right-only", newHeadSHA + "..." + remoteSHA}
	if baseSHA != "" && !git.IsZeroSHA(baseSHA) {
		if _, err := gitRun("rev-parse", "--verify", "--quiet", baseSHA+"^{commit}"); err == nil {
			args = append(args, "^"+baseSHA)
		}
	}
	out, err := gitRun(args...)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commits = append(commits, line)
		}
	}
	return commits, nil
}

func shortSHAs(shas []string) []string {
	out := make([]string, len(shas))
	for i, s := range shas {
		out[i] = shortSHA(s)
	}
	return out
}
