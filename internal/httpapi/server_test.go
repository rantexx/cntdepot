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
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"depot/internal/api"
	"depot/internal/auth"
	"depot/internal/blob"
	"depot/internal/db"
	"depot/internal/gate"
	"depot/internal/pgtest"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/tenancy"
	"depot/internal/testkit"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

var bg = context.Background()

// env is a running server on a fresh database with companies A and B (each
// with depot d1 and module ocr) and admin@x.com as platform admin.
type env struct {
	srv      *httptest.Server
	pool     *pgxpool.Pool
	sessions *auth.Sessions
	store    *store.Store
	blobs    *blob.Store
	owner    string // database owner URL
}

// newEnv builds the server; edit, if not nil, changes its options first.
func newEnv(t *testing.T, edit func(*Options, *gate.Service)) *env {
	t.Helper()
	st, bl, owner := testkit.OpenCompanies(t, "A", "B")
	if err := db.Migrate(bg, owner, db.MigrateOptions{AppPassword: "app-pw", PlatformAdminEmail: "admin@x.com"}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(bg, db.AppURL(owner, "app-pw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fake := &recognition.Fake{Reading: recognition.Reading{
		Candidates: []recognition.RawCandidate{{Number: "CSQU3054383", Confidence: 0.9}},
		TypeCode:   "45G1", Color: "blue", Visible: "full",
		Box: &recognition.Box{X0: 0.3, Y0: 0.2, X1: 0.62, Y1: 0.3},
	}}
	broker := NewBroker()
	svc := gate.New(st, bl, fake, gate.Config{}, broker.Publish)
	runCtx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, sessions: auth.NewSessions(pool, 0), store: st, blobs: bl, owner: owner}
	opts := Options{
		Service: svc, Store: st, Blobs: bl, Version: "test",
		Static:   fstest.MapFS{"index.html": {Data: []byte("<html><div id=root></div></html>")}},
		Sessions: e.sessions, Provider: auth.NewDevProvider(), Pool: pool,
	}
	if edit != nil {
		edit(&opts, svc)
	}
	e.srv = httptest.NewServer(New(opts, broker).Handler())
	t.Cleanup(func() {
		e.srv.Close()
		cancel()
		svc.Wait()
	})
	return e
}

// exec runs sql as the app in company (globally when company is "").
func (e *env) exec(t *testing.T, company, sql string, args ...any) {
	t.Helper()
	fn := func(tx pgx.Tx) error { _, err := tx.Exec(bg, sql, args...); return err }
	var err error
	if company == "" {
		err = db.Global(bg, e.pool, fn)
	} else {
		err = db.InCompany(tenancy.With(bg, tenancy.Scope{CompanyID: company}), e.pool, fn)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// user is a signed-in session.
type user struct {
	t      *testing.T
	e      *env
	cookie string
}

// signIn logs email in (verified) and returns the session's cookie.
func (e *env) signIn(t *testing.T, email string) string {
	t.Helper()
	cookie, err := e.sessions.Login(bg, auth.Identity{Subject: "test|" + email, Email: email, EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	return cookie
}

// as signs in a new member with role in company, scoped to its depot d1.
func (e *env) as(t *testing.T, company, role string) *user {
	t.Helper()
	email := role + "@" + strings.ToLower(company) + ".test"
	e.exec(t, company, `INSERT INTO invitations (company_id, id, email, role, created_by) VALUES ($1, $2, $3, $4, 'test')`,
		company, email, email, role)
	cookie := e.signIn(t, email)
	if err := e.sessions.SetScope(bg, cookie, company, "d1"); err != nil {
		t.Fatal(err)
	}
	return &user{t: t, e: e, cookie: cookie}
}

func (u *user) request(method, path string, body io.Reader, contentType string) *http.Request {
	u.t.Helper()
	req, err := http.NewRequest(method, u.e.srv.URL+path, body)
	if err != nil {
		u.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Depot-Client", "web")
	if u.cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: u.cookie})
	}
	return req
}

// call sends a JSON request and returns the status and body.
func (u *user) call(method, path, body string) (int, []byte) {
	u.t.Helper()
	var rd io.Reader
	ct := ""
	if body != "" {
		rd, ct = strings.NewReader(body), "application/json"
	}
	return do(u.t, u.request(method, path, rd, ct))
}

func do(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (u *user) upload(moveID, original string) (int, []byte) {
	u.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("role", "rear")
	_ = mw.WriteField("width", "1568")
	_ = mw.WriteField("height", "1176")
	for field, content := range map[string]string{"original": original, "ai": "\xff\xd8\xff\xe0ai-copy"} {
		fw, err := mw.CreateFormFile(field, field+".jpg")
		if err != nil {
			u.t.Fatal(err)
		}
		_, _ = fw.Write([]byte(content))
	}
	mw.Close()
	return do(u.t, u.request("POST", "/api/moves/"+moveID+"/photos", &buf, mw.FormDataContentType()))
}

func (u *user) newMove() string {
	u.t.Helper()
	status, body := u.call("POST", "/api/moves", `{"lane":"2"}`)
	if status != http.StatusCreated {
		u.t.Fatalf("create move = %d %s", status, body)
	}
	var m struct{ ID string }
	_ = json.Unmarshal(body, &m)
	return m.ID
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

// createAndRead makes a move with one photo and waits for the engine's read.
func (u *user) createAndRead(original string) (string, detail) {
	u.t.Helper()
	id := u.newMove()
	if code, body := u.upload(id, original); code != http.StatusAccepted {
		u.t.Fatalf("upload = %d %s", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var d detail
		_, body := u.call("GET", "/api/moves/"+id, "")
		_ = json.Unmarshal(body, &d)
		if d.Status == "needs_review" {
			return id, d
		}
		if time.Now().After(deadline) {
			u.t.Fatalf("move never reached needs_review: %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
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

func TestMoveLifecycleOverHTTP(t *testing.T) {
	u := newEnv(t, nil).as(t, "A", "gate")
	id, d := u.createAndRead("\xff\xd8\xff\xe0original")
	if d.Number != "CSQU3054383" || len(d.Photos) != 1 || len(d.Attempts) != 1 || d.Attempts[0].Engine != "fake" {
		t.Fatalf("detail = %+v", d)
	}
	status, body := u.call("GET", "/api/blobs/"+d.Photos[0].AISHA256, "")
	if status != http.StatusOK || !bytes.HasPrefix(body, []byte("\xff\xd8\xff")) {
		t.Fatalf("blob = %d %q", status, body)
	}
	if status, body := u.call("GET", "/api/moves?status=needs_review", ""); status != 200 || !strings.Contains(string(body), id) {
		t.Fatalf("list = %d %s", status, body)
	}
	status, body = u.call("POST", "/api/moves/"+id+"/resolve", `{"action":"accept","by":"ayse"}`)
	var resolved detail
	_ = json.Unmarshal(body, &resolved)
	if status != http.StatusOK || resolved.Status != "accepted" || !resolved.Verified {
		t.Fatalf("resolve = %d %s", status, body)
	}
	if m, err := u.e.store.GetMove(testkit.Ctx(), id); err != nil || m.ResolvedBy != "gate@a.test" {
		t.Fatalf("resolved by %q, %v; want the session's user, not the body's", m.ResolvedBy, err)
	}
	if status, _ := u.call("POST", "/api/moves/"+id+"/resolve", `{"action":"accept","by":"ayse"}`); status != http.StatusConflict {
		t.Fatalf("second resolve = %d; want 409", status)
	}
}

func TestEnterNumberOnMove(t *testing.T) {
	u := newEnv(t, nil).as(t, "A", "gate")
	id := u.newMove()
	status, body := u.call("POST", "/api/moves/"+id+"/number", `{"number":"MSCU1234566"}`)
	var m detail
	_ = json.Unmarshal(body, &m)
	if status != http.StatusOK || m.Status != "accepted" || m.Number != "MSCU1234566" || !m.Verified {
		t.Fatalf("enter number = %d %s", status, body)
	}
	if status, body := u.call("POST", "/api/moves/"+u.newMove()+"/number", `{"number":" "}`); status != 400 {
		t.Fatalf("empty number = %d %s; want 400", status, body)
	}
}

func TestRereadWithOCROffIs409(t *testing.T) {
	e := newEnv(t, nil)
	owner, office := e.as(t, "A", "owner"), e.as(t, "A", "office")
	if status, body := owner.call("PATCH", "/api/company/modules", `{"modules":["nope"]}`); status != 400 {
		t.Fatalf("unknown module = %d %s; want 400", status, body)
	}
	if status, body := owner.call("PATCH", "/api/company/modules", `{"modules":[]}`); status != 200 || !strings.Contains(string(body), `"modules":[]`) {
		t.Fatalf("modules off = %d %s", status, body)
	}
	if status, body := office.call("PATCH", "/api/company/modules", `{"modules":["ocr"]}`); status != 403 {
		t.Fatalf("office sets modules = %d %s; want 403", status, body)
	}
	id := office.newMove()
	status, body := office.call("POST", "/api/moves/"+id+"/reread", "")
	if status != http.StatusConflict || !strings.Contains(string(body), "OCR") {
		t.Fatalf("reread with ocr off = %d %s; want 409", status, body)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("ai", "ai.jpg")
	_, _ = fw.Write([]byte("\xff\xd8\xff\xe0test"))
	mw.Close()
	if status, body := do(t, office.request("POST", "/api/test-read", &buf, mw.FormDataContentType())); status != http.StatusConflict || errorCode(body) != "E4094" {
		t.Fatalf("test read with ocr off = %d %s; want 409 E4094", status, body)
	}
	if _, body := office.call("GET", "/api/company", ""); !strings.Contains(string(body), `"modules":[]`) {
		t.Fatalf("company = %s", body)
	}
}

func TestNonImageBlobsDownloadInsteadOfRendering(t *testing.T) {
	u := newEnv(t, nil).as(t, "A", "gate")
	_, d := u.createAndRead("<html><script>alert(1)</script></html>")
	resp, err := http.DefaultClient.Do(u.request("GET", "/api/blobs/"+d.Photos[0].SHA256, nil, ""))
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

func TestBlobOfOtherCompanyIs404(t *testing.T) {
	e := newEnv(t, nil)
	a, b := e.as(t, "A", "gate"), e.as(t, "B", "gate")
	_, d := b.createAndRead("\xff\xd8\xff\xe0b-original")
	for _, digest := range []string{d.Photos[0].SHA256, d.Photos[0].AISHA256} {
		if status, body := a.call("GET", "/api/blobs/"+digest, ""); status != 404 {
			t.Errorf("A reads B's blob = %d %s; want 404", status, body)
		}
		if status, _ := b.call("GET", "/api/blobs/"+digest, ""); status != 200 {
			t.Errorf("B reads its blob = %d; want 200", status)
		}
	}
}

func TestBlobPreviewWidthIsChecked(t *testing.T) {
	u := newEnv(t, nil).as(t, "A", "gate")
	_, d := u.createAndRead("orig-preview")
	digest := d.Photos[0].SHA256
	for _, w := range []string{"999", "abc"} {
		if status, body := u.call("GET", "/api/blobs/"+digest+"?w="+w, ""); status != 400 || errorCode(body) != "E4006" {
			t.Fatalf("w=%s = %d %s; want 400 E4006", w, status, body)
		}
	}
	// The width is checked before the blob is looked up, so a bad one is a 400
	// even for a digest that does not exist; a good one on that digest is a 404.
	missing := strings.Repeat("0", 64)
	if status, body := u.call("GET", "/api/blobs/"+missing+"?w=abc", ""); status != 400 || errorCode(body) != "E4006" {
		t.Fatalf("missing digest with w=abc = %d %s; want 400 E4006", status, body)
	}
	if status, body := u.call("GET", "/api/blobs/"+missing+"?w=320", ""); status != 404 || errorCode(body) != "E4042" {
		t.Fatalf("missing digest with w=320 = %d %s; want 404 E4042", status, body)
	}
	// The test photo is not a real image: a preview falls back to the original.
	if status, _ := u.call("GET", "/api/blobs/"+digest+"?w=320", ""); status != 200 {
		t.Fatalf("w=320 = %d; want 200", status)
	}
}

func TestValidateExplainsCheckDigit(t *testing.T) {
	u := newEnv(t, nil).as(t, "A", "gate")
	status, body := u.call("GET", "/api/validate/CSQU3054383", "")
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
	u := newEnv(t, nil).as(t, "A", "gate")
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/nope", "", 404},
		{"GET", "/api/moves/missing", "", 404},
		{"GET", "/api/blobs/not-a-digest", "", 404},
		{"POST", "/api/moves", "{not json", 400},
		{"GET", "/api/lines?page=abc", "", 400},
	} {
		status, body := u.call(tc.method, tc.path, tc.body)
		if status != tc.want || errorCode(body) == "" {
			t.Errorf("%s %s = %d %s; want %d with a JSON error", tc.method, tc.path, status, body, tc.want)
		}
	}
}

func TestUnauthenticatedApiIs401(t *testing.T) {
	e := newEnv(t, nil)
	anon := &user{t: t, e: e}
	for _, path := range []string{"/api/moves", "/api/session", "/api/stream", "/api/blobs/" + strings.Repeat("0", 64), "/api/camera"} {
		if status, body := anon.call("GET", path, ""); status != 401 || errorCode(body) != "E4010" {
			t.Errorf("GET %s signed out = %d %s; want 401 E4010", path, status, body)
		}
	}
	if status, _ := anon.call("GET", "/api/health", ""); status != 200 {
		t.Errorf("health = %d; want 200 signed out", status)
	}
	// Signed in but no company chosen yet: the session works, company data does not.
	fresh := &user{t: t, e: e, cookie: e.signIn(t, "new@x.com")}
	if status, body := fresh.call("GET", "/api/session", ""); status != 200 || strings.Contains(string(body), `"current"`) {
		t.Errorf("session without scope = %d %s", status, body)
	}
	if status, body := fresh.call("GET", "/api/moves", ""); status != 409 || errorCode(body) != "E4090" {
		t.Errorf("moves without scope = %d %s; want 409 E4090", status, body)
	}
}

func TestSessionScopeAndLogout(t *testing.T) {
	e := newEnv(t, nil)
	e.exec(t, "B", `INSERT INTO depots (company_id, id, name) VALUES ('B', 'd2', 'Second')`)
	e.exec(t, "B", `INSERT INTO invitations (company_id, id, email, role, depot_ids, created_by) VALUES ('B', 'i1', 'ali@x.com', 'office', '{d1}', 't')`)
	u := &user{t: t, e: e, cookie: e.signIn(t, "ali@x.com")}
	if status, body := u.call("POST", "/api/session/scope", `{"companyId":"A","depotId":""}`); status != 403 {
		t.Fatalf("scope into a foreign company = %d %s; want 403", status, body)
	}
	if status, body := u.call("POST", "/api/session/scope", `{"companyId":"B","depotId":""}`); status != 403 {
		t.Fatalf("all depots for a d1-only member = %d %s; want 403", status, body)
	}
	status, body := u.call("POST", "/api/session/scope", `{"companyId":"B","depotId":"d1"}`)
	var s api.Session
	if err := json.Unmarshal(body, &s); status != 200 || err != nil || s.Current == nil {
		t.Fatalf("scope = %d %s", status, body)
	}
	c := s.Current
	if c.CompanyId != "B" || c.DepotId == nil || *c.DepotId != "d1" || c.Role != "office" ||
		!reflect.DeepEqual(c.Permissions, []string{"gate", "office"}) || !reflect.DeepEqual(c.Modules, []string{"ocr"}) ||
		len(s.Companies) != 1 || s.User.Email != "ali@x.com" || s.PlatformAdmin {
		t.Fatalf("session = %s", body)
	}
	// The picker data: only the depots this member may use, known before any scope is set.
	if d := s.Companies[0].Depots; len(d) != 1 || d[0].Id != "d1" {
		t.Fatalf("companies[0].depots = %+v; want only d1", d)
	}
	req := u.request("POST", "/api/session/logout", nil, "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || !strings.Contains(resp.Header.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout = %d, Set-Cookie %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
	if status, _ := u.call("GET", "/api/session", ""); status != 401 {
		t.Fatalf("after logout = %d; want 401", status)
	}
}

// sse opens the event stream as u and sends each line to the returned channel.
func (u *user) sse(ctx context.Context) <-chan string {
	u.t.Helper()
	req := u.request("GET", "/api/stream", nil, "").WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || !strings.HasPrefix(ct, "text/event-stream") {
		u.t.Fatalf("stream = %d %q", resp.StatusCode, ct)
	}
	lines := make(chan string, 64)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	return lines
}

func TestStreamOnlyCarriesOwnCompany(t *testing.T) {
	e := newEnv(t, nil)
	a, b := e.as(t, "A", "gate"), e.as(t, "B", "gate")
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	aLines, bLines := a.sse(ctx), b.sse(ctx)
	// Wait for both ": connected" lines so neither subscription is still pending.
	<-aLines
	<-bLines
	id := b.newMove()
	deadline := time.After(2 * time.Second)
	for got := false; !got; {
		select {
		case l := <-bLines:
			got = strings.Contains(l, id)
		case <-deadline:
			t.Fatal("B's client got no event for B's move within 2 s")
		}
	}
	quiet := time.After(300 * time.Millisecond)
	for {
		select {
		case l := <-aLines:
			if strings.HasPrefix(l, "event:") || strings.HasPrefix(l, "data:") {
				t.Fatalf("A's client got %q", l)
			}
		case <-quiet:
			return
		}
	}
}

func TestStaticFallsBackToTheApp(t *testing.T) {
	e := newEnv(t, nil)
	status, body := (&user{t: t, e: e}).call("GET", "/queue", "")
	if status != http.StatusOK || !strings.Contains(string(body), "root") {
		t.Fatalf("GET /queue = %d %s", status, body)
	}
}

func TestStaticShellLoadsWithoutDatabase(t *testing.T) {
	e := newEnv(t, nil)
	u := e.as(t, "A", "gate")
	e.pool.Close()
	if status, body := u.call("GET", "/", ""); status != 200 || !strings.Contains(string(body), "root") {
		t.Fatalf("GET / with the database down = %d %s", status, body)
	}
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
	return do(t, req)
}

func TestHealthNeedsNoClientHeader(t *testing.T) {
	e := newEnv(t, nil)
	if status, body := rawCall(t, "GET", e.srv.URL+"/api/health", "", "", ""); status != http.StatusOK || !strings.Contains(string(body), `"auth_required":true`) {
		t.Fatalf("GET /api/health = %d %s; want 200", status, body)
	}
}

func TestWritesNeedTheClientHeader(t *testing.T) {
	e := newEnv(t, nil)
	u := e.as(t, "A", "gate")
	for _, header := range []string{"", "other"} {
		req := u.request("POST", "/api/moves", strings.NewReader(`{"lane":"1"}`), "application/json")
		req.Header.Del("X-Depot-Client")
		if header != "" {
			req.Header.Set("X-Depot-Client", header)
		}
		if status, body := do(t, req); status != http.StatusForbidden || errorCode(body) != "E5004" {
			t.Errorf("POST /api/moves with client header %q = %d %s; want 403 E5004", header, status, body)
		}
	}
}

func TestUnknownHostIsRefusedOnTheAPIOnly(t *testing.T) {
	srv := newEnv(t, nil).srv
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

func TestEveryOperationHasAPermission(t *testing.T) {
	typ := reflect.TypeFor[api.StrictServerInterface]()
	for i := range typ.NumMethod() {
		if _, ok := operationAccess[typ.Method(i).Name]; !ok {
			t.Errorf("operation %s has no entry in operationAccess", typ.Method(i).Name)
		}
	}
}

func TestGateRoleCannotManageMembers(t *testing.T) {
	e := newEnv(t, nil)
	g := e.as(t, "A", "gate")
	status, body := g.call("POST", "/api/invitations", `{"email":"x@y.com","role":"gate","depotIds":null}`)
	if status != 403 || errorCode(body) != "E4030" {
		t.Fatalf("gate invites = %d %s; want 403 E4030", status, body)
	}
	for _, path := range []string{"/api/members", "/api/invitations"} {
		if status, body := g.call("GET", path, ""); status != 403 {
			t.Errorf("gate GET %s = %d %s; want 403", path, status, body)
		}
	}
	if status, body := g.call("POST", "/api/lines", `{"code":"MSC","name":"MSC"}`); status != 403 {
		t.Errorf("gate creates a line = %d %s; want 403", status, body)
	}
}

func TestMembersAndInvitations(t *testing.T) {
	e := newEnv(t, nil)
	owner := e.as(t, "A", "owner")
	e.as(t, "A", "gate")
	for _, body := range []string{
		`{"email":"x@y.com","role":"gate"}`,                 // depotIds missing
		`{"email":"x@y.com","role":"gate","depotIds":[]}`,   // empty: would mean no depot
		`{"email":"x@y.com","role":"boss","depotIds":null}`, // unknown role
		`{"email":"not an email","role":"gate","depotIds":null}`,
	} {
		if status, resp := owner.call("POST", "/api/invitations", body); status != 400 {
			t.Errorf("invite %s = %d %s; want 400", body, status, resp)
		}
	}
	status, body := owner.call("POST", "/api/invitations", `{"email":"X@Y.com","role":"gate","depotIds":null}`)
	var inv api.Invitation
	if err := json.Unmarshal(body, &inv); status != 201 || err != nil || inv.Email != "x@y.com" || inv.DepotIds != nil {
		t.Fatalf("invite = %d %s", status, body)
	}
	if status, body := owner.call("POST", "/api/invitations", `{"email":"z@y.com","role":"office","depotIds":["d1"]}`); status != 201 {
		t.Fatalf("invite with depots = %d %s", status, body)
	}
	if _, body := owner.call("GET", "/api/invitations", ""); strings.Count(string(body), `"id"`) != 2 {
		t.Fatalf("invitations = %s", body)
	}
	if status, _ := owner.call("DELETE", "/api/invitations/"+inv.Id, ""); status != 204 {
		t.Fatalf("delete invitation = %d", status)
	}
	if status, _ := owner.call("DELETE", "/api/invitations/"+inv.Id, ""); status != 404 {
		t.Fatalf("delete again = %d; want 404", status)
	}

	_, body = owner.call("GET", "/api/members", "")
	var members []api.Member
	if err := json.Unmarshal(body, &members); err != nil || len(members) != 2 {
		t.Fatalf("members = %s", body)
	}
	ids := map[string]string{}
	for _, m := range members {
		ids[string(m.Role)] = m.UserId
	}
	for _, b := range []string{`{"role":"office"}`, `{"role":"office","depotIds":[]}`, `{"role":"boss","depotIds":null}`} {
		if status, resp := owner.call("PATCH", "/api/members/"+ids["gate"], b); status != 400 {
			t.Errorf("update %s = %d %s; want 400", b, status, resp)
		}
	}
	status, body = owner.call("PATCH", "/api/members/"+ids["gate"], `{"role":"office","depotIds":["d1"]}`)
	var m api.Member
	if err := json.Unmarshal(body, &m); status != 200 || err != nil || m.Role != "office" || m.DepotIds == nil || (*m.DepotIds)[0] != "d1" {
		t.Fatalf("update member = %d %s", status, body)
	}
	if status, body := owner.call("PATCH", "/api/members/nobody", `{"role":"office","depotIds":null}`); status != 404 {
		t.Fatalf("update unknown member = %d %s; want 404", status, body)
	}
	// The company must keep an owner.
	if status, body := owner.call("PATCH", "/api/members/"+ids["owner"], `{"role":"office","depotIds":null}`); status != 409 {
		t.Fatalf("demote the last owner = %d %s; want 409", status, body)
	}
	if status, body := owner.call("DELETE", "/api/members/"+ids["owner"], ""); status != 409 {
		t.Fatalf("remove the last owner = %d %s; want 409", status, body)
	}
	if status, _ := owner.call("DELETE", "/api/members/"+ids["gate"], ""); status != 204 {
		t.Fatalf("remove member = %d", status)
	}
}

func TestListsCrudAndDeactivate(t *testing.T) {
	e := newEnv(t, nil)
	office := e.as(t, "A", "office")
	status, body := office.call("POST", "/api/lines", `{"code":"MSC","name":"Mediterranean"}`)
	var l api.Line
	if err := json.Unmarshal(body, &l); status != 201 || err != nil || l.Code != "MSC" || !l.Active {
		t.Fatalf("create = %d %s", status, body)
	}
	if status, body := office.call("POST", "/api/lines", `{"code":" ","name":"x"}`); status != 400 {
		t.Fatalf("create without code = %d %s; want 400", status, body)
	}
	var list api.LineList
	_, body = office.call("GET", "/api/lines", "")
	if err := json.Unmarshal(body, &list); err != nil || list.Total != 1 || list.Items[0].Id != l.Id {
		t.Fatalf("list = %s", body)
	}
	status, body = office.call("PATCH", "/api/lines/"+l.Id, `{"name":"Mediterranean Shipping"}`)
	if status != 200 || !strings.Contains(string(body), `"name":"Mediterranean Shipping"`) || !strings.Contains(string(body), `"code":"MSC"`) {
		t.Fatalf("patch = %d %s", status, body)
	}
	if status, _ := office.call("DELETE", "/api/lines/"+l.Id, ""); status != 204 {
		t.Fatalf("delete = %d", status)
	}
	_, body = office.call("GET", "/api/lines?q=shipp&page=1&perPage=10", "")
	if err := json.Unmarshal(body, &list); err != nil || list.Total != 1 || list.Items[0].Active {
		t.Fatalf("search after deactivate = %s", body)
	}
	if status, _ := office.call("PATCH", "/api/lines/missing", `{"name":"x"}`); status != 404 {
		t.Fatalf("patch missing = %d; want 404", status)
	}
	// Each company sees its own list; customers and hauliers work the same way.
	if _, body := e.as(t, "B", "gate").call("GET", "/api/lines", ""); !strings.Contains(string(body), `"total":0`) {
		t.Fatalf("B sees A's lines: %s", body)
	}
	for _, res := range []string{"customers", "hauliers"} {
		if status, body := office.call("POST", "/api/"+res, `{"code":"C1","name":"One","contact":"a@b.c"}`); status != 201 || !strings.Contains(string(body), `"contact":"a@b.c"`) {
			t.Fatalf("create %s = %d %s", res, status, body)
		}
		if _, body := office.call("GET", "/api/"+res+"?q=one", ""); !strings.Contains(string(body), `"total":1`) {
			t.Fatalf("list %s = %s", res, body)
		}
	}
}

func TestDuplicateLineCodeIs409(t *testing.T) {
	office := newEnv(t, nil).as(t, "A", "office")
	office.call("POST", "/api/lines", `{"code":"MSC","name":"One"}`)
	if status, body := office.call("POST", "/api/lines", `{"code":"MSC","name":"Two"}`); status != 409 {
		t.Fatalf("duplicate create = %d %s; want 409", status, body)
	}
	_, body := office.call("POST", "/api/lines", `{"code":"MAEU","name":"Maersk"}`)
	var l api.Line
	_ = json.Unmarshal(body, &l)
	if status, body := office.call("PATCH", "/api/lines/"+l.Id, `{"code":"MSC"}`); status != 409 {
		t.Fatalf("duplicate rename = %d %s; want 409", status, body)
	}
}

func TestSearchLinersAndLocodesPutCodesFirst(t *testing.T) {
	e := newEnv(t, nil)
	conn, err := pgx.Connect(bg, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(bg)
	if _, err := conn.Exec(bg, `INSERT INTO ref_smdg_liners (code, name, version) VALUES
		('AAA', 'Msc feeder partner', 't'), ('MSC', 'Mediterranean Shipping Company', 't'), ('ZIM', 'Zim', 't') ON CONFLICT DO NOTHING;
		INSERT INTO ref_locodes (code, country, name, version) VALUES
		('TRMER', 'TR', 'Mersin', 't'), ('AAMER', 'AA', 'Trmer town', 't') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	u := e.as(t, "A", "gate")
	var liners []api.Liner
	_, body := u.call("GET", "/api/ref/liners?q=msc", "")
	if err := json.Unmarshal(body, &liners); err != nil || len(liners) != 2 || liners[0].Code != "MSC" || liners[1].Code != "AAA" {
		t.Fatalf("liners = %s", body)
	}
	if _, body := u.call("GET", "/api/ref/liners?q=%25", ""); body == nil || strings.Contains(string(body), "ZIM") {
		t.Fatalf("a %% in q must match literally: %s", body)
	}
	var locodes []api.Locode
	_, body = u.call("GET", "/api/ref/locodes?q=trmer", "")
	if err := json.Unmarshal(body, &locodes); err != nil || len(locodes) != 2 || locodes[0].Code != "TRMER" || locodes[0].Country != "TR" {
		t.Fatalf("locodes = %s", body)
	}
}

func TestSearchLiners(t *testing.T) {
	e := newEnv(t, nil)
	conn, err := pgx.Connect(bg, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(bg)
	if _, err := conn.Exec(bg, `INSERT INTO ref_smdg_liners (code, name, version) VALUES
		('ZZZ', 'Not a match', 't'), ('SGL', 'Sealand - A Maersk Company', 't'), ('MSK', 'Maersk', 't')`); err != nil {
		t.Fatal(err)
	}
	var liners []api.Liner
	_, body := e.as(t, "A", "gate").call("GET", "/api/ref/liners?q=maer", "")
	if err := json.Unmarshal(body, &liners); err != nil || len(liners) != 2 || liners[0].Code != "MSK" || liners[0].Name != "Maersk" {
		t.Fatalf("liners = %s", body)
	}
}

func TestNonAdminCannotListCompanies(t *testing.T) {
	owner := newEnv(t, nil).as(t, "A", "owner")
	if status, body := owner.call("GET", "/api/platform/companies", ""); status != 403 {
		t.Fatalf("owner lists companies = %d %s; want 403", status, body)
	}
	if status, body := owner.call("POST", "/api/platform/companies", `{"name":"X","country":"TR","ownerEmail":"a@b.c","modules":[]}`); status != 403 {
		t.Fatalf("owner creates a company = %d %s; want 403", status, body)
	}
}

// browser is a cookie-keeping client that follows redirects, as a browser does.
type browser struct {
	t   *testing.T
	e   *env
	c   *http.Client
	jar *cookiejar.Jar
}

func (e *env) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, e: e, c: &http.Client{Jar: jar}, jar: jar}
}

func (b *browser) get(path string) *http.Response {
	b.t.Helper()
	resp, err := b.c.Get(b.e.srv.URL + path)
	if err != nil {
		b.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func (b *browser) session() string {
	for _, c := range b.jar.Cookies(must(url.Parse(b.e.srv.URL))) {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	return ""
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// devLogin signs in through the dev login page as a browser would.
func (b *browser) devLogin(email string) *http.Response {
	b.t.Helper()
	page := b.get("/auth/login") // redirects to the dev page with a state
	state := page.Request.URL.Query().Get("state")
	if page.StatusCode != 200 || page.Request.URL.Path != "/auth/dev" || state == "" {
		b.t.Fatalf("login page = %d %s", page.StatusCode, page.Request.URL)
	}
	resp, err := b.c.PostForm(b.e.srv.URL+"/auth/dev", url.Values{"email": {email}, "state": {state}})
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestPlatformCreateCompanyInvitesOwner(t *testing.T) {
	e := newEnv(t, nil)
	admin := &user{t: t, e: e, cookie: e.signIn(t, "admin@x.com")}
	status, body := admin.call("POST", "/api/platform/companies", `{"name":"Acme","country":"TR","ownerEmail":"boss@x.com","modules":["ocr"]}`)
	var c api.Company
	if err := json.Unmarshal(body, &c); status != 201 || err != nil || c.Name != "Acme" || len(c.Modules) != 1 {
		t.Fatalf("create company = %d %s", status, body)
	}
	if status, body := admin.call("POST", "/api/platform/companies", `{"name":"X","country":"TR","ownerEmail":"b@x.com","modules":["nope"]}`); status != 400 {
		t.Fatalf("unknown module = %d %s; want 400", status, body)
	}
	if _, body := admin.call("GET", "/api/platform/companies", ""); strings.Count(string(body), `"id"`) != 3 {
		t.Fatalf("companies = %s", body)
	}

	b := e.browser(t)
	if resp := b.devLogin("boss@x.com"); resp.StatusCode != 200 || resp.Request.URL.Path != "/" {
		t.Fatalf("dev login ended at %d %s", resp.StatusCode, resp.Request.URL)
	}
	boss := &user{t: t, e: e, cookie: b.session()}
	_, body = boss.call("GET", "/api/session", "")
	var s api.Session
	if err := json.Unmarshal(body, &s); err != nil || len(s.Companies) != 1 || s.Companies[0].Id != c.Id || s.Companies[0].Role != "owner" {
		t.Fatalf("session = %s", body)
	}
	if status, body := boss.call("POST", "/api/session/scope", `{"companyId":"`+c.Id+`","depotId":""}`); status != 200 {
		t.Fatalf("scope = %d %s", status, body)
	}
	var depots []api.Depot
	_, body = boss.call("GET", "/api/depots", "")
	if err := json.Unmarshal(body, &depots); err != nil || len(depots) != 1 || depots[0].Id != "main" {
		t.Fatalf("depots = %s", body)
	}
	status, body = boss.call("POST", "/api/depots", `{"name":"Mersin","country":"TR","timezone":"Europe/Istanbul"}`)
	if status != 201 || !strings.Contains(string(body), `"name":"Mersin"`) {
		t.Fatalf("create depot = %d %s", status, body)
	}
}

func TestDevLoginNeedsTheDevProvider(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *gate.Service) { o.Provider = fakeIdP{} })
	if resp := e.browser(t).get("/auth/dev"); resp.StatusCode != 404 {
		t.Fatalf("GET /auth/dev with a real provider = %d; want 404", resp.StatusCode)
	}
}

// fakeIdP is an identity provider that signs everyone in as id.
type fakeIdP struct{ id auth.Identity }

func (fakeIdP) LoginURL(state string) string {
	return "/auth/callback?" + url.Values{"code": {"c"}, "state": {state}}.Encode()
}
func (f fakeIdP) Exchange(context.Context, string) (auth.Identity, error) { return f.id, nil }

func TestCallbackChecksStateAndReplacesTheSession(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *gate.Service) {
		o.Provider = fakeIdP{auth.Identity{Subject: "idp|ali", Email: "ali@x.com", EmailVerified: true}}
	})
	b := e.browser(t)
	if resp := b.get("/auth/callback?code=c&state=forged"); resp.StatusCode != 400 || b.session() != "" {
		t.Fatalf("callback without the state cookie = %d, session %q", resp.StatusCode, b.session())
	}
	if resp := b.get("/auth/login"); resp.StatusCode != 200 || resp.Request.URL.Path != "/" {
		t.Fatalf("login ended at %d %s", resp.StatusCode, resp.Request.URL)
	}
	first := b.session()
	if _, err := e.sessions.Resolve(bg, first); err != nil {
		t.Fatalf("first session: %v", err)
	}
	b.get("/auth/login")
	if second := b.session(); second == first || second == "" {
		t.Fatalf("second login kept cookie %q", second)
	}
	if _, err := e.sessions.Resolve(bg, first); err != auth.ErrNoSession {
		t.Fatalf("the replaced session still resolves: %v", err)
	}
}

func TestCallbackEmailHeldByAnotherSignInIs409(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *gate.Service) {
		o.Provider = fakeIdP{auth.Identity{Subject: "idp|boss", Email: "boss@x.com", EmailVerified: true}}
	})
	e.signIn(t, "boss@x.com") // subject test|boss@x.com holds the email
	resp, err := e.browser(t).c.Get(e.srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 409 || !strings.Contains(string(body), "already") {
		t.Fatalf("callback = %d %s; want 409", resp.StatusCode, body)
	}
}

// The client's can() is an exact match, so an owner's list must hold every permission up to owner.
func TestOwnerSessionListsEveryPermission(t *testing.T) {
	u := newEnv(t, nil).as(t, "A", "owner")
	var s api.Session
	_, body := u.call("GET", "/api/session", "")
	if err := json.Unmarshal(body, &s); err != nil || s.Current == nil ||
		!reflect.DeepEqual(s.Current.Permissions, []string{"gate", "office", "manage", "owner"}) {
		t.Fatalf("owner session = %s", body)
	}
	if d := s.Companies[0].Depots; len(d) != 1 || d[0].Id != "d1" {
		t.Fatalf("an unlimited member sees every depot: %+v", d)
	}
}

// A tab that still shows an old company or depot (another tab switched the
// browser's session) is refused instead of writing into the new scope.
func TestStaleTabScopeIs409(t *testing.T) {
	e := newEnv(t, nil)
	e.exec(t, "A", `INSERT INTO depots (company_id, id, name) VALUES ('A', 'd2', 'Second')`)
	u := e.as(t, "A", "office")
	withScope := func(method, path, scope string) (int, []byte) {
		req := u.request(method, path, nil, "")
		if scope != "" {
			req.Header.Set("X-Depot-Scope", scope)
		}
		return do(t, req)
	}
	if status, body := withScope("GET", "/api/moves", "A:d1"); status != 200 {
		t.Fatalf("matching scope = %d %s; want 200", status, body)
	}
	if status, body := withScope("GET", "/api/moves", ""); status != 200 {
		t.Fatalf("no scope header = %d %s; want 200", status, body)
	}
	if status, body := withScope("GET", "/api/moves", "A:d2"); status != 409 || errorCode(body) != "E4097" {
		t.Fatalf("other depot = %d %s; want 409 E4097", status, body)
	}
	if status, body := withScope("POST", "/api/moves", "B:d1"); status != 409 || errorCode(body) != "E4097" {
		t.Fatalf("other company = %d %s; want 409 E4097", status, body)
	}
	if status, body := withScope("GET", "/api/session", "A:d2"); status != 200 {
		t.Fatalf("session with a stale scope = %d %s; want 200", status, body)
	}
}

func TestInvitationAfterSignInCountsOnTheNextSessionRead(t *testing.T) {
	e := newEnv(t, nil)
	u := e.as(t, "A", "office")
	e.exec(t, "B", `INSERT INTO invitations (company_id, id, email, role, created_by) VALUES ('B', 'late', 'office@a.test', 'gate', 't')`)
	status, body := u.call("GET", "/api/session", "")
	var s api.Session
	if err := json.Unmarshal(body, &s); status != 200 || err != nil || len(s.Companies) != 2 {
		t.Fatalf("session = %d %s; want companies A and B", status, body)
	}
	if status, body := u.call("POST", "/api/session/scope", `{"companyId":"B","depotId":""}`); status != 200 {
		t.Fatalf("scope into the new company = %d %s", status, body)
	}
}

func TestStateCookieFollowsTheSecureSetting(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *gate.Service) { o.Sessions.SetSecureCookies(true) })
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(e.srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, ck := range resp.Cookies() {
		if ck.Name == stateCookie {
			if !ck.Secure {
				t.Fatal("state cookie is not Secure with secure cookies on")
			}
			return
		}
	}
	t.Fatalf("no state cookie in %v", resp.Header["Set-Cookie"])
}

func TestPublicHostsAreAllowed(t *testing.T) {
	public := []string{"App.Example.com.", " "}
	for host, want := range map[string]bool{
		"app.example.com":      true,
		"APP.example.com:443":  true,
		"app.example.com.":     true,
		"evil.example.com":     false,
		"app.example.com.evil": false,
		"":                     false,
	} {
		if got := publicHost(host, public); got != want {
			t.Errorf("publicHost(%q) = %v; want %v", host, got, want)
		}
	}
	if publicHost("app.example.com", nil) {
		t.Error("no configured hosts must allow nothing")
	}
}
