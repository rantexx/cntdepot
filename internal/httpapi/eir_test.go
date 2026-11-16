package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"depot/internal/gate"
	"depot/internal/store"
	"depot/internal/testkit"
	"depot/internal/yard"
)

const (
	eirNumA = "MSCU1234565"
	eirNumB = "CSQU3054383"
)

var eirPNG = []byte("\x89PNG\r\n\x1a\n0000000000000000")

type eirEnv struct {
	*env
	y *yard.Service
	s *yard.Store
}

func newEIREnv(t *testing.T) *eirEnv {
	x := &eirEnv{}
	x.env = newEnv(t, func(o *Options, _ *gate.Service) {
		x.s = yard.NewStore(o.Pool)
		x.y = yard.NewService(x.s, o.Store, nil)
		o.Yard = x.y
	})
	return x
}

// move saves a gate move of number with one damage photo; it returns the move's
// id, a fresh token and the damage photo's id.
func (x *eirEnv) move(t *testing.T, number string) (id, token, photo string) {
	t.Helper()
	ctx := testkit.Ctx()
	m, err := x.store.CreateMove(ctx, "1", false, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := x.blobs.Put(append(eirPNG, number...))
	if err != nil {
		t.Fatal(err)
	}
	p, err := x.store.AddPhoto(ctx, store.Photo{MoveID: m.ID, Role: "damage", SHA256: digest, AISHA256: digest,
		MediaType: "image/png", AIMediaType: "image/png", Bytes: 1}, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	form := yard.GateForm{MoveID: m.ID, Direction: "in", Number: number, TypeCode: "45G1", FullEmpty: "E", Seal: "SEAL77",
		Condition: "damaged", TruckPlate: "34ABC12", DriverName: "Ali <b>Veli</b>",
		Damages: []yard.DamageInput{{Location: "left side", Description: "dent", PhotoIDs: []string{p.ID}}}}
	if _, err := x.y.SaveGateMove(ctx, form, "clerk"); err != nil {
		t.Fatal(err)
	}
	token, _, err = x.y.CreateEIRToken(ctx, m.ID, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	return m.ID, token, p.ID
}

func (x *eirEnv) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(x.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestEIRTokenOpensOnlyItsMove(t *testing.T) {
	x := newEIREnv(t)
	_, tokA, photoA := x.move(t, eirNumA)
	_, tokB, _ := x.move(t, eirNumB)
	resp, body := x.get(t, "/eir/"+tokA)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{"MSCU", "123456", "SEAL77", "34ABC12", "/eir/" + tokA + "/photo/" + photoA, "left side"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "CSQU") || strings.Contains(body, tokB) {
		t.Error("page shows the other move")
	}
	if strings.Contains(body, "<b>Veli") || !strings.Contains(body, "Ali &lt;b&gt;Veli") {
		t.Error("driver name is not escaped")
	}
}

func TestEIRExpiredIs404(t *testing.T) {
	x := newEIREnv(t)
	id, _, _ := x.move(t, eirNumA)
	sum := sha256.Sum256([]byte("old-token"))
	if err := x.s.AddEIRToken(testkit.Ctx(), id, hex.EncodeToString(sum[:]), time.Now().Add(-time.Minute), "clerk"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"old-token", "no-such-token"} {
		resp, body := x.get(t, "/eir/"+tok)
		if resp.StatusCode != 404 || len(body) > 20 || strings.Contains(body, "MSCU") {
			t.Errorf("%s: status %d body %q", tok, resp.StatusCode, body)
		}
	}
}

func TestEIRPhotoOfOtherMoveIs404(t *testing.T) {
	x := newEIREnv(t)
	_, tokA, photoA := x.move(t, eirNumA)
	_, _, photoB := x.move(t, eirNumB)
	if resp, _ := x.get(t, "/eir/"+tokA+"/photo/"+photoB); resp.StatusCode != 404 {
		t.Errorf("other move's photo: status %d", resp.StatusCode)
	}
	resp, body := x.get(t, "/eir/"+tokA+"/photo/"+photoA)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") || !strings.HasPrefix(body, "\x89PNG") {
		t.Errorf("own photo: status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("photo Cache-Control %q", resp.Header.Get("Cache-Control"))
	}
	if resp, _ := x.get(t, "/eir/bogus/photo/"+photoA); resp.StatusCode != 404 {
		t.Errorf("bad token photo: status %d", resp.StatusCode)
	}
}

func TestEIRShowsCorrectionAndVoidBanner(t *testing.T) {
	x := newEIREnv(t)
	ctx := testkit.Ctx()
	idA, tokA, _ := x.move(t, eirNumA)
	idB, tokB, _ := x.move(t, eirNumB)
	_, plain, _ := x.move(t, "TGHU6543210")
	form := yard.GateForm{Direction: "in", Number: eirNumA, TypeCode: "45G1", FullEmpty: "F", Condition: "sound", TruckPlate: "34ABC12"}
	if _, err := x.y.CorrectGateMove(ctx, idA, form, "wrong full/empty <i>", "office"); err != nil {
		t.Fatal(err)
	}
	if err := x.y.VoidGateMove(ctx, idB, "entered twice", "office"); err != nil {
		t.Fatal(err)
	}
	_, a := x.get(t, "/eir/"+tokA)
	if !strings.Contains(a, "CORRECTED") || !strings.Contains(a, "wrong full/empty &lt;i&gt;") || strings.Contains(a, "VOIDED") {
		t.Errorf("corrected page: %s", a)
	}
	_, b := x.get(t, "/eir/"+tokB)
	if !strings.Contains(b, "VOIDED") || !strings.Contains(b, "entered twice") {
		t.Errorf("voided page lacks banner")
	}
	_, tr := x.get(t, "/eir/"+tokB+"?lang=tr")
	if !strings.Contains(tr, "İPTAL") || strings.Contains(tr, "VOIDED") {
		t.Errorf("turkish voided page lacks banner")
	}
	_, p := x.get(t, "/eir/"+plain)
	if strings.Contains(p, "CORRECTED") || strings.Contains(p, "VOIDED") {
		t.Error("plain move shows a banner")
	}
}

func TestEIRHeadersNoStoreNoIndex(t *testing.T) {
	x := newEIREnv(t)
	_, tok, _ := x.move(t, eirNumA)
	for _, path := range []string{"/eir/" + tok, "/eir/nope"} {
		resp, _ := x.get(t, path)
		h := resp.Header
		if h.Get("Cache-Control") != "no-store" || h.Get("X-Robots-Tag") != "noindex" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s headers: %v", path, h)
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "unsafe-inline") && !strings.Contains(csp, "style-src") {
			t.Errorf("%s csp %q", path, csp)
		}
	}
	// Needs no session and no client header, and does not require a known Host.
	if code, _ := rawCall(t, "GET", x.srv.URL+"/eir/"+tok, "evil.example", "", ""); code != 200 {
		t.Errorf("odd host: %d", code)
	}
}
