package catalog

import (
	"context"
	"encoding/json"

	"github.com/video-site/backend/internal/uploadjob"
)

// CrawlerUploadScope fixes the insertion boundary for one sweep. Updating a
// video's drive after upload cannot shift subsequent pages, and newly crawled
// rows wait for the next sweep.
func (c *Catalog) CrawlerUploadScope(ctx context.Context, driveID string) (beforeRowID int64, count int, err error) {
	err = c.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid), 0), COUNT(*) FROM videos
WHERE drive_id = ? AND COALESCE(hidden, 0) = 0`, driveID).Scan(&beforeRowID, &count)
	return
}

func (c *Catalog) ListCrawlerUploadCandidates(ctx context.Context, driveID, afterID string, beforeRowID int64, limit int) ([]*Video, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := c.db.QueryContext(ctx, `SELECT `+allVideoCols+` FROM videos
WHERE drive_id = ? AND COALESCE(hidden, 0) = 0 AND id > ? AND rowid <= ?
ORDER BY id LIMIT ?`, driveID, afterID, beforeRowID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var videos []*Video
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		videos = append(videos, v)
	}
	return videos, rows.Err()
}

func (c *Catalog) SaveCrawlerUploadResult(ctx context.Context, result uploadjob.Result) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx, `INSERT INTO crawler_upload_results (drive_id, result)
SELECT ?, ? WHERE EXISTS (SELECT 1 FROM drives WHERE id = ?)
ON CONFLICT(drive_id) DO UPDATE SET result = excluded.result`, result.DriveID, string(payload), result.DriveID)
	return err
}

func (c *Catalog) LatestCrawlerUploadResults(ctx context.Context) (map[string]uploadjob.Result, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT result FROM crawler_upload_results`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]uploadjob.Result)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var result uploadjob.Result
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			return nil, err
		}
		out[result.DriveID] = result
	}
	return out, rows.Err()
}
