package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sendrec/sendrec/internal/mpdevents"
)

// TestMPDCrossServiceHarness runs only when the external synthetic pytest drill
// explicitly requests it. Neither these controls nor this server ship in Go's
// production binary. All storage and identity values are synthetic.
func TestMPDCrossServiceHarness(t *testing.T) {
	output := os.Getenv("SENDREC_CROSS_SERVICE_OUTPUT")
	if output == "" {
		t.Skip("external cross-service drill not requested")
	}
	receiver := os.Getenv("SENDREC_CROSS_SERVICE_RECEIVER")
	for _, raw := range []string{receiver, os.Getenv("TEST_DATABASE_URL")} {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() != "127.0.0.1" {
			t.Fatal("cross-service drill requires explicit loopback endpoints")
		}
	}
	pool := accountDB(t)
	user := mustID(t, pool, `INSERT INTO users(email,password,name) VALUES('drill@example.test','','Synthetic') RETURNING id`)
	org := mustID(t, pool, `INSERT INTO organizations(name,slug) VALUES('Synthetic','drill') RETURNING id`)
	tenant, staff := os.Getenv("SENDREC_CROSS_SERVICE_TENANT"), os.Getenv("SENDREC_CROSS_SERVICE_STAFF")
	mustExec(t, pool, `INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,$2,'https://issuer.example.test',true,10)`, org, tenant)
	mustExec(t, pool, `INSERT INTO mpd_external_identities(issuer,subject,user_id,organization_id,staff_id,tenant_id) VALUES('https://issuer.example.test','synthetic',$1,$2,$3,$4)`, user, org, staff, tenant)
	id := mustID(t, pool, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status) VALUES($1,$2,'Synthetic','synthetic.mp4','synthetic','ready') RETURNING id`, user, org)
	mustExec(t, pool, `UPDATE videos SET transcript_generation=1,transcript_published_generation=1,transcript_status='ready',transcript_key='synthetic-1.vtt',transcript_json='[{"start":0,"end":1,"text":"Synthetic first caption"}]' WHERE id=$1`, id)
	router := chi.NewRouter()
	server := httptest.NewServer(router)
	defer server.Close()
	h := NewHandler(pool, newMemStorage(map[string]string{"synthetic.mp4": "synthetic"}), server.URL, 1000000, 0, 0, 0, "synthetic-hmac-key", true)
	sum := sha256.Sum256([]byte(strings.Repeat("s", 32)))
	adapter := h.MPDAdapter(MPDServiceConfig{Enabled: true, WorkspaceID: org, TenantID: tenant, TokenHashes: []string{hex.EncodeToString(sum[:])}, RequirePassword: true})
	router.Route("/api/integrations/mpd/videos", func(r chi.Router) {
		r.Use(adapter.Authenticate)
		r.Get("/{videoId}", adapter.Metadata)
		r.Get("/{videoId}/transcript", adapter.Transcript)
		r.Put("/{videoId}/password", adapter.Password)
		r.Put("/{videoId}/publication", adapter.Publication)
	})
	cfg := mpdevents.Config{Enabled: true, Destination: receiver, KeyID: "current", SigningKey: []byte("synthetic-signing-key-000000000000"), AllowHTTPForTests: true}
	done := make(chan struct{})
	var once sync.Once
	router.Post("/control/{operation}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-control" {
			w.WriteHeader(401)
			return
		}
		var err error
		switch chi.URLParam(r, "operation") {
		case "deliver":
			// Fresh worker proves that queued evidence does not belong to one process.
			var worker *mpdevents.Worker
			worker, err = mpdevents.NewWorker(pool, cfg)
			if err == nil {
				_, err = pool.Exec(r.Context(), `UPDATE mpd_event_outbox SET available_at=now() WHERE state='pending'`)
			}
			if err == nil {
				for range 50 {
					var worked bool
					worked, err = worker.DeliverOne(r.Context())
					if err != nil || !worked {
						break
					}
				}
			}
		case "preview", "anonymous":
			var s mpdevents.Session
			s, err = mpdevents.NewSession(r.Context(), pool, id, chi.URLParam(r, "operation") == "preview")
			if err == nil {
				p := mpdevents.Progress{SessionID: s.ID, Playing: true, CurrentTime: 0.5, ElapsedMilliseconds: 500}
				err = mpdevents.Accept(r.Context(), pool, id, p)
				if err == nil {
					err = mpdevents.Accept(r.Context(), pool, id, p)
				}
			}
		case "transcript":
			_, err = pool.Exec(r.Context(), `UPDATE videos SET transcript_generation=2,transcript_published_generation=2,transcript_key='synthetic-2.vtt',transcript_json='[{"start":0,"end":1,"text":"Synthetic updated caption"}]' WHERE id=$1`, id)
		case "delete":
			_, err = pool.Exec(r.Context(), `UPDATE videos SET status='deleted' WHERE id=$1`, id)
		case "stop":
			once.Do(func() { close(done) })
		default:
			w.WriteHeader(404)
			return
		}
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	info, _ := json.Marshal(map[string]string{"origin": server.URL, "videoId": id})
	if err := os.WriteFile(output, info, 0644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		t.Fatal("cross-service driver did not finish")
	}
}
