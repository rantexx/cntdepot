package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"depot/internal/blob"
	"depot/internal/camera"
	"depot/internal/gate"
	"depot/internal/gatecam"
	"depot/internal/live"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testdir"
)

// newCameraServer is newTestServer with camera support: a fake camera behind
// the manager, a running watcher, and live video that is never started.
func newCameraServer(t *testing.T, withCamera bool) *httptest.Server {
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
	engine := &recognition.Fake{Reading: recognition.Reading{
		Candidates: []recognition.RawCandidate{{Number: "CSQU3054383", Confidence: 0.9}},
		TypeCode:   "45G1", Color: "blue", Visible: "full",
		Box: &recognition.Box{X0: 0.3, Y0: 0.2, X1: 0.62, Y1: 0.3},
	}}
	broker := NewBroker()
	svc := gate.New(st, bl, engine, gate.Config{}, broker.Publish)
	opts := Options{
		Service: svc, Store: st, Blobs: bl, Version: "test",
		Static: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
	}
	runCtx, cancel := context.WithCancel(context.Background())
	watcherDone := make(chan struct{})
	if withCamera {
		cam := camera.NewFake(camera.DemoStill())
		w := gatecam.New(svc, gatecam.DefaultConfig())
		svc.SetReadHook(w.OnRead)
		sup := live.New(live.Config{}) // never run: live video stays off
		mgr := gatecam.NewManager(st, w, sup, func(camera.Settings) camera.Camera { return cam })
		if err := mgr.Load(runCtx); err != nil {
			t.Fatal(err)
		}
		opts.Camera, opts.Live = mgr, sup
		go func() { w.Run(runCtx); close(watcherDone) }()
	} else {
		close(watcherDone)
	}
	if err := svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(opts, broker).Handler())
	t.Cleanup(func() {
		srv.Close()
		cancel()
		<-watcherDone
		svc.Wait()
		st.Close()
	})
	return srv
}

const setupBody = `{"host":"192.168.1.50","user":"admin","password":"pw-secret"}`

func TestCameraEndpointsWithoutCameraSupport(t *testing.T) {
	srv := newCameraServer(t, false)
	c := srv.Client()
	status, body := call(t, c, "GET", srv.URL+"/api/camera", "")
	if status != 200 || !strings.Contains(string(body), `"available":false`) {
		t.Fatalf("GET /api/camera = %d %s", status, body)
	}
	if status, body := call(t, c, "PUT", srv.URL+"/api/camera", setupBody); status != 503 || errorCode(body) != "E5031" {
		t.Fatalf("PUT = %d %s; want 503 E5031", status, body)
	}
	if status, body := call(t, c, "GET", srv.URL+"/api/live/ws", ""); status != 503 || errorCode(body) != "E5030" {
		t.Fatalf("live = %d %s; want 503 E5030", status, body)
	}
}

func TestCameraSettingsNeverReturnThePassword(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	status, body := call(t, c, "PUT", srv.URL+"/api/camera", setupBody)
	if status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	for _, b := range [][]byte{body, func() []byte { _, b := call(t, c, "GET", srv.URL+"/api/camera", ""); return b }()} {
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
	if status, body := call(t, c, "PUT", srv.URL+"/api/camera", `{"host":"http://192.168.1.50/","user":"admin"}`); status != 400 || errorCode(body) != "E4007" {
		t.Fatalf("PUT with a URL = %d %s; want 400 E4007", status, body)
	}
}

func TestCameraMustBeSetUpBeforeUse(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	for _, path := range []string{"/api/camera/read", "/api/camera/capture", "/api/camera/test"} {
		if status, body := call(t, c, "POST", srv.URL+path, ""); status != 409 || errorCode(body) != "E4091" {
			t.Errorf("POST %s = %d %s; want 409 E4091", path, status, body)
		}
	}
}

func TestReadThisFrameMeasuresTheLetters(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	if status, body := call(t, c, "PUT", srv.URL+"/api/camera", setupBody); status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	status, body := call(t, c, "POST", srv.URL+"/api/camera/read", "")
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
	res, err := c.Get(srv.URL + "/api/blobs/" + r.Still + "?w=320")
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
	srv := newCameraServer(t, true)
	c := srv.Client()
	call(t, c, "PUT", srv.URL+"/api/camera", setupBody)
	status, body := call(t, c, "POST", srv.URL+"/api/camera/test", "")
	var rep camera.Report
	if status != 200 || json.Unmarshal(body, &rep) != nil || !rep.OK() {
		t.Fatalf("test = %d %s", status, body)
	}
	res, err := c.Get(srv.URL + "/api/camera/still?w=320")
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
	srv := newCameraServer(t, true)
	c := srv.Client()
	call(t, c, "PUT", srv.URL+"/api/camera", setupBody)
	status, body := call(t, c, "POST", srv.URL+"/api/camera/capture", "")
	var r struct {
		MoveID string `json:"move_id"`
	}
	if status != 202 || json.Unmarshal(body, &r) != nil || r.MoveID == "" {
		t.Fatalf("capture = %d %s", status, body)
	}
	_, body = call(t, c, "GET", srv.URL+"/api/moves?source=camera", "")
	if !strings.Contains(string(body), r.MoveID) {
		t.Fatalf("camera moves = %s; want %s", body, r.MoveID)
	}
	_, body = call(t, c, "GET", srv.URL+"/api/moves?source=phone", "")
	if strings.Contains(string(body), r.MoveID) {
		t.Fatalf("phone moves include the camera move: %s", body)
	}
}

func TestCameraWritesNeedTheClientHeaderAndStillWidthIsChecked(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	if status, body := rawCall(t, "PUT", srv.URL+"/api/camera", "", "", setupBody); status != 403 || errorCode(body) != "E5004" {
		t.Fatalf("PUT without the client header = %d %s; want 403 E5004", status, body)
	}
	if _, body := call(t, c, "GET", srv.URL+"/api/camera", ""); !strings.Contains(string(body), `"password_set":false`) {
		t.Fatalf("a refused PUT changed the camera settings: %s", body)
	}
	call(t, c, "PUT", srv.URL+"/api/camera", setupBody)
	if status, body := call(t, c, "GET", srv.URL+"/api/camera/still?w=999", ""); status != 400 || errorCode(body) != "E4006" {
		t.Fatalf("still with a bad width = %d %s; want 400 E4006", status, body)
	}
}
