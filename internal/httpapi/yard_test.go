package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"depot/internal/gate"
	"depot/internal/yard"
)

// newYardEnv is newEnv with the yard service wired, its notices going to the broker.
func newYardEnv(t *testing.T) *env {
	t.Helper()
	var e *env
	e = newEnv(t, func(o *Options, _ *gate.Service) {
		o.Yard = yard.NewService(yard.NewStore(o.Pool), o.Store, func(c, kind, id string) { e.broker.Notify(c, kind, id) })
	})
	return e
}

// must fails the test unless the call answered want; it returns the body.
func (u *user) must(want int, method, path, body string) []byte {
	u.t.Helper()
	status, got := u.call(method, path, body)
	if status != want {
		u.t.Fatalf("%s %s = %d %s; want %d", method, path, status, got, want)
	}
	return got
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

type refusalBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
	Refusals []struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	} `json:"refusals"`
	ApprovalID     string `json:"approvalId"`
	ApprovalStatus string `json:"approvalStatus"`
}

func TestSaveGateFormOverHTTP(t *testing.T) {
	e := newYardEnv(t)
	g, office := e.as(t, "A", "gate"), e.as(t, "A", "office")

	// Accepted: a walk-in gate-in opens a visit.
	id := g.newMove()
	res := decode[struct {
		VisitID string `json:"visitId"`
		Move    struct {
			ID     string `json:"id"`
			Number string `json:"number"`
		} `json:"move"`
		Flags []struct{ Code string } `json:"flags"`
	}](t, g.must(200, "POST", "/api/moves/"+id+"/gate", `{"direction":"in","number":"mscu 123456-5","typeCode":"45G1"}`))
	if res.VisitID == "" || res.Move.ID != id || res.Move.Number != "MSCU1234565" || len(res.Flags) != 1 || res.Flags[0].Code != "walkin" {
		t.Fatalf("save = %+v", res)
	}
	v := decode[struct{ Status, Number string }](t, g.must(200, "GET", "/api/visits/"+res.VisitID, ""))
	if v.Status != "in_yard" || v.Number != "MSCU1234565" {
		t.Fatalf("visit = %+v", v)
	}

	// Refused: a gate-out with no release order.
	status, body := g.call("POST", "/api/moves/"+g.newMove()+"/gate", `{"direction":"out","number":"CSQU3054383"}`)
	r := decode[refusalBody](t, body)
	if status != 409 || r.Error.Code != "E4098" || len(r.Refusals) != 1 || r.Refusals[0].Code != "no_order" {
		t.Fatalf("refused save = %d %s; want 409 E4098 no_order", status, body)
	}

	// Blocked: never passes, even with an override reason.
	office.must(201, "POST", "/api/blocks", `{"kind":"container","value":"TGHU 123456-7","reason":"customs"}`)
	status, body = g.call("POST", "/api/moves/"+g.newMove()+"/gate",
		`{"direction":"in","number":"TGHU1234567","override":{"reason":"boss said so"}}`)
	r = decode[refusalBody](t, body)
	if status != 409 || r.Error.Code != "E4099" || len(r.Refusals) != 1 || r.Refusals[0].Code != "blocked" || r.Refusals[0].Detail != "customs" {
		t.Fatalf("blocked save = %d %s; want 409 E4099 blocked", status, body)
	}

	// A link to another move's photo is 422; an unknown move is 404.
	if status, body := g.call("POST", "/api/moves/"+g.newMove()+"/gate", `{"direction":"in","number":"MSCU1234565","signaturePhotoId":"nope"}`); status != 422 {
		t.Errorf("bad link = %d %s; want 422", status, body)
	}
	if status, body := g.call("POST", "/api/moves/nope/gate", `{"direction":"in","number":"MSCU1234565"}`); status != 404 {
		t.Errorf("unknown move = %d %s; want 404", status, body)
	}
}

func TestCorrectAndVoidOverHTTP(t *testing.T) {
	e := newYardEnv(t)
	g, office := e.as(t, "A", "gate"), e.as(t, "A", "office")
	id := g.newMove()
	g.must(200, "POST", "/api/moves/"+id+"/gate", `{"direction":"in","number":"MSCU1234565","typeCode":"22G1"}`)

	if status, body := g.call("POST", "/api/moves/"+id+"/correct", `{"reason":"typo","form":{"direction":"in","number":"MSCU1234565"}}`); status != 403 {
		t.Errorf("gate corrects = %d %s; want 403", status, body)
	}
	if status, body := office.call("POST", "/api/moves/"+id+"/correct", `{"reason":"typo","form":{"direction":"out","number":"MSCU1234565"}}`); status != 409 || errorCode(body) != "E4101" {
		t.Errorf("direction change = %d %s; want 409 E4101", status, body)
	}
	if status, body := office.call("POST", "/api/moves/"+id+"/correct", `{"reason":" ","form":{"direction":"in","number":"MSCU1234565"}}`); status != 400 {
		t.Errorf("no reason = %d %s; want 400", status, body)
	}
	office.must(200, "POST", "/api/moves/"+id+"/correct", `{"reason":"type","form":{"direction":"in","number":"MSCU1234565","typeCode":"45G1"}}`)
	office.must(204, "POST", "/api/moves/"+id+"/void", `{"reason":"test truck"}`)
	if status, body := office.call("POST", "/api/moves/"+id+"/void", `{"reason":"again"}`); status != 409 || errorCode(body) != "E4102" {
		t.Errorf("void twice = %d %s; want 409 E4102", status, body)
	}
}

func TestGateRoleCannotCancelOrders(t *testing.T) {
	e := newYardEnv(t)
	g, office := e.as(t, "A", "gate"), e.as(t, "A", "office")
	if status, body := g.call("POST", "/api/orders", `{"kind":"release","reference":"R1","items":[{"typeCode":"45G1","quantity":2}]}`); status != 403 {
		t.Fatalf("gate creates an order = %d %s; want 403", status, body)
	}
	o := decode[struct {
		ID         string  `json:"id"`
		ValidUntil *string `json:"validUntil"`
		Items      []struct {
			Quantity int `json:"quantity"`
		} `json:"items"`
	}](t, office.must(201, "POST", "/api/orders", `{"kind":"release","reference":"R1","validUntil":"2026-12-31","items":[{"typeCode":"45G1","quantity":2}]}`))
	if o.ValidUntil == nil || *o.ValidUntil != "2026-12-31" || len(o.Items) != 1 || o.Items[0].Quantity != 2 {
		t.Fatalf("order = %+v", o)
	}
	g.must(200, "GET", "/api/orders/"+o.ID, "")
	if status, body := g.call("POST", "/api/orders/"+o.ID+"/cancel", ""); status != 403 || errorCode(body) != "E4030" {
		t.Fatalf("gate cancels = %d %s; want 403 E4030", status, body)
	}
	if status, _ := g.call("GET", "/api/orders", ""); status != 403 {
		t.Errorf("gate lists orders = %d; want 403", status)
	}
	office.must(204, "POST", "/api/orders/"+o.ID+"/cancel", "")
	if status, body := office.call("POST", "/api/orders/"+o.ID+"/cancel", ""); status != 409 || errorCode(body) != "E4105" {
		t.Errorf("cancel twice = %d %s; want 409 E4105", status, body)
	}
	if got := string(office.must(200, "GET", "/api/orders?status=cancelled&q=r1", "")); !strings.Contains(got, o.ID) {
		t.Errorf("list = %s; want the order", got)
	}
	if got := string(office.must(200, "GET", "/api/orders?q=zzz", "")); got != "[]\n" {
		t.Errorf("empty list = %q; want []", got)
	}
	if status, body := office.call("POST", "/api/orders", `{"kind":"release","reference":"R2","items":[{"number":"MSCU1234565","typeCode":"45G1"}]}`); status != 400 {
		t.Errorf("item with number and type = %d %s; want 400", status, body)
	}
}

func TestSnapshotForGateRole(t *testing.T) {
	e := newYardEnv(t)
	g, office := e.as(t, "A", "gate"), e.as(t, "A", "office")
	// An empty yard still answers arrays, never null.
	empty := string(g.must(200, "GET", "/api/gate/snapshot", ""))
	for _, k := range []string{"orders", "blocks", "holds", "inYard", "linePrefixes"} {
		if !strings.Contains(empty, `"`+k+`":[]`) {
			t.Errorf("empty snapshot %s: %q is not []", empty, k)
		}
	}
	office.must(201, "POST", "/api/orders", `{"kind":"release","reference":"R1","items":[{"typeCode":"45G1"}]}`)
	office.must(201, "POST", "/api/blocks", `{"kind":"container","value":"TGHU1234567","reason":"customs"}`)
	res := decode[struct {
		VisitID string `json:"visitId"`
	}](t, g.must(200, "POST", "/api/moves/"+g.newMove()+"/gate", `{"direction":"in","number":"MSCU1234565","typeCode":"45G1"}`))
	office.must(201, "POST", "/api/visits/"+res.VisitID+"/holds", `{"reason":"unpaid"}`)

	snap := decode[struct {
		Orders []struct {
			Reference string
			Items     []struct{ TypeCode string }
		}
		Blocks       []struct{ Value string }
		Holds        []struct{ Reason string }
		InYard       []struct{ Number string }
		LinePrefixes []any
		Settings     struct{ RefusalMode string }
	}](t, g.must(200, "GET", "/api/gate/snapshot", ""))
	if len(snap.Orders) != 1 || len(snap.Orders[0].Items) != 1 || snap.Orders[0].Items[0].TypeCode != "45G1" ||
		len(snap.Blocks) != 1 || len(snap.Holds) != 1 || len(snap.InYard) != 1 || snap.InYard[0].Number != "MSCU1234565" ||
		snap.Settings.RefusalMode != "clerk_override" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if status, body := e.as(t, "B", "gate").call("GET", "/api/gate/snapshot", ""); status != 200 || strings.Contains(string(body), "MSCU") {
		t.Errorf("B's snapshot = %d %s; want B's empty yard", status, body)
	}
}

func TestVisitsListFiltersAndHolds(t *testing.T) {
	e := newYardEnv(t)
	g, office := e.as(t, "A", "gate"), e.as(t, "A", "office")
	var visits []string
	for _, n := range []string{"MSCU1234565", "CSQU3054383"} {
		res := decode[struct {
			VisitID string `json:"visitId"`
		}](t, g.must(200, "POST", "/api/moves/"+g.newMove()+"/gate", `{"direction":"in","number":"`+n+`","typeCode":"45G1"}`))
		visits = append(visits, res.VisitID)
	}
	hold := decode[struct{ ID string }](t, office.must(201, "POST", "/api/visits/"+visits[0]+"/holds", `{"reason":"unpaid"}`))
	type list struct {
		Items []struct {
			ID     string
			OnHold bool
		}
		Total int
	}
	all := decode[list](t, office.must(200, "GET", "/api/visits?status=in_yard&type=45&perPage=1", ""))
	if all.Total != 2 || len(all.Items) != 1 {
		t.Fatalf("page 1 = %+v; want 1 of 2", all)
	}
	held := decode[list](t, office.must(200, "GET", "/api/visits?hold=true", ""))
	if held.Total != 1 || held.Items[0].ID != visits[0] || !held.Items[0].OnHold {
		t.Fatalf("on hold = %+v", held)
	}
	if byNumber := decode[list](t, office.must(200, "GET", "/api/visits?q=csqu%20305", "")); byNumber.Total != 1 || byNumber.Items[0].ID != visits[1] {
		t.Fatalf("q = %+v; want the CSQU visit", byNumber)
	}
	if none := decode[list](t, office.must(200, "GET", "/api/visits?minDays=3", "")); none.Total != 0 || none.Items == nil {
		t.Fatalf("minDays = %+v; want an empty list", none)
	}
	if status, _ := g.call("GET", "/api/visits", ""); status != 403 {
		t.Errorf("gate lists visits = %d; want 403", status)
	}
	office.must(200, "PATCH", "/api/visits/"+visits[0]+"/position", `{"block":"A","row":"3","tier":"2"}`)
	pos := decode[struct{ Block, RowNo, Tier, Grade string }](t, office.must(200, "PATCH", "/api/visits/"+visits[0]+"/grade", `{"grade":"B"}`))
	if pos.Block != "A" || pos.RowNo != "3" || pos.Tier != "2" || pos.Grade != "B" {
		t.Errorf("visit = %+v", pos)
	}
	if status, body := office.call("PATCH", "/api/visits/"+visits[0]+"/grade", `{"grade":"Z"}`); status != 400 {
		t.Errorf("grade Z = %d %s; want 400", status, body)
	}
	office.must(204, "POST", "/api/holds/"+hold.ID+"/release", "")
	if v := decode[struct{ Holds []struct{ ReleasedBy string } }](t, g.must(200, "GET", "/api/visits/"+visits[0], "")); len(v.Holds) != 1 || v.Holds[0].ReleasedBy == "" {
		t.Errorf("visit holds = %+v", v)
	}
	if status, _ := e.as(t, "B", "office").call("GET", "/api/visits/"+visits[0], ""); status != 404 {
		t.Errorf("B reads A's visit = %d; want 404", status)
	}
}

func TestApprovalNotifiesManagersStream(t *testing.T) {
	e := newYardEnv(t)
	owner, mgr, g := e.as(t, "A", "owner"), e.as(t, "A", "manager"), e.as(t, "A", "gate")
	if status, _ := mgr.call("PUT", "/api/settings/yard", `{"refusalMode":"manager_approval","walkinsAllowed":true,"eirTtlDays":30}`); status != 403 {
		t.Fatalf("manager sets settings = %d; want 403", status)
	}
	owner.must(200, "PUT", "/api/settings/yard", `{"refusalMode":"manager_approval","walkinsAllowed":true,"eirTtlDays":30}`)

	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	mLines, gLines := mgr.sse(ctx), g.sse(ctx)
	<-mLines
	<-gLines

	id := g.newMove()
	status, body := g.call("POST", "/api/moves/"+id+"/gate", `{"direction":"out","number":"CSQU3054383"}`)
	r := decode[refusalBody](t, body)
	if status != 409 || r.Error.Code != "E4098" || r.ApprovalID == "" || r.ApprovalStatus != "pending" {
		t.Fatalf("save = %d %s; want 409 E4098 with a pending approval", status, body)
	}
	deadline := time.After(2 * time.Second)
	for got := false; !got; {
		select {
		case l := <-mLines:
			got = l == "event: approval"
		case <-deadline:
			t.Fatal("the manager's stream got no approval event within 2 s")
		}
	}
	if l := <-mLines; !strings.Contains(l, r.ApprovalID) {
		t.Fatalf("approval data = %q", l)
	}
	quiet := time.After(300 * time.Millisecond)
	for done := false; !done; {
		select {
		case l := <-gLines:
			if l == "event: approval" {
				t.Fatal("the gate clerk's stream got an approval event")
			}
		case <-quiet:
			done = true
		}
	}

	if status, _ := g.call("GET", "/api/approvals?status=pending", ""); status != 403 {
		t.Errorf("gate lists approvals = %d; want 403", status)
	}
	if got := string(mgr.must(200, "GET", "/api/approvals?status=pending", "")); !strings.Contains(got, r.ApprovalID) || !strings.Contains(got, "no_order") {
		t.Errorf("pending = %s", got)
	}
	if got := string(mgr.must(200, "GET", "/api/approvals?status=pending", "")); !strings.Contains(got, `"number":"CSQU3054383"`) {
		t.Errorf("pending lacks the container number: %s", got)
	}
	mgr.must(204, "POST", "/api/approvals/"+r.ApprovalID, `{"approve":true}`)
	// The waiting gate phone follows moves: the decision reaches it as its move's event.
	deadline = time.After(2 * time.Second)
	for got := false; !got; {
		select {
		case l := <-gLines:
			got = strings.HasPrefix(l, "data:") && strings.Contains(l, id)
		case <-deadline:
			t.Fatal("the gate clerk's stream got no move event after the decision within 2 s")
		}
	}
	res := decode[struct{ Flags []struct{ Code string } }](t,
		g.must(200, "POST", "/api/moves/"+id+"/gate", `{"direction":"out","number":"CSQU3054383","approvalId":"`+r.ApprovalID+`"}`))
	if !strings.Contains(string(mustJSON(t, res.Flags)), "override:no_order") {
		t.Errorf("flags = %+v; want override:no_order", res.Flags)
	}
	if got := string(mgr.must(200, "GET", "/api/gate-flags?open=true", "")); !strings.Contains(got, "override:no_order") {
		t.Errorf("open flags = %s", got)
	}
	link := decode[struct{ URL, ExpiresAt string }](t, g.must(200, "POST", "/api/moves/"+id+"/eir", ""))
	if !strings.HasPrefix(link.URL, "/eir/") || link.ExpiresAt == "" {
		t.Errorf("eir link = %+v", link)
	}
	if resp, err := http.Get(e.srv.URL + link.URL); err != nil || resp.StatusCode != 200 {
		t.Errorf("eir page = %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMoveFormAndVisitDetail(t *testing.T) {
	e := newYardEnv(t)
	g, office := e.as(t, "A", "gate"), e.as(t, "A", "office")
	id := g.newMove()
	status, body := g.upload(id, "\xff\xd8\xff\xe0dent")
	if status != http.StatusAccepted {
		t.Fatalf("upload = %d %s", status, body)
	}
	photo := decode[struct{ ID string }](t, body).ID
	res := decode[struct {
		VisitID string `json:"visitId"`
	}](t, g.must(200, "POST", "/api/moves/"+id+"/gate", `{"direction":"in","number":"MSCU1234565","typeCode":"45G1","seal":"S1",
		"truckPlate":"34ABC12","condition":"damaged","damages":[{"location":"door","description":"dent","photoIds":["`+photo+`"]}]}`))

	type form struct {
		ID, Direction, Number, Seal, TruckPlate, Condition string
		FormSavedAt                                        *string
		Corrections                                        int
		Damages                                            []struct {
			Location string
			PhotoIds []string
		}
		Flags []struct{ Code string }
	}
	f := decode[form](t, g.must(200, "GET", "/api/moves/"+id+"/form", ""))
	if f.ID != id || f.Direction != "in" || f.Number != "MSCU1234565" || f.Seal != "S1" || f.TruckPlate != "34ABC12" ||
		f.Condition != "damaged" || f.FormSavedAt == nil || f.Corrections != 0 || len(f.Damages) != 1 ||
		f.Damages[0].Location != "door" || len(f.Damages[0].PhotoIds) != 1 || len(f.Flags) != 1 || f.Flags[0].Code != "walkin" {
		t.Fatalf("form = %+v", f)
	}
	office.must(200, "POST", "/api/moves/"+id+"/correct", `{"reason":"seal","form":{"direction":"in","number":"MSCU1234565","typeCode":"45G1","seal":"S2"}}`)
	v := decode[struct {
		InForm  *form
		OutForm *form
	}](t, g.must(200, "GET", "/api/visits/"+res.VisitID, ""))
	if v.InForm == nil || v.InForm.Seal != "S2" || v.InForm.Corrections != 1 || v.InForm.Damages == nil || v.OutForm != nil {
		t.Fatalf("visit forms = in %+v, out %+v", v.InForm, v.OutForm)
	}
	if status, _ := e.as(t, "B", "gate").call("GET", "/api/moves/"+id+"/form", ""); status != 404 {
		t.Errorf("B reads A's form = %d; want 404", status)
	}
}

func TestYardValidation(t *testing.T) {
	e := newYardEnv(t)
	g, office, owner := e.as(t, "A", "gate"), e.as(t, "A", "office"), e.as(t, "A", "owner")
	cases := []struct {
		u                  *user
		method, path, body string
		status             int
		code, field        string
	}{
		{g, "POST", "/api/moves/" + g.newMove() + "/gate", `{"direction":"sideways","number":"MSCU1234565"}`, 400, "E4000", "direction"},
		{g, "POST", "/api/moves/" + g.newMove() + "/gate", `{"direction":"in","number":"MSCU1234565","seal":"` + strings.Repeat("s", 65) + `"}`, 400, "E4000", "seal"},
		{g, "POST", "/api/moves/" + g.newMove() + "/gate", `{"direction":"in","number":"MSCU1234565","fullEmpty":"X"}`, 400, "E4000", "fullEmpty"},
		{owner, "PUT", "/api/settings/yard", `{"refusalMode":"maybe","walkinsAllowed":true,"eirTtlDays":30}`, 400, "E4000", "refusalMode"},
		{owner, "PUT", "/api/settings/yard", `{"refusalMode":"none","walkinsAllowed":true,"eirTtlDays":0}`, 400, "E4000", "eirTtlDays"},
		{office, "POST", "/api/orders", `{"kind":"release","reference":"R1","items":[{"typeCode":"45G1","quantity":0}]}`, 400, "E4000", "quantity"},
		{office, "POST", "/api/orders", `{"kind":"release","reference":"R1","items":[{}]}`, 400, "E4000", "items[0]"},
		{office, "POST", "/api/orders", `{"kind":"swap","reference":"R1","items":[{"typeCode":"45G1"}]}`, 400, "E4000", "kind"},
		{office, "POST", "/api/blocks", `{"kind":"vessel","value":"X","reason":"r"}`, 400, "E4000", "kind"},
		// The schema refuses a line of no such id: 422 with a message that names no table or constraint.
		{office, "POST", "/api/orders", `{"kind":"release","reference":"R1","lineId":"nope","items":[{"typeCode":"45G1"}]}`, 422, "E4221", "linked record"},
	}
	for _, c := range cases {
		status, body := c.u.call(c.method, c.path, c.body)
		if status != c.status || errorCode(body) != c.code || !strings.Contains(string(body), c.field) {
			t.Errorf("%s %s %s = %d %s; want %d %s naming %s", c.method, c.path, c.body, status, body, c.status, c.code, c.field)
		}
		if strings.Contains(string(body), "fkey") || strings.Contains(string(body), "release_orders") {
			t.Errorf("%s leaks the schema: %s", c.body, body)
		}
	}
}

func TestOversizedBodyIs413(t *testing.T) {
	e := newYardEnv(t)
	g := e.as(t, "A", "gate")
	status, body := g.call("POST", "/api/moves/"+g.newMove()+"/gate", `{"direction":"in","number":"MSCU1234565","seal":"`+strings.Repeat("s", 70<<10)+`"}`)
	if status != http.StatusRequestEntityTooLarge || errorCode(body) != "E4130" {
		t.Fatalf("70 KiB body = %d %s; want 413 E4130", status, body)
	}
}

func TestDepotLimitedMemberSeesOnlyItsDepot(t *testing.T) {
	e := newYardEnv(t)
	e.exec(t, "A", `INSERT INTO depots (company_id, id, name, country, timezone) VALUES ('A', 'd2', 'Depot 2', 'TR', 'UTC')`)
	owner := e.as(t, "A", "owner")
	if err := e.sessions.SetScope(bg, owner.cookie, "A", "d2"); err != nil {
		t.Fatal(err)
	}
	order := decode[struct{ ID string }](t, owner.must(201, "POST", "/api/orders", `{"kind":"release","reference":"R2","items":[{"typeCode":"45G1"}]}`))
	in := owner.newMove()
	visit := decode[struct {
		VisitID string `json:"visitId"`
	}](t, owner.must(200, "POST", "/api/moves/"+in+"/gate", `{"direction":"in","number":"MSCU1234565","typeCode":"22G1"}`))
	owner.must(200, "PUT", "/api/settings/yard", `{"refusalMode":"manager_approval","walkinsAllowed":true,"eirTtlDays":30}`)
	_, body := owner.call("POST", "/api/moves/"+owner.newMove()+"/gate", `{"direction":"out","number":"CSQU3054383"}`)
	approval := decode[refusalBody](t, body).ApprovalID
	if approval == "" {
		t.Fatalf("no approval: %s", body)
	}

	// A manager limited to d1.
	e.exec(t, "A", `INSERT INTO invitations (company_id, id, email, role, depot_ids, created_by) VALUES ('A', 'lim', 'lim@a.test', 'manager', ARRAY['d1'], 'test')`)
	lim := &user{t: t, e: e, cookie: e.signIn(t, "lim@a.test")}
	if err := e.sessions.SetScope(bg, lim.cookie, "A", "d1"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/visits/" + visit.VisitID, ""},
		{"GET", "/api/orders/" + order.ID, ""},
		{"GET", "/api/moves/" + in + "/form", ""},
		{"POST", "/api/approvals/" + approval, `{"approve":true}`},
	} {
		if status, body := lim.call(c.method, c.path, c.body); status != 404 {
			t.Errorf("limited %s %s = %d %s; want 404", c.method, c.path, status, body)
		}
	}
	if got := string(lim.must(200, "GET", "/api/approvals", "")); strings.Contains(got, approval) {
		t.Errorf("limited member lists d2's approval: %s", got)
	}
}

func TestTurnAwayOverHTTP(t *testing.T) {
	e := newYardEnv(t)
	g := e.as(t, "A", "gate")
	id := g.newMove()
	reasons := `{"number":"MSCU1234565","reasons":[{"code":"hold_open","detail":"customs"}]}`
	g.must(http.StatusNoContent, "POST", "/api/moves/"+id+"/turn-away", reasons)
	g.must(http.StatusNoContent, "POST", "/api/moves/"+id+"/turn-away", reasons) // a resend is a no-op
	form := decode[struct {
		Number     string `json:"number"`
		Voided     bool   `json:"voided"`
		VoidReason string `json:"voidReason"`
		Flags      []struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"flags"`
	}](t, g.must(http.StatusOK, "GET", "/api/moves/"+id+"/form", ""))
	if form.Number != "MSCU1234565" || !form.Voided || form.VoidReason != "turned away" ||
		len(form.Flags) != 1 || form.Flags[0].Code != "turned_away" || form.Flags[0].Detail != "hold_open" {
		t.Fatalf("form = %+v", form)
	}
	saved := g.newMove()
	g.must(http.StatusOK, "POST", "/api/moves/"+saved+"/gate", `{"direction":"in","number":"MSCU1234565"}`)
	if status, body := g.call("POST", "/api/moves/"+saved+"/turn-away", reasons); status != http.StatusConflict || !strings.Contains(string(body), "E4106") {
		t.Fatalf("turn away a saved move = %d %s; want 409 E4106", status, body)
	}
	other := e.as(t, "B", "gate")
	if status, body := other.call("POST", "/api/moves/"+id+"/turn-away", reasons); status != http.StatusNotFound {
		t.Fatalf("another company = %d %s; want 404", status, body)
	}
}
