package video

import (
	"context"
	"errors"
	"time"

	"github.com/sendrec/sendrec/internal/mpd"
)

type immutableUploadStorage interface {
	GenerateImmutableUploadURL(context.Context, string, string, int64, time.Duration) (string, error)
}

func (h *Handler) recordingUploadURL(ctx context.Context, key, content string, size int64) (string, map[string]string, error) {
	if _, managed := mpd.PrincipalFromContext(ctx); !managed {
		signed, err := h.storage.GenerateUploadURL(ctx, key, content, size, 30*time.Minute)
		return signed, nil, err
	}
	immutable, ok := h.storage.(immutableUploadStorage)
	if !ok {
		return "", nil, errors.New("managed recordings require write-once upload storage")
	}
	signed, err := immutable.GenerateImmutableUploadURL(ctx, key, content, size, 30*time.Minute)
	if err != nil {
		return "", nil, err
	}
	return signed, map[string]string{"If-None-Match": "*"}, nil
}

func (s boundedMPDStorage) GenerateImmutableUploadURL(ctx context.Context, key, content string, size int64, expiry time.Duration) (string, error) {
	immutable, ok := s.ObjectStorage.(immutableUploadStorage)
	if !ok {
		return "", errors.New("managed recordings require write-once upload storage")
	}
	return immutable.GenerateImmutableUploadURL(ctx, key, content, size, expiry)
}

// A lost finalize response may be retried without rescheduling work. Only the
// same authenticated owner/workspace and an existing finalized row qualify.
func (h *Handler) managedFinalizeCompleted(ctx context.Context, videoID, userID string) (bool, error) {
	principal, managed := mpd.PrincipalFromContext(ctx)
	if !managed || principal.UserID != userID {
		return false, nil
	}
	var completed bool
	err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM videos v JOIN mpd_video_state s ON s.video_id=v.id WHERE v.id=$1 AND v.user_id=$2 AND v.organization_id=$3 AND v.status IN ('ready','processing') AND s.deleted_at IS NULL)`, videoID, userID, principal.OrganizationID).Scan(&completed)
	return completed, err
}
