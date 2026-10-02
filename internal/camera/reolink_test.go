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
)

var ctx = context.Background()

// fakeAPI imitates a Reolink camera's /cgi-bin/api.cgi.
type fakeAPI struct {
	mu         sync.Mutex
	user, pass string
	logins     int
	expireNext bool   // the next command answers "please login first"
	aiShape    string // "new", "old" or "none" (no vehicle detection)
	vehicle    int
	motion     int
	cmds       []string
}

func (f *fakeAPI) token() string { return fmt.Sprintf("tok%d", f.logins) }

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/cgi-bin/api.cgi" {
		w.WriteHeader(http.StatusOK) // the camera's web page
		return
	}
	cmd := r.URL.Query().Get("cmd")
	f.cmds = append(f.cmds, cmd)
	reply := func(v any) { _ = json.NewEncoder(w).Encode([]any{map[string]any{"cmd": cmd, "code": 0, "value": v}}) }
	fail := func(rsp int, detail string) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"cmd": cmd, "code": 1, "error": map[string]any{"rspCode": rsp, "detail": detail}}})
	}
	if cmd != "Login" && (r.URL.Query().Get("token") != f.token() || f.expireNext) {
		f.expireNext = false
		fail(-6, "please login first")
		return
	}
	switch cmd {
	case "Login":
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
		reply(map[string]any{"Token": map[string]any{"leaseTime": 3600, "name": f.token()}})
	case "Snap":
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(DemoStill())
	case "GetAiState":
		switch f.aiShape {
		case "old":
			reply(map[string]any{"channel": 0, "people_state": 0, "vehicle_state": f.vehicle})
		case "none":
			fail(-9, "not support")
		default:
			reply(map[string]any{"channel": 0, "people": map[string]int{"alarm_state": 0, "support": 1},
				"vehicle": map[string]int{"alarm_state": f.vehicle, "support": 1}})
		}
	case "GetMdState":
		reply(map[string]any{"state": f.motion})
	case "GetDevInfo":
		reply(map[string]any{"DevInfo": map[string]any{"model": "RLC-810WA", "firmVer": "v3.1.0.2368"}})
	case "GetNetPort":
		reply(map[string]any{"NetPort": map[string]any{"httpEnable": 1, "rtspEnable": 1, "rtspPort": 554}})
	case "GetEnc":
		reply(map[string]any{"Enc": map[string]any{"mainStream": map[string]any{"size": "3840*2160"}}})
	default:
		fail(-1, "unknown command")
	}
}

func (f *fakeAPI) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func newFakeCamera(t *testing.T) (*fakeAPI, *Reolink) {
	t.Helper()
	api := &fakeAPI{user: "admin", pass: "pw-secret", aiShape: "new"}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, NewReolink(Settings{Host: strings.TrimPrefix(srv.URL, "http://"), User: "admin", Password: "pw-secret"})
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
	api.mu.Lock()
	api.expireNext = true
	api.mu.Unlock()
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatalf("Detect after expiry = %v", err)
	}
	if n := api.loginCount(); n != 2 {
		t.Fatalf("logins = %d; want 2", n)
	}
}

func TestReolinkReadsBothAiStateShapesAndFallsBackToMotion(t *testing.T) {
	for _, tc := range []struct {
		shape string
		want  Detection
	}{
		{"new", Detection{Present: true, Kind: KindVehicle}},
		{"old", Detection{Present: true, Kind: KindVehicle}},
		{"none", Detection{Present: true, Kind: KindMotion}},
	} {
		api, cam := newFakeCamera(t)
		api.aiShape, api.vehicle, api.motion = tc.shape, 1, 1
		got, err := cam.Detect(ctx)
		if err != nil || got != tc.want {
			t.Errorf("%s: Detect = %+v, %v; want %+v", tc.shape, got, err, tc.want)
		}
	}
}

func TestReolinkWrongPasswordIsAuthErrorWithoutThePassword(t *testing.T) {
	api := &fakeAPI{user: "admin", pass: "pw-secret", aiShape: "new"}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cam := NewReolink(Settings{Host: strings.TrimPrefix(srv.URL, "http://"), User: "admin", Password: "wrong-pw"})
	_, err := cam.Detect(ctx)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v; want ErrAuth", err)
	}
	if strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("error leaks the password: %v", err)
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

func TestReolinkUnreachableErrorHidesTheToken(t *testing.T) {
	cam := NewReolink(Settings{Host: "127.0.0.1:1", User: "admin", Password: "pw"})
	_, err := cam.Detect(ctx)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v; want ErrUnreachable", err)
	}
	if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "pw") {
		t.Fatalf("error leaks a secret: %v", err)
	}
}

func TestReolinkRawAnswerHidesTokenAndPassword(t *testing.T) {
	_, cam := newFakeCamera(t)
	if _, err := cam.login(ctx, true); err != nil {
		t.Fatal(err)
	}
	raw := cam.lastRaw()
	if !strings.Contains(raw, "Login") || strings.Contains(raw, "tok1") || strings.Contains(raw, "pw-secret") {
		t.Fatalf("raw answer = %s", raw)
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
