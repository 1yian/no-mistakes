package db

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

type Repo struct {
	ID            string
	WorkingPath   string
	UpstreamURL   string
	ForkURL       string
	DefaultBranch string
	CreatedAt     int64

	URLsVerified bool `json:"-"`
}

const repoReadColumns = "id, working_path, upstream_url, fork_url, default_branch, created_at"

func repoScanTargets(r *Repo) []any {
	return []any{&r.ID, &r.WorkingPath, &r.UpstreamURL, &r.ForkURL, &r.DefaultBranch, &r.CreatedAt}
}

func (d *DB) repoColumns() (string, error) {
	return d.readProjection("repos", repoReadColumns, repoScanTargets(&Repo{})...)
}

func (r *Repo) PushURL() string {
	if r == nil {
		return ""
	}
	if strings.TrimSpace(r.ForkURL) != "" {
		return r.ForkURL
	}
	return r.UpstreamURL
}

func (d *DB) InsertRepoWithID(id, workingPath, upstreamURL, defaultBranch string) (*Repo, error) {
	return d.InsertRepoWithIDAndFork(id, workingPath, upstreamURL, "", defaultBranch)
}

func (d *DB) InsertRepoWithIDAndFork(id, workingPath, upstreamURL, forkURL, defaultBranch string) (*Repo, error) {
	r := &Repo{
		ID:            id,
		WorkingPath:   workingPath,
		UpstreamURL:   upstreamURL,
		ForkURL:       strings.TrimSpace(forkURL),
		DefaultBranch: defaultBranch,
		CreatedAt:     now(),
	}
	_, err := d.sql.Exec(
		`INSERT INTO repos (id, working_path, upstream_url, fork_url, default_branch, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.WorkingPath, r.UpstreamURL, nullableString(r.ForkURL), r.DefaultBranch, r.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert repo: %w", err)
	}
	return r, nil
}

func (d *DB) InsertRepo(workingPath, upstreamURL, defaultBranch string) (*Repo, error) {
	return d.InsertRepoWithFork(workingPath, upstreamURL, "", defaultBranch)
}

func (d *DB) InsertRepoWithFork(workingPath, upstreamURL, forkURL, defaultBranch string) (*Repo, error) {
	r := &Repo{
		ID:            newID(),
		WorkingPath:   workingPath,
		UpstreamURL:   upstreamURL,
		ForkURL:       strings.TrimSpace(forkURL),
		DefaultBranch: defaultBranch,
		CreatedAt:     now(),
	}
	_, err := d.sql.Exec(
		`INSERT INTO repos (id, working_path, upstream_url, fork_url, default_branch, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.WorkingPath, r.UpstreamURL, nullableString(r.ForkURL), r.DefaultBranch, r.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert repo: %w", err)
	}
	return r, nil
}

func (d *DB) RepoWorkingPaths() ([]string, error) {
	rows, err := d.sql.Query(`SELECT working_path FROM repos ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("get repo working paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan repo working path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repo working paths: %w", err)
	}
	return paths, nil
}

func (d *DB) GetRepos() ([]*Repo, error) {
	columns, err := d.repoColumns()
	if err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(
		`SELECT ` + columns + ` FROM repos ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("get repos: %w", err)
	}
	defer rows.Close()

	var repos []*Repo
	for rows.Next() {
		r := &Repo{}
		if err := rows.Scan(repoScanTargets(r)...); err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		repos = append(repos, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repos: %w", err)
	}
	return repos, nil
}

func (d *DB) GetRepo(id string) (*Repo, error) {
	columns, err := d.repoColumns()
	if err != nil {
		return nil, err
	}
	r := &Repo{}
	err = d.sql.QueryRow(
		`SELECT `+columns+` FROM repos WHERE id = ?`, id,
	).Scan(repoScanTargets(r)...)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	return r, nil
}

func (d *DB) GetRepoByPath(workingPath string) (*Repo, error) {
	columns, err := d.repoColumns()
	if err != nil {
		return nil, err
	}
	r := &Repo{}
	err = d.sql.QueryRow(
		`SELECT `+columns+` FROM repos WHERE working_path = ?`, workingPath,
	).Scan(repoScanTargets(r)...)
	if err == nil {
		return r, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("get repo by path: %w", err)
	}
	return d.getRepoBySameFile(workingPath)
}

func (d *DB) getRepoBySameFile(workingPath string) (*Repo, error) {
	columns, err := d.repoColumns()
	if err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(
		`SELECT ` + columns + ` FROM repos ORDER BY created_at, rowid`,
	)
	if err != nil {
		return nil, fmt.Errorf("get repo by path: %w", err)
	}
	defer rows.Close()

	var target os.FileInfo
	for rows.Next() {
		r := &Repo{}
		if err := rows.Scan(repoScanTargets(r)...); err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		if target == nil {
			// Resolved lazily so an empty table costs no filesystem call.
			if target, err = os.Stat(workingPath); err != nil {
				return nil, nil
			}
		}
		if info, err := os.Stat(r.WorkingPath); err == nil && os.SameFile(target, info) {
			return r, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get repo by path: %w", err)
	}
	return nil, nil
}

func (d *DB) ReplaceRepoURLs(id, upstreamURL, forkURL string) (*Repo, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin repo URL replacement: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`UPDATE repos SET upstream_url = ?, fork_url = ? WHERE id = ?`,
		upstreamURL, nullableString(forkURL), id,
	)
	if err != nil {
		return nil, fmt.Errorf("replace repo URLs: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, fmt.Errorf("replace repo URLs rows affected: %w", err)
	} else if affected != 1 {
		return nil, fmt.Errorf("replace repo URLs: repository not found")
	}

	r := &Repo{}
	if err := tx.QueryRow(
		`SELECT id, working_path, upstream_url, COALESCE(fork_url, ''), default_branch, created_at FROM repos WHERE id = ?`, id,
	).Scan(&r.ID, &r.WorkingPath, &r.UpstreamURL, &r.ForkURL, &r.DefaultBranch, &r.CreatedAt); err != nil {
		return nil, fmt.Errorf("read replaced repo URLs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit repo URL replacement: %w", err)
	}
	return r, nil
}

func (d *DB) UpdateRepoMetadata(id, upstreamURL, defaultBranch string) (*Repo, error) {
	_, err := d.sql.Exec(
		`UPDATE repos SET upstream_url = ?, default_branch = ? WHERE id = ?`,
		upstreamURL, defaultBranch, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update repo metadata: %w", err)
	}
	return d.GetRepo(id)
}

func (d *DB) UpdateRepoMetadataWithFork(id, upstreamURL, forkURL, defaultBranch string) (*Repo, error) {
	_, err := d.sql.Exec(
		`UPDATE repos SET upstream_url = ?, fork_url = ?, default_branch = ? WHERE id = ?`,
		upstreamURL, nullableString(forkURL), defaultBranch, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update repo metadata: %w", err)
	}
	return d.GetRepo(id)
}

func (d *DB) UpdateRepoForkURL(id, forkURL string) (*Repo, error) {
	_, err := d.sql.Exec(
		`UPDATE repos SET fork_url = ? WHERE id = ?`,
		nullableString(forkURL), id,
	)
	if err != nil {
		return nil, fmt.Errorf("update repo fork URL: %w", err)
	}
	return d.GetRepo(id)
}

func (d *DB) UpdateRepoWorkingPath(id, workingPath string) (*Repo, error) {
	_, err := d.sql.Exec(
		`UPDATE repos SET working_path = ? WHERE id = ?`,
		workingPath, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update repo working path: %w", err)
	}
	return d.GetRepo(id)
}

func (d *DB) DeleteRepo(id string) error {
	_, err := d.sql.Exec(`DELETE FROM repos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete repo: %w", err)
	}
	return nil
}

func nullableString(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}
