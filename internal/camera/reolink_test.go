package camera

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()

// fakeAPI imitates a Reolink camera's /cgi-bin/api.cgi.
type fakeAPI struct {
	mu         sync.Mutex
	user, pass string
	logins     int
	session    string // the token the camera accepts; "" when logged out or expired
	expireNext bool   // the next command answers "please login first"
	maxSession bool   // Login answers "max session"
	aiShape    string // "new", "old", "support0", "novehicle" or "none" (rspCode -9)
	firmware   string
	vehicle    int
	motion     int
	cmds       []string
	logouts    []string             // the token of every Logout
	failNext   map[string][]failure // one scripted bad answer per command, in turn
	hold       chan struct{}        // when set, commands with a dead token wait for it to close
	held       int                  // commands that waited on hold
}

// failure is one scripted bad answer, sent once the session token checks
// out: an HTTP status, a body that is not JSON, or an API error.
type failure struct {
	status int
	body   string
	rsp    int
	detail string
}

func (f *fakeAPI) token() string { return fmt.Sprintf("tok%d", f.logins) }

func (f *fakeAPI) alive(token string) bool { return token != "" && token == f.session }

// arrive counts a command sent with a dead token, and returns what it must
// wait for before it is answered, if anything.
func (f *fakeAPI) arrive(r *http.Request) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	if f.hold == nil || q.Get("cmd") == "Login" || q.Get("cmd") == "Logout" || f.alive(q.Get("token")) {
		return nil
	}
	f.held++
	return f.hold
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if hold := f.arrive(r); hold != nil {
		<-hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/cgi-bin/api.cgi" {
		w.WriteHeader(http.StatusOK) // the camera's web page
		return
	}
	cmd, token := r.URL.Query().Get("cmd"), r.URL.Query().Get("token")
	f.cmds = append(f.cmds, cmd)
	reply := func(v any) { _ = json.NewEncoder(w).Encode([]any{map[string]any{"cmd": cmd, "code": 0, "value": v}}) }
	fail := func(rsp int, detail string) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"cmd": cmd, "code": 1, "error": map[string]any{"rspCode": rsp, "detail": detail}}})
	}
	if cmd == "Logout" {
		f.logouts = append(f.logouts, token)
	}
	if cmd != "Login" && (!f.alive(token) || f.expireNext) {
		f.expireNext = false
		fail(-6, "please login first")
		return
	}
	if fs := f.failNext[cmd]; len(fs) > 0 {
		f.failNext[cmd] = fs[1:]
		switch fl := fs[0]; {
		case fl.status != 0:
			w.WriteHeader(fl.status)
		case fl.body != "":
			_, _ = io.WriteString(w, fl.body)
		default:
			fail(fl.rsp, fl.detail)
		}
		return
	}
	switch cmd {
	case "Login":
		if f.maxSession {
			fail(-5, "max session")
			return
		}
		var body []struct {
			Param struct {
				User struct {
					UserName string `json:"userName"`
					Password string `json:"password"`
				} `json:"User"`
			} `json:"param"`
		}
		data, _ := io.ReadAll(r.Body)
		if json.Unmarshal(data, &body) != nil || len(body) == 0 || body[0].Param.User.UserName != f.user || body[0].Param.User.Password != f.pass {
			fail(-7, "login failed")
			return
		}
		f.logins++
		f.session = f.token()
		reply(map[string]any{"Token": map[string]any{"leaseTime": 3600, "name": f.token()}})
	case "Logout":
		f.session = ""
		reply(map[string]any{"rspCode": 200})
	case "Snap":
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(DemoStill())
	case "GetAiState":
		switch f.aiShape {
		case "old":
			reply(map[string]any{"channel": 0, "people_state": 0, "vehicle_state": f.vehicle})
		case "support0":
			reply(map[string]any{"channel": 0, "people": map[string]int{"alarm_state": 0, "support": 1},
				"vehicle": map[string]int{"alarm_state": 0, "support": 0}})
		case "novehicle":
			reply(map[string]any{"channel": 0, "people": map[string]int{"alarm_state": 0, "support": 1}})
		case "none":
			fail(-9, "not support")
		default:
			reply(map[string]any{"channel": 0, "people": map[string]int{"alarm_state": 0, "support": 1},
				"vehicle": map[string]int{"alarm_state": f.vehicle, "support": 1}})
		}
	case "GetMdState":
		reply(map[string]any{"state": f.motion})
	case "GetDevInfo":
		reply(map[string]any{"DevInfo": map[string]any{"model": "RLC-810WA", "firmVer": f.firmware}})
	case "GetNetPort":
		reply(map[string]any{"NetPort": map[string]any{"httpEnable": 1, "rtspEnable": 1, "rtspPort": 554}})
	case "GetEnc":
		reply(map[string]any{"Enc": map[string]any{"mainStream": map[string]any{"size": "3840*2160"}}})
	default:
		fail(-1, "unknown command")
	}
}

// set changes the fake camera between requests.
func (f *fakeAPI) set(fn func(*fakeAPI)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// fail scripts the next answers to cmd.
func (f *fakeAPI) fail(cmd string, fs ...failure) {
	f.set(func(f *fakeAPI) {
		if f.failNext == nil {
			f.failNext = map[string][]failure{}
		}
		f.failNext[cmd] = append(f.failNext[cmd], fs...)
	})
}

func (f *fakeAPI) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

// count is how many requests for cmd reached the camera.
func (f *fakeAPI) count(cmd string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.cmds {
		if c == cmd {
			n++
		}
	}
	return n
}

func newFakeCamera(t *testing.T) (*fakeAPI, *Reolink) {
	t.Helper()
	return newFakeCameraWith(t, "pw-secret", "pw-secret")
}

// newFakeCameraWith serves a camera whose password is pass and connects to it
// with password try.
func newFakeCameraWith(t *testing.T, pass, try string) (*fakeAPI, *Reolink) {
	t.Helper()
	api := &fakeAPI{user: "admin", pass: pass, aiShape: "new", firmware: "v3.1.0.2368"}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, NewReolink(Settings{Host: strings.TrimPrefix(srv.URL, "http://"), User: "admin", Password: try})
}

func TestReolinkLogsInOnceAndReusesTheToken(t *testing.T) {
	api, cam := newFakeCamera(t)
	for i := 0; i < 3; i++ {
		if _, err := cam.Detect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := api.loginCount(); n != 1 {
		t.Fatalf("logins = %d; want 1", n)
	}
}

func TestReolinkLogsInAgainWhenTheSessionExpires(t *testing.T) {
	api, cam := newFakeCamera(t)
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	api.set(func(f *fakeAPI) { f.expireNext = true })
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatalf("Detect after expiry = %v", err)
	}
	if n := api.loginCount(); n != 2 {
		t.Fatalf("logins = %d; want 2", n)
	}
	api.set(func(f *fakeAPI) {
		if strings.Join(f.logouts, ",") != "tok1" {
			t.Errorf("logouts = %q; want the old session tok1 logged out before the new login", f.logouts)
		}
	})
}

func TestReolinkRenewsAnOldTokenAndLogsItOut(t *testing.T) {
	api, cam := newFakeCamera(t)
	now := time.Now()
	cam.now = func() time.Time { return now }
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour) // past leaseTime minus a minute
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	if n := api.loginCount(); n != 2 {
		t.Fatalf("logins = %d; want 2", n)
	}
	api.set(func(f *fakeAPI) {
		if strings.Join(f.logouts, ",") != "tok1" {
			t.Errorf("logouts = %q; want tok1", f.logouts)
		}
	})
}

func TestReolinkLogsInOnceWhenManyCallsFindTheSessionExpired(t *testing.T) {
	api, cam := newFakeCamera(t)
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	// The session dies on the camera, and every call is told so before any
	// of them can log in again.
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	api.set(func(f *fakeAPI) { f.session, f.hold = "", hold })
	const calls = 8
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			_, err := cam.Detect(ctx)
			errs <- err
		})
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		held := 0
		api.set(func(f *fakeAPI) { held = f.held })
		if held >= calls {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d calls reached the camera", held, calls)
		}
	}
	release()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Detect = %v", err)
		}
	}
	if n := api.count("Login"); n != 2 {
		t.Fatalf("Login requests = %d; want 2 (the first, then exactly one new session)", n)
	}
	if n := api.count("Logout"); n != 1 {
		t.Fatalf("Logout requests = %d; want 1", n)
	}
}

func TestReolinkReadsBothAiStateShapesAndFallsBackToMotion(t *testing.T) {
	for _, tc := range []struct {
		shape string
		want  Detection
	}{
		{"new", Detection{Present: true, Kind: KindVehicle}},
		{"old", Detection{Present: true, Kind: KindVehicle}},
		{"none", Detection{Present: true, Kind: KindMotion}},      // rspCode -9
		{"support0", Detection{Present: true, Kind: KindMotion}},  // vehicle.support 0
		{"novehicle", Detection{Present: true, Kind: KindMotion}}, // no vehicle key at all
	} {
		api, cam := newFakeCamera(t)
		api.set(func(f *fakeAPI) { f.aiShape, f.vehicle, f.motion = tc.shape, 1, 1 })
		got, err := cam.Detect(ctx)
		if err != nil || got != tc.want {
			t.Errorf("%s: Detect = %+v, %v; want %+v", tc.shape, got, err, tc.want)
		}
	}
}

func TestReolinkKeepsVehicleDetectionThroughAFailedQuery(t *testing.T) {
	api, cam := newFakeCamera(t)
	api.set(func(f *fakeAPI) { f.vehicle, f.motion = 1, 0 })
	want := Detection{Present: true, Kind: KindVehicle}
	if d, err := cam.Detect(ctx); err != nil || d != want {
		t.Fatalf("Detect = %+v, %v; want %+v", d, err, want)
	}
	for _, fl := range []failure{
		{status: http.StatusServiceUnavailable},
		{body: "<html>busy</html>"},
		{rsp: -12, detail: "get config failed"},
		{rsp: -9, detail: "not support"}, // even "not support": the camera has shown it can
	} {
		api.fail("GetAiState", fl)
		if d, err := cam.Detect(ctx); err == nil {
			t.Fatalf("Detect with %+v = %+v; want an error", fl, d)
		}
		if d, err := cam.Detect(ctx); err != nil || d != want {
			t.Fatalf("Detect after %+v = %+v, %v; want %+v", fl, d, err, want)
		}
	}
	if n := api.count("GetMdState"); n != 0 {
		t.Fatalf("GetMdState asked %d times; want never", n)
	}
}

func TestReolinkTransientAiStateFailureDoesNotChooseMotion(t *testing.T) {
	api, cam := newFakeCamera(t)
	api.set(func(f *fakeAPI) { f.vehicle = 1 })
	api.fail("GetAiState", failure{status: http.StatusServiceUnavailable})
	if _, err := cam.Detect(ctx); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Detect = %v; want ErrProtocol", err)
	}
	if d, err := cam.Detect(ctx); err != nil || d.Kind != KindVehicle {
		t.Fatalf("Detect = %+v, %v; want vehicle", d, err)
	}
}

func TestReolinkWrongPasswordIsAuthErrorWithoutThePassword(t *testing.T) {
	_, cam := newFakeCameraWith(t, "pw-secret", "wrong-pw")
	_, err := cam.Detect(ctx)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v; want ErrAuth", err)
	}
	if strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestReolinkStopsAskingAfterAWrongPassword(t *testing.T) {
	api, cam := newFakeCameraWith(t, "pw-secret", "wrong-pw")
	for i := 0; i < 5; i++ {
		if _, err := cam.Snapshot(ctx); !errors.Is(err, ErrAuth) {
			t.Fatalf("Snapshot %d = %v; want ErrAuth", i, err)
		}
	}
	if _, err := cam.Detect(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("Detect = %v; want ErrAuth", err)
	}
	if _, err := cam.Info(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("Info = %v; want ErrAuth", err)
	}
	if n := api.count("Login"); n != 1 {
		t.Fatalf("Login requests = %d; want 1", n)
	}
	api.set(func(f *fakeAPI) {
		if len(f.cmds) != 1 {
			t.Fatalf("requests = %v; want only the one Login", f.cmds)
		}
	})
	// The setup test is a person asking: it tries once more, and only once.
	rep := cam.Test(ctx)
	if rep.OK() || len(rep.Steps) != 2 || rep.Steps[1].Name != "login" || rep.Steps[1].OK {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := cam.Snapshot(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("Snapshot after the test = %v; want ErrAuth", err)
	}
	if n := api.count("Login"); n != 2 {
		t.Fatalf("Login requests = %d; want 2", n)
	}
}

func TestReolinkMaxSessionIsRetryableNotAuth(t *testing.T) {
	api, cam := newFakeCamera(t)
	api.set(func(f *fakeAPI) { f.maxSession = true })
	_, err := cam.Detect(ctx)
	if errors.Is(err, ErrAuth) || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v; want ErrUnreachable, not ErrAuth", err)
	}
	if !strings.Contains(err.Error(), "too many open sessions") {
		t.Fatalf("err = %v; want it to say the camera has too many sessions", err)
	}
	if rep := cam.Test(ctx); len(rep.Steps) != 2 || !strings.Contains(rep.Steps[1].Detail, "too many open sessions") {
		t.Fatalf("report = %+v", rep)
	}
	api.set(func(f *fakeAPI) { f.maxSession = false })
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatalf("Detect once a session is free = %v", err)
	}
}

func TestReolinkSnapshotIsAJPEG(t *testing.T) {
	_, cam := newFakeCamera(t)
	data, err := cam.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if http.DetectContentType(data) != "image/jpeg" {
		t.Fatalf("snapshot is %s", http.DetectContentType(data))
	}
}

func TestReolinkSnapshotLogsInAgainWhenTheSessionExpires(t *testing.T) {
	api, cam := newFakeCamera(t)
	if _, err := cam.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	api.set(func(f *fakeAPI) { f.expireNext = true })
	data, err := cam.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot after expiry = %v", err)
	}
	if http.DetectContentType(data) != "image/jpeg" {
		t.Fatalf("snapshot is %s", http.DetectContentType(data))
	}
	if n := api.loginCount(); n != 2 {
		t.Fatalf("logins = %d; want 2", n)
	}
}

func TestReolinkSnapshotErrorCarriesTheCameraDetail(t *testing.T) {
	api, cam := newFakeCamera(t)
	api.fail("Snap", failure{rsp: -12, detail: "get config failed"})
	_, err := cam.Snapshot(ctx)
	if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "get config failed") || !strings.Contains(err.Error(), "-12") {
		t.Fatalf("err = %v; want ErrProtocol with the camera's detail", err)
	}
	if n := api.loginCount(); n != 1 {
		t.Fatalf("logins = %d; want 1", n)
	}
}

func TestReolinkUnreachableErrorHidesTheToken(t *testing.T) {
	var mu sync.Mutex
	dropped := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cmd") == "Login" {
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"cmd": "Login", "code": 0,
				"value": map[string]any{"Token": map[string]any{"leaseTime": 3600, "name": "tok1"}}}})
			return
		}
		if r.URL.Query().Get("token") == "tok1" {
			mu.Lock()
			dropped++
			mu.Unlock()
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close() // the connection drops mid-request
	}))
	defer srv.Close()
	cam := NewReolink(Settings{Host: strings.TrimPrefix(srv.URL, "http://"), User: "admin", Password: "pw-secret"})
	_, detectErr := cam.Detect(ctx)
	_, snapErr := cam.Snapshot(ctx)
	for name, err := range map[string]error{"Detect": detectErr, "Snapshot": snapErr} {
		if !errors.Is(err, ErrUnreachable) {
			t.Errorf("%s err = %v; want ErrUnreachable", name, err)
			continue
		}
		if strings.Contains(err.Error(), "tok1") || strings.Contains(err.Error(), "pw-secret") {
			t.Errorf("%s error leaks a secret: %v", name, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if dropped < 2 {
		t.Fatalf("%d requests carried the token; want both calls to have sent it", dropped)
	}
}

func TestReolinkRawAnswerHidesTokenAndPassword(t *testing.T) {
	_, cam := newFakeCamera(t)
	if _, err := cam.login(ctx, ""); err != nil {
		t.Fatal(err)
	}
	raw := cam.lastRaw()
	if !strings.Contains(raw, "Login") || strings.Contains(raw, "tok1") || strings.Contains(raw, "pw-secret") {
		t.Fatalf("raw answer = %s", raw)
	}
}

func TestReolinkRawAnswerKeepsTextThatContainsThePassword(t *testing.T) {
	_, cam := newFakeCameraWith(t, "2368", "2368")
	var dev map[string]any
	if err := cam.cmd(ctx, "GetDevInfo", map[string]any{}, &dev); err != nil {
		t.Fatal(err)
	}
	if raw := cam.lastRaw(); !strings.Contains(raw, `"firmVer":"v3.1.0.2368"`) {
		t.Fatalf("raw answer = %s; want the firmware intact", raw)
	}
	cam.remember("GetUser", []byte(`[{"cmd":"GetUser","code":0,"value":{"User":[`+
		`{"userName":"admin","Password":"2368"},{"userName":"guest","password":"x\"2368"}],"firmVer":"v3.1.0.2368"}}]`))
	raw := cam.lastRaw()
	if !strings.Contains(raw, `"firmVer":"v3.1.0.2368"`) || strings.Contains(raw, `"2368"`) || strings.Contains(raw, `x\"2368`) ||
		strings.Count(raw, `"***"`) != 2 {
		t.Fatalf("raw answer = %s; want both passwords hidden and the firmware intact", raw)
	}
}

func TestReolinkTestReportIsAllGreen(t *testing.T) {
	_, cam := newFakeCamera(t)
	rep := cam.Test(ctx)
	if !rep.OK() {
		t.Fatalf("report = %+v", rep)
	}
	names := []string{}
	for _, s := range rep.Steps {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "reach,login,info,ports,detect,snapshot" {
		t.Fatalf("steps = %v", names)
	}
	if rep.Info.Model != "RLC-810WA" || rep.Info.Width != 3840 || rep.Info.Height != 2160 || !rep.Info.RTSP || rep.Info.Detection != KindVehicle {
		t.Fatalf("info = %+v", rep.Info)
	}
	if rep.Raw == "" {
		t.Fatal("report has no raw answer")
	}
}

func TestReolinkTestKeepsAValidSession(t *testing.T) {
	api, cam := newFakeCamera(t)
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	if rep := cam.Test(ctx); !rep.OK() {
		t.Fatalf("report = %+v", rep)
	}
	if n, m := api.count("Login"), api.count("Logout"); n != 1 || m != 0 {
		t.Fatalf("Login requests = %d, Logout requests = %d; want 1 and 0", n, m)
	}
}

func TestReolinkTestReportSaysWhenThePortSettingsCannotBeRead(t *testing.T) {
	api, cam := newFakeCamera(t)
	api.fail("GetNetPort", failure{rsp: -26, detail: "ability error"})
	rep := cam.Test(ctx)
	var ports *Step
	for i := range rep.Steps {
		if rep.Steps[i].Name == "ports" {
			ports = &rep.Steps[i]
		}
	}
	if ports == nil || ports.OK {
		t.Fatalf("report = %+v; want a failed ports step", rep)
	}
	if !strings.HasPrefix(ports.Detail, "could not read the port settings: ") || !strings.Contains(ports.Detail, "ability error") ||
		strings.Contains(ports.Detail, "RTSP is off") {
		t.Fatalf("ports detail = %q", ports.Detail)
	}
	if b, _ := json.Marshal(rep.Info); !strings.Contains(string(b), `"rtsp":false`) {
		t.Fatalf("info JSON = %s", b)
	}
}

func TestReolinkTestReportExplainsAnUnreachableCamera(t *testing.T) {
	cam := NewReolink(Settings{Host: "127.0.0.1:1", User: "admin", Password: "pw"})
	rep := cam.Test(ctx)
	if rep.OK() || len(rep.Steps) != 1 || rep.Steps[0].Name != "reach" || !strings.Contains(rep.Steps[0].Detail, "Port Settings") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestReolinkLiveSourceUsesTheSubStreamOnPort554(t *testing.T) {
	cam := NewReolink(Settings{Host: "192.168.1.50:80", User: "admin", Password: "p@ss word"})
	want := "rtsp://admin:p%40ss%20word@192.168.1.50:554/h264Preview_01_sub"
	if got := cam.LiveSource(); got != want {
		t.Fatalf("LiveSource = %s; want %s", got, want)
	}
}

func TestFakeCamera(t *testing.T) {
	f := NewFake([]byte("a"), []byte("b"))
	d, _ := f.Detect(ctx)
	if d.Present {
		t.Fatal("vehicle present before SetVehicle")
	}
	f.SetVehicle(true)
	if d, _ := f.Detect(ctx); !d.Present || d.Kind != KindVehicle {
		t.Fatalf("Detect = %+v", d)
	}
	s1, _ := f.Snapshot(ctx)
	s2, _ := f.Snapshot(ctx)
	s3, _ := f.Snapshot(ctx)
	if string(s1) != "a" || string(s2) != "b" || string(s3) != "a" || f.Snaps() != 3 {
		t.Fatalf("stills = %q %q %q, snaps %d", s1, s2, s3, f.Snaps())
	}
	f.SetSnapErr(ErrUnreachable)
	if _, err := f.Snapshot(ctx); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Snapshot = %v", err)
	}
	if http.DetectContentType(DemoStill()) != "image/jpeg" {
		t.Fatal("DemoStill is not a JPEG")
	}
	if !NewDemoFake().Test(ctx).OK() {
		t.Fatal("demo fake test report is not green")
	}
}
