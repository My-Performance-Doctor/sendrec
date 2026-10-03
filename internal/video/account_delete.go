package video

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sendrec/sendrec/internal/auth"
	"github.com/sendrec/sendrec/internal/httputil"
)

// SubscriptionCanceler cancels a paid subscription. The billing package's
// Creem client satisfies it; it is nil when billing is not configured.
type SubscriptionCanceler interface {
	CancelSubscription(ctx context.Context, subscriptionID string) error
}

func (h *Handler) SetSubscriptionCanceler(c SubscriptionCanceler) {
	h.subscriptionCanceler = c
}

// DeleteAccount permanently deletes the signed-in user, their videos and
// stored files, and any workspace they own alone. It is ordered so that
// nothing is removed until it is safe to remove everything:
//
//  1. Refuse while they own a workspace other people use: deleting it would
//     take those people's work with it.
//  2. Cancel their subscriptions, personal and their own workspaces'. A paying
//     customer must not be charged after leaving, so a failure stops here.
//  3. Delete their stored objects. The rows that name them go in step 4, and
//     nothing could find the objects afterwards.
//  4. Delete the rows, ending with the user. Analytics tables and a few other
//     references do not cascade, so those rows go first.
//
// Each step can be repeated, so a request that fails part-way can be retried.
func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := auth.UserIDFromContext(ctx)

	shared, err := h.queryStrings(ctx,
		`SELECT o.name FROM organizations o
		 JOIN organization_members m ON m.organization_id = o.id AND m.user_id = $1 AND m.role = 'owner'
		 WHERE EXISTS (SELECT 1 FROM organization_members x WHERE x.organization_id = o.id AND x.user_id <> $1)
		 ORDER BY o.name`, userID)
	if err != nil {
		slog.Error("delete-account: failed to check workspaces", "user_id", userID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete account")
		return
	}
	if len(shared) > 0 {
		httputil.WriteError(w, http.StatusConflict, fmt.Sprintf(
			"You own workspaces other people use: %s. Transfer ownership or remove the other members before deleting your account.",
			strings.Join(shared, ", ")))
		return
	}

	ownOrgs, err := h.queryStrings(ctx,
		`SELECT organization_id::text FROM organization_members WHERE user_id = $1 AND role = 'owner'`, userID)
	if err != nil {
		slog.Error("delete-account: failed to list own workspaces", "user_id", userID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete account")
		return
	}

	subscriptions, err := h.queryStrings(ctx,
		`SELECT creem_subscription_id FROM users WHERE id = $1 AND creem_subscription_id IS NOT NULL
		 UNION ALL
		 SELECT creem_subscription_id FROM organizations WHERE id = ANY($2::uuid[]) AND creem_subscription_id IS NOT NULL`,
		userID, ownOrgs)
	if err != nil {
		slog.Error("delete-account: failed to list subscriptions", "user_id", userID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete account")
		return
	}
	if len(subscriptions) > 0 && h.subscriptionCanceler == nil {
		slog.Error("delete-account: subscription to cancel but billing is not configured", "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, "your subscription could not be canceled; nothing was deleted")
		return
	}
	for _, id := range subscriptions {
		if err := h.subscriptionCanceler.CancelSubscription(ctx, id); err != nil {
			slog.Error("delete-account: failed to cancel subscription", "user_id", userID, "subscription_id", id, "error", err)
			httputil.WriteError(w, http.StatusBadGateway, "your subscription could not be canceled; nothing was deleted. Please try again")
			return
		}
	}

	keys, err := h.queryStrings(ctx,
		`SELECT k FROM videos v
		 CROSS JOIN LATERAL unnest(ARRAY[v.file_key, v.thumbnail_key, v.transcript_key, v.webcam_key, v.branding_logo_key]) AS k
		 WHERE (v.user_id = $1 OR v.organization_id = ANY($2::uuid[])) AND v.file_purged_at IS NULL AND k IS NOT NULL
		 UNION
		 SELECT logo_key FROM user_branding
		 WHERE ((user_id = $1 AND organization_id IS NULL) OR organization_id = ANY($2::uuid[])) AND logo_key IS NOT NULL`,
		userID, ownOrgs)
	if err != nil {
		slog.Error("delete-account: failed to list stored files", "user_id", userID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to delete account")
		return
	}
	// Best effort, like every other purge: an object that will not go is
	// logged, and the account is still deleted as the user asked.
	purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	for _, key := range keys {
		if err := deleteWithRetry(purgeCtx, h.storage, key, 3); err != nil {
			slog.Error("delete-account: failed to delete object", "user_id", userID, "key", key, "error", err)
		}
	}

	videos := `SELECT id FROM videos WHERE user_id = $1 OR organization_id = ANY($2::uuid[])`
	both := []any{userID, ownOrgs}
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM video_views WHERE video_id IN (` + videos + `)`, both},
		{`DELETE FROM view_milestones WHERE video_id IN (` + videos + `)`, both},
		{`DELETE FROM segment_engagement WHERE video_id IN (` + videos + `)`, both},
		{`DELETE FROM cta_clicks WHERE video_id IN (` + videos + `)`, both},
		{`DELETE FROM videos WHERE user_id = $1 OR organization_id = ANY($2::uuid[])`, both},
		{`DELETE FROM notification_preferences WHERE user_id = $1`, []any{userID}},
		{`DELETE FROM organization_invites WHERE invited_by = $1`, []any{userID}},
		{`DELETE FROM organizations WHERE id = ANY($1::uuid[])`, []any{ownOrgs}},
		{`DELETE FROM users WHERE id = $1`, []any{userID}},
	} {
		if _, err := h.db.Exec(purgeCtx, step.sql, step.args...); err != nil {
			slog.Error("delete-account: failed to delete rows", "user_id", userID, "statement", step.sql, "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, "failed to delete account; please try again")
			return
		}
	}

	slog.Info("delete-account: account deleted", "user_id", userID, "videos_objects", len(keys), "workspaces", len(ownOrgs), "subscriptions", len(subscriptions))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) queryStrings(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := h.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
