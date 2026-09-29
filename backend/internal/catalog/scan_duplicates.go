package catalog

import (
	"context"
	"database/sql"
	"errors"

	"github.com/video-site/backend/internal/dedupe"
)

// InsertScannedVideo admits a new scan row only if neither its source identity
// nor a live duplicate is already stored. The caller owns metadata matching and
// attaches automatic tags after admission; only an inserted row needs generation.
// seenFileIDs identifies live files on v.DriveID so stale rows on that same drive
// cannot prevent replacement files from being admitted before presence cleanup.
func (c *Catalog) InsertScannedVideo(ctx context.Context, v *Video, seenFileIDs map[string]struct{}) (bool, error) {
	if v == nil || v.ID == "" || v.DriveID == "" || v.FileID == "" {
		return false, errors.New("catalog: scanned video requires video, drive, and file IDs")
	}
	return withImmediateWriteRetry(ctx, c.db, defaultBusyRetryPolicy, func(conn *sql.Conn) (bool, error) {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM videos WHERE id = ? OR (drive_id = ? AND file_id = ?)
)`, v.ID, v.DriveID, v.FileID).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
		duplicate, err := findScannedVideoDuplicate(ctx, conn, v, seenFileIDs)
		if err != nil {
			return false, err
		}
		if duplicate != nil {
			if err := recordScannedDuplicate(ctx, conn, v, duplicate, DuplicateOutcomeSkipped); err != nil {
				return false, err
			}
			return false, nil
		}
		if _, err := upsertVideoRow(ctx, conn, v); err != nil {
			return false, err
		}
		return true, nil
	})
}

// FindScannedVideoDuplicate applies the same duplicate policy to an existing
// scan row without treating the row itself as a duplicate. New rows must use
// InsertScannedVideo so the decision and insertion share one transaction.
func (c *Catalog) FindScannedVideoDuplicate(ctx context.Context, v *Video, seenFileIDs map[string]struct{}) (*Video, error) {
	return findScannedVideoDuplicate(ctx, c.db, v, seenFileIDs)
}

// RecordScannedDuplicate records the observed match for an already admitted
// row. It does not tombstone the file or change later admission decisions.
func (c *Catalog) RecordScannedDuplicate(ctx context.Context, source, duplicate *Video) error {
	return recordScannedDuplicate(ctx, c.db, source, duplicate, DuplicateOutcomeExisting)
}

func recordScannedDuplicate(ctx context.Context, exec videoRowExecer, source, duplicate *Video, outcome string) error {
	if source == nil || duplicate == nil {
		return errors.New("catalog: scan duplicate requires both videos")
	}
	reason := ""
	if hash := normalizeContentHash(source.ContentHash); hash != "" && hash == normalizeContentHash(duplicate.ContentHash) {
		reason = dedupe.ReasonContentHash
	} else if source.FileName != "" && source.FileName == duplicate.FileName && source.Size > 0 && source.Size == duplicate.Size {
		reason = dedupe.ReasonFileNameSize
	} else {
		return errors.New("catalog: scan duplicate snapshots do not match the scan policy")
	}
	evidence := dedupe.NewEvidence(reason, duplicate.ID, duplicate.ID, "earliest_matching_candidate")
	return recordDuplicateDecision(ctx, exec, DuplicateOriginScan, outcome, source, duplicate, duplicate, duplicate, evidence)
}

type scanDuplicateQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func findScannedVideoDuplicate(ctx context.Context, query scanDuplicateQuerier, v *Video, seenFileIDs map[string]struct{}) (*Video, error) {
	hash := normalizeContentHash(v.ContentHash)
	rows, err := query.QueryContext(ctx, `SELECT `+allVideoCols+` FROM videos
WHERE (? != '' AND content_hash = ?)
   OR (? != '' AND ? > 0 AND file_name = ? AND size_bytes = ?)
ORDER BY CASE WHEN ? != '' AND content_hash = ? THEN 0 ELSE 1 END, created_at ASC, id ASC`,
		hash, hash, v.FileName, v.Size, v.FileName, v.Size, hash, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		candidate, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		if candidate.ID == v.ID {
			continue
		}
		if candidate.DriveID == v.DriveID {
			// Keep looking after stale sources: a later candidate can still be live.
			if _, seen := seenFileIDs[candidate.FileID]; !seen {
				continue
			}
		}
		return candidate, nil
	}
	return nil, rows.Err()
}
