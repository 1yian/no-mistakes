package scm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

func ExtractHost(remote string) string {
	s := strings.TrimSpace(remote)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		// URL form: scheme://[user@]host[:port]/path. Split off the path at the
		// first '/' before scanning for userinfo, so a '@' inside the path
		// (e.g. .../group@prod/repo.git) cannot be mistaken for a "user@" prefix.
		s = s[i+3:]
		if slash := strings.Index(s, "/"); slash >= 0 {
			s = s[:slash]
		}
		if at := strings.LastIndex(s, "@"); at >= 0 {
			s = s[at+1:]
		}
		return strings.ToLower(stripPort(s))
	}
	// No scheme. scp-like syntax is [user@]host:path; the first ':' separates
	// the host from the path. Split off the path first, then strip any userinfo
	// prefix from the host segment only, so a '@' in the path (e.g.
	// git@host:group@prod/repo.git) cannot collapse host extraction.
	if c := strings.Index(s, ":"); c >= 0 {
		s = s[:c]
	} else if slash := strings.Index(s, "/"); slash >= 0 {
		s = s[:slash]
	}
	if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	return strings.ToLower(s)
}

func stripPort(host string) string {
	if strings.HasPrefix(host, "[") {
		// IPv6 literal: [::1]:22 -> [::1]
		if end := strings.Index(host, "]"); end >= 0 {
			return host[:end+1]
		}
		return host
	}
	if c := strings.LastIndex(host, ":"); c >= 0 {
		port := host[c+1:]
		if port != "" && strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			return host[:c]
		}
	}
	return host
}

func ExtractPRNumber(prURL string) (string, error) {
	trimmed := strings.TrimRight(prURL, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 {
		return "", fmt.Errorf("invalid PR URL: %s", prURL)
	}
	num := parts[len(parts)-1]
	if num == "" {
		return "", fmt.Errorf("invalid PR URL: %s", prURL)
	}
	if _, err := strconv.Atoi(num); err != nil {
		return "", fmt.Errorf("invalid PR number %q in URL: %s", num, prURL)
	}
	return num, nil
}

type PR struct {
	Number          string
	URL             string
	HeadSHA         string
	ObservedHeadSHA string
	BaseBranch      string
}

type PRContent struct {
	Title string
	Body  string
}

type PRState string

const (
	PRStateOpen   PRState = "OPEN"
	PRStateMerged PRState = "MERGED"
	PRStateClosed PRState = "CLOSED"
)

type MergeableState string

const (
	MergeableOK       MergeableState = "MERGEABLE"
	MergeableConflict MergeableState = "CONFLICTING"
	MergeablePending  MergeableState = "PENDING"
	MergeableUnknown  MergeableState = "UNKNOWN"
	MergeableBehind   MergeableState = "BEHIND"
)

func (s MergeableState) Conflict() bool { return s == MergeableConflict }

func (s MergeableState) Behind() bool { return s == MergeableBehind }

func (s MergeableState) Resolved() bool {
	return s == MergeableOK || s == MergeableBehind || s == MergeableConflict
}

type CheckBucket string

const (
	CheckBucketPass    CheckBucket = "pass"
	CheckBucketFail    CheckBucket = "fail"
	CheckBucketPending CheckBucket = "pending"
	CheckBucketCancel  CheckBucket = "cancel"
	CheckBucketSkip    CheckBucket = "skipping"
)

type CheckKind string

const (
	CheckKindRun    CheckKind = "run"
	CheckKindStatus CheckKind = "status"
)

type Check struct {
	ExternalReview string

	Name              string
	ProviderID        string `json:"provider_id,omitempty"`
	Bucket            CheckBucket
	Kind              CheckKind
	State             string
	CompletedAt       time.Time // zero when unknown; used to detect CI re-runs between polls
	ExecutionID       string    // provider execution discriminator when completion time is unavailable
	StartedAt         time.Time
	WorkflowID        int64
	Link              string
	PreRunFailure     bool
	AwaitingApproval  bool
	AwaitingManualJob bool
	App               string
	AppID             int64
	AppHost           string
	HeadSHA           string
}

func (c Check) Failing() bool { return c.Bucket == CheckBucketFail }

type ReviewBot struct {
	AppID          int64
	AuthorID       int64
	RequiredReview string

	AppSlug      string
	Logins       []string
	GitLabLogins []string
}

const CodeRabbitAppID int64 = 347564
const CodeRabbitAuthorID int64 = 136622811

var ReviewBots = []ReviewBot{
	{AppSlug: "greptile-apps", Logins: []string{"greptile-apps[bot]", "greptile-apps"}, GitLabLogins: []string{"greptileai"}},
	{AppSlug: "coderabbitai", RequiredReview: "coderabbit", AppID: CodeRabbitAppID, AuthorID: CodeRabbitAuthorID, Logins: []string{"coderabbitai", "coderabbitai[bot]"}},
}

func ReviewBotsFor(requiredReviews []string) []ReviewBot {
	var bots []ReviewBot
	for _, bot := range ReviewBots {
		if bot.RequiredReview == "" || slices.Contains(requiredReviews, bot.RequiredReview) {
			bots = append(bots, bot)
		}
	}
	return bots
}

func ReviewBotForCheck(bots []ReviewBot, check Check) (ReviewBot, bool) {
	for _, bot := range bots {
		if bot.AppID != 0 {
			if check.AppHost == "github.com" && check.AppID == bot.AppID {
				return bot, true
			}
			// A required-review adapter marks its synthesized check only after
			// validating that forge's trusted app or account identity.
			if check.ExternalReview == bot.RequiredReview && check.AppHost != "" && (check.AppID > 0 || check.App == bot.AppSlug && check.HeadSHA != "") {
				return bot, true
			}
		} else if check.App != "" && strings.EqualFold(strings.TrimSpace(check.App), bot.AppSlug) {
			return bot, true
		}
	}
	return ReviewBot{}, false
}

func ReviewBotForLogin(provider Provider, login string) (ReviewBot, bool) {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return ReviewBot{}, false
	}
	for _, bot := range ReviewBots {
		var logins []string
		switch provider {
		case ProviderGitHub:
			logins = bot.Logins
		case ProviderGitLab:
			logins = bot.GitLabLogins
		}
		for _, known := range logins {
			if strings.EqualFold(known, login) {
				return bot, true
			}
		}
	}
	return ReviewBot{}, false
}

func IsReviewBotLogin(provider Provider, login string) bool {
	_, ok := ReviewBotForLogin(provider, login)
	return ok
}

func (c Check) Pending() bool { return c.Bucket == CheckBucketPending }

func (c Check) HeldForHuman() bool { return c.AwaitingApproval || c.AwaitingManualJob }

type Capabilities struct {
	MergeableState    bool
	FailedCheckLogs   bool
	MergedProof       bool
	ReviewComments    bool
	ClosingReferences bool
}

var (
	ErrUnsupported = errors.New("operation not supported by this provider")
	ErrHeadChanged = errors.New("pull request head changed")
)

type CheckTarget struct {
	Name       string `json:"name"`
	ProviderID string `json:"provider_id,omitempty"`
}

func (t CheckTarget) Identity() string {
	if t.ProviderID != "" {
		return t.ProviderID
	}
	return t.Name
}

type FailedCheckLog struct {
	Target CheckTarget
	Output string
	Err    error
}

type TargetedFailedCheckLogsHost interface {
	FetchFailedCheckTargetLogs(ctx context.Context, pr *PR, branch, headSHA string, targets []CheckTarget) ([]FailedCheckLog, error)
}

func CombineFailedCheckLogs(logs []FailedCheckLog) (string, error) {
	var outputs []string
	var errs []error
	for _, log := range logs {
		if output := strings.TrimSpace(log.Output); output != "" {
			outputs = append(outputs, output)
		}
		if log.Err != nil {
			errs = append(errs, log.Err)
		}
	}
	return strings.Join(outputs, "\n\n"), errors.Join(errs...)
}

type ReviewComment struct {
	AuthorID       int64
	VerifiedAuthor bool

	ID        string
	Author    string
	Path      string
	Line      int
	Body      string
	CreatedAt time.Time
	URL       string
}

type ReviewCommentsHost interface {
	GetReviewComments(ctx context.Context, pr *PR) ([]ReviewComment, error)
}

type PRContentReader interface {
	GetPRContent(ctx context.Context, pr *PR) (PRContent, error)
}

type MergedProof struct {
	Merged         bool
	Number         string
	URL            string
	HeadSHA        string
	MergeCommitSHA string
	MergedAt       time.Time
	MergedBy       string
}

type MergedProofHost interface {
	GetMergedProof(ctx context.Context, pr *PR, expectedHead string) (MergedProof, error)
}

type Host interface {
	Provider() Provider
	Capabilities() Capabilities

	Available(ctx context.Context) error

	FindPR(ctx context.Context, branch, base string) (*PR, error)
	CreatePR(ctx context.Context, branch, base string, content PRContent) (*PR, error)
	UpdatePR(ctx context.Context, pr *PR, content PRContent) (*PR, error)

	GetPRState(ctx context.Context, pr *PR) (PRState, error)
	GetChecks(ctx context.Context, pr *PR) ([]Check, error)

	GetMergeableState(ctx context.Context, pr *PR) (MergeableState, error)

	FetchFailedCheckLogs(ctx context.Context, pr *PR, branch, headSHA string, failingNames []string) (string, error)
}

type PRBaseBranchReader interface {
	GetPRBaseBranch(ctx context.Context, pr *PR) (string, error)
}

type PRHeadVerifier interface {
	VerifyPRHead(ctx context.Context, pr *PR, branch, pushedHead string) error
}

type PRHeadChangedError struct {
	HeadSHA string
}

func (e *PRHeadChangedError) Error() string {
	return fmt.Sprintf("%s: live head %q", ErrHeadChanged, e.HeadSHA)
}

func (e *PRHeadChangedError) Unwrap() error { return ErrHeadChanged }

type PRBaseRetargeter interface {
	SetPRBaseBranch(ctx context.Context, pr *PR, baseBranch string) error
}

type PreRunFailureDetector interface {
	PreRunFailures(ctx context.Context, checks []Check) ([]bool, error)
}

type CheckRerunner interface {
	RerunCheck(ctx context.Context, pr *PR, check Check) error
}

func RepoPath(remoteURL string) string {
	raw := strings.TrimSpace(remoteURL)
	if raw == "" {
		return ""
	}

	host := ExtractHost(raw)
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return ""
		}
		raw = u.Path
	case strings.Contains(raw, ":"):
		colon := strings.IndexByte(raw, ':')
		if colon <= 0 || strings.Contains(raw[:colon], "/") {
			return ""
		}
		raw = raw[colon+1:]
	}

	parts := strings.Split(strings.Trim(raw, "/"), "/")
	clean := parts[:0]
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	parts = clean
	if len(parts) == 0 {
		return ""
	}

	isAzureDevOps := host == "dev.azure.com" || host == "ssh.dev.azure.com" || strings.HasSuffix(host, ".visualstudio.com")
	if isAzureDevOps {
		for i, part := range parts {
			if strings.EqualFold(part, "_git") && i > 0 && i+1 < len(parts) {
				return parts[i-1] + "/" + strings.TrimSuffix(parts[i+1], ".git")
			}
		}
	}
	if (host == "ssh.dev.azure.com" || host == "vs-ssh.visualstudio.com") && len(parts) >= 4 && strings.EqualFold(parts[0], "v3") {
		return parts[len(parts)-2] + "/" + strings.TrimSuffix(parts[len(parts)-1], ".git")
	}
	parts[len(parts)-1] = strings.TrimSuffix(parts[len(parts)-1], ".git")
	return strings.Join(parts, "/")
}
