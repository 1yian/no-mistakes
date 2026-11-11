package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

type CmdFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

type Host struct {
	cmd                 CmdFactory
	cliAvailable        func() bool
	host                string // repo's GitLab hostname; scopes the auth check
	projectPath         string // repo's "group/project" path; enables REST job reads
	draft               bool   // open created MRs as drafts (glab mr create --draft)
	requiredReviews     []string
	requiredReviewBotID string
	formProbeOnce       sync.Once
	formProbeOK         bool
}

func New(cmd CmdFactory, cliAvailable func() bool, host, projectPath string) *Host {
	return &Host{
		cmd:          cmd,
		cliAvailable: cliAvailable,
		host:         strings.TrimSpace(host),
		projectPath:  strings.TrimSpace(projectPath),
	}
}

func NewWithDraft(cmd CmdFactory, cliAvailable func() bool, host, projectPath string, draft bool) *Host {
	h := New(cmd, cliAvailable, host, projectPath)
	h.draft = draft
	return h
}

func (h *Host) WithRequiredReviews(reviewers []string, botID string) *Host {
	h.requiredReviews = append([]string(nil), reviewers...)
	h.requiredReviewBotID = strings.TrimSpace(botID)
	return h
}

func ProjectPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var path string
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil {
			path = u.Path
		}
	} else if colon := strings.Index(raw, ":"); colon >= 0 && !isWindowsDrivePath(raw) {
		// scp-style: [user@]host:group/project.git -> group/project. The first
		// ':' separates host from path, so the path is recovered whether or not
		// a "user@" prefix is present (e.g. gitlab.example.com:group/project.git).
		// A Windows drive-letter path (C:\...) carries a colon too, but it is a
		// local filesystem path, not a remote URL, so it is excluded above.
		path = raw[colon+1:]
	}
	path = strings.Trim(path, "/")
	return strings.TrimSuffix(path, ".git")
}

func isWindowsDrivePath(raw string) bool {
	if len(raw) < 2 || raw[1] != ':' {
		return false
	}
	c := raw[0]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
		return false
	}
	return len(raw) == 2 || raw[2] == '\\' || raw[2] == '/'
}

func (h *Host) pipelineJobsArgs(pipelineID int) []string {
	if h.projectPath != "" {
		// --paginate walks every page; a pipeline with more jobs than fit on one
		// page (GitLab defaults to 20 per page) would otherwise silently drop the
		// jobs on later pages and the CI verdict could miss a failed job. glab
		// writes one JSON array per page, so the parser handles concatenated docs.
		return []string{"api", "--paginate", fmt.Sprintf("projects/%s/pipelines/%d/jobs", encodeProjectPath(h.projectPath), pipelineID)}
	}
	return []string{"ci", "get", "--pipeline-id", fmt.Sprintf("%d", pipelineID), "--output", "json", "--with-job-details"}
}

func encodeProjectPath(projectPath string) string {
	segments := strings.Split(projectPath, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "%2F")
}

func (h *Host) pipelineRouteArgs(pipelineID int, route string) []string {
	return []string{"api", "--paginate", fmt.Sprintf("projects/%s/pipelines/%d/%s", encodeProjectPath(h.projectPath), pipelineID, route)}
}

func (h *Host) mergeRequestStatusChecksArgs(mrIID string) []string {
	return []string{"api", "--paginate", fmt.Sprintf("projects/%s/merge_requests/%s/status_checks", encodeProjectPath(h.projectPath), url.PathEscape(mrIID))}
}

func (h *Host) Provider() scm.Provider { return scm.ProviderGitLab }

func (h *Host) Capabilities() scm.Capabilities {
	return scm.Capabilities{
		MergeableState:    true,
		FailedCheckLogs:   true,
		ReviewComments:    true,
		ClosingReferences: true,
	}
}

func (h *Host) Available(ctx context.Context) error {
	if h.cliAvailable != nil && !h.cliAvailable() {
		return errors.New("glab CLI is not installed")
	}
	// Scope the auth check to this repo's host. Unscoped `glab auth status`
	// checks every configured instance and exits non-zero if ANY of them has a
	// stale/expired token, even when this repo's own host is fully
	// authenticated. Passing --hostname keeps an unrelated bad credential from
	// poisoning availability for this repo. When the host is unknown we fall
	// back to the unscoped check (fail-safe: same behavior as before).
	authArgs := []string{"auth", "status"}
	if h.host != "" {
		authArgs = append(authArgs, "--hostname", h.host)
	}
	if err := h.cmd(ctx, "glab", authArgs...).Run(); err != nil {
		return errors.New("glab CLI is not authenticated")
	}
	return nil
}

func (h *Host) parseMergeRequestURL(ctx context.Context, raw string) (int, error) {
	number, err := parseMergeRequestURL(raw, h.host, h.projectPath)
	if err == nil || h.host == "" {
		return number, err
	}
	parsed, parseErr := url.Parse(strings.TrimSpace(raw))
	if parseErr != nil || parsed.Hostname() == "" || strings.EqualFold(parsed.Hostname(), h.host) {
		return 0, err
	}
	out, configErr := h.cmd(ctx, "glab", "config", "get", "api_host", "--host", h.host, "--global").Output()
	if configErr != nil {
		return 0, fmt.Errorf("%w (cannot read global glab api_host: %v)", err, configErr)
	}
	// api_host is a host[:port], not a URL or a path. Reject malformed or
	// empty output rather than allowing the parser's legacy unscoped mode.
	apiHost := strings.TrimSpace(string(out))
	configured, configErr := url.Parse("https://" + apiHost)
	if configErr != nil || apiHost == "" || strings.ContainsAny(apiHost, "/?#@\\\t\r\n ") || configured.Hostname() == "" {
		return 0, fmt.Errorf("%w (invalid global glab api_host)", err)
	}
	return parseMergeRequestURL(raw, configured.Hostname(), h.projectPath)
}

func parseMergeRequestURL(raw, expectedHost, expectedProject string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return 0, errors.New("expected absolute GitLab merge request URL")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return 0, errors.New("expected HTTP GitLab merge request URL")
	}
	if expectedHost != "" && !strings.EqualFold(parsed.Hostname(), expectedHost) {
		return 0, fmt.Errorf("URL host %q does not match GitLab host %q", parsed.Hostname(), expectedHost)
	}
	projectSegments, number, ok := mergeRequestURLPath(parsed)
	if !ok {
		return 0, errors.New("expected GitLab /group/project/-/merge_requests/number URL")
	}
	// The project half is checked here rather than inside the shape reader:
	// callers that only want the project path accept the same shape with less
	// validation, while a merge request identity must be unambiguous.
	for _, segment := range projectSegments {
		if segment == "" || segment == "." || segment == ".." {
			return 0, errors.New("expected unambiguous GitLab project path")
		}
	}
	actualProject := strings.Join(projectSegments, "/")
	expectedProject = strings.Trim(strings.TrimSpace(expectedProject), "/")
	if expectedProject != "" && !strings.EqualFold(actualProject, expectedProject) {
		return 0, fmt.Errorf("URL project %q does not match GitLab project %q", actualProject, expectedProject)
	}
	escapedSegments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(escapedSegments) != len(projectSegments)+3 || escapedSegments[len(escapedSegments)-1] != strconv.Itoa(number) {
		return 0, errors.New("expected canonical GitLab merge request number path")
	}
	if parsed.ForceQuery || parsed.RawQuery != "" || strings.Contains(trimmed, "#") {
		return 0, errors.New("expected GitLab merge request URL without query or fragment")
	}
	return number, nil
}

func mergeRequestURLPath(parsed *url.URL) ([]string, int, bool) {
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 5 || segments[len(segments)-3] != "-" || segments[len(segments)-2] != "merge_requests" {
		return nil, 0, false
	}
	number, err := strconv.Atoi(segments[len(segments)-1])
	if err != nil || number <= 0 {
		return nil, 0, false
	}
	return segments[:len(segments)-3], number, true
}

type mrPayload struct {
	Description         *string         `json:"description"`
	IID                 int             `json:"iid"`
	Title               string          `json:"title"`
	WebURL              string          `json:"web_url"`
	URL                 string          `json:"url"`
	State               string          `json:"state"`
	HasConflicts        bool            `json:"has_conflicts"`
	DetailedMergeStatus string          `json:"detailed_merge_status"`
	MergeStatus         string          `json:"merge_status"`
	TargetBranch        string          `json:"target_branch"`
	SHA                 string          `json:"sha"`
	DiffRefs            mrDiffRefs      `json:"diff_refs"`
	HeadPipeline        json.RawMessage `json:"head_pipeline"`
}

type mrDiffRefs struct {
	HeadSHA string `json:"head_sha"`
}

type gitlabHeadPipeline struct {
	ID         int    `json:"id"`
	SHA        string `json:"sha"`
	Ref        string `json:"ref"`
	Status     string `json:"status"`
	WebURL     string `json:"web_url"`
	FinishedAt string `json:"finished_at"`
	UpdatedAt  string `json:"updated_at"`
}

func (p gitlabHeadPipeline) completedAt() time.Time {
	for _, raw := range []string{p.FinishedAt, p.UpdatedAt} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func (p gitlabHeadPipeline) awaitingManualJob() bool {
	return strings.EqualFold(strings.TrimSpace(p.Status), "manual")
}

func (p mrPayload) sourceRevision() string {
	if sha := strings.TrimSpace(p.SHA); sha != "" {
		return sha
	}
	return strings.TrimSpace(p.DiffRefs.HeadSHA)
}

func (p mrPayload) headPipeline() (*gitlabHeadPipeline, error) {
	if len(p.HeadPipeline) == 0 {
		return nil, errors.New(`response has no "head_pipeline" field`)
	}
	trimmed := bytes.TrimSpace(p.HeadPipeline)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var pipeline gitlabHeadPipeline
	if err := json.Unmarshal(trimmed, &pipeline); err != nil {
		return nil, fmt.Errorf("cannot read head_pipeline %.200s: %w", trimmed, err)
	}
	if pipeline.ID <= 0 {
		return nil, fmt.Errorf("head_pipeline has no positive ID: %.200s", trimmed)
	}
	return &pipeline, nil
}

func (p mrPayload) toPR() *scm.PR {
	url := strings.TrimSpace(p.WebURL)
	if url == "" {
		url = strings.TrimSpace(p.URL)
	}
	pr := &scm.PR{URL: url, BaseBranch: strings.TrimSpace(p.TargetBranch)}
	if p.IID > 0 {
		pr.Number = fmt.Sprintf("%d", p.IID)
	}
	return pr
}

func (h *Host) FindPR(ctx context.Context, branch, base string) (*scm.PR, error) {
	args := []string{"mr", "list", "--source-branch", branch}
	if strings.TrimSpace(base) != "" {
		args = append(args, "--target-branch", base)
	}
	// `glab mr list` returns open MRs by default. Older glab accepted
	// `--state opened`, but glab v1.5x removed it (it now exposes
	// -c/--closed, -M/--merged, -A/--all); passing the unknown flag fails the
	// whole command. Rely on the open-by-default behavior.
	args = append(args, "--output", "json")
	cmd := h.cmd(ctx, "glab", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("glab mr list: %s: %w", strings.TrimSpace(string(out)), err)
	}
	trimmed := bytesTrimToJSON(out)
	if len(trimmed) == 0 {
		return nil, errors.New("parse glab mr list JSON: no JSON found")
	}
	var mrs []mrPayload
	if err := json.Unmarshal(trimmed, &mrs); err != nil {
		return nil, fmt.Errorf("parse glab mr list JSON: %w", err)
	}
	if mrs == nil {
		return nil, errors.New("parse glab mr list JSON: expected array")
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	for i, candidate := range mrs {
		url := strings.TrimSpace(candidate.WebURL)
		if url == "" {
			url = strings.TrimSpace(candidate.URL)
		}
		if url == "" {
			return nil, fmt.Errorf("parse glab mr list JSON: entry %d missing merge request URL", i)
		}
		number, err := h.parseMergeRequestURL(ctx, url)
		if err != nil {
			return nil, fmt.Errorf("parse glab mr list JSON: entry %d invalid merge request URL: %w", i, err)
		}
		if candidate.IID != 0 && candidate.IID != number {
			return nil, fmt.Errorf("parse glab mr list JSON: entry %d IID %d does not match URL number %d", i, candidate.IID, number)
		}
	}
	pr := mrs[0].toPR()
	return pr, nil
}

func (h *Host) CreatePR(ctx context.Context, branch, base string, content scm.PRContent) (*scm.PR, error) {
	effectiveTitle := content.Title
	if h.draft && !isDraftTitle(effectiveTitle) {
		effectiveTitle = "Draft: " + effectiveTitle
	}
	if err := validateMRTitle(effectiveTitle); err != nil {
		return nil, fmt.Errorf("glab mr create: %w", err)
	}
	args := []string{"mr", "create",
		"--source-branch", branch,
		"--target-branch", base,
		"--title", content.Title,
		"--description", content.Body,
		"--yes",
	}
	if h.draft {
		args = append(args, "--draft")
	}
	cmd := h.cmd(ctx, "glab", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("glab mr create: %s: %w", strings.TrimSpace(string(out)), err)
	}
	url := extractMRURL(out)
	pr := &scm.PR{URL: url}
	if num, nerr := scm.ExtractPRNumber(url); nerr == nil {
		pr.Number = num
	}
	return pr, nil
}

func (h *Host) UpdatePR(ctx context.Context, pr *scm.PR, content scm.PRContent) (*scm.PR, error) {
	id := pr.Number
	if id == "" && pr != nil {
		if num, err := scm.ExtractPRNumber(pr.URL); err == nil {
			id = num
		}
	}
	if id == "" && pr != nil {
		id = pr.URL
	}
	// Unlike `glab mr create`, `glab mr update` (glab v1.5x) has no
	// -y/--yes confirmation-skip flag at all; passing it fails the whole
	// command with "unknown flag: --yes", so every UpdatePR call errored.
	//
	// GitLab has no separate draft field: an MR is a draft because its title
	// carries a draft marker. Updating with a plain title would silently mark a
	// draft MR ready for review, so read the live title first and re-apply the
	// marker. Preserve only: a non-draft MR never gains one. A failed read fails
	// the update closed rather than risk toggling draft state.
	args := []string{"mr", "update", id}
	// Body-only updates must omit title, not read then resend it: doing so
	// would overwrite a concurrent title/draft edit.
	if content.Title != "" {
		mr, err := h.viewMR(ctx, id)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(mr.Title) == "" {
			return nil, errors.New("glab mr view: missing merge request title")
		}
		title := content.Title
		if isDraftTitle(mr.Title) && !isDraftTitle(title) {
			title = "Draft: " + title
		}
		if err := validateMRTitle(title); err != nil {
			return nil, fmt.Errorf("glab mr update: %w", err)
		}
		args = append(args, "--title", title)
	}
	args = append(args, "--description", content.Body)
	cmd := h.cmd(ctx, "glab", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("glab mr update: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return pr, nil
}

func (h *Host) SetPRBaseBranch(ctx context.Context, pr *scm.PR, baseBranch string) error {
	id := ""
	if pr != nil {
		id = pr.Number
		if id == "" {
			if num, err := scm.ExtractPRNumber(pr.URL); err == nil {
				id = num
			}
		}
		if id == "" {
			id = pr.URL
		}
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("merge request identity is required to retarget")
	}
	cmd := h.cmd(ctx, "glab", "mr", "update", id, "--target-branch", baseBranch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("glab mr update --target-branch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func isDraftTitle(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	return strings.HasPrefix(t, "draft:") || strings.HasPrefix(t, "[draft]") || strings.HasPrefix(t, "(draft)")
}

func validateMRTitle(title string) error {
	if utf8.RuneCountInString(title) > 255 {
		return errors.New("GitLab merge request title must not exceed 255 characters")
	}
	return nil
}

func (h *Host) GetPRState(ctx context.Context, pr *scm.PR) (scm.PRState, error) {
	mr, err := h.viewMR(ctx, pr.Number)
	if err != nil {
		return "", err
	}
	return normalizePRState(mr.State), nil
}

func (h *Host) GetMergeableState(ctx context.Context, pr *scm.PR) (scm.MergeableState, error) {
	mr, err := h.viewMR(ctx, pr.Number)
	if err != nil {
		return "", err
	}
	if mr.HasConflicts {
		return scm.MergeableConflict, nil
	}
	// detailed_merge_status is preferred; merge_status is the legacy field.
	status := strings.ToLower(strings.TrimSpace(mr.DetailedMergeStatus))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(mr.MergeStatus))
	}
	switch status {
	case "mergeable", "can_be_merged":
		return scm.MergeableOK, nil
	case "broken_status", "cannot_be_merged":
		return scm.MergeableConflict, nil
	case "checking", "unchecked", "ci_still_running", "":
		return scm.MergeablePending, nil
	default:
		return scm.MergeableOK, nil
	}
}

func (h *Host) viewMR(ctx context.Context, id string) (mrPayload, error) {
	return h.viewMRInRepository(ctx, id, "")
}

func (h *Host) viewMRInRepository(ctx context.Context, id, repo string) (mrPayload, error) {
	args := []string{"mr", "view", id, "--output", "json"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	cmd := h.cmd(ctx, "glab", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return mrPayload{}, fmt.Errorf("glab mr view: %s: %w", strings.TrimSpace(string(out)), err)
	}
	mr, ok := parseMRPayload(out)
	if !ok {
		return mrPayload{}, fmt.Errorf("glab mr view: invalid JSON output: %s", strings.TrimSpace(string(out)))
	}
	return mr, nil
}

func (h *Host) GetChecks(ctx context.Context, pr *scm.PR) ([]scm.Check, error) {
	pr.ObservedHeadSHA = ""
	if headSHA := strings.TrimSpace(pr.HeadSHA); headSHA != "" {
		return h.getChecksForHead(ctx, pr, headSHA)
	}
	// Without a named source commit there is nothing to bind a read to, so the
	// pipeline is taken as the merge request reports it. The observation is
	// still composed the same way: a head without a bound commit must not hide
	// a failed or pending pipeline either.
	mr, err := h.viewMR(ctx, pr.Number)
	if err != nil {
		return nil, err
	}
	pipeline, err := mr.headPipeline()
	if err != nil {
		return nil, fmt.Errorf("glab mr view: %w", err)
	}
	return h.pipelineChecks(ctx, pr, pipeline, nil)
}

func (h *Host) pipelineChecks(ctx context.Context, pr *scm.PR, pipeline *gitlabHeadPipeline, afterReads func() error) ([]scm.Check, error) {
	var checks []scm.Check
	if pipeline != nil {
		jobsOut, err := h.readPipelineJobs(ctx, pipeline.ID)
		if err != nil {
			return nil, err
		}
		jobs, err := parseGitlabJobs(jobsOut)
		if err != nil {
			return nil, err
		}
		checks = jobs
		if h.projectPath != "" {
			// A trigger job is not a `/jobs` row: GitLab exposes it only on its
			// own route, and its status is where a downstream pipeline's result
			// reaches the head pipeline's aggregate.
			triggerJobs, err := h.pipelineTriggerJobs(ctx, pipeline.ID)
			if err != nil {
				return nil, err
			}
			checks = append(checks, triggerJobsToChecks(triggerJobs)...)
		}
		// The aggregate is compared against the pipeline's own rows only: a
		// failed external status check is a separate requirement, so letting it
		// stand in for the pipeline's state would hide a pipeline failure no job
		// row explains (a rejected configuration, most often).
		if aggregate, ok := pipelineAggregateCheck(*pipeline, checks); ok {
			checks = append(checks, aggregate)
		}
	}
	if h.projectPath != "" {
		external, err := h.mergeRequestStatusChecks(ctx, pr.Number)
		if err != nil {
			return nil, err
		}
		if pipeline != nil || hasNonPassingCheck(external) {
			checks = append(checks, external...)
		}
	}
	if check, ok, err := h.codeRabbitReviewCheck(ctx, pr); err != nil {
		return nil, err
	} else if ok {
		checks = append(checks, check)
		if pipeline == nil {
			checks = append(checks, scm.Check{
				Name: "Pipeline registration", ProviderID: "gitlab:pipeline-registration",
				Bucket: scm.CheckBucketPending, State: "WAITING FOR PIPELINE",
			})
		}
	}
	if afterReads != nil {
		if err := afterReads(); err != nil {
			return nil, err
		}
	}
	return checks, nil
}

func hasNonPassingCheck(checks []scm.Check) bool {
	for _, check := range checks {
		if check.Bucket != scm.CheckBucketPass {
			return true
		}
	}
	return false
}

func (h *Host) getChecksForHead(ctx context.Context, pr *scm.PR, headSHA string) ([]scm.Check, error) {
	mr, err := h.viewMR(ctx, pr.Number)
	if err != nil {
		return nil, err
	}
	pr.ObservedHeadSHA = mr.sourceRevision()
	pipeline, err := h.headPipelineBoundTo(ctx, pr, mr, headSHA)
	if err != nil {
		return nil, err
	}
	// Everything the observation carries - the job list, the trigger jobs, the
	// merge request's external status checks and the pipeline's aggregate - is
	// read from this proven pipeline and merge request. The stability re-read
	// runs after every provider read, so a source revision that moved
	// mid-observation rejects the whole set.
	after := func() error {
		afterMR, err := h.viewMR(ctx, pr.Number)
		if err != nil {
			return err
		}
		if revision := afterMR.sourceRevision(); revision != "" {
			pr.ObservedHeadSHA = revision
		}
		if revision := afterMR.sourceRevision(); revision == "" {
			return fmt.Errorf("merge request %s reported no source commit", pr.Number)
		} else if !sameCommitSHA(revision, headSHA) {
			return fmt.Errorf("%w: merge request %s source commit changed during check discovery from %s to %s", scm.ErrHeadChanged, pr.Number, headSHA, revision)
		}
		return nil
	}
	// A nil pipeline means no job has registered for this commit; the merge
	// request's external status checks are still read (and still bound: the
	// source revision above already proved the merge request is at headSHA).
	return h.pipelineChecks(ctx, pr, pipeline, after)
}

func (h *Host) headPipelineBoundTo(ctx context.Context, pr *scm.PR, mr mrPayload, headSHA string) (*gitlabHeadPipeline, error) {
	revision := mr.sourceRevision()
	if revision == "" {
		// A read that cannot name the source revision cannot bind a pipeline to
		// it; failing closed keeps an unidentified pipeline from being graded.
		return nil, fmt.Errorf("merge request %s reported no source commit", pr.Number)
	}
	if !sameCommitSHA(revision, headSHA) {
		return nil, fmt.Errorf("%w: merge request %s source commit is %s, not the commit being delivered %s", scm.ErrHeadChanged, pr.Number, revision, headSHA)
	}
	pipeline, err := mr.headPipeline()
	if err != nil {
		return nil, fmt.Errorf("merge request %s: %w", pr.Number, err)
	}
	if pipeline == nil {
		return nil, nil
	}
	if strings.TrimSpace(pipeline.SHA) == "" {
		return nil, fmt.Errorf("merge request %s head pipeline %d reported no commit", pr.Number, pipeline.ID)
	}
	if sameCommitSHA(pipeline.SHA, headSHA) {
		return pipeline, nil
	}
	// A pipeline that ran at another commit is only evidence for headSHA when it
	// is the merge request's merged-results pipeline and its temporary commit
	// includes headSHA. Anything else - a branch pipeline from before the last
	// push, most of all - cannot be attached to this head.
	if !mergeResultRef(pipeline.Ref, pr.Number) {
		return nil, fmt.Errorf("%w: merge request %s head pipeline %d ran at %s (%s), not at the commit being delivered %s", scm.ErrHeadChanged, pr.Number, pipeline.ID, pipeline.SHA, strings.TrimSpace(pipeline.Ref), headSHA)
	}
	includes, err := h.mergeResultIncludes(ctx, pipeline.SHA, headSHA)
	if err != nil {
		return nil, err
	}
	if !includes {
		return nil, fmt.Errorf("%w: merge request %s merged-results pipeline %d ran at %s, which does not include the commit being delivered %s", scm.ErrHeadChanged, pr.Number, pipeline.ID, pipeline.SHA, headSHA)
	}
	return pipeline, nil
}

func mergeResultRef(ref, number string) bool {
	number = strings.TrimSpace(number)
	if number == "" {
		return false
	}
	trimmed := strings.TrimSpace(ref)
	for _, suffix := range []string{"/merge", "/train"} {
		if trimmed == "refs/merge-requests/"+number+suffix {
			return true
		}
	}
	return false
}

func (h *Host) mergeResultIncludes(ctx context.Context, mergedSHA, revision string) (bool, error) {
	if h.projectPath == "" {
		return false, errors.New("cannot verify a merged-results pipeline includes the delivered commit: GitLab project path unknown")
	}
	mergedSHA = strings.TrimSpace(mergedSHA)
	revision = strings.TrimSpace(revision)
	if !isHexCommitSHA(mergedSHA) {
		return false, fmt.Errorf("read GitLab merge-result commit %s: not a commit id", mergedSHA)
	}
	cmd := h.cmd(ctx, "glab", "api", fmt.Sprintf("projects/%s/repository/commits/%s", encodeProjectPath(h.projectPath), mergedSHA))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("read GitLab merge-result commit %s: %s: %w", mergedSHA, strings.TrimSpace(string(out)), err)
	}
	var commit struct {
		ID        string   `json:"id"`
		ParentIDs []string `json:"parent_ids"`
	}
	trimmed := bytesTrimToJSON(out)
	if len(trimmed) == 0 || json.Unmarshal(trimmed, &commit) != nil {
		return false, fmt.Errorf("read GitLab merge-result commit %s: invalid JSON output: %s", mergedSHA, strings.TrimSpace(string(out)))
	}
	if !sameCommitSHA(commit.ID, mergedSHA) {
		return false, fmt.Errorf("read GitLab merge-result commit %s: response identified commit %s", mergedSHA, strings.TrimSpace(commit.ID))
	}
	for _, parent := range commit.ParentIDs {
		if sameCommitSHA(parent, revision) {
			return true, nil
		}
	}
	return false, nil
}

func sameCommitSHA(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func isHexCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func (h *Host) readPipelineJobs(ctx context.Context, pipelineID int) ([]byte, error) {
	jobsCmd := h.cmd(ctx, "glab", h.pipelineJobsArgs(pipelineID)...)
	var stderr bytes.Buffer
	jobsCmd.Stderr = &stderr
	jobsOut, err := jobsCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("glab pipeline jobs: %s: %w", strings.TrimSpace(string(append(jobsOut, stderr.Bytes()...))), err)
	}
	return jobsOut, nil
}

func (h *Host) glabStdout(ctx context.Context, args []string) (stdout, combined []byte, err error) {
	cmd := h.cmd(ctx, "glab", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err = cmd.Output()
	return stdout, append(append([]byte{}, stdout...), stderr.Bytes()...), err
}

func (h *Host) pipelineTriggerJobs(ctx context.Context, pipelineID int) ([]gitlabTriggerJob, error) {
	out, combined, err := h.glabStdout(ctx, h.pipelineRouteArgs(pipelineID, "trigger_jobs"))
	if err != nil && gitlabRouteUnavailable(combined) {
		out, combined, err = h.glabStdout(ctx, h.pipelineRouteArgs(pipelineID, "bridges"))
	}
	if err != nil {
		return nil, fmt.Errorf("glab pipeline trigger jobs: %s: %w", strings.TrimSpace(string(combined)), err)
	}
	triggerJobs, decodeErr := decodeGitlabTriggerJobs(out)
	if decodeErr != nil {
		return nil, decodeErr
	}
	return triggerJobs, nil
}

func (h *Host) mergeRequestStatusChecks(ctx context.Context, mrIID string) ([]scm.Check, error) {
	out, combined, err := h.glabStdout(ctx, h.mergeRequestStatusChecksArgs(mrIID))
	if err != nil {
		if gitlabExternalStatusChecksUnavailable(combined) {
			return nil, nil
		}
		return nil, fmt.Errorf("glab merge request status checks: %s: %w", strings.TrimSpace(string(combined)), err)
	}
	checks, decodeErr := decodeGitlabExternalStatusChecks(out)
	if decodeErr != nil {
		return nil, decodeErr
	}
	return externalStatusChecksToChecks(checks), nil
}

func (h *Host) FetchFailedCheckLogs(ctx context.Context, pr *scm.PR, branch, headSHA string, failingNames []string) (string, error) {
	targets := make([]scm.CheckTarget, 0, len(failingNames))
	for _, name := range failingNames {
		targets = append(targets, scm.CheckTarget{Name: name})
	}
	logs, err := h.FetchFailedCheckTargetLogs(ctx, pr, branch, headSHA, targets)
	if err != nil {
		return "", err
	}
	return scm.CombineFailedCheckLogs(logs)
}

func (h *Host) FetchFailedCheckTargetLogs(ctx context.Context, pr *scm.PR, _ string, headSHA string, targets []scm.CheckTarget) ([]scm.FailedCheckLog, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	// Get the MR's pipeline jobs and trace the selected failures.
	mr, err := h.viewMR(ctx, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("resolve GitLab merge request for selected logs: %w", err)
	}
	bound := false
	var pipeline *gitlabHeadPipeline
	if headSHA = strings.TrimSpace(headSHA); headSHA != "" {
		bound = true
		pipeline, err = h.headPipelineBoundTo(ctx, pr, mr, headSHA)
		if err != nil {
			return nil, fmt.Errorf("resolve GitLab pipeline for selected logs: %w", err)
		}
	} else {
		pipeline, err = mr.headPipeline()
		if err != nil {
			return nil, fmt.Errorf("resolve GitLab pipeline for selected logs: %w", err)
		}
	}
	if pipeline == nil {
		return nil, errors.New("resolve GitLab pipeline for selected logs: pipeline ID is empty")
	}
	jobsOut, err := h.readPipelineJobs(ctx, pipeline.ID)
	if err != nil {
		return nil, fmt.Errorf("list GitLab jobs for selected logs: %w", err)
	}
	results := make([]scm.FailedCheckLog, 0, len(targets))
	for _, target := range targets {
		result := scm.FailedCheckLog{Target: target}
		jobIDs := findFailedJobTargetIDs(jobsOut, []scm.CheckTarget{target})
		if len(jobIDs) == 0 {
			result.Err = fmt.Errorf("selected GitLab check %q was not found", target.Identity())
			results = append(results, result)
			continue
		}
		var outputs []string
		var errs []error
		for _, jobID := range jobIDs {
			traceCmd := h.cmd(ctx, "glab", "ci", "trace", fmt.Sprintf("%d", jobID))
			traceOut, err := traceCmd.Output()
			if err != nil {
				errs = append(errs, fmt.Errorf("fetch GitLab job %d trace: %w", jobID, err))
				continue
			}
			if log := strings.TrimSpace(string(traceOut)); log != "" {
				outputs = append(outputs, log)
			}
		}
		result.Output = strings.Join(outputs, "\n\n")
		result.Err = errors.Join(errs...)
		results = append(results, result)
	}
	if bound {
		after, err := h.viewMR(ctx, pr.Number)
		if err != nil {
			return nil, fmt.Errorf("re-read GitLab merge request after selected logs: %w", err)
		}
		if revision := after.sourceRevision(); revision == "" {
			return nil, fmt.Errorf("merge request %s reported no source commit", pr.Number)
		} else if !sameCommitSHA(revision, headSHA) {
			return nil, fmt.Errorf("%w: merge request %s source commit changed during log retrieval from %s to %s", scm.ErrHeadChanged, pr.Number, headSHA, revision)
		}
	}
	return results, nil
}

func parseMRPayload(out []byte) (mrPayload, bool) {
	trimmed := bytesTrimToJSON(out)
	if len(trimmed) == 0 {
		return mrPayload{}, false
	}
	var mr mrPayload
	if err := json.Unmarshal(trimmed, &mr); err != nil {
		return mrPayload{}, false
	}
	return mr, true
}

func bytesTrimToJSON(out []byte) []byte {
	// glab may emit a banner line before JSON; skip until '{'.
	idx := -1
	for i, b := range out {
		if b == '{' || b == '[' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	return out[idx:]
}

type gitlabJob struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Stage        string `json:"stage"`
	FinishedAt   string `json:"finished_at"`
	WebURL       string `json:"web_url"`
	AllowFailure bool   `json:"allow_failure"`
}

func (j gitlabJob) bucket() scm.CheckBucket {
	status := strings.ToLower(strings.TrimSpace(j.Status))
	switch {
	case status == "manual":
		if j.AllowFailure {
			return scm.CheckBucketSkip
		}
		return scm.CheckBucketPending
	case j.AllowFailure && status == "failed":
		return scm.CheckBucketSkip
	}
	return gitlabStatusBucket(j.Status)
}

func (j gitlabJob) awaitingManualJob() bool {
	return strings.EqualFold(strings.TrimSpace(j.Status), "manual") && !j.AllowFailure
}

func (j gitlabJob) completedAt() time.Time {
	if strings.TrimSpace(j.FinishedAt) == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339, j.FinishedAt); err == nil {
		return parsed
	}
	return time.Time{}
}

type gitlabTriggerJob struct {
	gitlabJob
	DownstreamPipeline *gitlabDownstreamPipeline `json:"downstream_pipeline"`
}

type gitlabDownstreamPipeline struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

type gitlabExternalStatusCheck struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ExternalURL string `json:"external_url"`
	Status      string `json:"status"`
}

func decodeGitlabJobs(out []byte) ([]gitlabJob, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, errors.New("decode gitlab jobs: response contained no JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var jobs []gitlabJob
	for {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return jobs, nil
		}
		if err != nil {
			// Malformed mid-stream document: stop, but keep what parsed so far
			// and report the error rather than silently swallowing the page.
			return jobs, fmt.Errorf("decode gitlab jobs: %w", err)
		}
		page, err := decodeGitlabJobPage(raw)
		if err != nil {
			return jobs, err
		}
		jobs = append(jobs, page...)
	}
}

func decodeGitlabJobPage(raw json.RawMessage) ([]gitlabJob, error) {
	trimmed := bytes.TrimSpace(raw)
	var entries []json.RawMessage
	switch {
	case len(trimmed) > 0 && trimmed[0] == '[':
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil, fmt.Errorf("decode gitlab jobs: %w", err)
		}
	case len(trimmed) > 0 && trimmed[0] == '{':
		var page struct {
			Jobs json.RawMessage `json:"jobs"`
		}
		if err := json.Unmarshal(trimmed, &page); err != nil {
			return nil, fmt.Errorf("decode gitlab jobs: %w", err)
		}
		pageJobs := bytes.TrimSpace(page.Jobs)
		if len(pageJobs) == 0 {
			return nil, errors.New(`decode gitlab jobs: pipeline object has no "jobs" field`)
		}
		// glab builds this object's job list with client-go's ScanAndCollect,
		// which returns a nil slice for a pipeline with no jobs, and its
		// PrintJSON turns only a top-level nil slice into []. A pipeline with
		// no jobs therefore prints a present "jobs": null here, the same empty
		// listing the REST route reports as []. Only a missing key is
		// unreadable.
		if bytes.Equal(pageJobs, []byte("null")) {
			return nil, nil
		}
		if err := json.Unmarshal(pageJobs, &entries); err != nil {
			return nil, fmt.Errorf(`decode gitlab jobs: "jobs" is not an array: %w`, err)
		}
	default:
		return nil, fmt.Errorf("decode gitlab jobs: page is neither a job array nor a pipeline object: %.80s", trimmed)
	}
	jobs := make([]gitlabJob, 0, len(entries))
	for _, entry := range entries {
		var job gitlabJob
		if err := json.Unmarshal(entry, &job); err != nil {
			return nil, fmt.Errorf("decode gitlab jobs: %w", err)
		}
		if strings.TrimSpace(job.Name) == "" {
			return nil, errors.New("decode gitlab jobs: job entry has no name")
		}
		if strings.TrimSpace(job.Status) == "" {
			return nil, fmt.Errorf("decode gitlab jobs: job %q has no status", job.Name)
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func decodeGitlabPages(what string, out []byte, decodePage func(json.RawMessage) error) error {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return fmt.Errorf("decode gitlab %s: response contained no JSON", what)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	for {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode gitlab %s: %w", what, err)
		}
		if err := decodePage(raw); err != nil {
			return err
		}
	}
}

func decodeGitlabTriggerJobs(out []byte) ([]gitlabTriggerJob, error) {
	var jobs []gitlabTriggerJob
	err := decodeGitlabPages("trigger jobs", out, func(raw json.RawMessage) error {
		var page []gitlabTriggerJob
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
			return fmt.Errorf("decode gitlab trigger jobs: page is not an array")
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("decode gitlab trigger jobs: %w", err)
		}
		for _, job := range page {
			if strings.TrimSpace(job.Name) == "" {
				return errors.New("decode gitlab trigger jobs: entry has no name")
			}
			if strings.TrimSpace(job.Status) == "" {
				return fmt.Errorf("decode gitlab trigger jobs: %q has no status", job.Name)
			}
		}
		jobs = append(jobs, page...)
		return nil
	})
	return jobs, err
}

func decodeGitlabExternalStatusChecks(out []byte) ([]gitlabExternalStatusCheck, error) {
	var checks []gitlabExternalStatusCheck
	err := decodeGitlabPages("external status checks", out, func(raw json.RawMessage) error {
		var page []gitlabExternalStatusCheck
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
			return fmt.Errorf("decode gitlab external status checks: page is not an array")
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("decode gitlab external status checks: %w", err)
		}
		for _, check := range page {
			if strings.TrimSpace(check.Name) == "" {
				return errors.New("decode gitlab external status checks: entry has no name")
			}
			if strings.TrimSpace(check.Status) == "" {
				return fmt.Errorf("decode gitlab external status checks: %q has no status", check.Name)
			}
		}
		checks = append(checks, page...)
		return nil
	})
	return checks, err
}

func parseGitlabJobs(out []byte) ([]scm.Check, error) {
	jobs, err := decodeGitlabJobs(out)
	if len(jobs) == 0 {
		return nil, err
	}
	// Surface any decode error even when some jobs parsed. A corrupt later page
	// of paginated `glab api` output must not let a partial slice look
	// authoritative: a failed job on the dropped page would otherwise be hidden
	// and the CI verdict would read green.
	return jobsToChecks(jobs), err
}

func jobsToChecks(jobs []gitlabJob) []scm.Check {
	checks := make([]scm.Check, 0, len(jobs))
	for _, job := range jobs {
		checks = append(checks, scm.Check{
			Name:              job.Name,
			ProviderID:        gitlabJobProviderID("gitlab-job", job.ID),
			Bucket:            job.bucket(),
			Kind:              scm.CheckKindRun,
			State:             gitlabCheckState(job.Status),
			CompletedAt:       job.completedAt(),
			Link:              strings.TrimSpace(job.WebURL),
			AwaitingManualJob: job.awaitingManualJob(),
		})
	}
	return checks
}

func triggerJobsToChecks(jobs []gitlabTriggerJob) []scm.Check {
	checks := make([]scm.Check, 0, len(jobs))
	for _, job := range jobs {
		link := strings.TrimSpace(job.WebURL)
		if job.DownstreamPipeline != nil {
			if url := strings.TrimSpace(job.DownstreamPipeline.WebURL); url != "" {
				link = url
			}
		}
		checks = append(checks, scm.Check{
			Name:              job.Name,
			ProviderID:        gitlabJobProviderID("gitlab-trigger-job", job.ID),
			Bucket:            job.bucket(),
			Kind:              scm.CheckKindRun,
			State:             gitlabCheckState(job.Status),
			CompletedAt:       job.completedAt(),
			Link:              link,
			AwaitingManualJob: job.awaitingManualJob(),
		})
	}
	return checks
}

func externalStatusChecksToChecks(statusChecks []gitlabExternalStatusCheck) []scm.Check {
	checks := make([]scm.Check, 0, len(statusChecks))
	for _, status := range statusChecks {
		id := ""
		if status.ID != 0 {
			id = "gitlab-status-check:" + strconv.Itoa(status.ID)
		}
		checks = append(checks, scm.Check{
			Name:       status.Name,
			ProviderID: id,
			Bucket:     gitlabExternalStatusCheckBucket(status.Status),
			Kind:       scm.CheckKindStatus,
			State:      gitlabCheckState(status.Status),
			Link:       strings.TrimSpace(status.ExternalURL),
		})
	}
	return checks
}

func pipelineAggregateCheck(pipeline gitlabHeadPipeline, checks []scm.Check) (scm.Check, bool) {
	bucket := gitlabStatusBucket(pipeline.Status)
	if bucket == "" {
		// An unrecognized or incomplete pipeline state must not certify the
		// commit as green; keep monitoring until GitLab reports a state that can
		// be classified.
		bucket = scm.CheckBucketPending
	}
	switch bucket {
	case scm.CheckBucketFail, scm.CheckBucketCancel, scm.CheckBucketPending:
	default:
		return scm.Check{}, false
	}
	for _, check := range checks {
		if check.Bucket == bucket {
			return scm.Check{}, false
		}
	}
	return scm.Check{
		Name:              fmt.Sprintf("Pipeline #%d", pipeline.ID),
		ProviderID:        "gitlab-pipeline:" + strconv.Itoa(pipeline.ID),
		Bucket:            bucket,
		Kind:              scm.CheckKindRun,
		State:             gitlabCheckState(pipeline.Status),
		CompletedAt:       pipeline.completedAt(),
		Link:              strings.TrimSpace(pipeline.WebURL),
		AwaitingManualJob: pipeline.awaitingManualJob(),
	}, true
}

func gitlabJobProviderID(prefix string, id int) string {
	if id == 0 {
		return ""
	}
	return prefix + ":" + strconv.Itoa(id)
}

func gitlabCheckState(state string) string {
	return strings.ToUpper(strings.TrimSpace(state))
}

func findFailedJobTargetIDs(out []byte, checkTargets []scm.CheckTarget) []int {
	names := map[string]struct{}{}
	ids := map[string]struct{}{}
	for _, target := range checkTargets {
		if id := strings.TrimSpace(target.ProviderID); id != "" {
			ids[id] = struct{}{}
		} else if name := strings.TrimSpace(target.Name); name != "" {
			names[name] = struct{}{}
		}
	}
	// Best effort: scan whatever jobs parsed; a corrupt later page does not
	// prevent locating a failed job that already decoded.
	jobs, _ := decodeGitlabJobs(out)
	var matched []int
	for _, job := range jobs {
		if !strings.EqualFold(job.Status, "failed") || job.AllowFailure {
			continue
		}
		_, nameMatch := names[job.Name]
		_, idMatch := ids[fmt.Sprintf("gitlab-job:%d", job.ID)]
		if nameMatch || idMatch || len(names)+len(ids) == 0 {
			matched = append(matched, job.ID)
		}
	}
	return matched
}

func gitlabStatusBucket(state string) scm.CheckBucket {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "success":
		return scm.CheckBucketPass
	case "failed":
		return scm.CheckBucketFail
	case "canceled", "cancelled":
		return scm.CheckBucketCancel
	case "skipped":
		return scm.CheckBucketSkip
	case "manual", "pending", "running", "created", "waiting_for_resource", "preparing", "scheduled":
		return scm.CheckBucketPending
	default:
		return ""
	}
}

func gitlabExternalStatusCheckBucket(state string) scm.CheckBucket {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "passed":
		return scm.CheckBucketPass
	case "failed":
		return scm.CheckBucketFail
	default:
		return scm.CheckBucketPending
	}
}

func gitlabRouteUnavailable(out []byte) bool {
	text := string(out)
	return strings.Contains(text, `"error":"404 Not Found"`) || strings.Contains(text, "glab: HTTP 404")
}

func gitlabExternalStatusChecksUnavailable(out []byte) bool {
	text := string(out)
	if strings.Contains(text, `"error":"404 Not Found"`) {
		return true
	}
	for _, refusal := range []string{"HTTP 401", `"message":"401 Unauthorized"`} {
		if strings.Contains(text, refusal) {
			return true
		}
	}
	return false
}

func normalizePRState(raw string) scm.PRState {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "opened", "open":
		return scm.PRStateOpen
	case "merged":
		return scm.PRStateMerged
	case "closed", "locked":
		return scm.PRStateClosed
	default:
		return scm.PRState(strings.ToUpper(raw))
	}
}

func extractMRURL(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	trimmed := bytesTrimToJSON(raw)
	if len(trimmed) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"web_url", "url", "webUrl"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
