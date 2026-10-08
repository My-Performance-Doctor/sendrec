package mpd

import (
	"context"
	"fmt"
	"github.com/sendrec/sendrec/internal/database"
)

// CleanupExpired bounds each deletion batch and leaves identities, audit rows,
// live sessions and media intact. Run even when managed sign-in is disabled.
func CleanupExpired(ctx context.Context, db database.DBTX, batch int) error {
	if batch < 1 || batch > 10000 {
		return fmt.Errorf("identity cleanup batch must be 1-10000")
	}
	for _, q := range []string{
		`DELETE FROM mpd_preview_handoffs WHERE token_hash IN (SELECT token_hash FROM mpd_preview_handoffs WHERE expires_at<=now() LIMIT $1 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM mpd_preview_sessions WHERE token_hash IN (SELECT token_hash FROM mpd_preview_sessions WHERE expires_at<=now() LIMIT $1 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM mpd_playback_sessions WHERE session_id IN (SELECT session_id FROM mpd_playback_sessions WHERE expires_at<=now() LIMIT $1 FOR UPDATE SKIP LOCKED)`,

		`DELETE FROM mpd_login_transactions WHERE state_hash IN (SELECT state_hash FROM mpd_login_transactions WHERE expires_at<=now() LIMIT $1 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM mpd_login_handoffs WHERE code_hash IN (SELECT code_hash FROM mpd_login_handoffs WHERE expires_at<=now() LIMIT $1 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM mpd_sessions WHERE id IN (SELECT id FROM mpd_sessions WHERE expires_at<=now() OR revoked LIMIT $1 FOR UPDATE SKIP LOCKED)`,
	} {
		if _, err := db.Exec(ctx, q, batch); err != nil {
			return fmt.Errorf("identity cleanup failed: %w", err)
		}
	}
	return nil
}
