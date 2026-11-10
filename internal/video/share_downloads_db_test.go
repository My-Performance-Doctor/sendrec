package video

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Share links never offer a download, so no recording can turn one on.
func TestShareDownloadsStayOff(t *testing.T) {
	pool := accountDB(t)
	user := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ('share-downloads@example.com', 'x', 'Synthetic') RETURNING id`)
	video := mustID(t, pool, `INSERT INTO videos (user_id, title, file_key, share_token, status) VALUES ($1, 'v', 'share-downloads/file.mp4', 'share-downloads-tok', 'ready') RETURNING id`, user)

	if exists(t, pool, `SELECT 1 FROM videos WHERE id = $1 AND download_enabled`, video) {
		t.Fatal("a new recording has share-link downloads on")
	}
	_, err := pool.Exec(context.Background(), `UPDATE videos SET download_enabled = true WHERE id = $1`, video)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "videos_share_downloads_off" {
		t.Fatalf("turning share-link downloads on: got %v, want the videos_share_downloads_off check to reject it", err)
	}
}
