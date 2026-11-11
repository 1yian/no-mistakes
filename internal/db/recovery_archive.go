package db

import (
	"database/sql"
	"fmt"
)

type RecoveryArchive struct {
	ID               string
	OwnerRunID       string
	RepoID           string
	RunID            string
	Branch           string
	RequiredHeadSHA  string
	PreservedHeadSHA string
	ArchiveRef       string
	CreatedAt        int64
}

const recoveryArchiveReadColumns = "id, owner_run_id, repo_id, run_id, branch, required_head_sha, preserved_head_sha, archive_ref, created_at"

func recoveryArchiveScanTargets(record *RecoveryArchive) []any {
	return []any{&record.ID, &record.OwnerRunID, &record.RepoID, &record.RunID, &record.Branch,
		&record.RequiredHeadSHA, &record.PreservedHeadSHA, &record.ArchiveRef, &record.CreatedAt}
}

func (d *DB) RecordRecoveryArchive(record RecoveryArchive) (*RecoveryArchive, error) {
	if record.ID == "" {
		record.ID = newID()
	}
	if record.OwnerRunID == "" {
		record.OwnerRunID = record.RunID
	}
	if record.CreatedAt == 0 {
		record.CreatedAt = now()
	}

	existing, found, err := d.recoveryArchiveByOwnerAndRef(record.OwnerRunID, record.ArchiveRef)
	if err != nil {
		return nil, err
	}
	if found {
		if sameRecoveryArchiveBinding(existing, &record) {
			return existing, nil
		}
		return nil, fmt.Errorf("recovery archive %s for run %s is already bound with different provenance", record.ArchiveRef, record.OwnerRunID)
	}

	_, err = d.sql.Exec(`
		INSERT INTO recovery_archives
			(id, owner_run_id, repo_id, run_id, branch, required_head_sha, preserved_head_sha, archive_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.OwnerRunID, record.RepoID, record.RunID, record.Branch,
		record.RequiredHeadSHA, record.PreservedHeadSHA, record.ArchiveRef, record.CreatedAt,
	)
	if err != nil {
		// A concurrent exact retry may have won the unique-key race. Re-read it
		// and retain idempotence without ever replacing the winning evidence.
		existing, found, readErr := d.recoveryArchiveByOwnerAndRef(record.OwnerRunID, record.ArchiveRef)
		if readErr == nil && found && sameRecoveryArchiveBinding(existing, &record) {
			return existing, nil
		}
		return nil, fmt.Errorf("record recovery archive: %w", err)
	}
	return &record, nil
}

func (d *DB) GetRecoveryArchivesByRun(ownerRunID string) ([]*RecoveryArchive, error) {
	columns, err := d.readProjection("recovery_archives", recoveryArchiveReadColumns, recoveryArchiveScanTargets(&RecoveryArchive{})...)
	if err != nil {
		return nil, err
	}
	rows, err := d.sql.Query(`
		SELECT `+columns+`
		FROM recovery_archives
		WHERE owner_run_id = ?
		ORDER BY created_at, id`, ownerRunID)
	if err != nil {
		return nil, fmt.Errorf("get recovery archives: %w", err)
	}
	defer rows.Close()

	var records []*RecoveryArchive
	for rows.Next() {
		record := &RecoveryArchive{}
		if err := scanRecoveryArchive(rows, record); err != nil {
			return nil, fmt.Errorf("scan recovery archive: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovery archives: %w", err)
	}
	return records, nil
}

func (d *DB) recoveryArchiveByOwnerAndRef(ownerRunID, archiveRef string) (*RecoveryArchive, bool, error) {
	columns, err := d.readProjection("recovery_archives", recoveryArchiveReadColumns, recoveryArchiveScanTargets(&RecoveryArchive{})...)
	if err != nil {
		return nil, false, err
	}
	record := &RecoveryArchive{}
	err = scanRecoveryArchive(d.sql.QueryRow(`
		SELECT `+columns+`
		FROM recovery_archives
		WHERE owner_run_id = ? AND archive_ref = ?`, ownerRunID, archiveRef), record)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get recovery archive: %w", err)
	}
	return record, true, nil
}

func scanRecoveryArchive(row interface{ Scan(...any) error }, record *RecoveryArchive) error {
	return row.Scan(recoveryArchiveScanTargets(record)...)
}

func sameRecoveryArchiveBinding(a, b *RecoveryArchive) bool {
	return a != nil && b != nil &&
		a.OwnerRunID == b.OwnerRunID &&
		a.RepoID == b.RepoID &&
		a.RunID == b.RunID &&
		a.Branch == b.Branch &&
		a.RequiredHeadSHA == b.RequiredHeadSHA &&
		a.PreservedHeadSHA == b.PreservedHeadSHA &&
		a.ArchiveRef == b.ArchiveRef
}
