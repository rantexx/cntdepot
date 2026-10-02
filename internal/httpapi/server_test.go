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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testdir"
)

func newTestServer(t *testing.T, accessCode string) *httptest.Server {
	t.Helper()
	return newTestServerClock(t, accessCode, nil)
}

// newTestServerClock is newTestServer with the server's clock replaced (nil
// keeps the real one), so lock-out timing can be tested without sleeping.
func newTestServerClock(t *testing.T, accessCode string, now func() time.Time) *httptest.Server {
	t.Helper()
	dir := testdir.New(t)
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
	api := New(Options{
		Service: svc, Store: st, Blobs: bl, Static: static, AccessCode: accessCode, Version: "test",
	}, broker)
	if now != nil {
		api.now = now
	}
	srv := httptest.NewServer(api.Handler())
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
	req.Header.Set("X-Depot-Client", "web")
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
	req, err := http.NewRequest("POST", base+"/api/moves/"+moveID+"/photos", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Depot-Client", "web")
	resp, err := client.Do(req)
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
		r, err := http.NewRequest("POST", srv.URL+"/api/moves", strings.NewReader(`{"lane":"1"}`))
		if err != nil {
			return
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Depot-Client", "web")
		if resp, err := srv.Client().Do(r); err == nil {
			resp.Body.Close()
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

// errorCode extracts the API error code from a response body.
func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

// rawCall sends a request exactly as given: no client header unless the
// caller names one, and an optional Host override.
func rawCall(t *testing.T, method, url, host, clientHeader, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if clientHeader != "" {
		req.Header.Set("X-Depot-Client", clientHeader)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestHealthNeedsNoClientHeader(t *testing.T) {
	srv := newTestServer(t, "")
	if status, body := rawCall(t, "GET", srv.URL+"/api/health", "", "", ""); status != http.StatusOK {
		t.Fatalf("GET /api/health = %d %s; want 200", status, body)
	}
}

func TestWritesNeedTheClientHeader(t *testing.T) {
	srv := newTestServer(t, "1234")
	for _, tc := range []struct{ path, body string }{
		{"/api/moves", `{"lane":"1"}`},
		{"/api/login", `{"code":"1234"}`},
	} {
		for _, header := range []string{"", "other"} {
			status, body := rawCall(t, "POST", srv.URL+tc.path, "", header, tc.body)
			if status != http.StatusForbidden || errorCode(body) != "E5004" {
				t.Errorf("POST %s with client header %q = %d %s; want 403 E5004", tc.path, header, status, body)
			}
		}
	}
	if status, body := rawCall(t, "POST", srv.URL+"/api/login", "", "web", `{"code":"1234"}`); status != http.StatusOK {
		t.Fatalf("login with the client header = %d %s; want 200", status, body)
	}
}

func TestUnknownHostIsRefusedOnTheAPIOnly(t *testing.T) {
	srv := newTestServer(t, "")
	status, body := rawCall(t, "GET", srv.URL+"/api/health", "evil.example.com", "", "")
	if status != http.StatusForbidden || errorCode(body) != "E5005" {
		t.Fatalf("GET /api/health as evil.example.com = %d %s; want 403 E5005", status, body)
	}
	if status, _ := rawCall(t, "GET", srv.URL+"/", "evil.example.com", "", ""); status != http.StatusOK {
		t.Fatalf("the app shell is not host restricted, got %d", status)
	}
	hosts := []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080", "192.168.1.20:8080"}
	if name, err := os.Hostname(); err == nil && name != "" {
		hosts = append(hosts, name+":8080", strings.ToUpper(name), name+".local:8080")
	}
	for _, host := range hosts {
		if status, body := rawCall(t, "GET", srv.URL+"/api/health", host, "", ""); status != http.StatusOK {
			t.Errorf("GET /api/health as %s = %d %s; want 200", host, status, body)
		}
	}
}

func TestAllowedHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":           true,
		"LOCALHOST:8080":      true,
		"127.0.0.1:8080":      true,
		"192.168.1.20":        true,
		"[::1]":               true,
		"[::1]:8080":          true,
		"depot-pc":            true,
		"Depot-PC:8080":       true,
		"DEPOT-PC":            true,
		"depot-pc.local:8080": true,
		"depot-pc.lan":        true,
		"depot-pc.evil.com":   false,
		"evil.example.com":    false,
		"evil.example.com:80": false,
		"localhost.evil.com":  false,
		"depot-pcx":           false,
		"notdepot-pc":         false,
		"10.0.0.1.evil.com":   false,
		"":                    false,
		":8080":               false,
	} {
		if got := allowedHost(host, "Depot-PC"); got != want {
			t.Errorf("allowedHost(%q) = %v; want %v", host, got, want)
		}
	}
	// A machine name that already carries its domain also answers to its short name.
	for host, want := range map[string]bool{"depot-pc": true, "depot-pc.corp.example.com": true, "other": false} {
		if got := allowedHost(host, "depot-pc.corp.example.com"); got != want {
			t.Errorf("allowedHost(%q) with a fully qualified machine name = %v; want %v", host, got, want)
		}
	}
}

// fakeClock is a hand-wound clock safe to share with the server's goroutines.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func login(t *testing.T, base, code string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", base+"/api/login", strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Depot-Client", "web")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, data
}

func TestFiveWrongCodesLockLoginForAMinute(t *testing.T) {
	clock := newFakeClock()
	srv := newTestServerClock(t, "1234", clock.Now)
	for i := 1; i <= 5; i++ {
		if status, _, _ := login(t, srv.URL, "0000"); status != http.StatusUnauthorized {
			t.Fatalf("wrong code #%d = %d; want 401", i, status)
		}
	}
	status, header, body := login(t, srv.URL, "1234")
	if status != http.StatusTooManyRequests || errorCode(body) != "E5003" {
		t.Fatalf("right code while locked = %d %s; want 429 E5003", status, body)
	}
	if header.Get("Retry-After") == "" {
		t.Error("a locked-out answer should say when to retry")
	}
	clock.Advance(59 * time.Second)
	if status, _, _ := login(t, srv.URL, "1234"); status != http.StatusTooManyRequests {
		t.Fatalf("after 59 s = %d; want still 429", status)
	}
	clock.Advance(2 * time.Second)
	if status, _, body := login(t, srv.URL, "1234"); status != http.StatusOK {
		t.Fatalf("after the lock-out = %d %s; want 200", status, body)
	}
}

func TestGuessesWhileLockedDoNotExtendTheLock(t *testing.T) {
	clock := newFakeClock()
	srv := newTestServerClock(t, "1234", clock.Now)
	for i := 0; i < 5; i++ {
		login(t, srv.URL, "0000")
	}
	for i := 0; i < 11; i++ {
		clock.Advance(5 * time.Second)
		if status, _, _ := login(t, srv.URL, "0000"); status != http.StatusTooManyRequests {
			t.Fatalf("guess %d s into the lock-out = %d; want 429", 5*(i+1), status)
		}
	}
	// 55 s have passed; the lock lifts 60 s after the fifth miss.
	clock.Advance(6 * time.Second)
	if status, _, body := login(t, srv.URL, "1234"); status != http.StatusOK {
		t.Fatalf("after the lock-out = %d %s; want 200", status, body)
	}
}

func TestSuccessfulLoginClearsTheFailureCount(t *testing.T) {
	clock := newFakeClock()
	srv := newTestServerClock(t, "1234", clock.Now)
	for round := 0; round < 3; round++ {
		for i := 0; i < 4; i++ {
			if status, _, _ := login(t, srv.URL, "0000"); status != http.StatusUnauthorized {
				t.Fatalf("round %d wrong code #%d = %d; want 401", round, i+1, status)
			}
		}
		if status, _, body := login(t, srv.URL, "1234"); status != http.StatusOK {
			t.Fatalf("round %d right code = %d %s; want 200", round, status, body)
		}
	}
}

func TestOldFailuresDoNotCount(t *testing.T) {
	clock := newFakeClock()
	srv := newTestServerClock(t, "1234", clock.Now)
	for i := 0; i < 4; i++ {
		login(t, srv.URL, "0000")
	}
	clock.Advance(11 * time.Minute)
	if status, _, _ := login(t, srv.URL, "0000"); status != http.StatusUnauthorized {
		t.Fatalf("fifth miss, but the first four are 11 minutes old = %d; want 401", status)
	}
	if status, _, body := login(t, srv.URL, "1234"); status != http.StatusOK {
		t.Fatalf("right code = %d %s; want 200", status, body)
	}
}

func TestParallelGuessesCannotSlipPastTheLimit(t *testing.T) {
	srv := newTestServerClock(t, "1234", newFakeClock().Now)
	statuses := make(chan int, 40)
	var wg sync.WaitGroup
	for i := 0; i < cap(statuses); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, _ := login(t, srv.URL, "0000")
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	counted := map[int]int{}
	for s := range statuses {
		counted[s]++
	}
	if counted[http.StatusUnauthorized] != 5 || counted[http.StatusTooManyRequests] != 35 {
		t.Fatalf("answers = %v; want exactly 5 wrong-code answers and 35 lock-outs", counted)
	}
}

func TestBlobPreviewWidthIsChecked(t *testing.T) {
	srv := newTestServer(t, "")
	client := srv.Client()
	_, d := createAndRead(t, client, srv.URL, "orig-preview")
	if len(d.Photos) == 0 {
		t.Fatal("move has no photos")
	}
	digest := d.Photos[0].SHA256
	if status, body := call(t, client, "GET", srv.URL+"/api/blobs/"+digest+"?w=999", ""); status != 400 || errorCode(body) != "E4006" {
		t.Fatalf("w=999 = %d %s; want 400 E4006", status, body)
	}
	// The test photo is not a real image: a preview falls back to the original.
	if status, _ := call(t, client, "GET", srv.URL+"/api/blobs/"+digest+"?w=320", ""); status != 200 {
		t.Fatalf("w=320 = %d; want 200", status)
	}
}
