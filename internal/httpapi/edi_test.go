package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"depot/internal/edi"
	"depot/internal/gate"
	"depot/internal/mail"
	"depot/internal/yard"
)

const hookSecret = "hook-s3cret"

// newEDIEnv is the yard env with EDI wired (file mailer) and, when secret is
// set, the Postmark webhook. Company A has line lA (code MSC).
func newEDIEnv(t *testing.T, secret string) *env {
	t.Helper()
	var e *env
	e = newEnv(t, func(o *Options, _ *gate.Service) {
		o.Yard = yard.NewService(yard.NewStore(o.Pool), o.Store, func(c, kind, id string) { e.broker.Notify(c, kind, id) })
		o.EDI = edi.NewService(o.Pool, o.Blobs, o.Yard, mail.NewFile(t.TempDir(), "edi@depot.test"),
			edi.Config{From: "edi@depot.test", InboundDomain: "in.depot.test", Tick: time.Hour})
		o.InboundSecret = secret
	})
	e.exec(t, "A", `INSERT INTO shipping_lines (company_id, id, code, name) VALUES ('A', 'lA', 'MSC', 'MSC')`)
	return e
}

func ediFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../edifact/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const ediLine = `{"mode":"edi","profile":{"senderId":"MSC","senderQual":"","recipientId":"MSC","recipientQual":"","lineCode":"",
	"sendTo":["line@msc.test"],"allowedFrom":["edi@msc.test"],"emptiesOnly":false,"sendAperak":true,"test":false,"typeMap":[],
	"reportTo":[],"reportTime":"06:00"}}`

// onEDI sets company A's party id and puts lA on EDI.
func onEDI(m *user) {
	m.must(204, "PUT", "/api/edi/company", `{"partyId":"DEPOTX"}`)
	m.must(200, "PUT", "/api/edi/lines/lA", ediLine)
}

type ediMsg struct {
	ID        string  `json:"id"`
	Direction string  `json:"direction"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	LineID    *string `json:"lineId"`
	DepotID   *string `json:"depotId"`
	OrderID   *string `json:"orderId"`
	Via       *string `json:"via"`
	Trusted   *bool   `json:"trusted"`
}

type ediList struct {
	Messages []ediMsg `json:"messages"`
	Total    int      `json:"total"`
}

func TestEDIEndpointsNeedManage(t *testing.T) {
	e := newEDIEnv(t, "")
	for _, role := range []string{"gate", "office"} {
		if status, body := e.as(t, "A", role).call("GET", "/api/edi/messages", ""); status != 403 {
			t.Errorf("%s lists EDI = %d %s; want 403", role, status, body)
		}
	}
	e.as(t, "A", "manager").must(200, "GET", "/api/edi/messages", "")
}

func (u *user) uploadEDI(files map[string][]byte) (int, []byte) {
	u.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write(data)
	}
	mw.Close()
	return do(u.t, u.request("POST", "/api/edi/upload", &buf, mw.FormDataContentType()))
}

func TestUploadThenList(t *testing.T) {
	e := newEDIEnv(t, "")
	m := e.as(t, "A", "manager")
	onEDI(m)
	data := ediFile(t, "coparn_release.edi")
	status, body := m.uploadEDI(map[string][]byte{"rls.edi": data})
	up := decode[struct{ Messages []ediMsg }](t, body)
	if status != 200 || len(up.Messages) != 1 || up.Messages[0].Status != "done" || up.Messages[0].Kind != "coparn" {
		t.Fatalf("upload = %d %s", status, body)
	}
	id := up.Messages[0].ID

	in := decode[ediList](t, m.must(200, "GET", "/api/edi/messages?direction=in", ""))
	if in.Total != 1 || len(in.Messages) != 1 || in.Messages[0].ID != id || in.Messages[0].Status != "done" ||
		in.Messages[0].Via == nil || *in.Messages[0].Via != "upload" || in.Messages[0].LineID == nil || *in.Messages[0].LineID != "lA" {
		t.Fatalf("in = %s", body)
	}
	// The APERAK out carries no via or trusted.
	outBody := m.must(200, "GET", "/api/edi/messages?direction=out", "")
	out := decode[ediList](t, outBody)
	if out.Total != 1 || out.Messages[0].Kind != "aperak" || strings.Contains(string(outBody), `"via"`) || strings.Contains(string(outBody), `"trusted"`) {
		t.Fatalf("out = %s", outBody)
	}
	if got := decode[ediMsg](t, m.must(200, "GET", "/api/edi/messages/"+id, "")); got.ID != id {
		t.Fatalf("get = %+v", got)
	}

	resp, err := http.DefaultClient.Do(m.request("GET", "/api/edi/messages/"+id+"/raw", nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	if resp.StatusCode != 200 || !bytes.Equal(raw.Bytes(), data) || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") ||
		resp.Header.Get("Content-Type") != "application/EDIFACT" {
		t.Fatalf("raw = %d %q %v", resp.StatusCode, raw.Bytes(), resp.Header)
	}

	// Filters, a file over 2 MiB, another company.
	if l := decode[ediList](t, m.must(200, "GET", "/api/edi/messages?status=error", "")); l.Total != 0 {
		t.Errorf("status filter = %+v", l)
	}
	if status, body := m.uploadEDI(map[string][]byte{"big.edi": bytes.Repeat([]byte("x"), 2<<20+1)}); status != 413 {
		t.Errorf("big upload = %d %s; want 413", status, body)
	}
	if status, _ := e.as(t, "B", "manager").call("GET", "/api/edi/messages/"+id, ""); status != 404 {
		t.Errorf("other company reads the message = %d; want 404", status)
	}
	if status, body := m.call("POST", "/api/edi/messages/"+id+"/resend", ""); status != 409 {
		t.Errorf("resend an inbound message = %d %s; want 409", status, body)
	}
	m.must(204, "POST", "/api/edi/messages/"+out.Messages[0].ID+"/resend", "")
	if status, body := m.call("POST", "/api/edi/messages/"+id+"/process", `{}`); status != 409 {
		t.Errorf("process a done message = %d %s; want 409", status, body)
	}
	if msg := decode[ediMsg](t, m.must(200, "POST", "/api/edi/lines/lA/test", "")); msg.Kind != "test" || msg.Direction != "out" {
		t.Errorf("test CODECO = %+v", msg)
	}
}

func TestEDIStatusAndCompany(t *testing.T) {
	e := newEDIEnv(t, "")
	m := e.as(t, "A", "manager")
	type statusBody struct {
		Mail, InboundAddress, PartyID string
		Problems                      int
	}
	s := decode[statusBody](t, m.must(200, "GET", "/api/edi/status", ""))
	if s.Mail != "file" || !strings.HasPrefix(s.InboundAddress, "edi-") || !strings.HasSuffix(s.InboundAddress, "@in.depot.test") || s.PartyID != "" || s.Problems != 0 {
		t.Fatalf("status = %+v", s)
	}
	for _, bad := range []string{`{"partyId":"  "}`, `{"partyId":"DEPOT'X"}`, `{"partyId":"` + strings.Repeat("A", 36) + `"}`} {
		if status, body := m.call("PUT", "/api/edi/company", bad); status != 400 || errorCode(body) != "E4000" {
			t.Errorf("party id %s = %d %s; want 400 E4000", bad, status, body)
		}
	}
	m.must(204, "PUT", "/api/edi/company", `{"partyId":" depot-x (1) "}`)
	m.uploadEDI(map[string][]byte{"junk.edi": []byte("not edifact")})
	if s = decode[statusBody](t, m.must(200, "GET", "/api/edi/status", "")); s.PartyID != "DEPOT-X (1)" || s.Problems != 1 {
		t.Fatalf("status = %+v", s)
	}

	m.must(204, "PUT", "/api/depots/d1/edi-location", `{"location":"trmer"}`)
	if status, body := m.call("PUT", "/api/depots/d1/edi-location", `{"location":"TR;MER"}`); status != 400 || errorCode(body) != "E4000" {
		t.Errorf("bad location = %d %s; want 400 E4000", status, body)
	}
	if status, _ := m.call("PUT", "/api/depots/nope/edi-location", `{"location":"X"}`); status != 404 {
		t.Errorf("unknown depot = %d; want 404", status)
	}
	if status, _ := e.as(t, "A", "office").call("PUT", "/api/edi/company", `{"partyId":"X"}`); status != 403 {
		t.Errorf("office sets the party id = %d; want 403", status)
	}
}

func TestLineSettingsValidation(t *testing.T) {
	e := newEDIEnv(t, "")
	m := e.as(t, "A", "manager")
	// Mode edi needs our party id first.
	if status, body := m.call("PUT", "/api/edi/lines/lA", ediLine); status != 400 || errorCode(body) != "E4000" || !strings.Contains(string(body), "party id") {
		t.Fatalf("edi without party id = %d %s; want 400 E4000", status, body)
	}
	onEDI(m)
	e.exec(t, "A", `INSERT INTO shipping_lines (company_id, id, code, name) VALUES ('A', 'lB', 'MAE', 'Maersk')`)
	for name, body := range map[string]string{
		"bad address":      strings.Replace(ediLine, `"line@msc.test"`, `"not an address"`, 1),
		"named address":    strings.Replace(ediLine, `"line@msc.test"`, `"Line <line@msc.test>"`, 1),
		"bad report to":    strings.Replace(ediLine, `"reportTo":[]`, `"reportTo":["x@"]`, 1),
		"bad allowed from": strings.Replace(ediLine, `"edi@msc.test"`, `"a@b.c, d@e.f"`, 1),
		"bad time":         strings.Replace(ediLine, `"06:00"`, `"6pm"`, 1),
		"hour 25":          strings.Replace(ediLine, `"06:00"`, `"25:00"`, 1),
		"minute 60":        strings.Replace(ediLine, `"06:00"`, `"06:60"`, 1),
		"no recipient":     strings.Replace(ediLine, `"recipientId":"MSC"`, `"recipientId":" "`, 1),
		"bad mode":         strings.Replace(ediLine, `"mode":"edi"`, `"mode":"on"`, 1),
		"sender id of lA":  strings.Replace(ediLine, `"senderId":"MSC"`, `"senderId":"msc"`, 1),
	} {
		path := "/api/edi/lines/lA"
		if name == "sender id of lA" {
			path = "/api/edi/lines/lB"
		}
		if status, got := m.call("PUT", path, body); status != 400 || errorCode(got) != "E4000" {
			t.Errorf("%s = %d %s; want 400 E4000", name, status, got)
		}
	}
	// lB may share the sender id while it is not on EDI; every line is listed.
	m.must(200, "PUT", "/api/edi/lines/lB", strings.Replace(ediLine, `"mode":"edi"`, `"mode":"report"`, 1))
	if status, _ := m.call("PUT", "/api/edi/lines/nope", ediLine); status != 404 {
		t.Errorf("unknown line = %d; want 404", status)
	}
	type lineBody struct {
		LineID, Code, Mode string
		Profile            struct{ SenderID, ReportTime string }
	}
	ls := decode[[]lineBody](t, m.must(200, "GET", "/api/edi/lines", ""))
	if len(ls) != 2 || ls[0].LineID != "lB" || ls[0].Mode != "report" || ls[1].LineID != "lA" || ls[1].Mode != "edi" || ls[1].Code != "MSC" ||
		ls[1].Profile.SenderID != "MSC" || ls[1].Profile.ReportTime != "06:00" {
		t.Fatalf("lines = %+v", ls)
	}
	e.exec(t, "A", `INSERT INTO shipping_lines (company_id, id, code, name) VALUES ('A', 'lC', 'ZIM', 'Zim')`)
	if ls = decode[[]lineBody](t, m.must(200, "GET", "/api/edi/lines", "")); len(ls) != 3 || ls[2].LineID != "lC" || ls[2].Mode != "off" {
		t.Fatalf("lines = %+v", ls)
	}
	if status, _ := m.call("POST", "/api/edi/lines/lC/test", ""); status != 409 {
		t.Errorf("test CODECO to a line off EDI = %d; want 409", status)
	}
}

// postmark posts a Postmark inbound webhook body with basic auth user:pw ("" sends none).
func postmark(t *testing.T, e *env, user, pw string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest("POST", e.srv.URL+"/hooks/postmark/inbound", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.SetBasicAuth(user, pw)
	}
	req.Header.Set("Content-Type", "application/json")
	status, _ := do(t, req)
	return status
}

func inboundMail(t *testing.T, to string, data []byte) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"FromFull": map[string]string{"Email": "edi@msc.test"}, "ToFull": []map[string]string{{"Email": to}}, "Subject": "COPARN",
		"Attachments": []map[string]string{{"Name": "rls.edi", "ContentType": "application/EDIFACT", "Content": base64.StdEncoding.EncodeToString(data)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWebhookAuth(t *testing.T) {
	e := newEDIEnv(t, hookSecret)
	m := e.as(t, "A", "manager")
	onEDI(m)
	addr := decode[struct{ InboundAddress string }](t, m.must(200, "GET", "/api/edi/status", "")).InboundAddress
	body := inboundMail(t, addr, ediFile(t, "coparn_release.edi"))

	if got := postmark(t, e, "", "", body); got != 401 {
		t.Errorf("no auth = %d; want 401", got)
	}
	if got := postmark(t, e, "postmark", "wrong", body); got != 401 {
		t.Errorf("wrong password = %d; want 401", got)
	}
	if got := postmark(t, e, "other", hookSecret, body); got != 401 {
		t.Errorf("wrong user = %d; want 401", got)
	}
	if got := postmark(t, e, "postmark", hookSecret, []byte("{nope")); got != 400 {
		t.Errorf("bad body = %d; want 400", got)
	}
	if got := postmark(t, e, "postmark", hookSecret, body); got != 200 {
		t.Fatalf("right auth = %d; want 200", got)
	}
	// An unknown token answers like a known one.
	if got := postmark(t, e, "postmark", hookSecret, inboundMail(t, "edi-nosuchtoken@in.depot.test", []byte("x"))); got != 200 {
		t.Errorf("unknown token = %d; want 200", got)
	}
	in := decode[ediList](t, m.must(200, "GET", "/api/edi/messages?direction=in", ""))
	if in.Total != 1 || in.Messages[0].Via == nil || *in.Messages[0].Via != "mail" || in.Messages[0].Trusted == nil {
		t.Fatalf("company A in = %+v", in)
	}
	if b := decode[ediList](t, e.as(t, "B", "manager").must(200, "GET", "/api/edi/messages", "")); b.Total != 0 {
		t.Fatalf("company B sees %+v", b)
	}
	if got := postmark(t, e, "postmark", hookSecret, bytes.Repeat([]byte(" "), 40<<20+1)); got != 413 {
		t.Errorf("body over the cap = %d; want 413", got)
	}
}

func TestWebhookNotMountedWithoutSecret(t *testing.T) {
	e := newEDIEnv(t, "")
	if got := postmark(t, e, "postmark", "", []byte(`{}`)); got != 404 {
		t.Fatalf("webhook without a secret = %d; want 404", got)
	}
}

// limitedManager adds depot d2 to company A and signs in a manager limited to it.
func limitedManager(t *testing.T, e *env) *user {
	t.Helper()
	e.exec(t, "A", `INSERT INTO depots (company_id, id, name) VALUES ('A', 'd2', 'Depot 2')`)
	e.exec(t, "A", `INSERT INTO invitations (company_id, id, email, role, depot_ids, created_by) VALUES ('A', 'lim', 'lim@a.test', 'manager', ARRAY['d2'], 'test')`)
	lim := &user{t: t, e: e, cookie: e.signIn(t, "lim@a.test")}
	if err := e.sessions.SetScope(bg, lim.cookie, "A", "d2"); err != nil {
		t.Fatal(err)
	}
	return lim
}

func uploaded(t *testing.T, u *user, name string, data []byte) ediMsg {
	t.Helper()
	status, body := u.uploadEDI(map[string][]byte{name: data})
	up := decode[struct{ Messages []ediMsg }](t, body)
	if status != 200 || len(up.Messages) != 1 {
		t.Fatalf("upload %s = %d %s", name, status, body)
	}
	return up.Messages[0]
}

func TestDepotLimitedManagerSeesOwnDepotsOnly(t *testing.T) {
	e := newEDIEnv(t, "")
	m := e.as(t, "A", "manager")
	onEDI(m)
	lim := limitedManager(t, e)
	e.exec(t, "A", `UPDATE depots SET edi_location = 'TRMER' WHERE id = 'd1'`)
	atD1 := uploaded(t, m, "rls.edi", ediFile(t, "coparn_release.edi"))
	junk := uploaded(t, m, "junk.edi", []byte("not edifact"))
	if atD1.Status != "done" || atD1.DepotID == nil || *atD1.DepotID != "d1" || junk.Status != "error" || junk.DepotID != nil {
		t.Fatalf("uploads = %+v %+v", atD1, junk)
	}

	got := string(lim.must(200, "GET", "/api/edi/messages", ""))
	if strings.Contains(got, atD1.ID) || !strings.Contains(got, junk.ID) {
		t.Fatalf("limited list = %s", got)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/edi/messages/" + atD1.ID, ""},
		{"GET", "/api/edi/messages/" + atD1.ID + "/raw", ""},
		{"POST", "/api/edi/messages/" + atD1.ID + "/resend", ""},
		{"POST", "/api/edi/messages/" + junk.ID + "/process", `{"depotId":"d1"}`},
		{"PUT", "/api/depots/d1/edi-location", `{"location":"TRIST"}`},
	} {
		if status, body := lim.call(c.method, c.path, c.body); status != 404 || errorCode(body) != "E4043" {
			t.Errorf("limited %s %s = %d %s; want 404 E4043", c.method, c.path, status, body)
		}
	}
	lim.must(200, "GET", "/api/edi/messages/"+junk.ID, "")
	lim.must(204, "PUT", "/api/depots/d2/edi-location", `{"location":"TRIST"}`)
	if ds := decode[[]struct{ ID string }](t, lim.must(200, "GET", "/api/depots", "")); len(ds) != 1 || ds[0].ID != "d2" {
		t.Errorf("limited depots = %+v", ds)
	}
	// The full manager sees both; a huge offset is an empty page, not an error.
	if l := decode[ediList](t, m.must(200, "GET", "/api/edi/messages?direction=in", "")); l.Total != 2 {
		t.Errorf("manager in = %+v", l)
	}
	if l := decode[ediList](t, m.must(200, "GET", "/api/edi/messages?offset=99999999999&limit=99999999999", "")); len(l.Messages) != 0 || l.Total == 0 {
		t.Errorf("huge offset = %+v", l)
	}
}

func TestProcessChosenLineAndDepot(t *testing.T) {
	e := newEDIEnv(t, "")
	m := e.as(t, "A", "manager")
	lim := limitedManager(t, e)
	// No line is on EDI: the file is held unmatched.
	held := uploaded(t, m, "rls.edi", ediFile(t, "coparn_release.edi"))
	if held.Status != "unmatched" {
		t.Fatalf("held = %+v", held)
	}
	if status, body := m.call("POST", "/api/edi/messages/"+held.ID+"/process", `{"lineId":"nope","depotId":"d2"}`); status != 400 || errorCode(body) != "E4000" {
		t.Errorf("unknown line = %d %s; want 400 E4000", status, body)
	}
	if status, body := m.call("POST", "/api/edi/messages/"+held.ID+"/process", `{"lineId":"lA","depotId":"nope"}`); status != 404 || errorCode(body) != "E4043" {
		t.Errorf("unknown depot = %d %s; want 404 E4043", status, body)
	}
	done := decode[ediMsg](t, lim.must(200, "POST", "/api/edi/messages/"+held.ID+"/process", `{"lineId":"lA","depotId":"d2"}`))
	if done.Status != "done" || done.LineID == nil || *done.LineID != "lA" || done.DepotID == nil || *done.DepotID != "d2" || done.OrderID == nil {
		t.Fatalf("processed = %+v", done)
	}
	o := decode[struct{ DepotID, Reference, Kind string }](t, lim.must(200, "GET", "/api/orders/"+*done.OrderID, ""))
	if o.DepotID != "d2" || o.Reference != "RLS4471" || o.Kind != "release" {
		t.Fatalf("order = %+v", o)
	}
}
