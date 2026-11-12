package video

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestPatientChapterSegmentsStayWithinMedia(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute player JavaScript")
	}
	const script = `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(process.argv[1], 'utf8');
const begin = source.indexOf('function renderChapters()');
const end = source.indexOf("player.addEventListener('timeupdate'", begin);
assert.ok(begin >= 0 && end > begin);
for (const starts of [[0, 100.25, 900], [0, 10, 10, 20], [0, 100.499]]) {
    const segments = [];
    const context = {
        player: { duration: 100.5 },
        chapters: starts.map(start => ({ start, title: 'Synthetic point' })),
        chaptersLayer: { appendChild: segment => segments.push(segment) },
        document: { createElement: () => ({
            style: {}, attributes: {},
            setAttribute(name, value) { this.attributes[name] = value; },
            appendChild() {}, addEventListener() {}
        }) }
    };
    vm.runInNewContext(source.slice(begin, end) + '\nrenderChapters();', context);
    const expected = starts.length === 3 && starts[2] === 900 ? 2 : starts.length === 4 ? 3 : 2;
    assert.equal(segments.length, expected);
    for (const segment of segments) {
        const left = parseFloat(segment.style.left);
        const width = parseFloat(segment.style.width);
        assert.ok(Number.isFinite(left) && Number.isFinite(width));
        assert.ok(left >= 0 && width > 0 && left + width <= 100.000001);
    }
    assert.ok(segments.some(segment => segment.attributes['data-start'] === starts.at(-1))
        || starts.at(-1) > context.player.duration);
}`
	for _, file := range []string{"watch_page.go", "embed_page.go"} {
		t.Run(file, func(t *testing.T) {
			if out, err := exec.Command(node, "-e", script, file).CombinedOutput(); err != nil {
				t.Fatalf("chapter bounds: %v\n%s", err, out)
			}
		})
	}
}

func TestTidyPatientSummary(t *testing.T) {
	text := "  Your scan shows a healthy heart.  "
	summary, ok := tidyPatientSummary(&text, []patientSummaryPoint{{Start: 40, Text: " Keep walking daily. "}, {Start: 3, Text: "Your heart is healthy."}})
	if !ok || summary.Text != "Your scan shows a healthy heart." || len(summary.Points) != 2 ||
		summary.Points[0].Start != 3 || summary.Points[1].Text != "Keep walking daily." {
		t.Fatalf("expected a trimmed summary in video order, got %+v %v", summary, ok)
	}
	if summary, ok := tidyPatientSummary(nil, nil); !ok || summary != nil {
		t.Fatal("a null summary with no points clears it")
	}
	blank, long, nul := "   ", strings.Repeat("a", maxPatientSummaryRunes+1), "Fine\x00."
	tooMany := make([]patientSummaryPoint, maxPatientSummaryItems+1)
	for i := range tooMany {
		tooMany[i] = patientSummaryPoint{Start: float64(i), Text: "Point"}
	}
	for name, c := range map[string]struct {
		text   *string
		points []patientSummaryPoint
	}{
		"points without a summary": {nil, []patientSummaryPoint{{Start: 1, Text: "Point"}}},
		"blank summary":            {&blank, nil},
		"long summary":             {&long, nil},
		"NUL in summary":           {&nul, nil},
		"NUL in point":             {&text, []patientSummaryPoint{{Start: 1, Text: nul}}},
		"too many points":          {&text, tooMany},
		"blank point":              {&text, []patientSummaryPoint{{Start: 1, Text: " "}}},
		"long point":               {&text, []patientSummaryPoint{{Start: 1, Text: strings.Repeat("b", maxPatientSummaryPoint+1)}}},
		"negative start":           {&text, []patientSummaryPoint{{Start: -1, Text: "Point"}}},
		"infinite start":           {&text, []patientSummaryPoint{{Start: math.Inf(1), Text: "Point"}}},
		"start past a day":         {&text, []patientSummaryPoint{{Start: maxPatientSummaryStart + 1, Text: "Point"}}},
	} {
		if _, ok := tidyPatientSummary(c.text, c.points); ok {
			t.Errorf("%s: expected a refusal", name)
		}
	}
}

func summaryRequest(a *MPDAdapter, id, body, work, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PUT", "/api/integrations/mpd/videos/"+id+"/patient-summary", strings.NewReader(body))
	r = withURLParam(r, "videoId", id)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	r.Header.Set("X-MPD-Work-Version", work)
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	a.Authenticate(http.HandlerFunc(a.PatientSummary)).ServeHTTP(w, r)
	return w
}

func TestMPDAdapterPatientSummary(t *testing.T) {
	db, h, a, id, _ := seedManagedAdapter(t)
	ctx := context.Background()
	set := `{"mediaVersion":1,"summary":" Your heart is healthy. ","points":[{"start":30,"text":"Keep walking."},{"start":2,"text":"The scan is normal."}]}`
	for _, c := range []struct {
		name, body, work, key string
		want                  int
	}{
		{"unknown field", `{"mediaVersion":1,"summary":"Fine.","tasks":[]}`, "1", "a", 400},
		{"blank summary", `{"mediaVersion":1,"summary":" "}`, "1", "b", 400},
		{"points alone", `{"mediaVersion":1,"summary":null,"points":[{"start":1,"text":"Point"}]}`, "1", "c", 400},
		{"stale media", `{"mediaVersion":2,"summary":"Fine."}`, "1", "d", 409},
		{"no work fence", set, "0", "e", 400},
		{"set", set, "5", "set", 200},
		{"old worker", `{"mediaVersion":1,"summary":"Older."}`, "4", "old", 409},
		{"same key, other body", `{"mediaVersion":1,"summary":"Changed."}`, "6", "set", 409},
	} {
		if w := summaryRequest(a, id, c.body, c.work, c.key); w.Code != c.want {
			t.Fatalf("%s: got %d %s", c.name, w.Code, w.Body.String())
		}
	}
	summary := currentPatientSummary(ctx, db, id)
	if summary == nil || summary.Text != "Your heart is healthy." || len(summary.Points) != 2 || summary.Points[0].Text != "The scan is normal." {
		t.Fatalf("expected the trimmed summary in video order, got %+v", summary)
	}
	// A replay returns the stored answer without applying it again.
	var answer struct {
		MediaVersion   int  `json:"mediaVersion"`
		PatientSummary bool `json:"patientSummary"`
	}
	w := summaryRequest(a, id, set, "7", "set")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &answer) != nil || !answer.PatientSummary || answer.MediaVersion != 1 {
		t.Fatalf("replay: %d %s", w.Code, w.Body.String())
	}

	w = summaryRequest(a, id, `{"mediaVersion":1,"summary":null}`, "8", "clear")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &answer) != nil || answer.PatientSummary {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if currentPatientSummary(ctx, db, id) != nil {
		t.Fatal("a cleared summary still shows")
	}

	if w := summaryRequest(a, id, set, "9", "again"); w.Code != 200 {
		t.Fatalf("set again: %d", w.Code)
	}
	// Python's default encoder escapes non-ASCII text. The maximum valid
	// payload must fit even when every character uses a surrogate pair.
	points := make([]string, maxPatientSummaryItems)
	for i := range points {
		points[i] = `{"start":1,"text":"` + strings.Repeat(`\ud83d\ude00`, maxPatientSummaryPoint) + `"}`
	}
	escaped := `{"mediaVersion":1,"summary":"` + strings.Repeat(`\ud83d\ude00`, maxPatientSummaryRunes) + `","points":[` + strings.Join(points, ",") + `]}`
	if len(escaped) <= 32*1024 {
		t.Fatal("fixture must exceed the old request limit")
	}
	if w := summaryRequest(a, id, escaped, "10", "escaped"); w.Code != 200 {
		t.Fatalf("escaped maximum: %d %s", w.Code, w.Body.String())
	}
	// New media hides the summary written for the old one.
	mustExec(t, db, `UPDATE mpd_video_state SET media_version=2 WHERE video_id=$1`, id)
	if currentPatientSummary(ctx, db, id) != nil {
		t.Fatal("a summary for older media still shows")
	}
	if text, points, status := managedSummary(ctx, h.db, id); text != "" || len(points) != 0 || status != "none" {
		t.Fatal("the page should show no summary for new media")
	}
}

func expectManagedSummary(mock pgxmock.PgxPoolIface, summary *patientSummary) {
	query := mock.ExpectQuery(`SELECT patient_summary FROM mpd_video_state`).WithArgs("vid-1")
	if summary == nil {
		query.WillReturnError(pgx.ErrNoRows)
		return
	}
	raw, _ := json.Marshal(summary)
	query.WillReturnRows(pgxmock.NewRows([]string{"patient_summary"}).AddRow(raw))
}

func TestWatchPage_Managed_ShowsTheApprovedSummaryNotSendRecs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.MatchExpectationsInOrder(false)
	password, _ := hashSharePassword("K7QM-2XPA-9RTF-HD4W")
	shareToken := "managedsumm1"
	transcript := `[{"start":0,"end":4,"text":"Hello from your care team."}]`
	sendrecSummary, sendrecChapters := "SendRec's own words.", `[{"title":"SendRec chapter","start":1}]`
	mock.ExpectQuery(`SELECT v.id, v.title, v.file_key`).
		WithArgs(shareToken).
		WillReturnRows(pgxmock.NewRows(watchPageColumns).AddRow(
			"vid-1", "Sleep check-in", "recordings/u1/abc.webm", "Alice", time.Date(2026, 2, 5, 14, 0, 0, 0, time.UTC), (*time.Time)(nil),
			(*string)(nil), &password, "disabled",
			(*string)(nil), &transcript, "ready",
			"owner-user-id", "owner@example.com", (*string)(nil), "video/webm",
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			false, (*string)(nil), (*string)(nil),
			false,
			&sendrecSummary, &sendrecChapters, "ready",
			12,
			"free",
			"ready",
			(*string)(nil),
		))
	expectViewRecording(mock, "vid-1")
	expectManagedSummary(mock, &patientSummary{Text: "Your sleep looks steady.", Points: []patientSummaryPoint{{Start: 7, Text: "Keep a regular bedtime."}}})

	handler := NewHandler(mock, &mockStorage{downloadURL: "https://s3.example.com/video.webm"}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	req := managedWatchRequest(shareToken)
	req.AddCookie(&http.Cookie{Name: watchCookieName(shareToken), Value: signWatchCookie(testHMACSecret, shareToken, password)})
	body := serveWatchPage(handler, req).Body.String()

	for _, want := range []string{"Your sleep looks steady.", "In this video", `data-start="7"`, "Keep a regular bedtime.", `data-tab="transcript"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q on the page", want)
		}
	}
	for _, absent := range []string{"SendRec&#39;s own words.", "SendRec's own words.", "SendRec chapter", ">Chapters<"} {
		if strings.Contains(body, absent) {
			t.Errorf("SendRec's own summary leaked: %q", absent)
		}
	}
	waitAndCheckExpectations(t, mock)
}

func TestBrandFont(t *testing.T) {
	serve := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		BrandFont(w, httptest.NewRequest("GET", "/fonts/brand/"+brandHeadingFont, nil))
		return w
	}
	t.Cleanup(func() { SetBrandFontDir("") })

	SetBrandFontDir("")
	if w := serve(); w.Code != 404 {
		t.Fatalf("no directory: got %d", w.Code)
	}
	dir := t.TempDir()
	SetBrandFontDir(dir)
	if w := serve(); w.Code != 404 || w.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("missing file: got %d, cache %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if err := os.WriteFile(filepath.Join(dir, brandHeadingFont), []byte("synthetic font bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := serve()
	if w.Code != 200 || w.Header().Get("Content-Type") != "font/ttf" || w.Body.String() != "synthetic font bytes" || w.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("got %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}

func TestMPDPages_HeadingsNameTTNeorisFirst(t *testing.T) {
	for name, css := range map[string]string{"patient pages": mpdPageCSS, "watch page": mpdWatchCSS} {
		if !strings.Contains(css, `url("/fonts/brand/`+brandHeadingFont+`")`) || !strings.Contains(css, `font-family: "TT Neoris", "Geist"`) {
			t.Errorf("%s: expected TT Neoris first with Geist behind it", name)
		}
	}
}

func TestManagedWatchAPIAndEmbedServeOnlyTheApprovedSummary(t *testing.T) {
	db, h, a, id, share := seedManagedAdapter(t)
	mustExec(t, db, `UPDATE videos SET summary='SendRec own words.',chapters='[{"title":"SendRec chapter","start":1}]',summary_status='ready',document='SendRec document.',document_status='ready',duration=60 WHERE id=$1`, id)
	if w := summaryRequest(a, id, `{"mediaVersion":1,"summary":"Approved words.","points":[{"start":5,"text":"Approved point."}]}`, "1", "set"); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	request := func(path string, managed bool) *http.Request {
		r := withURLParam(httptest.NewRequest("GET", path, nil), "shareToken", share)
		if managed {
			r = r.WithContext(context.WithValue(r.Context(), mpdManagedTokensKey{}, map[string]bool{share: true}))
		}
		return r
	}
	for _, managed := range []bool{true, false} {
		w := httptest.NewRecorder()
		h.Watch(w, request("/api/watch/"+share, managed))
		var body struct {
			Summary       string    `json:"summary"`
			Chapters      []Chapter `json:"chapters"`
			SummaryStatus string    `json:"summaryStatus"`
			Document      string    `json:"document"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("managed=%v: %d %s", managed, w.Code, w.Body.String())
		}
		want, chapter := "SendRec own words.", "SendRec chapter"
		if managed {
			want, chapter = "Approved words.", "Approved point."
		}
		if body.Summary != want || len(body.Chapters) != 1 || body.Chapters[0].Title != chapter || body.SummaryStatus != "ready" {
			t.Errorf("managed=%v: got %+v", managed, body)
		}
		if wantDoc := map[bool]string{true: "", false: "SendRec document."}[managed]; body.Document != wantDoc {
			t.Errorf("managed=%v: document %q", managed, body.Document)
		}
	}
	w := httptest.NewRecorder()
	h.EmbedPage(w, request("/embed/"+share, true))
	if page := w.Body.String(); w.Code != 200 || strings.Contains(page, "SendRec chapter") || !strings.Contains(page, "Approved point.") {
		t.Errorf("the managed embed should carry only the approved points: %d", w.Code)
	}
}
