package db

import (
	"database/sql"
	"errors"
	"strings"
)

func (d *DB) GetGitlabEvidenceUpload(projectPath, digest string) (string, bool, error) {
	projectPath = strings.TrimSpace(projectPath)
	digest = strings.TrimSpace(digest)
	if projectPath == "" || digest == "" {
		return "", false, nil
	}
	var markdown string
	err := d.sql.QueryRow(`SELECT markdown FROM gitlab_evidence_uploads WHERE project_path = ? AND digest = ?`, projectPath, digest).Scan(&markdown)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if strings.TrimSpace(markdown) == "" {
		return "", false, nil
	}
	return markdown, true, nil
}

func (d *DB) PutGitlabEvidenceUpload(projectPath, digest, markdown string) error {
	projectPath = strings.TrimSpace(projectPath)
	digest = strings.TrimSpace(digest)
	markdown = strings.TrimSpace(markdown)
	if projectPath == "" || digest == "" || markdown == "" {
		return errors.New("gitlab evidence upload cache: project, digest, and markdown are required")
	}
	_, err := d.sql.Exec(`
		INSERT INTO gitlab_evidence_uploads (project_path, digest, markdown, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_path, digest) DO NOTHING`,
		projectPath, digest, markdown, now())
	return err
}
