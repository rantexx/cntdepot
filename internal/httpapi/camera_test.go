package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"depot/internal/camera"
	"depot/internal/gate"
	"depot/internal/gatecam"
	"depot/internal/live"
	"depot/internal/tenancy"
	"depot/internal/testkit"
)

// newCameraEnv is newEnv on a hub with camera support for company A's depot
// d1: a fake camera behind the manager, a running watcher, and live video that
// is never started. It returns a manager of A and the fake camera.
func newCameraEnv(t *testing.T) (*user, *camera.Fake) {
	t.Helper()
	cam := camera.NewFake(camera.DemoStill())
	runCtx, cancel := context.WithCancel(context.Background())
	watcherDone := make(chan struct{})
	e := newEnv(t, func(o *Options, svc *gate.Service) {
		w := gatecam.New(svc, gatecam.DefaultConfig())
		svc.SetReadHook(w.OnRead)
		sup := live.New(live.Config{}) // never run: live video stays off
		mgr := gatecam.NewManager(o.Store, w, sup, func(camera.Settings) camera.Camera { return cam }, testkit.Scope)
		if err := mgr.Load(runCtx); err != nil {
			t.Fatal(err)
		}
		o.Camera, o.Live, o.Hub = mgr, sup, true
		go func() { w.Run(tenancy.With(runCtx, testkit.Scope)); close(watcherDone) }()
	})
	t.Cleanup(func() {
		cancel()
		<-watcherDone
	})
	return e.as(t, "A", "manager"), cam
}

const setupBody = `{"host":"192.168.1.50","user":"admin","password":"pw-secret"}`

func TestCameraUnavailableOutsideHub(t *testing.T) {
	e := newEnv(t, nil)
	m := e.as(t, "A", "manager")
	status, body := m.call("GET", "/api/camera", "")
	if status != 200 || strings.TrimSpace(string(body)) != `{"available":false,"reason":"needs_onsite_box"}` {
		t.Fatalf("GET /api/camera = %d %s", status, body)
	}
	for _, r := range []struct{ method, path string }{{"PUT", "/api/camera"}, {"POST", "/api/camera/capture"}, {"GET", "/api/camera/still"}, {"GET", "/api/live/ws"}} {
		if status, body := m.call(r.method, r.path, ""); status != 404 {
			t.Errorf("%s %s outside a hub = %d %s; want 404", r.method, r.path, status, body)
		}
	}
	if status, body := e.as(t, "A", "office").call("GET", "/api/camera", ""); status != 403 {
		t.Fatalf("office GET /api/camera = %d %s; want 403", status, body)
	}
}

func TestCameraBelongsToItsCompany(t *testing.T) {
	m, _ := newCameraEnv(t)
	other := m.e.as(t, "B", "owner")
	for _, r := range []struct{ method, path, body string }{{"GET", "/api/camera", ""}, {"PUT", "/api/camera", setupBody}, {"POST", "/api/camera/capture", ""}} {
		if status, body := other.call(r.method, r.path, r.body); status != 404 {
			t.Errorf("B %s %s = %d %s; want 404", r.method, r.path, status, body)
		}
	}
	if _, body := m.call("GET", "/api/camera", ""); !strings.Contains(string(body), `"password_set":false`) {
		t.Fatalf("B changed A's camera: %s", body)
	}
}

func TestCameraBelongsToItsDepot(t *testing.T) {
	m, _ := newCameraEnv(t)
	e := m.e
	call(m, "PUT", "/api/camera", setupBody, 200)
	_, body := m.call("POST", "/api/camera/read", "")
	var r struct{ Still string }
	if err := json.Unmarshal(body, &r); err != nil || r.Still == "" {
		t.Fatalf("read = %s", body)
	}
	e.exec(t, "A", `INSERT INTO depots (company_id, id, name) VALUES ('A', 'd2', 'Two')`)
	e.exec(t, "A", `INSERT INTO invitations (company_id, id, email, role, depot_ids, created_by) VALUES ('A', 'i2', 'm2@a.test', 'manager', '{d2}', 't')`)
	cookie := e.signIn(t, "m2@a.test")
	if err := e.sessions.SetScope(bg, cookie, "A", "d2"); err != nil {
		t.Fatal(err)
	}
	other := &user{t: t, e: e, cookie: cookie}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/camera", ""}, {"PUT", "/api/camera", setupBody}, {"POST", "/api/camera/capture", ""},
		{"POST", "/api/camera/read", ""}, {"GET", "/api/camera/stills/" + r.Still, ""}, {"GET", "/api/live/ws", ""},
	} {
		if status, body := other.call(c.method, c.path, c.body); status != 404 || errorCode(body) != "E4044" {
			t.Errorf("d2 manager %s %s = %d %s; want 404 E4044", c.method, c.path, status, body)
		}
	}
}

// call sends a request as u and fails the test unless it answers want.
func call(u *user, method, path, body string, want int) {
	u.t.Helper()
	if status, resp := u.call(method, path, body); status != want {
		u.t.Fatalf("%s %s = %d %s; want %d", method, path, status, resp, want)
	}
}

func TestCameraSettingsNeverReturnThePassword(t *testing.T) {
	m, _ := newCameraEnv(t)
	status, body := m.call("PUT", "/api/camera", setupBody)
	if status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	for _, b := range [][]byte{body, func() []byte { _, b := m.call("GET", "/api/camera", ""); return b }()} {
		if strings.Contains(string(b), "pw-secret") {
			t.Fatalf("camera answer leaks the password: %s", b)
		}
		var v struct {
			Available bool `json:"available"`
			Settings  struct {
				Host        string `json:"host"`
				PasswordSet bool   `json:"password_set"`
			} `json:"settings"`
		}
		if err := json.Unmarshal(b, &v); err != nil || !v.Available || v.Settings.Host != "192.168.1.50" || !v.Settings.PasswordSet {
			t.Fatalf("camera view = %s", b)
		}
	}
	if status, body := m.call("PUT", "/api/camera", `{"host":"http://192.168.1.50/","user":"admin"}`); status != 400 || errorCode(body) != "E4007" {
		t.Fatalf("PUT with a URL = %d %s; want 400 E4007", status, body)
	}
}

func TestANewCameraAddressNeedsThePasswordAgain(t *testing.T) {
	m, _ := newCameraEnv(t)
	if status, body := m.call("PUT", "/api/camera", setupBody); status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	status, body := m.call("PUT", "/api/camera", `{"host":"203.0.113.9","user":"admin","password":""}`)
	if status != 400 || errorCode(body) != "E4007" || !strings.Contains(string(body), "enter the camera password again") {
		t.Fatalf("PUT with a new host and no password = %d %s; want 400 E4007", status, body)
	}
	if _, body := m.call("GET", "/api/camera", ""); !strings.Contains(string(body), `"host":"192.168.1.50"`) {
		t.Fatalf("a refused PUT changed the camera settings: %s", body)
	}
	// The same address with an empty password is the re-save that reconnects.
	if status, body := m.call("PUT", "/api/camera", `{"host":"192.168.1.50","user":"admin","password":""}`); status != 200 {
		t.Fatalf("PUT with the same host = %d %s", status, body)
	}
}

func TestCameraMustBeSetUpBeforeUse(t *testing.T) {
	m, _ := newCameraEnv(t)
	for _, path := range []string{"/api/camera/read", "/api/camera/capture", "/api/camera/test"} {
		if status, body := m.call("POST", path, ""); status != 409 || errorCode(body) != "E4091" {
			t.Errorf("POST %s = %d %s; want 409 E4091", path, status, body)
		}
	}
}

func TestReadThisFrameMeasuresTheLetters(t *testing.T) {
	m, _ := newCameraEnv(t)
	if status, body := m.call("PUT", "/api/camera", setupBody); status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	status, body := m.call("POST", "/api/camera/read", "")
	if status != 200 {
		t.Fatalf("read = %d %s", status, body)
	}
	var r struct {
		Still    string `json:"still"`
		Width    int    `json:"width"`
		LetterPX int    `json:"letter_px"`
		Verdict  string `json:"verdict"`
		Result   struct {
			Frame struct {
				Candidates []struct {
					Number string `json:"number"`
				} `json:"candidates"`
			} `json:"frame"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.Width != 1280 || r.LetterPX != 72 || r.Verdict != gatecam.VerdictGood || len(r.Result.Frame.Candidates) == 0 {
		t.Fatalf("read = %s", body)
	}
	// The still is no gate photo: the blob route does not serve it, the camera's does.
	if status, _ := m.call("GET", "/api/blobs/"+r.Still, ""); status != 404 {
		t.Fatalf("still through /api/blobs = %d; want 404", status)
	}
	res, err := http.DefaultClient.Do(m.request("GET", "/api/camera/stills/"+r.Still+"?w=320", nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	img, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if res.StatusCode != 200 || err != nil || img.Width != 320 {
		t.Fatalf("still preview = %d, %v, width %d", res.StatusCode, err, img.Width)
	}
}

func TestCameraTestAndStill(t *testing.T) {
	m, _ := newCameraEnv(t)
	m.call("PUT", "/api/camera", setupBody)
	status, body := m.call("POST", "/api/camera/test", "")
	var rep camera.Report
	if status != 200 || json.Unmarshal(body, &rep) != nil || !rep.OK() {
		t.Fatalf("test = %d %s", status, body)
	}
	res, err := http.DefaultClient.Do(m.request("GET", "/api/camera/still?w=320", nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("still = %d, Cache-Control %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	if img, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil || img.Width != 320 {
		t.Fatalf("still = %v, width %d", err, img.Width)
	}
}

func TestTakePhotosNowCreatesACameraMove(t *testing.T) {
	m, _ := newCameraEnv(t)
	m.call("PUT", "/api/camera", setupBody)
	waitCameraState(t, m, gatecam.StateOnline)
	status, body := m.call("POST", "/api/camera/capture", "")
	var r struct {
		MoveID string `json:"move_id"`
	}
	if status != 202 || json.Unmarshal(body, &r) != nil || r.MoveID == "" {
		t.Fatalf("capture = %d %s", status, body)
	}
	_, body = m.call("GET", "/api/moves?source=camera", "")
	if !strings.Contains(string(body), r.MoveID) {
		t.Fatalf("camera moves = %s; want %s", body, r.MoveID)
	}
	_, body = m.call("GET", "/api/moves?source=phone", "")
	if strings.Contains(string(body), r.MoveID) {
		t.Fatalf("phone moves include the camera move: %s", body)
	}
}

func TestTakePhotosNowNeedsAConnectedCamera(t *testing.T) {
	m, cam := newCameraEnv(t)
	if status, body := m.call("PUT", "/api/camera", setupBody); status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	waitCameraState(t, m, gatecam.StateOnline)
	cam.SetDetectErr(fmt.Errorf("%w: login failed", camera.ErrAuth))
	waitCameraState(t, m, gatecam.StateAuth)
	if status, body := m.call("POST", "/api/camera/capture", ""); status != 409 || errorCode(body) != "E4093" {
		t.Fatalf("capture with a rejected password = %d %s; want 409 E4093", status, body)
	}
	if _, body := m.call("GET", "/api/moves?source=camera", ""); strings.Contains(string(body), `"id"`) {
		t.Fatalf("a refused capture made a move: %s", body)
	}
}

func TestCameraWritesNeedTheClientHeaderAndStillWidthIsChecked(t *testing.T) {
	m, _ := newCameraEnv(t)
	if status, body := rawCall(t, "PUT", m.e.srv.URL+"/api/camera", "", "", setupBody); status != 403 || errorCode(body) != "E5004" {
		t.Fatalf("PUT without the client header = %d %s; want 403 E5004", status, body)
	}
	if _, body := m.call("GET", "/api/camera", ""); !strings.Contains(string(body), `"password_set":false`) {
		t.Fatalf("a refused PUT changed the camera settings: %s", body)
	}
	m.call("PUT", "/api/camera", setupBody)
	if status, body := m.call("GET", "/api/camera/still?w=999", ""); status != 400 || errorCode(body) != "E4006" {
		t.Fatalf("still with a bad width = %d %s; want 400 E4006", status, body)
	}
}

// waitCameraState polls GET /api/camera until the watcher reports want.
func waitCameraState(t *testing.T, m *user, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := m.call("GET", "/api/camera", "")
		var v struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		if v.Status.State == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("camera state = %q; want %q", v.Status.State, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAGreenTestAfterAWrongPasswordResumesPolling(t *testing.T) {
	m, cam := newCameraEnv(t)
	if status, body := m.call("PUT", "/api/camera", setupBody); status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	waitCameraState(t, m, gatecam.StateOnline)
	cam.SetDetectErr(fmt.Errorf("%w: login failed", camera.ErrAuth))
	waitCameraState(t, m, gatecam.StateAuth)

	// A failed test changes nothing.
	m.call("POST", "/api/camera/test", "")
	waitCameraState(t, m, gatecam.StateAuth)

	// The password works again (fixed on the camera): the test passes and
	// polling resumes without re-saving.
	cam.SetDetectErr(nil)
	status, body := m.call("POST", "/api/camera/test", "")
	var rep camera.Report
	if status != 200 || json.Unmarshal(body, &rep) != nil || !rep.OK() {
		t.Fatalf("test = %d %s", status, body)
	}
	waitCameraState(t, m, gatecam.StateOnline)
}

func TestATestThatLogsInAfterAWrongPasswordResumesPolling(t *testing.T) {
	m, cam := newCameraEnv(t)
	if status, body := m.call("PUT", "/api/camera", setupBody); status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	waitCameraState(t, m, gatecam.StateOnline)
	cam.SetDetectErr(fmt.Errorf("%w: login failed", camera.ErrAuth))
	waitCameraState(t, m, gatecam.StateAuth)

	// The password works again, but the still fails: the test is not all
	// green, yet its login passed, so polling resumes.
	cam.SetDetectErr(nil)
	cam.SetSnapErr(fmt.Errorf("%w: timeout", camera.ErrUnreachable))
	status, body := m.call("POST", "/api/camera/test", "")
	var rep camera.Report
	if status != 200 || json.Unmarshal(body, &rep) != nil || rep.OK() || !rep.LoginOK() {
		t.Fatalf("test = %d %s; want a passed login and a failed still", status, body)
	}
	waitCameraState(t, m, gatecam.StateOnline)
}
