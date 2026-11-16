package yard

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"depot/internal/db"
	"depot/internal/store"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
)

const (
	numA = "MSCU1234565"
	numB = "CSQU3054383"
)

var ctxD2 = tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", DepotID: "d2", UserID: "u1", Role: tenancy.Owner})

type recorder struct {
	mu  sync.Mutex
	got [][3]string
}

func (r *recorder) notify(company, kind, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, [3]string{company, kind, id})
}

func (r *recorder) count(company, kind, id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, g := range r.got {
		if g == [3]string{company, kind, id} {
			n++
		}
	}
	return n
}

type fixture struct {
	t     *testing.T
	svc   *Service
	st    *Store
	moves *store.Store
	rec   *recorder
}

func newFixture(t *testing.T) *fixture {
	st, pool := openTest(t)
	moves := store.New(pool)
	rec := &recorder{}
	return &fixture{t: t, svc: NewService(st, moves, rec.notify), st: st, moves: moves, rec: rec}
}

func (x *fixture) move(ctx context.Context) string {
	x.t.Helper()
	m, err := x.moves.CreateMove(ctx, "1", false, "clerk")
	if err != nil {
		x.t.Fatal(err)
	}
	return m.ID
}

func (x *fixture) photo(ctx context.Context, moveID, role string) string {
	x.t.Helper()
	p, err := x.moves.AddPhoto(ctx, store.Photo{MoveID: moveID, Role: role, SHA256: role + moveID, AISHA256: "ai",
		MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1}, "clerk")
	if err != nil {
		x.t.Fatal(err)
	}
	return p.ID
}

// save stores a fresh move's form and fails the test on an error.
func (x *fixture) save(ctx context.Context, f GateForm) Result {
	x.t.Helper()
	if f.MoveID == "" {
		f.MoveID = x.move(ctx)
	}
	r, err := x.svc.SaveGateMove(ctx, f, "clerk")
	if err != nil {
		x.t.Fatal(err)
	}
	return r
}

func (x *fixture) order(ctx context.Context, kind string, items ...OrderItem) (string, []OrderItem) {
	x.t.Helper()
	id, err := x.svc.CreateOrder(ctx, Order{Kind: kind, Reference: "R-" + kind}, items, "office")
	if err != nil {
		x.t.Fatal(err)
	}
	_, got, err := x.st.GetOrder(ctx, id)
	if err != nil {
		x.t.Fatal(err)
	}
	return id, got
}

func (x *fixture) used(ctx context.Context, orderID string) []int64 {
	x.t.Helper()
	_, items, err := x.st.GetOrder(ctx, orderID)
	if err != nil {
		x.t.Fatal(err)
	}
	var n []int64
	for _, it := range items {
		n = append(n, it.Used)
	}
	return n
}

func (x *fixture) visit(ctx context.Context, id string) Visit {
	x.t.Helper()
	v, err := x.st.GetVisit(ctx, id)
	if err != nil {
		x.t.Fatal(err)
	}
	return v
}

func gateIn(number string) GateForm {
	return GateForm{Direction: "in", Number: number, TypeCode: "45G1", FullEmpty: "E", Condition: "sound", LineID: "lA", TruckPlate: "34ABC12"}
}

func gateOut(number string) GateForm {
	f := gateIn(number)
	f.Direction = "out"
	return f
}

func codes(rs []Reason) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Code)
	}
	slices.Sort(out)
	return out
}

func flagCodes(t *testing.T, st *Store, ctx context.Context, moveID string) []string {
	t.Helper()
	fl, err := st.ListFlags(ctx, moveID, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range fl {
		out = append(out, f.Code)
	}
	slices.Sort(out)
	return out
}

func TestGateInOpensVisitAndUsesRedeliveryItem(t *testing.T) {
	x := newFixture(t)
	orderID, _ := x.order(ctxA, "redelivery", OrderItem{Number: ptr("mscu 123456-5")})
	f := gateIn(" mscu1234565 ")
	f.MoveID = x.move(ctxA)
	r := x.save(ctxA, f)
	if r.Refused != nil || r.VisitID == "" || len(r.Flags) != 0 {
		t.Fatalf("result = %+v", r)
	}
	v := x.visit(ctxA, r.VisitID)
	if v.Number != numA || v.Status != "in_yard" || v.DepotID != "d1" || *v.InMoveID != f.MoveID || *v.LineID != "lA" || v.TypeCode != "45G1" {
		t.Fatalf("visit = %+v", v)
	}
	if got := x.used(ctxA, orderID); got[0] != 1 {
		t.Fatalf("used = %v", got)
	}
	if r.Move.Number != numA || r.Move.Direction != "in" || r.Move.ID != f.MoveID {
		t.Fatalf("move = %+v", r.Move)
	}
	form, err := x.st.GetMoveForm(ctxA, f.MoveID)
	if err != nil || form.FormSavedAt == nil || *form.VisitID != r.VisitID || form.OrderID == nil || *form.OrderID != orderID || form.TruckPlate != "34ABC12" {
		t.Fatalf("form = %+v %v", form, err)
	}
	if p, _ := x.st.LinePrefixes(ctxA); len(p) != 1 || p[0].Prefix != "MSCU" || p[0].LineID != "lA" {
		t.Fatalf("line prefixes = %+v", p)
	}
	if x.rec.count("A", "move", f.MoveID) != 1 {
		t.Fatalf("notifications = %v", x.rec.got)
	}
}

func TestGateOutClosesVisitAndUsesReleaseItem(t *testing.T) {
	x := newFixture(t)
	in := x.save(ctxA, gateIn(numA)) // a walk-in
	if codes(in.Flags)[0] != "walkin" {
		t.Fatalf("gate-in flags = %+v", in.Flags)
	}
	orderID, _ := x.order(ctxA, "release", OrderItem{TypeCode: ptr("45G0"), Quantity: 2})
	f := gateOut(numA)
	f.MoveID = x.move(ctxA)
	r := x.save(ctxA, f)
	if r.Refused != nil || r.VisitID != in.VisitID || len(r.Flags) != 0 {
		t.Fatalf("result = %+v", r)
	}
	v := x.visit(ctxA, in.VisitID)
	if v.Status != "out" || v.OutMoveID == nil || *v.OutMoveID != f.MoveID || v.OutAt == nil {
		t.Fatalf("visit = %+v", v)
	}
	if got := x.used(ctxA, orderID); got[0] != 1 {
		t.Fatalf("used = %v", got)
	}
	if r.Move.Direction != "out" {
		t.Fatalf("move direction %q", r.Move.Direction)
	}
}

func TestBlockedCannotBeOverridden(t *testing.T) {
	x := newFixture(t)
	if _, err := x.svc.UpsertBlock(ctxA, "line", " msc ", "unpaid", "office"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"clerk_override", "manager_approval", "none"} {
		if err := x.st.SetSettings(ctxA, Settings{RefusalMode: mode, WalkinsAllowed: true, EirTtlDays: 30}); err != nil {
			t.Fatal(err)
		}
		f := gateIn(numA) // line lA has code MSC
		f.Override = &Override{Reason: "boss said so"}
		f.MoveID = x.move(ctxA)
		r := x.save(ctxA, f)
		if r.Refused == nil || !r.Refused.Blocked || r.Refused.ApprovalID != "" || codes(r.Refused.Reasons)[0] != "blocked" {
			t.Fatalf("%s: result = %+v", mode, r)
		}
		if form, _ := x.st.GetMoveForm(ctxA, f.MoveID); form.FormSavedAt != nil {
			t.Fatalf("%s: a blocked move was saved", mode)
		}
	}
	// A container entry matches whatever way the number was typed.
	if _, err := x.svc.UpsertBlock(ctxA, "container", "csqu 305438-3", "stolen", "office"); err != nil {
		t.Fatal(err)
	}
	f := gateIn("CSQU3054383")
	f.LineID = ""
	if r := x.save(ctxA, f); r.Refused == nil || !r.Refused.Blocked {
		t.Fatalf("container block: %+v", r)
	}
	if got, _ := x.st.ListVisits(ctxA, ""); len(got) != 0 {
		t.Fatalf("visits = %+v", got)
	}
	if got, _ := x.st.ListApprovals(ctxA, ""); len(got) != 0 {
		t.Fatalf("approvals = %+v", got)
	}
}

func TestClerkOverrideFlagsMove(t *testing.T) {
	x := newFixture(t)
	in := x.save(ctxA, gateIn(numA))
	if _, err := x.svc.PlaceHold(ctxA, in.VisitID, "customs", "office"); err != nil {
		t.Fatal(err)
	}
	f := gateOut(numA)
	f.MoveID = x.move(ctxA)
	r := x.save(ctxA, f)
	if r.Refused == nil || r.Refused.Blocked || !slices.Equal(codes(r.Refused.Reasons), []string{"hold_open", "no_order"}) {
		t.Fatalf("without override: %+v", r)
	}
	if x.visit(ctxA, in.VisitID).Status != "in_yard" {
		t.Fatal("a refused gate-out closed the visit")
	}
	f.Override = &Override{Reason: "customs cleared by phone"}
	r = x.save(ctxA, f)
	if r.Refused != nil || !slices.Equal(codes(r.Flags), []string{"override:hold_open", "override:no_order"}) {
		t.Fatalf("with override: %+v", r)
	}
	if r.Flags[0].Detail != "customs cleared by phone" {
		t.Fatalf("override detail = %+v", r.Flags)
	}
	if got := flagCodes(t, x.st, ctxA, f.MoveID); !slices.Equal(got, []string{"override:hold_open", "override:no_order"}) {
		t.Fatalf("stored flags = %v", got)
	}
	if x.visit(ctxA, in.VisitID).Status != "out" {
		t.Fatal("the overridden gate-out did not close the visit")
	}
}

func TestManagerApprovalFlow(t *testing.T) {
	x := newFixture(t)
	x.save(ctxA, gateIn(numA))
	if err := x.st.SetSettings(ctxA, Settings{RefusalMode: "manager_approval", WalkinsAllowed: true, EirTtlDays: 30}); err != nil {
		t.Fatal(err)
	}
	f := gateOut(numA)
	f.MoveID = x.move(ctxA)
	f.Override = &Override{Reason: "a clerk cannot pass it in this mode"}
	r := x.save(ctxA, f)
	if r.Refused == nil || r.Refused.ApprovalID == "" || codes(r.Refused.Reasons)[0] != "no_order" {
		t.Fatalf("first save: %+v", r)
	}
	apprID := r.Refused.ApprovalID
	if x.rec.count("A", "approval", apprID) != 1 {
		t.Fatalf("the manager was not told: %v", x.rec.got)
	}
	// Asking again reuses the pending approval; so does naming it while pending.
	if r2 := x.save(ctxA, f); r2.Refused == nil || r2.Refused.ApprovalID != apprID {
		t.Fatalf("second save: %+v", r2)
	}
	f.ApprovalID = apprID
	if r3 := x.save(ctxA, f); r3.Refused == nil || r3.Refused.ApprovalID != apprID {
		t.Fatalf("save while pending: %+v", r3)
	}
	if got, _ := x.st.ListApprovals(ctxA, ""); len(got) != 1 {
		t.Fatalf("approvals = %+v", got)
	}
	if err := x.svc.DecideApproval(ctxA, apprID, true, "manager"); err != nil {
		t.Fatal(err)
	}
	if x.rec.count("A", "approval", apprID) != 2 {
		t.Fatalf("decision not notified: %v", x.rec.got)
	}
	r = x.save(ctxA, f)
	if r.Refused != nil || r.VisitID == "" || codes(r.Flags)[0] != "override:no_order" {
		t.Fatalf("after approval: %+v", r)
	}
	if x.visit(ctxA, r.VisitID).Status != "out" {
		t.Fatal("approved gate-out did not close the visit")
	}
}

func TestRefusalModeNone(t *testing.T) {
	x := newFixture(t)
	x.save(ctxA, gateIn(numA))
	if err := x.st.SetSettings(ctxA, Settings{RefusalMode: "none", WalkinsAllowed: true, EirTtlDays: 30}); err != nil {
		t.Fatal(err)
	}
	f := gateOut(numA)
	f.Override = &Override{Reason: "please"}
	r := x.save(ctxA, f)
	if r.Refused == nil || r.Refused.ApprovalID != "" || codes(r.Refused.Reasons)[0] != "no_order" {
		t.Fatalf("result = %+v", r)
	}
	if got, _ := x.st.ListApprovals(ctxA, ""); len(got) != 0 {
		t.Fatalf("approvals = %+v", got)
	}
	if got, _ := x.st.ListVisits(ctxA, "in_yard"); len(got) != 1 {
		t.Fatalf("in yard = %+v", got)
	}
}

func TestSaveGateMoveIsIdempotent(t *testing.T) {
	x := newFixture(t)
	orderID, _ := x.order(ctxA, "redelivery", OrderItem{TypeCode: ptr("45G1"), Quantity: 3})
	f := gateIn(numA)
	f.MoveID = x.move(ctxA)
	f.Condition = "damaged"
	f.Damages = []DamageInput{{Location: "door", Description: "dent", PhotoIDs: []string{x.photo(ctxA, f.MoveID, "damage")}}}
	f.SignaturePhotoID = x.photo(ctxA, f.MoveID, "signature")
	first := x.save(ctxA, f)
	second := x.save(ctxA, f)
	if first.VisitID == "" || second.VisitID != first.VisitID || second.Refused != nil || second.Move.ID != f.MoveID {
		t.Fatalf("first %+v second %+v", first, second)
	}
	if got, _ := x.st.ListVisits(ctxA, ""); len(got) != 1 {
		t.Fatalf("visits = %+v", got)
	}
	if got := x.used(ctxA, orderID); got[0] != 1 {
		t.Fatalf("used = %v", got)
	}
	if d, _ := x.st.ListDamages(ctxA, f.MoveID); len(d) != 1 {
		t.Fatalf("damages = %+v", d)
	}
	if x.rec.count("A", "move", f.MoveID) != 1 {
		t.Fatalf("notifications = %v", x.rec.got)
	}
}

func TestOfflineGateOutAfterOrderCancelledIsFlaggedNotLost(t *testing.T) {
	x := newFixture(t)
	in := x.save(ctxA, gateIn(numA))
	orderID, _ := x.order(ctxA, "release", OrderItem{Number: ptr(numA)})
	if err := x.svc.CancelOrder(ctxA, orderID, "office"); err != nil {
		t.Fatal(err)
	}
	f := gateOut(numA)
	f.MoveID = x.move(ctxA)
	f.Override = &Override{Reason: "saved offline"}
	r := x.save(ctxA, f)
	if r.Refused != nil || !slices.Contains(codes(r.Flags), "order_not_open") {
		t.Fatalf("result = %+v", r)
	}
	if got := flagCodes(t, x.st, ctxA, f.MoveID); !slices.Contains(got, "order_not_open") {
		t.Fatalf("stored flags = %v", got)
	}
	if v := x.visit(ctxA, in.VisitID); v.Status != "out" || *v.OutMoveID != f.MoveID {
		t.Fatalf("visit = %+v", v)
	}
	if form, _ := x.st.GetMoveForm(ctxA, f.MoveID); form.FormSavedAt == nil {
		t.Fatal("the move was not stored")
	}
}

func TestGateInOfNumberInYardElsewhereClosesOldVisit(t *testing.T) {
	x := newFixture(t)
	old := x.save(ctxA, gateIn(numA))
	f := gateIn(numA)
	f.MoveID = x.move(ctxD2)
	r := x.save(ctxD2, f)
	if r.Refused != nil || !slices.Contains(codes(r.Flags), "missed_gate_out") {
		t.Fatalf("result = %+v", r)
	}
	if v := x.visit(ctxA, old.VisitID); v.Status != "out" || v.CloseReason != "missed_gate_out" {
		t.Fatalf("old visit = %+v", v)
	}
	if v := x.visit(ctxD2, r.VisitID); v.Status != "in_yard" || v.DepotID != "d2" {
		t.Fatalf("new visit = %+v", v)
	}
}

func TestCorrectionMovesVisitAndItem(t *testing.T) {
	x := newFixture(t)
	orderID, _ := x.order(ctxA, "redelivery", OrderItem{Number: ptr(numA)}, OrderItem{Number: ptr(numB)})
	// numA was already (wrongly) shown in the yard; the misread gate-in closes it.
	imported, err := x.st.CreateVisit(ctxA, Visit{Number: numA, Imported: true})
	if err != nil {
		t.Fatal(err)
	}
	f := gateIn(numA)
	f.MoveID = x.move(ctxA)
	r := x.save(ctxA, f)
	if x.visit(ctxA, imported).Status != "out" {
		t.Fatal("setup: the gate-in did not close the imported visit")
	}
	f.Number = numB
	c, err := x.svc.CorrectGateMove(ctxA, f.MoveID, f, "OCR misread", "office")
	if err != nil {
		t.Fatal(err)
	}
	if c.VisitID == "" || c.VisitID == r.VisitID || c.Move.Number != numB || c.Move.CorrectedFrom != numA {
		t.Fatalf("correction = %+v", c)
	}
	if _, err := x.st.GetVisit(ctxA, r.VisitID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the wrong visit remains: %v", err)
	}
	if v := x.visit(ctxA, imported); v.Status != "in_yard" || v.OutMoveID != nil || v.CloseReason != "" {
		t.Fatalf("the visit closed by the misread was not reopened: %+v", v)
	}
	if v := x.visit(ctxA, c.VisitID); v.Number != numB || v.Status != "in_yard" {
		t.Fatalf("corrected visit = %+v", v)
	}
	if got := x.used(ctxA, orderID); !slices.Equal(got, []int64{0, 1}) { // items in creation order: numA, numB
		t.Fatalf("used = %v", got)
	}
	ev, err := x.moves.Events(ctxA, f.MoveID)
	if err != nil || !slices.ContainsFunc(ev, func(e store.Event) bool { return e.Kind == "move.corrected" }) {
		t.Fatalf("events = %+v %v", ev, err)
	}
}

func TestVoidReleasesItem(t *testing.T) {
	x := newFixture(t)
	in := gateIn(numA)
	in.MoveID = x.move(ctxA)
	inRes := x.save(ctxA, in)
	orderID, _ := x.order(ctxA, "release", OrderItem{Number: ptr(numA)})
	out := gateOut(numA)
	out.MoveID = x.move(ctxA)
	x.save(ctxA, out)
	if err := x.svc.VoidGateMove(ctxA, out.MoveID, "wrong truck", "office"); err != nil {
		t.Fatal(err)
	}
	if got := x.used(ctxA, orderID); got[0] != 0 {
		t.Fatalf("used = %v", got)
	}
	if v := x.visit(ctxA, inRes.VisitID); v.Status != "in_yard" || v.OutMoveID != nil {
		t.Fatalf("visit not reopened: %+v", v)
	}
	if form, _ := x.st.GetMoveForm(ctxA, out.MoveID); !form.Voided || form.VoidReason != "wrong truck" {
		t.Fatalf("form = %+v", form)
	}
	if err := x.svc.VoidGateMove(ctxA, out.MoveID, "again", "office"); !errors.Is(err, ErrVoided) {
		t.Fatalf("second void = %v", err)
	}
	// Voiding the gate-in removes the visit it opened.
	if err := x.svc.VoidGateMove(ctxA, in.MoveID, "test truck", "office"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.st.GetVisit(ctxA, inRes.VisitID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("visit after voiding its gate-in: %v", err)
	}
	ev, _ := x.moves.Events(ctxA, out.MoveID)
	if !slices.ContainsFunc(ev, func(e store.Event) bool { return e.Kind == "move.voided" }) {
		t.Fatalf("events = %+v", ev)
	}
}

func TestSaveGateMoveRejectsWrongLinks(t *testing.T) {
	x := newFixture(t)
	other := x.move(ctxA)
	foreign := x.photo(ctxA, other, "signature")
	_, d2Items := x.order(ctxD2, "redelivery", OrderItem{TypeCode: ptr("45G1")})

	sig := gateIn(numA)
	sig.MoveID = x.move(ctxA)
	sig.SignaturePhotoID = foreign
	dmg := gateIn(numA)
	dmg.MoveID = x.move(ctxA)
	dmg.Damages = []DamageInput{{Location: "roof", PhotoIDs: []string{foreign}}}
	item := gateIn(numA)
	item.MoveID = x.move(ctxA)
	item.ChosenOrderItemID = d2Items[0].ID // an item of another depot's order
	appr := gateOut(numA)
	appr.MoveID = x.move(ctxA)
	for name, f := range map[string]GateForm{"signature": sig, "damage photo": dmg, "order item": item} {
		if _, err := x.svc.SaveGateMove(ctxA, f, "clerk"); !errors.Is(err, ErrBadLink) {
			t.Errorf("%s: err = %v, want ErrBadLink", name, err)
		}
	}
	// An approval of another move does not pass this one.
	if err := x.st.SetSettings(ctxA, Settings{RefusalMode: "manager_approval", WalkinsAllowed: true, EirTtlDays: 30}); err != nil {
		t.Fatal(err)
	}
	o := gateOut(numB)
	o.MoveID = other
	r := x.save(ctxA, o)
	if err := x.svc.DecideApproval(ctxA, r.Refused.ApprovalID, true, "manager"); err != nil {
		t.Fatal(err)
	}
	appr.ApprovalID = r.Refused.ApprovalID
	if _, err := x.svc.SaveGateMove(ctxA, appr, "clerk"); !errors.Is(err, ErrBadLink) {
		t.Errorf("approval: err = %v, want ErrBadLink", err)
	}
	if got, _ := x.st.ListVisits(ctxA, ""); len(got) != 0 {
		t.Fatalf("visits = %+v", got)
	}
}

func TestSnapshotIsDepotScoped(t *testing.T) {
	x := newFixture(t)
	d1Order, _ := x.order(ctxA, "release", OrderItem{TypeCode: ptr("45G1"), Quantity: 2})
	x.order(ctxD2, "release", OrderItem{TypeCode: ptr("22G1")})
	cancelled, _ := x.order(ctxA, "release", OrderItem{Number: ptr(numB)})
	if err := x.svc.CancelOrder(ctxA, cancelled, "office"); err != nil {
		t.Fatal(err)
	}
	in1 := x.save(ctxA, gateIn(numA))
	in2 := x.save(ctxD2, gateIn(numB))
	if _, err := x.svc.PlaceHold(ctxA, in1.VisitID, "customs", "office"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.PlaceHold(ctxD2, in2.VisitID, "unpaid", "office"); err != nil {
		t.Fatal(err)
	}
	blockID, err := x.svc.UpsertBlock(ctxA, "container", "TGHU0000001", "stolen", "office")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.UpsertBlock(ctxA, "container", "TGHU0000002", "old", "office"); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.SetBlockActive(ctxA, blockID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.UpsertBlock(ctxOf("B"), "container", "TGHU0000003", "B's", "office"); err != nil {
		t.Fatal(err)
	}

	s, err := x.svc.Snapshot(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Orders) != 1 || s.Orders[0].ID != d1Order || len(s.Orders[0].Items) != 1 {
		t.Fatalf("orders = %+v", s.Orders)
	}
	if len(s.Blocks) != 1 || s.Blocks[0].Value != "TGHU0000002" {
		t.Fatalf("blocks = %+v", s.Blocks)
	}
	if len(s.Holds) != 1 || s.Holds[0].VisitID != in1.VisitID {
		t.Fatalf("holds = %+v", s.Holds)
	}
	if len(s.InYard) != 1 || s.InYard[0].Number != numA {
		t.Fatalf("in yard = %+v", s.InYard)
	}
	if len(s.LinePrefixes) != 2 { // MSCU and CSQU, company-wide
		t.Fatalf("line prefixes = %+v", s.LinePrefixes)
	}
	if s.At.IsZero() {
		t.Fatal("snapshot without a time")
	}
	all := tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", UserID: "u1", Role: tenancy.Owner})
	if _, err := x.svc.Snapshot(all); !errors.Is(err, tenancy.ErrNoScope) {
		t.Fatalf("company-wide snapshot = %v", err)
	}
	b, err := x.svc.Snapshot(ctxOf("B"))
	if err != nil || len(b.Orders)+len(b.Holds)+len(b.InYard)+len(b.LinePrefixes) != 0 || len(b.Blocks) != 1 {
		t.Fatalf("B's snapshot = %+v %v", b, err)
	}
}

// Store carry-overs: a replayed close never overwrites a closed visit;
// numbers and block values are stored and matched normalised; in_at defaults to now.
func TestVisitCloseNormaliseAndDefaults(t *testing.T) {
	s, pool := openTest(t)
	m1, m2 := newMove(t, pool, ctxA), newMove(t, pool, ctxA)
	before := time.Now().Add(-time.Minute)
	id, err := s.CreateVisit(ctxA, Visit{Number: " mscu 123456-5"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.InYardVisit(ctxA, "MSCU-1234565")
	if err != nil || v.ID != id || v.Number != numA || v.InAt.Before(before) {
		t.Fatalf("visit = %+v %v", v, err)
	}
	if err := s.CloseVisit(ctxA, id, &m1, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseVisit(ctxA, id, &m2, time.Now(), "replay"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closing a closed visit = %v", err)
	}
	if v, _ := s.GetVisit(ctxA, id); *v.OutMoveID != m1 || v.CloseReason != "" {
		t.Fatalf("closed visit overwritten: %+v", v)
	}
	if _, err := s.UpsertBlock(ctxA, "container", " mscu1234565 ", "x", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertBlock(ctxA, "line", " msc ", "x", "u1"); err != nil {
		t.Fatal(err)
	}
	bl, _ := s.ListBlocks(ctxA, true)
	if len(bl) != 2 || bl[0].Value != numA || bl[1].Value != "MSC" {
		t.Fatalf("blocks = %+v", bl)
	}
	var n int
	if err := db.InCompany(ctxA, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctxA, `SELECT count(*) FROM block_list`).Scan(&n)
	}); err != nil || n != 2 {
		t.Fatalf("block rows %d %v", n, err)
	}
}
