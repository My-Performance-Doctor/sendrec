package video

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mssola/useragent"
	"github.com/sendrec/sendrec/internal/webhook"
)

func (h *Handler) dispatchWebhook(userID string, event webhook.Event) {
	dispatchWebhookEvent(h.webhookClient, userID, event)
}

// dispatchWebhookEvent sends event to the user's webhook in the background.
// A nil client sends nothing.
func dispatchWebhookEvent(client *webhook.Client, userID string, event webhook.Event) {
	if client == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		webhookURL, secret, err := client.LookupConfigByUserID(ctx, userID)
		if err != nil {
			return
		}
		if err := client.Dispatch(ctx, userID, webhookURL, secret, event); err != nil {
			slog.Error("webhook: dispatch failed", "user_id", userID, "event", event.Name, "error", err)
		}
	}()
}

func deleteWithRetry(ctx context.Context, storage ObjectStorage, key string, maxAttempts int) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		lastErr = storage.DeleteObject(ctx, key)
		if lastErr == nil {
			return nil
		}
		slog.Error("storage: delete attempt failed", "attempt", attempt+1, "max_attempts", maxAttempts, "key", key, "error", lastErr)
	}
	return fmt.Errorf("all %d delete attempts failed for %s: %w", maxAttempts, key, lastErr)
}

// videoReadyHook sends video.ready. The zero value sends nothing, for
// installs and tests without webhooks.
type videoReadyHook struct {
	client  *webhook.Client
	baseURL string
}

func (r videoReadyHook) send(userID, videoID, shareToken string, duration int) {
	dispatchWebhookEvent(r.client, userID, webhook.Event{
		Name:      "video.ready",
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"videoId":    videoID,
			"duration":   duration,
			"shareToken": shareToken,
			"watchUrl":   r.baseURL + "/watch/" + shareToken,
		},
	})
}

func (h *Handler) dispatchVideoReady(userID, videoID, shareToken string, duration int) {
	videoReadyHook{h.webhookClient, h.baseURL}.send(userID, videoID, shareToken, duration)
}

// dispatchVideoReadyIfReady sends video.ready for a video a background job
// has just finished, provided the job left it watchable.
func (h *Handler) dispatchVideoReadyIfReady(ctx context.Context, videoID string) {
	if h.webhookClient == nil {
		return
	}
	var userID, shareToken string
	var duration int
	if err := h.db.QueryRow(ctx,
		`SELECT user_id, share_token, duration FROM videos WHERE id = $1 AND status = 'ready'`, videoID,
	).Scan(&userID, &shareToken, &duration); err != nil {
		return
	}
	h.dispatchVideoReady(userID, videoID, shareToken, duration)
}

func viewerHash(ip, userAgent string) string {
	h := sha256.Sum256([]byte(ip + "|" + userAgent))
	return fmt.Sprintf("%x", h[:8])
}

func categorizeReferrer(referer string) string {
	if referer == "" {
		return "Direct"
	}
	lower := strings.ToLower(referer)
	switch {
	case strings.Contains(lower, "mail") || strings.Contains(lower, "outlook") || strings.Contains(lower, "proton"):
		return "Email"
	case strings.Contains(lower, "slack"):
		return "Slack"
	case strings.Contains(lower, "twitter") || strings.Contains(lower, "x.com"):
		return "Twitter"
	case strings.Contains(lower, "linkedin"):
		return "LinkedIn"
	default:
		return "Other"
	}
}

func parseBrowser(userAgent string) string {
	if userAgent == "" {
		return "Other"
	}
	ua := useragent.New(userAgent)
	name, _ := ua.Browser()
	switch {
	case strings.Contains(name, "Edge"):
		return "Edge"
	case strings.Contains(name, "Chrome"):
		return "Chrome"
	case strings.Contains(name, "Firefox"):
		return "Firefox"
	case strings.Contains(name, "Safari"):
		return "Safari"
	default:
		return "Other"
	}
}

func parseDevice(userAgent string) string {
	if userAgent == "" {
		return "Desktop"
	}
	ua := useragent.New(userAgent)
	lower := strings.ToLower(userAgent)
	if ua.Mobile() {
		if strings.Contains(lower, "ipad") || strings.Contains(lower, "tablet") ||
			(strings.Contains(lower, "android") && !strings.Contains(lower, "mobile")) {
			return "Tablet"
		}
		return "Mobile"
	}
	return "Desktop"
}
