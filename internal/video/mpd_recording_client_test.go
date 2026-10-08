package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/auth"
	"github.com/sendrec/sendrec/internal/mpd"
	objectstore "github.com/sendrec/sendrec/internal/storage"
)

// syntheticRecordingIdentity represents an already verified MPD grant. This
// client drill tests handler/identity-context boundaries, not Cognito login.
type syntheticRecordingIdentity struct {
	db        *pgxpool.Pool
	principal *mpd.Principal
}

func (s syntheticRecordingIdentity) ValidateSession(ctx context.Context, id string) (*mpd.Principal, error) {
	var active bool
	err := s.db.QueryRow(ctx, `SELECT NOT revoked AND expires_at>now() FROM mpd_sessions WHERE id=$1`, id).Scan(&active)
	if err != nil || !active || id != s.principal.SessionID {
		return nil, mpd.ErrDenied
	}
	return s.principal, nil
}

type recordingObjectGrant struct {
	key, content, method string
	immutable            bool
	expires              time.Time
}
type recordingHTTPStorage struct {
	*memStorage
	origin    string
	types     map[string]string
	grants    map[string]recordingObjectGrant
	headError error
}

func (s *recordingHTTPStorage) grant(key, content, method string, expiry time.Duration, immutable bool) string {
	proof := randomProof()
	s.mu.Lock()
	s.grants[proof] = recordingObjectGrant{key, content, method, immutable, time.Now().Add(expiry)}
	s.mu.Unlock()
	return s.origin + "/objects/" + proof
}
func (s *recordingHTTPStorage) GenerateUploadURL(_ context.Context, key, content string, _ int64, expiry time.Duration) (string, error) {
	return s.grant(key, content, "PUT", expiry, false), nil
}
func (s *recordingHTTPStorage) GenerateImmutableUploadURL(_ context.Context, key, content string, _ int64, expiry time.Duration) (string, error) {
	return s.grant(key, content, "PUT", expiry, true), nil
}
func (s *recordingHTTPStorage) GenerateDownloadURL(_ context.Context, key string, expiry time.Duration) (string, error) {
	return s.grant(key, "", "GET", expiry, false), nil
}
func (s *recordingHTTPStorage) HeadObject(_ context.Context, key string) (int64, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.headError != nil {
		return 0, "", s.headError
	}
	body, ok := s.objects[key]
	if !ok {
		return 0, "", objectstore.ErrObjectNotFound
	}
	return int64(len(body)), s.types[key], nil
}
func (s *recordingHTTPStorage) DownloadToFile(context.Context, string, string) error {
	return fmt.Errorf("media processing intentionally outside synthetic HTTP client drill")
}
func (s *recordingHTTPStorage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	proof := chi.URLParam(r, "proof")
	s.mu.Lock()
	grant, ok := s.grants[proof]
	s.mu.Unlock()
	if !ok || grant.method != r.Method || time.Now().After(grant.expires) {
		w.WriteHeader(403)
		return
	}
	if r.Method == "PUT" {
		if grant.immutable && r.Header.Get("If-None-Match") != "*" {
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("Content-Type") != grant.content {
			w.WriteHeader(400)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 100000))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		s.mu.Lock()
		if _, exists := s.objects[grant.key]; grant.immutable && exists {
			s.mu.Unlock()
			w.WriteHeader(412)
			return
		}
		s.objects[grant.key] = string(body)
		s.types[grant.key] = grant.content
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	s.mu.Lock()
	body, ok := s.objects[grant.key]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(404)
		return
	}
	_, _ = io.WriteString(w, body)
}

func TestMPDStandaloneRecordingClientHTTP(t *testing.T) {
	t.Setenv("TRANSCRIPTION_ENABLED", "false")
	for _, format := range []string{"video/mp4", "video/webm"} {
		t.Run(format, func(t *testing.T) {
			db := accountDB(t)
			_, user, org := seedManagedLifecycle(t, db, "ready")
			sid := mustID(t, db, `INSERT INTO mpd_sessions(user_id,access_encrypted,classification_hash,cognito_expires_at,expires_at,grant_json,grant_expires_at) VALUES($1,'synthetic-unused','synthetic-classification',now()+interval '1 hour',now()+interval '1 hour','{}',now()+interval '30 seconds') RETURNING id`, user)
			principal := &mpd.Principal{UserID: user, OrganizationID: org, StaffID: "22222222-2222-4222-8222-222222222222", TenantID: "33333333-3333-4333-8333-333333333333", SessionID: sid, Capabilities: mpd.Capabilities{Record: true, Read: true, ManageOwn: true, ReadPassword: true}}
			identity := syntheticRecordingIdentity{db, principal}
			router := chi.NewRouter()
			server := httptest.NewServer(router)
			server.Client().Timeout = 10 * time.Second
			defer server.Close()
			storage := &recordingHTTPStorage{memStorage: newMemStorage(map[string]string{}), origin: server.URL, types: map[string]string{}, grants: map[string]recordingObjectGrant{}}
			h := NewHandler(db, storage, server.URL, 1000000, 0, 0, 0, "synthetic-key", false)
			h.BoundMPDMediaURLs()
			media := h.MPDMedia(identity, MPDServiceConfig{Enabled: true, WorkspaceID: org, TenantID: principal.TenantID, RequirePassword: true}, nil)
			router.Method("PUT", "/objects/{proof}", storage)
			router.Method("GET", "/objects/{proof}", storage)
			router.Group(func(r chi.Router) {
				r.Use(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Authorization") != "Bearer synthetic-staff" {
							w.WriteHeader(401)
							return
						}
						p, err := identity.ValidateSession(r.Context(), sid)
						if err != nil {
							w.WriteHeader(403)
							return
						}
						ctx := mpd.ContextWithPrincipal(auth.ContextWithOrg(auth.ContextWithUserID(r.Context(), p.UserID), p.OrganizationID, p.Capabilities.Role()), p)
						next.ServeHTTP(w, r.WithContext(ctx))
					})
				})
				r.Post("/api/videos", h.Create)
				r.Get("/api/videos", h.List)
				r.Patch("/api/videos/{id}", func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Synthetic-Drop-Acknowledgement") != "1" {
						h.Update(w, r)
						return
					}
					captured := httptest.NewRecorder()
					h.Update(captured, r)
					if captured.Code != 204 {
						w.WriteHeader(captured.Code)
						_, _ = w.Write(captured.Body.Bytes())
						return
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				})
				r.Post("/api/videos/{id}/upload-url", h.RenewUpload)
				r.Get("/api/videos/{id}/mpd", media.Info)
				r.Post("/api/videos/{id}/mpd/preview", media.CreatePreview)
				r.Post("/api/videos/{id}/transcript", h.UploadTranscript)
			})
			router.Post("/api/mpd/preview/redeem", media.RedeemPreview)
			router.Get("/api/mpd/preview/media", media.PreviewMedia)
			router.Post("/api/mpd/preview/playback/session", media.PreviewPlayback)
			router.Post("/api/mpd/preview/playback/start", media.PreviewPlayback)
			router.With(h.MPDPublicAccess).Get("/watch/{shareToken}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
			call := func(method, path, body, token string, want int) map[string]any {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", server.URL)
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				raw, _ := io.ReadAll(response.Body)
				if response.StatusCode != want {
					t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, raw)
				}
				var result map[string]any
				if len(raw) > 0 && raw[0] == '{' {
					if err = json.Unmarshal(raw, &result); err != nil {
						t.Fatal(err)
					}
				}
				return result
			}
			const body = "synthetic recording bytes"
			createBody := fmt.Sprintf(`{"title":"Synthetic","duration":10,"fileSize":%d,"contentType":%q}`, len(body), format)
			call("POST", "/api/videos", createBody, "", 401)
			created := call("POST", "/api/videos", createBody, "synthetic-staff", 201)
			if headers, ok := created["uploadHeaders"].(map[string]any); !ok || headers["If-None-Match"] != "*" {
				t.Fatal("managed conditional header absent", created)
			}
			if created["managed"] != true || created["mediaVersion"] != float64(1) || created["published"] != false {
				t.Fatal(created)
			}
			id, share := created["id"].(string), created["shareToken"].(string)
			// An independent client can recover the existing pending record and renew
			// its object URL without creating another recording.
			listReq, _ := http.NewRequest("GET", server.URL+"/api/videos", nil)
			listReq.Header.Set("Authorization", "Bearer synthetic-staff")
			listResponse, err := server.Client().Do(listReq)
			if err != nil {
				t.Fatal(err)
			}
			var listed []listItem
			raw, _ := io.ReadAll(listResponse.Body)
			_ = listResponse.Body.Close()
			if listResponse.StatusCode != 200 || json.Unmarshal(raw, &listed) != nil {
				t.Fatal(string(raw))
			}
			found := false
			for _, item := range listed {
				if item.ID == id {
					found = item.Status == "uploading" && item.Managed && item.MediaVersion == 1 && !item.Published && item.ShareURL == ""
				}
			}
			if !found {
				t.Fatalf("pending managed recording absent: %s", raw)
			}
			call("PATCH", "/api/videos/"+id, `{"status":"ready"}`, "synthetic-staff", 400)
			storage.mu.Lock()
			storage.headError = fmt.Errorf("synthetic storage outage")
			storage.mu.Unlock()
			call("POST", "/api/videos/"+id+"/upload-url", fmt.Sprintf(`{"kind":"screen","fileSize":%d,"contentType":%q}`, len(body), format), "synthetic-staff", 503)
			storage.mu.Lock()
			storage.headError = nil
			storage.mu.Unlock()
			renewed := call("POST", "/api/videos/"+id+"/upload-url", fmt.Sprintf(`{"kind":"screen","fileSize":%d,"contentType":%q}`, len(body), format), "synthetic-staff", 200)
			put, _ := http.NewRequest("PUT", renewed["uploadUrl"].(string), strings.NewReader(body))
			put.Header.Set("Content-Type", format)
			put.Header.Set("If-None-Match", "*")
			uploaded, err := server.Client().Do(put)
			if err != nil {
				t.Fatal(err)
			}
			_ = uploaded.Body.Close()
			if uploaded.StatusCode != 204 {
				t.Fatal("synthetic object upload failed")
			}
			if headers, ok := renewed["uploadHeaders"].(map[string]any); !ok || headers["If-None-Match"] != "*" {
				t.Fatal("renewal conditional header absent")
			}
			// Existing inconsistent content must stop recovery instead of inviting overwrite.
			originalProof := strings.TrimPrefix(created["uploadUrl"].(string), server.URL+"/objects/")
			storage.mu.Lock()
			objectKey := storage.grants[originalProof].key
			storage.types[objectKey] = "application/octet-stream"
			storage.mu.Unlock()
			call("POST", "/api/videos/"+id+"/upload-url", fmt.Sprintf(`{"kind":"screen","fileSize":%d,"contentType":%q}`, len(body), format), "synthetic-staff", 409)
			storage.mu.Lock()
			storage.types[objectKey] = format
			storage.mu.Unlock()
			already := call("POST", "/api/videos/"+id+"/upload-url", fmt.Sprintf(`{"kind":"screen","fileSize":%d,"contentType":%q}`, len(body), format), "synthetic-staff", 200)
			if already["uploaded"] != true || already["uploadUrl"] != nil {
				t.Fatal("lost upload acknowledgement was not recovered", already)
			}
			replayPut := func(condition string, want int) {
				request, _ := http.NewRequest("PUT", created["uploadUrl"].(string), strings.NewReader(strings.Repeat("x", len(body))))
				request.Header.Set("Content-Type", format)
				if condition != "" {
					request.Header.Set("If-None-Match", condition)
				}
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("PUT replay %d want %d", response.StatusCode, want)
				}
			}
			replayPut("", 403)
			replayPut("*", 412)
			lostFinalize, _ := http.NewRequest("PATCH", server.URL+"/api/videos/"+id, strings.NewReader(`{"status":"ready"}`))
			lostFinalize.Header.Set("Content-Type", "application/json")
			lostFinalize.Header.Set("Authorization", "Bearer synthetic-staff")
			lostFinalize.Header.Set("X-Synthetic-Drop-Acknowledgement", "1")
			lostResponse, lostErr := server.Client().Do(lostFinalize)
			if lostResponse != nil {
				_ = lostResponse.Body.Close()
			}
			if lostErr == nil {
				t.Fatal("finalize acknowledgement was not lost")
			}
			call("PATCH", "/api/videos/"+id, `{"status":"ready"}`, "synthetic-staff", 204)
			replayPut("*", 412)
			if !exists(t, db, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND event_type='video.ready' GROUP BY video_id HAVING count(*)=1`, id) {
				t.Fatal("finalize retry duplicated readiness")
			}

			call("POST", "/api/videos/"+id+"/upload-url", fmt.Sprintf(`{"kind":"screen","fileSize":%d,"contentType":%q}`, len(body), format), "synthetic-staff", 409)
			info := call("GET", "/api/videos/"+id+"/mpd", "", "synthetic-staff", 200)["metadata"].(map[string]any)
			if info["mediaVersion"] != float64(1) || info["status"] != "ready" || info["published"] != false || info["ownerStaffId"] != principal.StaffID {
				t.Fatal(info)
			}
			call("GET", "/watch/"+share, "", "", 404)
			preview := call("POST", "/api/videos/"+id+"/mpd/preview", fmt.Sprintf(`{"origin":%q,"nonce":"synthetic-preview-nonce"}`, server.URL), "synthetic-staff", 201)
			redeemBody := fmt.Sprintf(`{"handoff":%q,"nonce":"synthetic-preview-nonce","parentOrigin":%q}`, preview["handoff"], server.URL)
			redeemed := call("POST", "/api/mpd/preview/redeem", redeemBody, "", 200)
			previewToken := redeemed["previewToken"].(string)
			call("POST", "/api/mpd/preview/redeem", redeemBody, "", 403)
			previewMedia := call("GET", "/api/mpd/preview/media", "", previewToken, 200)
			response, err := server.Client().Get(previewMedia["videoUrl"].(string))
			if err != nil {
				t.Fatal(err)
			}
			downloaded, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 || string(downloaded) != body {
				t.Fatal("protected preview did not return uploaded bytes")
			}
			session := call("POST", "/api/mpd/preview/playback/session", `{}`, previewToken, 201)
			call("POST", "/api/mpd/preview/playback/start", fmt.Sprintf(`{"playbackSessionId":%q,"playing":true,"previousTime":0,"currentTime":0.5,"elapsedMilliseconds":500}`, session["playbackSessionId"]), previewToken, 204)
			if !exists(t, db, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND event_type='video.playback_started' AND convert_from(body,'UTF8')::jsonb->'data'->>'viewerClass'='staff_preview'`, id) {
				t.Fatal("protected preview missing staff classification")
			}
			// Upload captions through the same authenticated handler used by standalone clients.
			var multipartBody bytes.Buffer
			writer := multipart.NewWriter(&multipartBody)
			part, _ := writer.CreateFormFile("file", "synthetic.vtt")
			_, _ = io.WriteString(part, "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nSynthetic caption\n")
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			transcriptReq, _ := http.NewRequest("POST", server.URL+"/api/videos/"+id+"/transcript", &multipartBody)
			transcriptReq.Header.Set("Content-Type", writer.FormDataContentType())
			transcriptReq.Header.Set("Authorization", "Bearer synthetic-staff")
			transcriptResponse, err := server.Client().Do(transcriptReq)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ = io.ReadAll(transcriptResponse.Body)
			_ = transcriptResponse.Body.Close()
			if transcriptResponse.StatusCode != 200 {
				t.Fatalf("caption upload: %d %s", transcriptResponse.StatusCode, raw)
			}
			if !exists(t, db, `SELECT 1 FROM mpd_event_outbox WHERE video_id=$1 AND event_type='video.transcript_ready' AND media_version=1`, id) {
				t.Fatal("caption event absent")
			}
			mustExec(t, db, `UPDATE mpd_sessions SET revoked=true WHERE id=$1`, sid)
			call("GET", "/api/mpd/preview/media", "", previewToken, 403)
			// Wait for intentionally failed background media jobs to finish their DB
			// bookkeeping before closing the isolated fixture, without running encoders.
			deadline := time.Now().Add(5 * time.Second)
			for !exists(t, db, `SELECT 1 FROM videos WHERE id=$1 AND transcode_attempts=1 AND thumbnail_attempts=1`, id) {
				if time.Now().After(deadline) {
					t.Fatal("background bookkeeping did not settle")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestMPDManagedUploadCannotFallbackToMutableStorage(t *testing.T) {
	h := &Handler{storage: &mockStorage{uploadURL: "https://storage.example.test/synthetic"}}
	ctx := mpd.ContextWithPrincipal(context.Background(), &mpd.Principal{})
	if _, _, err := h.recordingUploadURL(ctx, "synthetic.mp4", "video/mp4", 4); err == nil {
		t.Fatal("managed storage silently fell back to mutable PUT")
	}
	h.BoundMPDMediaURLs()
	if _, _, err := h.recordingUploadURL(ctx, "synthetic.mp4", "video/mp4", 4); err == nil {
		t.Fatal("bounded storage silently fell back to mutable PUT")
	}
	signed, headers, err := h.recordingUploadURL(context.Background(), "ordinary.mp4", "video/mp4", 4)
	if err != nil || signed == "" || len(headers) != 0 {
		t.Fatalf("ordinary storage changed: %q %v %v", signed, headers, err)
	}
}
