package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/recognition"
	"depot/internal/store"
)

func newTestServer(t *testing.T, accessCode string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "depot.db"), "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	bl, err := blob.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &recognition.Fake{Reading: recognition.Reading{
		Candidates: []recognition.RawCandidate{{Number: "CSQU3054383", Confidence: 0.9}},
		TypeCode:   "45G1", Color: "blue", Visible: "full",
	}}
	broker := NewBroker()
	svc := gate.New(st, bl, fake, gate.Config{}, broker.Publish)
	runCtx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<html><div id=root></div></html>")}}
	srv := httptest.NewServer(New(Options{
		Service: svc, Store: st, Blobs: bl, Static: static, AccessCode: accessCode, Version: "test",
	}, broker).Handler())
	t.Cleanup(func() {
		srv.Close()
		cancel()
		svc.Wait()
		st.Close()
	})
	return srv
}

func call(t *testing.T, client *http.Client, method, url, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func uploadPhoto(t *testing.T, client *http.Client, base, moveID, original string) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("role", "rear")
	_ = mw.WriteField("width", "1568")
	_ = mw.WriteField("height", "1176")
	for field, content := range map[string]string{"original": original, "ai": "\xff\xd8\xff\xe0ai-copy"} {
		fw, err := mw.CreateFormFile(field, field+".jpg")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(content))
	}
	mw.Close()
	resp, err := client.Post(base+"/api/moves/"+moveID+"/photos", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

type detail struct {
	Status   string `json:"status"`
	Number   string `json:"number"`
	Verified bool   `json:"verified"`
	Photos   []struct {
		SHA256   string `json:"sha256"`
		AISHA256 string `json:"ai_sha256"`
	} `json:"photos"`
	Attempts []struct {
		Engine string `json:"engine"`
	} `json:"attempts"`
}

func createAndRead(t *testing.T, client *http.Client, base, original string) (string, detail) {
	t.Helper()
	status, body := call(t, client, "POST", base+"/api/moves", `{"lane":"2"}`)
	if status != http.StatusCreated {
		t.Fatalf("create move = %d %s", status, body)
	}
	var m struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &m)
	if code := uploadPhoto(t, client, base, m.ID, original); code != http.StatusAccepted {
		t.Fatalf("upload = %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var d detail
		_, body = call(t, client, "GET", base+"/api/moves/"+m.ID, "")
		_ = json.Unmarshal(body, &d)
		if d.Status == "needs_review" {
			return m.ID, d
		}
		if time.Now().After(deadline) {
			t.Fatalf("move never reached needs_review: %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMoveLifecycleOverHTTP(t *testing.T) {
	srv := newTestServer(t, "")
	c := srv.Client()
	id, d := createAndRead(t, c, srv.URL, "\xff\xd8\xff\xe0original")
	if d.Number != "CSQU3054383" || len(d.Photos) != 1 || len(d.Attempts) != 1 || d.Attempts[0].Engine != "fake" {
		t.Fatalf("detail = %+v", d)
	}

	status, body := call(t, c, "GET", srv.URL+"/api/blobs/"+d.Photos[0].AISHA256, "")
	if status != http.StatusOK || !bytes.HasPrefix(body, []byte("\xff\xd8\xff")) {
		t.Fatalf("blob = %d %q", status, body)
	}

	status, body = call(t, c, "POST", srv.URL+"/api/moves/"+id+"/resolve", `{"action":"accept","by":"ayse"}`)
	var resolved detail
	_ = json.Unmarshal(body, &resolved)
	if status != http.StatusOK || resolved.Status != "accepted" || !resolved.Verified {
		t.Fatalf("resolve = %d %s", status, body)
	}
	if status, _ := call(t, c, "POST", srv.URL+"/api/moves/"+id+"/resolve", `{"action":"accept"}`); status != http.StatusConflict {
		t.Fatalf("second resolve = %d; want 409", status)
	}
}

func TestNonImageBlobsDownloadInsteadOfRendering(t *testing.T) {
	srv := newTestServer(t, "")
	_, d := createAndRead(t, srv.Client(), srv.URL, "<html><script>alert(1)</script></html>")
	resp, err := srv.Client().Get(srv.URL + "/api/blobs/" + d.Photos[0].SHA256)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content type = %q; want application/octet-stream", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("content disposition = %q; want attachment", cd)
	}
}

func TestValidateExplainsCheckDigit(t *testing.T) {
	srv := newTestServer(t, "")
	status, body := call(t, srv.Client(), "GET", srv.URL+"/api/validate/CSQU3054383", "")
	var v struct {
		Valid bool  `json:"valid"`
		Steps []any `json:"steps"`
		Sum   int   `json:"sum"`
	}
	_ = json.Unmarshal(body, &v)
	if status != http.StatusOK || !v.Valid || len(v.Steps) != 10 || v.Sum != 6185 {
		t.Fatalf("validate = %d %s", status, body)
	}
}

func TestErrorsAreJSON(t *testing.T) {
	srv := newTestServer(t, "")
	for url, want := range map[string]int{
		srv.URL + "/api/nope":               http.StatusNotFound,
		srv.URL + "/api/moves/missing":      http.StatusNotFound,
		srv.URL + "/api/blobs/not-a-digest": http.StatusBadRequest,
	} {
		status, body := call(t, srv.Client(), "GET", url, "")
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if status != want || json.Unmarshal(body, &e) != nil || e.Error.Code == "" {
			t.Errorf("GET %s = %d %s; want %d with a JSON error", url, status, body, want)
		}
	}
}

func TestStaticFallsBackToTheApp(t *testing.T) {
	srv := newTestServer(t, "")
	status, body := call(t, srv.Client(), "GET", srv.URL+"/queue", "")
	if status != http.StatusOK || !strings.Contains(string(body), "root") {
		t.Fatalf("GET /queue = %d %s", status, body)
	}
}

func TestAccessCodeProtectsTheAPI(t *testing.T) {
	srv := newTestServer(t, "1234")
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if status, _ := call(t, c, "GET", srv.URL+"/api/moves", ""); status != http.StatusUnauthorized {
		t.Fatalf("without code = %d; want 401", status)
	}
	if status, _ := call(t, c, "POST", srv.URL+"/api/login", `{"code":"0000"}`); status != http.StatusUnauthorized {
		t.Fatalf("wrong code = %d; want 401", status)
	}
	if status, _ := call(t, c, "POST", srv.URL+"/api/login", `{"code":"1234"}`); status != http.StatusOK {
		t.Fatalf("right code = %d; want 200", status)
	}
	if status, _ := call(t, c, "GET", srv.URL+"/api/moves", ""); status != http.StatusOK {
		t.Fatalf("after login = %d; want 200", status)
	}
	if status, _ := call(t, &http.Client{}, "GET", srv.URL+"/api/health", ""); status != http.StatusOK {
		t.Fatalf("health must stay open for the login screen, got %d", status)
	}
	if status, _ := call(t, &http.Client{}, "GET", srv.URL+"/", ""); status != http.StatusOK {
		t.Fatalf("the app shell must stay open for the login screen, got %d", status)
	}
}

func TestStreamAnnouncesMoveChanges(t *testing.T) {
	srv := newTestServer(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	go func() {
		r, err := srv.Client().Post(srv.URL+"/api/moves", "application/json", strings.NewReader(`{"lane":"1"}`))
		if err == nil {
			r.Body.Close()
		}
	}()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == "event: move" {
			return
		}
	}
	t.Fatal("no move event before timeout")
}
