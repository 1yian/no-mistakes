package db

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/buildinfo"
	"github.com/kunchenguid/no-mistakes/internal/closingissues"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/kunchenguid/no-mistakes/internal/verificationplan"
)

type Run struct {
	ID                         string
	RepoID                     string
	Branch                     string
	HeadSHA                    string
	BaseSHA                    string
	WorktreeDir                *string
	EvidenceDir                *string
	SubmittedHeadSHA           *string
	NoMistakesVersion          *string
	NoMistakesBuildSHA         *string
	ReviewApprovedHeadSHA      *string
	Status                     types.RunStatus
	PRURL                      *string
	PRState                    *string
	PRStateObservedAt          *int64
	CIReadyAt                  *int64
	CIReadyNoCI                bool
	CIReleasedAt               *int64
	LastPushedSHA              *string
	PushTargetKind             *string
	PushTargetFingerprint      *string
	PushRef                    *string
	LastPushedAt               *int64
	PushGeneration             *int64
	PushActive                 bool
	TerminalHeadVerifiedAt     *int64
	CustodyReturnedAt          *int64
	Error                      *string
	AwaitingAgentSince         *int64
	ParkedMS                   int64
	Intent                     *string
	IntentSource               *string
	IntentCheckJSON            *string `json:"-"`
	IntentSessionID            *string
	IntentScore                *float64
	LaunchNonce                *string
	LaunchValidationGeneration *string
	LaunchIntentDigest         *string
	LaunchReceiptClaimedAt     *int64
	PRBaseBranch               *string
	OmitIntent                 bool
	PiProfile                  *agentcfg.PiProfile
	ReviewerProfile            *agentcfg.ReviewerProfile
	TranscriptHomes            *runenv.TranscriptHomes `json:"-"`
	QuotaRouting               string
	VerificationPlan           *verificationplan.Snapshot
	ClosingIssueRefs           []string
	ClosingIssueRefsLockedAt   *int64
	PRTitle                    *string
	PRBody                     *string
	CreatedAt                  int64
	UpdatedAt                  int64
}

const runColumns = `id, repo_id, branch, head_sha, base_sha, worktree_dir, submitted_head_sha, no_mistakes_version, no_mistakes_build_sha, review_approved_head_sha, status, pr_url, pr_state, pr_state_observed_at, ci_ready_at, ci_ready_no_ci, ci_released_at, last_pushed_sha, push_target_kind, push_target_fingerprint, push_ref, last_pushed_at, push_generation, push_active, terminal_head_verified_at, custody_returned_at, error, awaiting_agent_since, parked_ms, intent, intent_source, intent_session_id, intent_score, intent_check_json, launch_nonce, launch_validation_generation, launch_intent_digest, launch_receipt_claimed_at, pr_base_branch, omit_intent, pi_profile, quota_routing, verification_plan, closing_issue_refs, closing_issue_refs_locked_at, pr_title, pr_body, evidence_dir, transcript_homes, created_at, updated_at, reviewer_profile`

func (d *DB) runColumns() (string, error) {
	return d.readProjection("runs", runColumns, runScanTargets(&Run{}, &sql.NullString{})...)
}

func runScanTargets(r *Run, storedClosingIssues *sql.NullString) []any {
	return []any{
		&r.ID, &r.RepoID, &r.Branch, &r.HeadSHA, &r.BaseSHA, &r.WorktreeDir, &r.SubmittedHeadSHA, &r.NoMistakesVersion, &r.NoMistakesBuildSHA, &r.ReviewApprovedHeadSHA, &r.Status,
		&r.PRURL, &r.PRState, &r.PRStateObservedAt, &r.CIReadyAt, &r.CIReadyNoCI, &r.CIReleasedAt,
		&r.LastPushedSHA, &r.PushTargetKind, &r.PushTargetFingerprint, &r.PushRef,
		&r.LastPushedAt, &r.PushGeneration, &r.PushActive, &r.TerminalHeadVerifiedAt,
		&r.CustodyReturnedAt, &r.Error, &r.AwaitingAgentSince, &r.ParkedMS,
		&r.Intent, &r.IntentSource, &r.IntentSessionID, &r.IntentScore, &r.IntentCheckJSON,
		&r.LaunchNonce, &r.LaunchValidationGeneration, &r.LaunchIntentDigest, &r.LaunchReceiptClaimedAt,
		&r.PRBaseBranch, &r.OmitIntent, &r.PiProfile, &r.QuotaRouting, &r.VerificationPlan,
		storedClosingIssues, &r.ClosingIssueRefsLockedAt,
		&r.PRTitle, &r.PRBody, &r.EvidenceDir, &r.TranscriptHomes,
		&r.CreatedAt, &r.UpdatedAt,
		&r.ReviewerProfile,
	}
}

func scanRun(row interface {
	Scan(...any) error
}, r *Run) error {
	var storedClosingIssues sql.NullString
	err := row.Scan(runScanTargets(r, &storedClosingIssues)...)
	if err != nil {
		return err
	}
	r.ClosingIssueRefs, err = closingissues.Decode(storedClosingIssues.String)
	if err != nil {
		return fmt.Errorf("decode run closing issue references: %w", err)
	}
	return nil
}

func (r *Run) WorktreePath() string {
	if r == nil || r.WorktreeDir == nil {
		return ""
	}
	return *r.WorktreeDir
}

func (r *Run) EvidencePath() string {
	if r == nil || r.EvidenceDir == nil {
		return ""
	}
	return *r.EvidenceDir
}

func (d *DB) InsertRun(repoID, branch, headSHA, baseSHA string) (*Run, error) {
	return d.InsertRunWithIntent(repoID, branch, headSHA, baseSHA, nil, "")
}

func (d *DB) InsertRunWithIntent(repoID, branch, headSHA, baseSHA string, intent *RunIntent, prBaseBranch string) (*Run, error) {
	return d.InsertRunWithIntentAndLaunchNonce(repoID, branch, headSHA, baseSHA, intent, "", "", "", prBaseBranch, false, nil)
}

func (d *DB) InsertRunWithIntentAndLaunchNonce(repoID, branch, headSHA, baseSHA string, intent *RunIntent, launchNonce, validationGeneration, intentDigest, prBaseBranch string, omitIntent bool, plan *verificationplan.Snapshot, profiles ...agentcfg.RunSelection) (*Run, error) {
	return d.InsertRunWithPRText(repoID, branch, headSHA, baseSHA, intent, launchNonce, validationGeneration, intentDigest, prBaseBranch, omitIntent, plan, RunPRText{}, profiles...)
}

type RunPRText struct {
	Title string
	Body  string
}

func PRTextDigest(title, body string) string {
	if title == "" && body == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s\n%d:%s", len(title), title, len(body), body)))
	return fmt.Sprintf("%x", sum)
}

func (r *Run) PRTextDigest() string {
	if r == nil {
		return ""
	}
	var title, body string
	if r.PRTitle != nil {
		title = *r.PRTitle
	}
	if r.PRBody != nil {
		body = *r.PRBody
	}
	return PRTextDigest(title, body)
}

func (d *DB) InsertRunWithPRText(repoID, branch, headSHA, baseSHA string, intent *RunIntent, launchNonce, validationGeneration, intentDigest, prBaseBranch string, omitIntent bool, plan *verificationplan.Snapshot, prText RunPRText, profiles ...agentcfg.RunSelection) (*Run, error) {
	return d.InsertRunWithTranscriptHomes(repoID, branch, headSHA, baseSHA, intent, launchNonce, validationGeneration, intentDigest, prBaseBranch, omitIntent, plan, prText, nil, profiles...)
}

func (d *DB) InsertRunWithTranscriptHomes(repoID, branch, headSHA, baseSHA string, intent *RunIntent, launchNonce, validationGeneration, intentDigest, prBaseBranch string, omitIntent bool, plan *verificationplan.Snapshot, prText RunPRText, homes *runenv.TranscriptHomes, profiles ...agentcfg.RunSelection) (*Run, error) {
	pin := agentcfg.OptionalPiProfile(profiles)
	if err := pin.Validate(); err != nil {
		return nil, err
	}
	reviewer := agentcfg.OptionalReviewerProfile(profiles)
	if err := reviewer.Validate(); err != nil {
		return nil, err
	}
	ts := now()
	version := buildinfo.CurrentVersion()
	buildSHA := buildinfo.Commit
	r := &Run{
		ID:                 newID(),
		RepoID:             repoID,
		Branch:             branch,
		HeadSHA:            headSHA,
		BaseSHA:            baseSHA,
		SubmittedHeadSHA:   &headSHA,
		NoMistakesVersion:  &version,
		NoMistakesBuildSHA: &buildSHA,
		PiProfile:          pin,
		ReviewerProfile:    reviewer,
		TranscriptHomes:    homes,
		Status:             types.RunPending,
		CreatedAt:          ts,
		UpdatedAt:          ts,
	}
	if plan != nil {
		r.ID = plan.ID
		r.VerificationPlan = plan
	}
	if launchNonce != "" {
		r.LaunchNonce = &launchNonce
		r.LaunchValidationGeneration = &validationGeneration
		r.LaunchIntentDigest = &intentDigest
	}
	if intent != nil {
		r.Intent = &intent.Summary
		r.IntentSource = &intent.Source
		r.IntentSessionID = &intent.SessionID
		r.IntentScore = &intent.Score
	}
	prBaseBranch = strings.TrimSpace(prBaseBranch)
	if prBaseBranch != "" {
		r.PRBaseBranch = &prBaseBranch
	}
	r.OmitIntent = omitIntent
	if prText.Title != "" {
		r.PRTitle = &prText.Title
	}
	if prText.Body != "" {
		r.PRBody = &prText.Body
	}
	_, err := d.sql.Exec(
		`INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, submitted_head_sha, no_mistakes_version, no_mistakes_build_sha, status, pr_state, intent, intent_source, intent_session_id, intent_score, launch_nonce, launch_validation_generation, launch_intent_digest, pr_base_branch, omit_intent, pi_profile, verification_plan, pr_title, pr_body, transcript_homes, created_at, updated_at, reviewer_profile) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'none', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.RepoID, r.Branch, r.HeadSHA, r.BaseSHA, headSHA, r.NoMistakesVersion, r.NoMistakesBuildSHA, r.Status, r.Intent, r.IntentSource, r.IntentSessionID, r.IntentScore, r.LaunchNonce, r.LaunchValidationGeneration, r.LaunchIntentDigest, r.PRBaseBranch, r.OmitIntent, r.PiProfile, r.VerificationPlan, r.PRTitle, r.PRBody, r.TranscriptHomes, r.CreatedAt, r.UpdatedAt, r.ReviewerProfile,
	)
	if err != nil {
		return nil, fmt.Errorf("insert run: %w", err)
	}
	return r, nil
}

type RunWorktree struct {
	RunID  string
	RepoID string
	Dir    string
}

func (d *DB) ActiveRunWorktrees() ([]RunWorktree, error) {
	placement := "''"
	if d.hasColumn("runs", "worktree_dir") {
		placement = "COALESCE(worktree_dir, '')"
	}
	rows, err := d.sql.Query(
		`SELECT id, repo_id, `+placement+` FROM runs WHERE status IN (?, ?) ORDER BY created_at DESC, id DESC`,
		types.RunPending, types.RunRunning,
	)
	if err != nil {
		return nil, fmt.Errorf("get active run worktrees: %w", err)
	}
	defer rows.Close()
	var out []RunWorktree
	for rows.Next() {
		var wt RunWorktree
		if err := rows.Scan(&wt.RunID, &wt.RepoID, &wt.Dir); err != nil {
			return nil, fmt.Errorf("scan active run worktree: %w", err)
		}
		out = append(out, wt)
	}
	return out, rows.Err()
}

func (d *DB) hasColumn(table, column string) bool {
	rows, err := d.sql.Query(`SELECT 1 FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		return false
	}
	defer rows.Close()
	return rows.Next()
}

func (d *DB) RunWorktreesOutside(prefix string) ([]RunWorktree, error) {
	return d.runWorktreesOutside(prefix, "")
}

func (d *DB) ActiveRunWorktreesOutside(prefix string) ([]RunWorktree, error) {
	return d.runWorktreesOutside(prefix, ` AND status IN ('pending', 'running')`)
}

func (d *DB) runWorktreesOutside(prefix, statusClause string) ([]RunWorktree, error) {
	bounded := strings.TrimSuffix(filepath.Clean(prefix), string(filepath.Separator)) + string(filepath.Separator)
	rows, err := d.sql.Query(
		`SELECT id, repo_id, worktree_dir FROM runs
		 WHERE worktree_dir IS NOT NULL AND worktree_dir <> '' AND substr(worktree_dir, 1, ?) <> ?`+
			statusClause+
			` ORDER BY created_at DESC, id DESC`,
		len(bounded), bounded,
	)
	if err != nil {
		return nil, fmt.Errorf("get run worktrees outside %s: %w", prefix, err)
	}
	defer rows.Close()
	var out []RunWorktree
	for rows.Next() {
		var wt RunWorktree
		if err := rows.Scan(&wt.RunID, &wt.RepoID, &wt.Dir); err != nil {
			return nil, fmt.Errorf("scan run worktree: %w", err)
		}
		out = append(out, wt)
	}
	return out, rows.Err()
}

func (d *DB) GetRun(id string) (*Run, error) {
	columns, err := d.runColumns()
	if err != nil {
		return nil, err
	}
	r := &Run{}
	err = scanRun(d.sql.QueryRow(`SELECT `+columns+` FROM runs WHERE id = ?`, id), r)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	return r, nil
}

func (d *DB) GetRunByLaunchNonce(repoID, branch, launchNonce string) (*Run, error) {
	columns, err := d.runColumns()
	if err != nil {
		return nil, err
	}
	r := &Run{}
	err = scanRun(d.sql.QueryRow(
		`SELECT `+columns+` FROM runs WHERE repo_id = ? AND branch = ? AND launch_nonce = ?`,
		repoID, branch, launchNonce,
	), r)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get run by launch nonce: %w", err)
	}
	return r, nil
}

func (d *DB) ClaimLaunchReceipt(repoID, branch, launchNonce, submittedHeadSHA, validationGeneration, intentDigest, prBaseBranch string, omitIntent bool, profiles ...agentcfg.RunSelection) (*Run, bool, error) {
	return d.ClaimLaunchReceiptWithPRText(repoID, branch, launchNonce, submittedHeadSHA, validationGeneration, intentDigest, prBaseBranch, omitIntent, "", profiles...)
}

func (d *DB) ClaimLaunchReceiptWithPRText(repoID, branch, launchNonce, submittedHeadSHA, validationGeneration, intentDigest, prBaseBranch string, omitIntent bool, prTextDigest string, profiles ...agentcfg.RunSelection) (*Run, bool, error) {
	request := agentcfg.OptionalPiProfile(profiles)
	if err := request.ValidateRequest(); err != nil {
		return nil, false, err
	}
	model, effort := "", ""
	if request != nil {
		model, effort = request.Model, string(request.Effort)
	}
	reviewer := agentcfg.OptionalReviewerProfile(profiles)
	if err := reviewer.ValidateRequest(); err != nil {
		return nil, false, err
	}
	rAgent, rModel, rEffort := "", "", ""
	if reviewer != nil {
		rAgent, rModel, rEffort = string(reviewer.Agent), reviewer.Model, string(reviewer.Effort)
	}
	prBaseBranch = strings.TrimSpace(prBaseBranch)
	existing, err := d.GetRunByLaunchNonce(repoID, branch, launchNonce)
	if err != nil || existing == nil {
		return nil, false, err
	}
	if existing.PRTextDigest() != prTextDigest {
		return existing, false, nil
	}
	columns, err := d.runColumns()
	if err != nil {
		return nil, false, err
	}
	r := &Run{}
	err = scanRun(d.sql.QueryRow(
		`UPDATE runs SET launch_receipt_claimed_at = ?
		 WHERE id = ? AND repo_id = ? AND branch = ? AND launch_nonce = ?
		   AND submitted_head_sha = ? AND launch_validation_generation = ? AND launch_intent_digest = ?
		   AND (? = '' OR pr_base_branch = ?)
		   AND (? = 0 OR omit_intent = ?)
		   AND (? = '' OR json_extract(pi_profile, '$.model') = ?)
		   AND (? = '' OR json_extract(pi_profile, '$.effort') = ?)
		   AND (? = '' OR json_extract(reviewer_profile, '$.agent') = ?)
		   AND (? = '' OR json_extract(reviewer_profile, '$.model') = ?)
		   AND (? = '' OR json_extract(reviewer_profile, '$.effort') = ?)
		   AND launch_receipt_claimed_at IS NULL
		 RETURNING `+columns,
		now(), existing.ID, repoID, branch, launchNonce, submittedHeadSHA, validationGeneration, intentDigest, prBaseBranch, prBaseBranch, omitIntent, omitIntent, model, model, effort, effort, rAgent, rAgent, rModel, rModel, rEffort, rEffort,
	), r)
	if err == nil {
		return r, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("claim launch receipt: %w", err)
	}
	r, err = d.GetRunByLaunchNonce(repoID, branch, launchNonce)
	if err != nil {
		return nil, false, err
	}
	return r, false, nil
}

func (d *DB) GetRunsByRepo(repoID string) ([]*Run, error) {
	columns, err := d.runColumns()
	if err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(`SELECT `+columns+` FROM runs WHERE repo_id = ? ORDER BY created_at DESC, id DESC`, repoID)
	if err != nil {
		return nil, fmt.Errorf("get runs by repo: %w", err)
	}
	defer rows.Close()
	var runs []*Run
	for rows.Next() {
		r := &Run{}
		if err := scanRun(rows, r); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (d *DB) GetRunsByRepoHead(repoID, branch, headSHA string) ([]*Run, error) {
	columns, err := d.runColumns()
	if err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(
		`SELECT `+columns+` FROM runs WHERE repo_id = ? AND branch = ? AND head_sha = ? ORDER BY created_at DESC, id DESC`,
		repoID, branch, headSHA,
	)
	if err != nil {
		return nil, fmt.Errorf("get runs by repo head: %w", err)
	}
	defer rows.Close()
	var runs []*Run
	for rows.Next() {
		r := &Run{}
		if err := scanRun(rows, r); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (d *DB) GetActiveRun(repoID, branch string) (*Run, error) {
	columns, err := d.runColumns()
	if err != nil {
		return nil, err
	}
	r := &Run{}
	if branch == "" {
		err = scanRun(d.sql.QueryRow(
			`SELECT `+columns+` FROM runs WHERE repo_id = ? AND status IN ('pending', 'running') ORDER BY created_at DESC, id DESC LIMIT 1`, repoID,
		), r)
	} else {
		err = scanRun(d.sql.QueryRow(
			`SELECT `+columns+` FROM runs WHERE repo_id = ? AND branch = ? AND status IN ('pending', 'running') ORDER BY created_at DESC, id DESC LIMIT 1`, repoID, branch,
		), r)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active run: %w", err)
	}
	return r, nil
}

func (d *DB) GetActiveRuns() ([]*Run, error) {
	columns, err := d.runColumns()
	if err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(
		`SELECT `+columns+` FROM runs WHERE status IN (?, ?) ORDER BY created_at DESC, id DESC`,
		types.RunPending, types.RunRunning,
	)
	if err != nil {
		return nil, fmt.Errorf("get active runs: %w", err)
	}
	defer rows.Close()

	var runs []*Run
	for rows.Next() {
		r := &Run{}
		if err := scanRun(rows, r); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (d *DB) UpdateRunStatus(id string, status types.RunStatus) error {
	// push_active is cleared for a terminal status: a run that can no longer
	// push (or repair and re-push) must not keep the marker that makes
	// concurrent sync refuse.
	_, err := d.sql.Exec(`UPDATE runs SET status = ?, push_active = CASE WHEN ? THEN 0 ELSE push_active END, terminal_head_verified_at = NULL, updated_at = ? WHERE id = ?`, status, status.Terminal(), now(), id)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunPRURL(id, prURL string) error {
	ts := now()
	_, err := d.sql.Exec(`UPDATE runs SET pr_url = ?, pr_state = CASE WHEN pr_state IN ('merged', 'closed') THEN pr_state ELSE 'open' END, pr_state_observed_at = ?, updated_at = ? WHERE id = ?`, prURL, ts, ts, id)
	if err != nil {
		return fmt.Errorf("update run pr url: %w", err)
	}
	return nil
}

type PushBinding struct {
	HeadSHA           string
	TargetKind        string
	TargetFingerprint string
	Ref               string
}

func (d *DB) UpdateRunPushBinding(id string, binding PushBinding) error {
	ts := now()
	_, err := d.sql.Exec(
		`UPDATE runs SET last_pushed_sha = ?, push_target_kind = ?, push_target_fingerprint = ?, push_ref = ?, last_pushed_at = ?, push_generation = COALESCE(push_generation, 0) + 1, updated_at = ? WHERE id = ?`,
		binding.HeadSHA, binding.TargetKind, binding.TargetFingerprint, binding.Ref, ts, ts, id,
	)
	if err != nil {
		return fmt.Errorf("update run push binding: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunPublication(id string, binding PushBinding) error {
	ts := now()
	_, err := d.sql.Exec(
		`UPDATE runs SET head_sha = ?, last_pushed_sha = ?, push_target_kind = ?, push_target_fingerprint = ?, push_ref = ?, last_pushed_at = ?, push_generation = COALESCE(push_generation, 0) + 1, ci_test_restart_pending = 0, updated_at = ? WHERE id = ?`,
		binding.HeadSHA, binding.HeadSHA, binding.TargetKind, binding.TargetFingerprint, binding.Ref, ts, ts, id,
	)
	if err != nil {
		return fmt.Errorf("update run publication: %w", err)
	}
	return nil
}

type PushRebind struct {
	Status             types.RunStatus
	ExpectedPushed     string
	ExpectedGeneration int64
	ExpectedHead       string
	PRState            *string
	CustodyReturned    bool
	UpstreamURL        string
	ForkURL            string
	TargetKind         string
	TargetFingerprint  string
	Ref                string
	Head               string
}

func (d *DB) RebindRunPushedHead(id string, rebind PushRebind) (bool, error) {
	result, err := d.sql.Exec(
		`UPDATE runs SET head_sha = CASE WHEN head_sha = last_pushed_sha THEN ? ELSE head_sha END, last_pushed_sha = ?, push_generation = COALESCE(push_generation, 0) + 1, updated_at = ?
		WHERE id = ? AND status = ? AND last_pushed_sha = ? AND COALESCE(push_generation, 0) = ?
			AND head_sha = ? AND (custody_returned_at IS NOT NULL) = ?
			AND push_target_kind = ? AND push_target_fingerprint = ? AND push_ref = ? AND COALESCE(push_active, 0) = 0
			AND pr_state IS ? AND COALESCE(pr_state, '') NOT IN ('merged', 'closed')
			AND NOT EXISTS (SELECT 1 FROM runs other WHERE other.repo_id = runs.repo_id AND other.branch = runs.branch AND other.id <> runs.id
				AND other.status NOT IN (?, ?, ?, ?, ?, ?))
			AND EXISTS (SELECT 1 FROM repos WHERE repos.id = runs.repo_id AND repos.upstream_url = ? AND COALESCE(repos.fork_url, '') = ?)`,
		rebind.Head, rebind.Head, now(), id, string(rebind.Status), rebind.ExpectedPushed, rebind.ExpectedGeneration,
		rebind.ExpectedHead, rebind.CustodyReturned,
		rebind.TargetKind, rebind.TargetFingerprint, rebind.Ref, rebind.PRState,
		string(types.RunCompleted), string(types.RunFailed), string(types.RunCancelled), string(types.RunChecksPassed), string(types.RunCIMonitorInterrupted), string(types.RunHandedOff),
		rebind.UpstreamURL, rebind.ForkURL,
	)
	if err != nil {
		return false, fmt.Errorf("rebind run pushed head: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rebind run pushed head: %w", err)
	}
	return affected == 1, nil
}

func (d *DB) SetRunCustodyReturned(id string) error {
	return d.SetRunsCustodyReturned([]string{id})
}

func (d *DB) SetRunsCustodyReturned(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("set runs custody returned: begin: %w", err)
	}
	defer tx.Rollback()
	ts := now()
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE runs SET custody_returned_at = COALESCE(custody_returned_at, ?), updated_at = ? WHERE id = ?`, ts, ts, id); err != nil {
			return fmt.Errorf("set runs custody returned: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set runs custody returned: commit: %w", err)
	}
	return nil
}

func (d *DB) SetRunPushActive(id string, active bool) error {
	_, err := d.sql.Exec(`UPDATE runs SET push_active = ?, updated_at = ? WHERE id = ?`, active, now(), id)
	if err != nil {
		return fmt.Errorf("set run push active: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunPRState(id, state string) error {
	state = strings.ToLower(strings.TrimSpace(state))
	ts := now()
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("update run PR state: begin transaction: %w", err)
	}
	defer tx.Rollback()

	var current sql.NullString
	if err := tx.QueryRow(`SELECT pr_state FROM runs WHERE id = ?`, id).Scan(&current); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("update run PR state: read current state: %w", err)
	}
	state = monotonicPRState(current.String, state)
	if _, err := tx.Exec(`UPDATE runs SET pr_state = ?, pr_state_observed_at = ?, updated_at = ? WHERE id = ?`, state, ts, ts, id); err != nil {
		return fmt.Errorf("update run PR state: %w", err)
	}
	if terminalPRState(state) {
		if err := finalizeTerminalPRRun(tx, id, ts); err != nil {
			return fmt.Errorf("update run PR state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update run PR state: commit: %w", err)
	}
	return nil
}

func (d *DB) ReconcileTerminalPRRuns() (int, error) {
	ts := now()
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, fmt.Errorf("reconcile terminal PR runs: begin transaction: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id FROM runs WHERE status IN (?, ?) AND pr_state IN ('merged', 'closed')`, types.RunPending, types.RunRunning)
	if err != nil {
		return 0, fmt.Errorf("reconcile terminal PR runs: list runs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("reconcile terminal PR runs: scan run: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("reconcile terminal PR runs: close rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("reconcile terminal PR runs: list runs: %w", err)
	}

	for _, id := range ids {
		if err := finalizeTerminalPRRun(tx, id, ts); err != nil {
			return 0, fmt.Errorf("reconcile terminal PR runs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("reconcile terminal PR runs: commit: %w", err)
	}
	return len(ids), nil
}

func monotonicPRState(current, observed string) string {
	current = strings.ToLower(strings.TrimSpace(current))
	observed = strings.ToLower(strings.TrimSpace(observed))
	switch {
	case current == "merged":
		return current
	case observed == "merged":
		return observed
	case current == "closed":
		return current
	default:
		return observed
	}
}

func terminalPRState(state string) bool {
	return state == "merged" || state == "closed"
}

func finalizeTerminalPRRun(tx *sql.Tx, id string, ts int64) error {
	if _, err := tx.Exec(
		`UPDATE step_results SET status = ?, exit_code = COALESCE(exit_code, 0), completed_at = COALESCE(completed_at, ?),
			last_activity_at = ?, last_activity = ?, agent_pid = NULL
		 WHERE run_id = ? AND step_name = ? AND status IN (?, ?, ?, ?)
		   AND EXISTS (SELECT 1 FROM runs WHERE id = ? AND status IN (?, ?))`,
		types.StepStatusCompleted, ts, ts, "status: completed", id, types.StepCI,
		types.StepStatusRunning, types.StepStatusAwaitingApproval, types.StepStatusFixing, types.StepStatusFixReview,
		id, types.RunPending, types.RunRunning,
	); err != nil {
		return fmt.Errorf("complete terminal CI step: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE runs SET
			status = CASE WHEN status IN (?, ?) THEN ? ELSE status END,
			push_active = 0,
			parked_ms = COALESCE(parked_ms, 0) + CASE
				WHEN awaiting_agent_since IS NOT NULL AND ? > awaiting_agent_since
				THEN (? - awaiting_agent_since) * 1000 ELSE 0 END,
			awaiting_agent_since = NULL, updated_at = ?
		 WHERE id = ?`,
		types.RunPending, types.RunRunning, types.RunCompleted, ts, ts, ts, id,
	); err != nil {
		return fmt.Errorf("finalize terminal PR run: %w", err)
	}
	return nil
}

func (d *DB) SetRunCIReady(id string, ready bool) error {
	return d.SetRunCIReadyWithReason(id, ready, false)
}

func (d *DB) SetRunCIReadyWithReason(id string, ready, declaredNoCI bool) error {
	readyValue := 0
	declaredValue := 0
	var readyAt any
	if ready {
		readyValue = 1
		readyAt = now()
		if declaredNoCI {
			declaredValue = 1
		}
	}
	_, err := d.sql.Exec(`UPDATE runs SET ci_ready_at = ?, ci_ready_no_ci = ?, updated_at = ? WHERE id = ? AND ((ci_ready_at IS NULL AND ? = 1) OR (ci_ready_at IS NOT NULL AND ? = 0) OR (COALESCE(ci_ready_no_ci, 0) != ?))`, readyAt, declaredValue, now(), id, readyValue, readyValue, declaredValue)
	if err != nil {
		return fmt.Errorf("set run CI ready: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunReviewApprovedHeadSHA(id, headSHA string) error {
	_, err := d.sql.Exec(`UPDATE runs SET review_approved_head_sha = ?, updated_at = ? WHERE id = ?`, headSHA, now(), id)
	if err != nil {
		return fmt.Errorf("update run review-approved head sha: %w", err)
	}
	return nil
}

func (d *DB) SetRunWorktreeDir(id, dir string) error {
	_, err := d.sql.Exec(`UPDATE runs SET worktree_dir = ?, updated_at = ? WHERE id = ?`, dir, now(), id)
	if err != nil {
		return fmt.Errorf("set run worktree dir: %w", err)
	}
	return nil
}

func (d *DB) SetRunEvidenceDir(id, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("set run evidence dir: empty path")
	}
	_, err := d.sql.Exec(`UPDATE runs SET evidence_dir = ?, updated_at = ? WHERE id = ? AND evidence_dir IS NULL`, dir, now(), id)
	if err != nil {
		return fmt.Errorf("set run evidence dir: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunHeadSHA(id, headSHA string) error {
	_, err := d.sql.Exec(`UPDATE runs SET head_sha = ?, updated_at = ? WHERE id = ?`, headSHA, now(), id)
	if err != nil {
		return fmt.Errorf("update run head sha: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunHeadSHAForRevalidation(id, headSHA string) error {
	_, err := d.sql.Exec(`UPDATE runs SET head_sha = ?, review_approved_head_sha = NULL, updated_at = ? WHERE id = ?`, headSHA, now(), id)
	if err != nil {
		return fmt.Errorf("update run head sha for revalidation: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunHeadSHAWithCarriedReview(id, approvedHeadSHA, headSHA string) (bool, error) {
	result, err := d.sql.Exec(`UPDATE runs SET head_sha = ?, review_approved_head_sha = ?, ci_test_restart_pending = 1, updated_at = ? WHERE id = ? AND review_approved_head_sha = ?`, headSHA, headSHA, now(), id, approvedHeadSHA)
	if err != nil {
		return false, fmt.Errorf("update run head sha with carried review: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update run head sha with carried review: %w", err)
	}
	return rows == 1, nil
}

func (d *DB) RunTestRestartPending(id string) (bool, error) {
	var pending bool
	if err := d.sql.QueryRow(`SELECT ci_test_restart_pending FROM runs WHERE id = ?`, id).Scan(&pending); err != nil {
		return false, fmt.Errorf("read run test restart pending: %w", err)
	}
	return pending, nil
}

func (d *DB) UpdateRunError(id, errMsg string) error {
	return d.UpdateRunErrorStatus(id, errMsg, types.RunFailed)
}

func (d *DB) UpdateRunErrorStatus(id, errMsg string, status types.RunStatus) error {
	_, err := d.sql.Exec(`UPDATE runs SET error = ?, status = ?, push_active = 0, terminal_head_verified_at = NULL, updated_at = ? WHERE id = ?`, errMsg, status, now(), id)
	if err != nil {
		return fmt.Errorf("update run error: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunErrorStatusWithVerifiedHead(id, errMsg string, status types.RunStatus, headSHA string) error {
	ts := now()
	_, err := d.sql.Exec(`UPDATE runs SET error = ?, status = ?, head_sha = ?, push_active = 0, terminal_head_verified_at = ?, updated_at = ? WHERE id = ?`, errMsg, status, headSHA, ts, ts, id)
	if err != nil {
		return fmt.Errorf("update run error with verified head: %w", err)
	}
	return nil
}

func (d *DB) UpdateRunStatusWithVerifiedHead(id string, status types.RunStatus, headSHA string) error {
	ts := now()
	_, err := d.sql.Exec(`UPDATE runs SET status = ?, head_sha = ?, push_active = 0, terminal_head_verified_at = ?, updated_at = ? WHERE id = ?`, status, headSHA, ts, ts, id)
	if err != nil {
		return fmt.Errorf("update run status with verified head: %w", err)
	}
	return nil
}

func (d *DB) VerifyTerminalRunHeadRewrite(id string, status types.RunStatus, recordedHead, liveHead string) (bool, error) {
	ts := now()
	result, err := d.sql.Exec(`UPDATE runs SET head_sha = ?, push_active = 0, terminal_head_verified_at = ?, updated_at = ? WHERE id = ? AND status = ? AND head_sha = ? AND review_approved_head_sha = ? AND terminal_head_verified_at IS NULL AND custody_returned_at IS NULL`, liveHead, ts, ts, id, status, recordedHead, recordedHead)
	if err != nil {
		return false, fmt.Errorf("verify terminal run head rewrite: %w", err)
	}
	updated, err := result.RowsAffected()
	return updated == 1, err
}

func (d *DB) VerifyTerminalRunRecordedHead(id string, status types.RunStatus, recordedHead, reviewedHead string) (bool, error) {
	ts := now()
	result, err := d.sql.Exec(`UPDATE runs SET push_active = 0, terminal_head_verified_at = ?, updated_at = ? WHERE id = ? AND status = ? AND head_sha = ? AND review_approved_head_sha = ? AND terminal_head_verified_at IS NULL AND custody_returned_at IS NULL`, ts, ts, id, status, recordedHead, reviewedHead)
	if err != nil {
		return false, fmt.Errorf("verify terminal run recorded head: %w", err)
	}
	updated, err := result.RowsAffected()
	return updated == 1, err
}

func (d *DB) RecordRunTerminalHeadEvidence(id, headSHA string) error {
	ts := now()
	_, err := d.sql.Exec(`UPDATE runs SET head_sha = ?, terminal_head_verified_at = ?, updated_at = ? WHERE id = ? AND status IN (?, ?)`,
		headSHA, ts, ts, id, types.RunPending, types.RunRunning)
	return err
}

const RunIntentSourceAgent = "agent"

const RunIntentSourceRerun = "rerun"

func IsAuthoritativeRunIntentSource(source string) bool {
	return source == RunIntentSourceAgent || source == RunIntentSourceRerun
}

type RunIntent struct {
	Summary   string
	Source    string
	SessionID string
	Score     float64
}

func (d *DB) SetRunIntentCheck(id, policyJSON string) error {
	result, err := d.sql.Exec(`UPDATE runs SET intent_check_json = ? WHERE id = ? AND intent_check_json IS NULL`, policyJSON, id)
	if err != nil {
		return fmt.Errorf("pin run intent check: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("pin run intent check: %w", err)
	} else if changed != 1 {
		return fmt.Errorf("pin run intent check: run is missing or policy is already pinned")
	}
	return nil
}

func (d *DB) UpdateRunIntent(id string, intent RunIntent) error {
	_, err := d.sql.Exec(
		`UPDATE runs SET intent = ?, intent_source = ?, intent_session_id = ?, intent_score = ?, updated_at = ? WHERE id = ?`,
		intent.Summary, intent.Source, intent.SessionID, intent.Score, now(), id,
	)
	if err != nil {
		return fmt.Errorf("update run intent: %w", err)
	}
	return nil
}

var ErrClosingIssueRefsLocked = errors.New("closing issue references already consumed by PR body composition")

func (d *DB) UpdateRunClosingIssueRefs(id string, refs []string) error {
	encoded, err := closingissues.Encode(refs)
	if err != nil {
		return fmt.Errorf("update run closing issue references: %w", err)
	}
	var value any
	if encoded != "" {
		value = encoded
	}
	res, err := d.sql.Exec(
		`UPDATE runs SET closing_issue_refs = ?, updated_at = ? WHERE id = ? AND closing_issue_refs_locked_at IS NULL`,
		value, now(), id,
	)
	if err != nil {
		return fmt.Errorf("update run closing issue references: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update run closing issue references: %w", err)
	}
	if affected > 0 {
		return nil
	}
	var locked sql.NullInt64
	switch err := d.sql.QueryRow(`SELECT closing_issue_refs_locked_at FROM runs WHERE id = ?`, id).Scan(&locked); {
	case err == sql.ErrNoRows:
		return fmt.Errorf("update run closing issue references: run not found: %s", id)
	case err != nil:
		return fmt.Errorf("update run closing issue references: %w", err)
	case locked.Valid:
		return ErrClosingIssueRefsLocked
	default:
		// The lock was released between the update and this read, which the
		// claim protocol never does. Treat an unexplained no-op as a failure
		// rather than reporting a write that did not happen.
		return fmt.Errorf("update run closing issue references: no row updated for run %s", id)
	}
}

func (d *DB) MergeRunClosingIssueRefs(id string, refs []string) error {
	incoming, err := closingissues.Normalize(refs)
	if err != nil {
		return fmt.Errorf("merge run closing issue references: %w", err)
	}
	if len(incoming) == 0 {
		return nil
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("merge run closing issue references: %w", err)
	}
	defer tx.Rollback()

	var storedRefs sql.NullString
	var lockedAt sql.NullInt64
	if err := tx.QueryRow(
		`SELECT closing_issue_refs, closing_issue_refs_locked_at FROM runs WHERE id = ?`, id,
	).Scan(&storedRefs, &lockedAt); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("merge run closing issue references: run not found: %s", id)
		}
		return fmt.Errorf("merge run closing issue references: %w", err)
	}
	existing, err := closingissues.Decode(storedRefs.String)
	if err != nil {
		return fmt.Errorf("merge run closing issue references: %w", err)
	}
	if closingissues.Covers(existing, incoming) {
		// Already recorded (for example at run creation from the same
		// --closes values), so nothing can be missed even after the claim.
		return nil
	}
	if lockedAt.Valid {
		return ErrClosingIssueRefsLocked
	}
	encoded, err := closingissues.Encode(append(existing, incoming...))
	if err != nil {
		return fmt.Errorf("merge run closing issue references: %w", err)
	}
	res, err := tx.Exec(
		`UPDATE runs SET closing_issue_refs = ?, updated_at = ? WHERE id = ? AND closing_issue_refs_locked_at IS NULL`,
		encoded, now(), id,
	)
	if err != nil {
		return fmt.Errorf("merge run closing issue references: %w", err)
	}
	if affected, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("merge run closing issue references: %w", err)
	} else if affected == 0 {
		return ErrClosingIssueRefsLocked
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("merge run closing issue references: %w", err)
	}
	return nil
}

func (d *DB) ClaimClosingIssueRefsForPRBody(id string) ([]string, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("claim closing issue references: %w", err)
	}
	defer tx.Rollback()

	var storedRefs sql.NullString
	var lockedAt sql.NullInt64
	err = tx.QueryRow(
		`SELECT closing_issue_refs, closing_issue_refs_locked_at FROM runs WHERE id = ?`, id,
	).Scan(&storedRefs, &lockedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("claim closing issue references: run not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("claim closing issue references: %w", err)
	}

	if !lockedAt.Valid {
		if _, err := tx.Exec(
			`UPDATE runs SET closing_issue_refs_locked_at = ? WHERE id = ? AND closing_issue_refs_locked_at IS NULL`,
			now(), id,
		); err != nil {
			return nil, fmt.Errorf("claim closing issue references: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim closing issue references: %w", err)
	}
	refs, err := closingissues.Decode(storedRefs.String)
	if err != nil {
		return nil, fmt.Errorf("claim closing issue references: %w", err)
	}
	return refs, nil
}

func (d *DB) SetRunAwaitingAgent(id string) error {
	ts := now()
	_, err := d.sql.Exec(`UPDATE runs SET awaiting_agent_since = ?, updated_at = ? WHERE id = ?`, ts, ts, id)
	if err != nil {
		return fmt.Errorf("set run awaiting agent: %w", err)
	}
	return nil
}

func (d *DB) ClearRunAwaitingAgent(id string) error {
	_, err := d.sql.Exec(`UPDATE runs SET awaiting_agent_since = NULL, updated_at = ? WHERE id = ?`, now(), id)
	if err != nil {
		return fmt.Errorf("clear run awaiting agent: %w", err)
	}
	return nil
}

func (d *DB) AddRunParkedDuration(id string, ms int64) error {
	if ms <= 0 {
		return nil
	}
	_, err := d.sql.Exec(`UPDATE runs SET parked_ms = COALESCE(parked_ms, 0) + ?, updated_at = ? WHERE id = ?`, ms, now(), id)
	if err != nil {
		return fmt.Errorf("add run parked duration: %w", err)
	}
	return nil
}

func (d *DB) CompleteRunAwaitingAgent(id string, ms int64) error {
	if ms < 0 {
		ms = 0
	}
	_, err := d.sql.Exec(
		`UPDATE runs SET awaiting_agent_since = NULL,
			parked_ms = COALESCE(parked_ms, 0) + CASE WHEN awaiting_agent_since IS NOT NULL THEN ? ELSE 0 END,
			updated_at = ? WHERE id = ?`,
		ms, now(), id,
	)
	if err != nil {
		return fmt.Errorf("complete run awaiting agent: %w", err)
	}
	return nil
}

func (d *DB) RecoverStaleRuns(errMsg string) (int, error) {
	return d.RecoverStaleRunsExcept(errMsg, nil)
}

func (d *DB) RecoverStaleRunsExcept(errMsg string, preserved map[string]struct{}) (int, error) {
	ts := now()

	tx, err := d.sql.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	placeholders, args := recoveryExclusionClause(preserved)

	// Older recovery paths left push ownership on terminal CI monitors.
	// Repair those rows without changing publication or custody evidence.
	_, err = tx.Exec(
		`UPDATE runs SET push_active = 0 WHERE push_active = 1 AND status IN (?, ?, ?, ?, ?)`,
		types.RunCompleted, types.RunFailed, types.RunCancelled, types.RunChecksPassed, types.RunCIMonitorInterrupted,
	)
	if err != nil {
		return 0, fmt.Errorf("release terminal push ownership: %w", err)
	}

	readyArgs := []any{types.RunChecksPassed, ts, types.RunPending, types.RunRunning, types.StepCI, types.StepStatusCompleted}
	readyArgs = append(readyArgs, args...)
	readyResult, err := tx.Exec(
		`UPDATE runs SET status = ?, error = NULL, push_active = 0,
		 awaiting_agent_since = NULL, updated_at = ?
		 WHERE status IN (?, ?) AND ci_ready_at IS NOT NULL AND ci_released_at IS NOT NULL
		   AND lower(trim(pr_state)) = 'open'
		   AND EXISTS (
		       SELECT 1 FROM step_results ci
		       WHERE ci.run_id = runs.id AND ci.step_name = ? AND ci.status = ?
		   )`+placeholders,
		readyArgs...,
	)
	if err != nil {
		return 0, fmt.Errorf("recover completed CI runs: %w", err)
	}
	readyCount, err := readyResult.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("completed CI rows affected: %w", err)
	}

	// A daemon restart during the long-lived CI monitor should not turn an
	// already-pushed PR into a failed run. Recover those runs before the broad
	// failure update below so the hard-fail path keeps handling mid-pipeline
	// crashes unchanged. Preserved runs are excluded: those are parked CI runs
	// the caller resumes and reconciles against live PR state, so that
	// more-specific recovery path owns them instead of this coarse marking.
	ciArgs := []any{
		types.RunCIMonitorInterrupted, types.RunCIMonitorInterruptedReason, ts,
		types.RunPending, types.RunRunning,
		types.StepCI,
		types.StepStatusRunning, types.StepStatusAwaitingApproval, types.StepStatusFixing, types.StepStatusFixReview,
		types.StepCI,
		types.StepStatusRunning, types.StepStatusAwaitingApproval, types.StepStatusFixing, types.StepStatusFixReview,
	}
	ciArgs = append(ciArgs, args...)
	ciResult, err := tx.Exec(
		`UPDATE runs
		 SET status = ?, error = ?, push_active = 0, awaiting_agent_since = NULL, updated_at = ?
		 WHERE status IN (?, ?)
		   AND pr_url IS NOT NULL
		   AND pr_url <> ''
		   AND EXISTS (
		       SELECT 1 FROM step_results ci
		       WHERE ci.run_id = runs.id
		         AND ci.step_name = ?
		         AND ci.status IN (?, ?, ?, ?)
		   )
		   AND NOT EXISTS (
		       SELECT 1 FROM step_results active
		       WHERE active.run_id = runs.id
		         AND active.step_name <> ?
		         AND active.status IN (?, ?, ?, ?)
		   )`+placeholders,
		ciArgs...,
	)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted ci monitor runs: %w", err)
	}
	ciCount, err := ciResult.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("ci rows affected: %w", err)
	}

	// Mark the CI monitor step for each run just recovered above as skipped so
	// the broad step-failure update below leaves it alone.
	_, err = tx.Exec(
		`UPDATE step_results
		 SET status = ?, error = ?, completed_at = ?
		 WHERE step_name = ?
		   AND status IN (?, ?, ?, ?)
		   AND run_id IN (
		       SELECT id FROM runs
		       WHERE status = ?
		         AND error = ?
		         AND updated_at = ?
		   )`,
		types.StepStatusSkipped, types.RunCIMonitorInterruptedReason, ts,
		types.StepCI,
		types.StepStatusRunning, types.StepStatusAwaitingApproval, types.StepStatusFixing, types.StepStatusFixReview,
		types.RunCIMonitorInterrupted, types.RunCIMonitorInterruptedReason, ts,
	)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted ci monitor steps: %w", err)
	}

	// Fail stale steps (running, awaiting_approval, fixing, fix_review) for
	// pending/running runs, excluding preserved runs. The CI monitor runs
	// recovered above are no longer pending/running, so they are left alone.
	stepArgs := []any{
		types.StepStatusFailed, errMsg, ts,
		types.StepStatusRunning, types.StepStatusAwaitingApproval, types.StepStatusFixing, types.StepStatusFixReview,
		types.RunPending, types.RunRunning,
	}
	stepArgs = append(stepArgs, args...)
	_, err = tx.Exec(
		`UPDATE step_results SET status = ?, error = ?, completed_at = ?
		 WHERE status IN (?, ?, ?, ?) AND run_id IN (
			SELECT id FROM runs WHERE status IN (?, ?)`+placeholders+`
		 )`,
		stepArgs...,
	)
	if err != nil {
		return 0, fmt.Errorf("recover stale steps: %w", err)
	}

	// Fail stale runs. Clear any awaiting-agent marker so a recovered (now
	// failed) run is never reported as still parked awaiting the agent,
	// accumulating the marker's elapsed time into the run's parked total so
	// the parked evidence survives the crash.
	runArgs := []any{types.RunFailed, errMsg, ts, ts, ts, types.RunPending, types.RunRunning}
	runArgs = append(runArgs, args...)
	result, err := tx.Exec(
		`UPDATE runs SET status = ?, error = ?, push_active = 0,
			parked_ms = COALESCE(parked_ms, 0) + CASE
				WHEN awaiting_agent_since IS NOT NULL AND ? > awaiting_agent_since
				THEN (? - awaiting_agent_since) * 1000 ELSE 0 END,
			awaiting_agent_since = NULL, updated_at = ? WHERE status IN (?, ?)`+placeholders,
		runArgs...,
	)
	if err != nil {
		return 0, fmt.Errorf("recover stale runs: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return int(readyCount + ciCount + count), nil
}

func recoveryExclusionClause(preserved map[string]struct{}) (string, []any) {
	if len(preserved) == 0 {
		return "", nil
	}
	args := make([]any, 0, len(preserved))
	placeholders := make([]string, 0, len(preserved))
	for id := range preserved {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	return " AND id NOT IN (" + strings.Join(placeholders, ", ") + ")", args
}

func (d *DB) GetRunGates(id string) (string, error) {
	var gates sql.NullString
	err := d.sql.QueryRow(`SELECT gates_json FROM runs WHERE id = ?`, id).Scan(&gates)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get run gates: %w", err)
	}
	return gates.String, nil
}

func (d *DB) SetRunGates(id, gates string) error {
	_, err := d.sql.Exec(`UPDATE runs SET gates_json = ?, updated_at = ? WHERE id = ?`, gates, now(), id)
	if err != nil {
		return fmt.Errorf("set run gates: %w", err)
	}
	return nil
}

func (d *DB) SetRunQuotaRouting(id, payload string) error {
	_, err := d.sql.Exec(`UPDATE runs SET quota_routing = ?, updated_at = ? WHERE id = ?`, payload, now(), id)
	if err != nil {
		return fmt.Errorf("set run quota routing: %w", err)
	}
	return nil
}

func (d *DB) GetRunQuotaRouting(id string) (string, error) {
	var payload sql.NullString
	err := d.sql.QueryRow(`SELECT quota_routing FROM runs WHERE id = ?`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get run quota routing: %w", err)
	}
	return payload.String, nil
}

func (d *DB) GetRunCIRerunState(id string) (string, error) {
	var state sql.NullString
	err := d.sql.QueryRow(`SELECT ci_rerun_state FROM runs WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get run ci rerun state: %w", err)
	}
	return state.String, nil
}

func (d *DB) SetRunCIRerunState(id, state string) error {
	_, err := d.sql.Exec(`UPDATE runs SET ci_rerun_state = ?, updated_at = ? WHERE id = ?`, state, now(), id)
	if err != nil {
		return fmt.Errorf("set run ci rerun state: %w", err)
	}
	return nil
}

func (d *DB) GetRunEvidenceDirs() ([]string, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT evidence_dir FROM runs WHERE evidence_dir IS NOT NULL AND evidence_dir != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dirs []string
	for rows.Next() {
		var dir string
		if err := rows.Scan(&dir); err != nil {
			return nil, err
		}
		dirs = append(dirs, dir)
	}
	return dirs, rows.Err()
}

func (d *DB) GetRunWorktreeDirs() ([]string, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT worktree_dir FROM runs WHERE worktree_dir IS NOT NULL AND worktree_dir != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dirs []string
	for rows.Next() {
		var dir string
		if err := rows.Scan(&dir); err != nil {
			return nil, err
		}
		dirs = append(dirs, dir)
	}
	return dirs, rows.Err()
}
