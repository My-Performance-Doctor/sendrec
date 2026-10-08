// Package mpdevents delivers the deployment-managed MPD stream. It never uses
// user notification preferences or destination URLs supplied by API clients.
package mpdevents

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

type Config struct {
	Enabled     bool
	Destination string
	KeyID       string
	SigningKey  []byte
	// Private routing is deployment-owned. User webhook SSRF validation is unchanged.
	AllowHTTPForTests bool
}

type Worker struct {
	db     database.DBTX
	config Config
	client *http.Client
}

func NewWorker(db database.DBTX, c Config) (*Worker, error) {
	if !c.Enabled {
		return &Worker{db: db, config: c}, nil
	}
	u, err := url.Parse(c.Destination)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && (!c.AllowHTTPForTests || u.Scheme != "http")) {
		return nil, errors.New("MPD receiver must be a deployment-approved HTTPS URL")
	}
	if c.KeyID == "" || len(c.SigningKey) < 32 {
		return nil, errors.New("MPD events require a key ID and at least 32 signing-key bytes")
	}
	return &Worker{db: db, config: c, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Signature signs precisely the persisted bytes; only the attempt timestamp changes.
func Signature(key []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// DeliverOne leases one row atomically. A lost response is safely retried with
// the original body/event ID. The consumer owns durable duplicate acceptance.
func (w *Worker) DeliverOne(ctx context.Context) (bool, error) {
	if !w.config.Enabled {
		return false, nil
	}
	var id, lease string
	var body []byte
	var attempts int
	var created time.Time
	err := w.db.QueryRow(ctx, `UPDATE mpd_event_outbox SET state='leased',lease_token=gen_random_uuid(),lease_until=now()+interval '60 seconds',attempts=attempts+1
 WHERE event_id=(SELECT event_id FROM mpd_event_outbox WHERE (state='pending' AND available_at<=now()) OR (state='leased' AND lease_until<now()) ORDER BY available_at,event_id LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING event_id,lease_token,body,attempts,COALESCE(replayed_at,created_at)`).Scan(&id, &lease, &body, &attempts, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Expired work needs explicit operator replay. Do not resume deliveries
	// beyond the retained 24-hour window after a long deployment outage.
	if time.Since(created) >= 24*time.Hour {
		_, err = w.db.Exec(ctx, `UPDATE mpd_event_outbox SET state='dead',lease_token=NULL,lease_until=NULL WHERE event_id=$1 AND lease_token=$2 AND state='leased'`, id, lease)
		slog.Error("mpd-events: delivery requires recovery", "event_id", id, "state", "dead")
		return true, err
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.config.Destination, bytes.NewReader(body))
	if err != nil {
		return true, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SendRec-Delivery-Timestamp", stamp)
	req.Header.Set("X-SendRec-Key-Id", w.config.KeyID)
	req.Header.Set("X-SendRec-Signature", Signature(w.config.SigningKey, stamp, body))
	status := 0
	resp, deliveryErr := w.client.Do(req)
	if resp != nil {
		status = resp.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}
	state := "pending"
	if deliveryErr == nil && status >= 200 && status < 300 {
		state = "acknowledged"
	} else if status >= 400 && status < 500 && status != 408 && status != 429 {
		state = "blocked"
	} else if time.Since(created) >= 24*time.Hour {
		state = "dead"
	}
	delay := time.Duration(1<<min(attempts, 12)) * time.Second
	if delay > time.Hour {
		delay = time.Hour
	}
	_, err = w.db.Exec(ctx, `UPDATE mpd_event_outbox SET state=$3,available_at=now()+make_interval(secs=>$4),lease_token=NULL,lease_until=NULL,last_status=$5,acknowledged_at=CASE WHEN $3='acknowledged' THEN now() ELSE NULL END
 WHERE event_id=$1 AND lease_token=$2 AND state='leased'`, id, lease, state, delay.Seconds(), status)
	if state == "blocked" || state == "dead" {
		slog.Error("mpd-events: delivery requires recovery", "event_id", id, "state", state, "status", status)
	}
	return true, err
}

// Replay is an operator operation, deliberately not exposed to managed staff.
// It cannot alter immutable evidence or duplicate an acknowledged event blindly.
func Replay(ctx context.Context, db database.DBTX, eventID string) error {
	tag, err := db.Exec(ctx, `UPDATE mpd_event_outbox SET state='pending',available_at=now(),replayed_at=now(),attempts=0,lease_token=NULL,lease_until=NULL WHERE event_id=$1 AND state IN ('dead','blocked')`, eventID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("event is not retained dead or blocked work")
	}
	return nil
}

type Backlog struct {
	Pending       int
	Blocked       int
	Dead          int
	OldestSeconds float64
}

func Inspect(ctx context.Context, db database.DBTX) (Backlog, error) {
	var b Backlog
	err := db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state IN ('pending','leased')),count(*) FILTER(WHERE state='blocked'),count(*) FILTER(WHERE state='dead'),COALESCE(EXTRACT(EPOCH FROM now()-min(created_at) FILTER(WHERE state<>'acknowledged')),0)::double precision FROM mpd_event_outbox`).Scan(&b.Pending, &b.Blocked, &b.Dead, &b.OldestSeconds)
	return b, err
}
func (w *Worker) Run(ctx context.Context) {
	if !w.config.Enabled {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for range 20 {
				worked, err := w.DeliverOne(ctx)
				if err != nil {
					slog.Error("mpd-events: worker database failure")
					break
				}
				if !worked {
					break
				}
			}
			if b, err := Inspect(ctx, w.db); err == nil {
				slog.Info("mpd-events: backlog", "pending", b.Pending, "blocked", b.Blocked, "dead", b.Dead, "oldest_seconds", b.OldestSeconds)
			}
		}
	}
}

func (w *Worker) String() string {
	return fmt.Sprintf("MPD event delivery enabled=%t", w.config.Enabled)
}
