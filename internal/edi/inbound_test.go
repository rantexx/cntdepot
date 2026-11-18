package edi

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"depot/internal/blob"
	"depot/internal/db"
	"depot/internal/edifact"
	"depot/internal/mail"
	"depot/internal/store"
	"depot/internal/tenancy"
	"depot/internal/yard"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const lineFrom = "edi@msc.test"

type fx struct {
	t     *testing.T
	s     *Service
	pool  *pgxpool.Pool
	y     *yard.Service
	moves *store.Store
	bl    *blob.Store
	token string
}

// newFx is company A (depot d1, line lA) with lA on EDI: sender MSC,
// mail allowed from lineFrom, APERAK on.
func newFx(t *testing.T) *fx {
	t.Helper()
	st, pool := openTest(t)
	bl, err := blob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	moves := store.New(pool)
	y := yard.NewService(yard.NewStore(pool), moves, nil)
	x := &fx{t: t, pool: pool, y: y, moves: moves, bl: bl,
		s: NewService(pool, bl, y, mail.NewFile(t.TempDir(), "edi@depot.test"), Config{From: "edi@depot.test", InboundDomain: "in.depot.test"})}
	ce, err := st.CompanyEDI(ctxOf("A"))
	if err != nil {
		t.Fatal(err)
	}
	x.token = ce.InboundToken
	x.line(Profile{SenderID: "MSC", AllowedFrom: []string{lineFrom}, SendAPERAK: true})
	return x
}

func (x *fx) exec(sql string, args ...any) {
	x.t.Helper()
	ctx := ctxOf("A")
	if err := db.InCompany(ctx, x.pool, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, args...); return err }); err != nil {
		x.t.Fatal(err)
	}
}

func (x *fx) line(p Profile) {
	x.t.Helper()
	b, _ := json.Marshal(p)
	x.exec(`INSERT INTO line_edi (company_id, line_id, mode, profile) VALUES ('A', 'lA', 'edi', $1)
		ON CONFLICT (company_id, line_id) DO UPDATE SET profile = EXCLUDED.profile`, b)
}

func file(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../edifact/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (x *fx) upload(data []byte) Message {
	x.t.Helper()
	ms, err := x.s.Upload(ctxOf("A"), "f.edi", data, "u1", nil)
	if err != nil || len(ms) != 1 {
		x.t.Fatalf("Upload = %+v, %v", ms, err)
	}
	return ms[0]
}

func (x *fx) mail(data []byte, trusted bool) {
	x.t.Helper()
	in := mail.Inbound{To: []string{"EDI-" + strings.ToUpper(x.token) + "@in.depot.test"}, From: lineFrom, Trusted: trusted,
		Attachments: []mail.Attachment{{Name: "f.edi", Data: data}}}
	if err := x.s.ReceiveMail(context.Background(), in); err != nil {
		x.t.Fatal(err)
	}
}

// messages returns company A's rows of one direction, oldest first.
func (x *fx) messages(direction string) []Message {
	x.t.Helper()
	ctx := ctxOf("A")
	var ids []string
	if err := db.InCompany(ctx, x.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM edi_messages WHERE direction = $1 ORDER BY created_at, id`, direction)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		x.t.Fatal(err)
	}
	var out []Message
	for _, id := range ids {
		m, err := x.s.Message(ctx, id)
		if err != nil {
			x.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// order reads an order in its depot.
func (x *fx) order(id, depot string) (yard.Order, []yard.OrderItem) {
	x.t.Helper()
	o, items, err := x.y.GetOrder(tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", DepotID: depot}), id)
	if err != nil {
		x.t.Fatal(err)
	}
	return o, items
}

// answer reads the APERAK of an out row.
func (x *fx) answer(m Message) edifact.Answer {
	x.t.Helper()
	as := x.answers(m)
	if len(as) != 1 {
		x.t.Fatalf("%d APERAK messages", len(as))
	}
	return as[0]
}

// answers reads every APERAK message in an out row's interchange.
func (x *fx) answers(m Message) []edifact.Answer {
	x.t.Helper()
	ic, err := edifact.Parse(x.raw(m))
	if err != nil {
		x.t.Fatal(err)
	}
	var out []edifact.Answer
	for _, msg := range ic.Messages {
		out = append(out, edifact.ReadAPERAK(msg))
	}
	return out
}

func (x *fx) raw(m Message) []byte {
	x.t.Helper()
	data, err := x.bl.Get(m.BlobDigest)
	if err != nil {
		x.t.Fatal(err)
	}
	return data
}

func codes(ps []edifact.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestCOPARNCreatesOrder(t *testing.T) {
	x := newFx(t)
	m := x.upload(file(t, "coparn_release.edi"))
	if m.Status != "done" || m.Kind != "coparn" || m.LineID != "lA" || m.DepotID != "d1" || m.OrderID == "" ||
		m.ControlRef != "20001" || m.Sender != "u1" || len(m.Problems) != 0 {
		t.Fatalf("message = %+v", m)
	}
	o, items := x.order(m.OrderID, "d1")
	if o.Source != "edi" || deref(o.EdiMessageID) != m.ID || deref(o.LineID) != "lA" || o.Kind != "release" ||
		o.Reference != "RLS4471" || o.Status != "open" || o.ValidUntil == nil {
		t.Fatalf("order = %+v", o)
	}
	if len(items) != 1 || deref(items[0].Number) != "MSCU1234566" || items[0].FullEmpty != "E" || items[0].Quantity != 1 {
		t.Fatalf("items = %+v", items)
	}
	out := x.messages("out")
	if len(out) != 1 || out[0].Kind != "aperak" || out[0].Status != "pending" || out[0].AnswerOf != m.ID || out[0].ControlRef == "" {
		t.Fatalf("out = %+v", out)
	}
	if a := x.answer(out[0]); !a.Accepted || !slices.Equal(a.RefersTo, []string{"20001"}) {
		t.Fatalf("APERAK = %+v", a)
	}
}

func TestQuantityItemsUseTypeMap(t *testing.T) {
	x := newFx(t)
	x.line(Profile{SenderID: "MSC", TypeMap: [][2]string{{"45G0", "45g1"}}})
	m := x.upload(file(t, "coparn_redelivery_quantity.edi"))
	if m.Status != "done" {
		t.Fatalf("message = %+v", m)
	}
	o, items := x.order(m.OrderID, "d1")
	var got []string
	for _, it := range items {
		got = append(got, deref(it.TypeCode)+"x"+string(rune('0'+it.Quantity)))
	}
	if o.Kind != "redelivery" || !slices.Equal(got, []string{"22G1x3", "45G0x2"}) {
		t.Fatalf("order = %+v, items %v", o, got)
	}
	if out := x.messages("out"); len(out) != 0 {
		t.Fatalf("APERAK off, but out = %+v", out)
	}
}

func TestSameFileTwiceMakesOneOrder(t *testing.T) {
	x := newFx(t)
	data := file(t, "coparn_release.edi")
	x.mail(data, true)
	x.mail(data, true)
	x.upload(data)
	in := x.messages("in")
	var st []string
	for _, m := range in {
		st = append(st, m.Status)
	}
	if !slices.Equal(st, []string{"done", "ignored", "ignored"}) || in[0].Sender != lineFrom || in[0].FileName != "f.edi" {
		t.Fatalf("in = %+v", in)
	}
	if n := count(t, ctxOf("A"), x.pool, `SELECT count(*) FROM release_orders`); n != 1 {
		t.Fatalf("%d orders", n)
	}
}

func TestRepeatRefWithOtherContentIsProcessed(t *testing.T) {
	x := newFx(t)
	data := file(t, "coparn_release.edi")
	x.upload(data)
	m := x.upload([]byte(strings.Replace(string(data), "DTM+36:202611301200:203", "DTM+36:202612011200:203", 1)))
	if m.Status != "done" || !slices.Equal(codes(m.Problems), []string{"W-REPREF"}) {
		t.Fatalf("message = %+v", m)
	}
	if n := count(t, ctxOf("A"), x.pool, `SELECT count(*) FROM release_orders`); n != 1 {
		t.Fatalf("%d orders", n)
	}
}

const replacement = `UNA:+.? '
UNB+UNOA:2+MSC:ZZZ+DEPOTX:ZZZ+261118:1300+20009'
UNH+20009+COPARN:D:00B:UN:SMDG21'
BGM+108+RLS4471+5+AB'
RFF+ACW:20001'
RFF+ATY:RLS4471'
EQD+CN++22G1:102:5+2++4'
EQN+1'
UNT+6+20009'
UNZ+1+20009'
`

func TestReplaceKeepsGatedItems(t *testing.T) {
	x := newFx(t)
	first := x.upload(file(t, "coparn_release.edi"))
	ctx := tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", DepotID: "d1", UserID: "clerk"})
	for _, dir := range []string{"in", "out"} {
		mv, err := x.moves.CreateMove(ctx, "1", false, "clerk")
		if err != nil {
			t.Fatal(err)
		}
		r, err := x.y.SaveGateMove(ctx, yard.GateForm{MoveID: mv.ID, Direction: dir, Number: "MSCU1234566", TypeCode: "22G1",
			FullEmpty: "E", Condition: "sound", LineID: "lA", TruckPlate: "34ABC12"}, "clerk")
		if err != nil || r.Refused != nil {
			t.Fatalf("gate %s = %+v, %v", dir, r, err)
		}
	}
	m := x.upload([]byte(replacement))
	if m.Status != "done" || m.OrderID != first.OrderID || !slices.Equal(codes(m.Problems), []string{"W-KEPT"}) ||
		!strings.Contains(m.Problems[0].Text, "MSCU1234566 already gated") {
		t.Fatalf("message = %+v", m)
	}
	_, items := x.order(m.OrderID, "d1")
	var got []string
	for _, it := range items {
		got = append(got, deref(it.Number)+deref(it.TypeCode)+" "+string(rune('0'+it.Used))+"/"+string(rune('0'+it.Quantity)))
	}
	if !slices.Equal(got, []string{"MSCU1234566 1/1", "22G1 0/1"}) {
		t.Fatalf("items = %v", got)
	}
	out := x.messages("out")
	if len(out) != 2 || out[1].AnswerOf != m.ID {
		t.Fatalf("out = %+v", out)
	}
	if a := x.answer(out[1]); !a.Accepted {
		t.Fatalf("APERAK = %+v", a)
	}
	if raw := string(x.raw(out[1])); !strings.Contains(raw, "W-KEPT MSCU1234566 ALREADY GATED") {
		t.Fatalf("APERAK lacks the kept warning: %s", raw)
	}
}

const twoMessages = `UNA:+.? '
UNB+UNOA:2+MSC:ZZZ+DEPOTX:ZZZ+261118:1400+20020'
UNH+1+COPARN:D:00B:UN:SMDG21'
BGM+108+RLS4471+9+AB'
RFF+ATY:RLS4471'
EQD+CN+MSCU1234566+22G1:102:5+2++4'
UNT+5+1'
UNH+2+COPARN:D:00B:UN:SMDG21'
BGM+108+RLS9999+1+AB'
RFF+ATY:RLS9999'
UNT+4+2'
UNZ+2+20020'
`

// Each COPARN message gets its own APERAK answer.
func TestOneAnswerPerMessage(t *testing.T) {
	x := newFx(t)
	m := x.upload([]byte(twoMessages))
	if m.Status != "error" || !slices.Equal(codes(m.Problems), []string{"E-NOORD"}) || !strings.HasPrefix(m.Problems[0].Text, "message 2: ") {
		t.Fatalf("message = %+v", m)
	}
	if o, _ := x.order(m.OrderID, "d1"); o.Reference != "RLS4471" || o.Status != "open" {
		t.Fatalf("order = %+v", o)
	}
	out := x.messages("out")
	if len(out) != 1 {
		t.Fatalf("out = %+v", out)
	}
	as := x.answers(out[0])
	if len(as) != 2 || !as[0].Accepted || !slices.Equal(as[0].RefersTo, []string{"1"}) ||
		as[1].Accepted || !slices.Equal(as[1].RefersTo, []string{"2"}) || !slices.Equal(codes(as[1].Problems), []string{"E-NOORD"}) {
		t.Fatalf("APERAKs = %+v", as)
	}
}

func TestCancelCancelsOrder(t *testing.T) {
	x := newFx(t)
	first := x.upload(file(t, "coparn_release.edi"))
	m := x.upload(file(t, "coparn_cancel.edi"))
	if m.Status != "done" || m.OrderID != first.OrderID || m.Function != "1" {
		t.Fatalf("message = %+v", m)
	}
	if o, _ := x.order(m.OrderID, "d1"); o.Status != "cancelled" {
		t.Fatalf("order = %+v", o)
	}
}

func TestCancelUnknownIsError(t *testing.T) {
	x := newFx(t)
	m := x.upload(file(t, "coparn_cancel.edi"))
	if m.Status != "error" || m.OrderID != "" || !slices.Equal(codes(m.Problems), []string{"E-NOORD"}) {
		t.Fatalf("message = %+v", m)
	}
	out := x.messages("out")
	if len(out) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if a := x.answer(out[0]); a.Accepted || !slices.Equal(codes(a.Problems), []string{"E-NOORD"}) {
		t.Fatalf("APERAK = %+v", a)
	}
}

func TestUntrustedSenderIsUnmatched(t *testing.T) {
	x := newFx(t)
	x.mail(file(t, "coparn_release.edi"), false)
	in := x.messages("in")
	if len(in) != 1 || in[0].Status != "unmatched" || in[0].LineID != "" || in[0].BlobDigest == "" {
		t.Fatalf("in = %+v", in)
	}
	if n := count(t, ctxOf("A"), x.pool, `SELECT count(*) FROM release_orders`); n != 0 {
		t.Fatalf("%d orders", n)
	}
	// The office picks the line: processed.
	m, err := x.s.Process(ctxOf("A"), in[0].ID, "lA", "", "u1", nil)
	if err != nil || m.Status != "done" || m.OrderID == "" {
		t.Fatalf("Process = %+v, %v", m, err)
	}
}

func TestMultiDepotWithoutLocationIsError(t *testing.T) {
	x := newFx(t)
	x.exec(`INSERT INTO depots (company_id, id, name) VALUES ('A', 'd2', 'D2')`)
	m := x.upload(file(t, "coparn_release.edi"))
	if m.Status != "error" || !slices.Equal(codes(m.Problems), []string{"E-DEPOT"}) {
		t.Fatalf("message = %+v", m)
	}
	m, err := x.s.Process(ctxOf("A"), m.ID, "", "d2", "u1", nil)
	if err != nil || m.Status != "done" || m.DepotID != "d2" || len(m.Problems) != 0 {
		t.Fatalf("Process = %+v, %v", m, err)
	}
	if o, _ := x.order(m.OrderID, "d2"); o.DepotID != "d2" {
		t.Fatalf("order = %+v", o)
	}
	if _, err := x.s.Process(ctxOf("A"), m.ID, "", "d2", "u1", nil); err == nil {
		t.Fatal("a done message processed again")
	}
}

// A file left "received" (the worker's case) is processed with the trust it
// arrived with.
func TestStoredMailKeepsItsTrust(t *testing.T) {
	x := newFx(t)
	data := file(t, "coparn_release.edi")
	ctx := tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", UserID: mailActor})
	untrusted, err := x.s.store(ctx, "f.edi", data, lineFrom, "mail", false)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := x.s.processStored(ctx, untrusted.ID); err != nil || m.Status != "unmatched" || m.Via != "mail" || m.Trusted {
		t.Fatalf("untrusted = %+v, %v", m, err)
	}
	trusted, err := x.s.store(ctx, "f.edi", data, lineFrom, "mail", true)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := x.s.processStored(ctx, trusted.ID); err != nil || m.Status != "done" || !m.Trusted {
		t.Fatalf("trusted = %+v, %v", m, err)
	}
}

// A location code must match when the company has set depot locations; a
// one-depot company without locations takes every file.
func TestLocationMustMatchSetLocations(t *testing.T) {
	x := newFx(t)
	x.exec(`UPDATE depots SET edi_location = 'TRIST' WHERE id = 'd1'`)
	m := x.upload(file(t, "coparn_release.edi")) // names TRMER
	if m.Status != "error" || !slices.Equal(codes(m.Problems), []string{"E-DEPOT"}) {
		t.Fatalf("message = %+v", m)
	}
	x.exec(`UPDATE depots SET edi_location = '' WHERE id = 'd1'`)
	if m, err := x.s.Process(ctxOf("A"), m.ID, "", "", "u1", nil); err != nil || m.Status != "done" || m.DepotID != "d1" {
		t.Fatalf("Process = %+v, %v", m, err)
	}
}

func TestLocationPicksDepot(t *testing.T) {
	x := newFx(t)
	x.exec(`INSERT INTO depots (company_id, id, name, edi_location) VALUES ('A', 'd2', 'D2', 'trmer')`)
	if m := x.upload(file(t, "coparn_release.edi")); m.Status != "done" || m.DepotID != "d2" {
		t.Fatalf("message = %+v", m)
	}
}

// Spec 5.8.1: equipment for another depot is an error for the message.
func TestReplacementForOtherDepotIsError(t *testing.T) {
	x := newFx(t)
	x.exec(`UPDATE depots SET edi_location = 'TRMER' WHERE id = 'd1'`)
	x.exec(`INSERT INTO depots (company_id, id, name, edi_location) VALUES ('A', 'd2', 'D2', 'TRIST')`)
	first := x.upload(file(t, "coparn_release.edi"))
	if first.Status != "done" || first.DepotID != "d1" {
		t.Fatalf("original = %+v", first)
	}
	repl := strings.Replace(replacement, "EQN+1'\n", "EQN+1'\nLOC+98+TRIST:139:6'\n", 1)
	m := x.upload([]byte(repl))
	if m.Status != "error" || !slices.Equal(codes(m.Problems), []string{"E-DEPOT"}) ||
		!strings.Contains(m.Problems[0].Text, "at depot D1, this file names D2") {
		t.Fatalf("message = %+v", m)
	}
	o, items := x.order(first.OrderID, "d1")
	if o.DepotID != "d1" || len(items) != 1 || deref(items[0].Number) != "MSCU1234566" {
		t.Fatalf("order = %+v, items %+v", o, items)
	}
}

func TestAPERAKInMarksOutRejected(t *testing.T) {
	x := newFx(t)
	x.exec(`INSERT INTO edi_messages (company_id, id, direction, kind, status, line_id, message_ref, control_ref)
		VALUES ('A', 'c1', 'out', 'codeco', 'sent', 'lA', '30001', '30001')`)
	m := x.upload(file(t, "aperak_reject.edi"))
	if m.Status != "done" || m.Kind != "aperak" {
		t.Fatalf("message = %+v", m)
	}
	out := x.messages("out")
	if len(out) != 1 || out[0].Status != "rejected" || len(out[0].Problems) != 2 || out[0].Problems[0].Text != "EQUIPMENT NUMBER UNKNOWN" {
		t.Fatalf("out = %+v", out)
	}
	// An answer by move id (the CODECO document number) leaves a later CODECO
	// of that move, still queued, alone.
	mv, err := x.moves.CreateMove(ctxOf("A"), "1", false, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	x.exec(`INSERT INTO edi_messages (company_id, id, direction, kind, status, line_id, move_id, message_ref, control_ref)
		VALUES ('A', 'c2', 'out', 'codeco', 'sent', 'lA', $1, '30002', '30002'),
		       ('A', 'c3', 'out', 'codeco', 'pending', 'lA', $1, '30003', '30003')`, mv.ID)
	byMove := strings.Replace(string(file(t, "aperak_reject.edi")), "RFF+ACW:30001", "RFF+ACW:"+mv.ID, 1)
	byMove = strings.ReplaceAll(byMove, "88123", "88124") // another interchange
	if m := x.upload([]byte(byMove)); m.Status != "done" {
		t.Fatalf("message = %+v", m)
	}
	for id, want := range map[string]string{"c2": "rejected", "c3": "pending"} {
		if got, err := x.s.Message(ctxOf("A"), id); err != nil || got.Status != want {
			t.Fatalf("%s = %+v, %v; want %s", id, got, err, want)
		}
	}
	// An answer to nothing of ours is unmatched.
	x.exec(`DELETE FROM edi_messages`)
	if m := x.upload(file(t, "aperak_reject.edi")); m.Status != "unmatched" {
		t.Fatalf("message = %+v", m)
	}
}

func TestBadFilesAreKeptAsErrors(t *testing.T) {
	x := newFx(t)
	if m := x.upload([]byte("not edifact")); m.Status != "error" || m.BlobDigest == "" || len(m.Problems) != 1 {
		t.Fatalf("message = %+v", m)
	}
	codeco, err := os.ReadFile("../edifact/testdata/codeco_gate_in.edi")
	if err != nil {
		t.Fatal(err)
	}
	if m := x.upload(codeco); m.Status != "error" || m.Kind != "unknown" {
		t.Fatalf("message = %+v", m)
	}
}

func TestUnknownTokenIsDropped(t *testing.T) {
	x := newFx(t)
	in := mail.Inbound{To: []string{"edi-nope@in.depot.test", "someone@else.test"}, From: lineFrom, Trusted: true,
		Attachments: []mail.Attachment{{Name: "f.edi", Data: file(t, "coparn_release.edi")}}}
	if err := x.s.ReceiveMail(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"A", "B"} {
		if n := count(t, ctxOf(c), x.pool, `SELECT count(*) FROM edi_messages`); n != 0 {
			t.Fatalf("company %s has %d messages", c, n)
		}
	}
}

// A member limited to d2 cancels nothing at d1 (and creates nothing there by
// the only-depot fallback); the message is an error a full manager can process.
func TestLimitedMemberAppliesNothingOutsideItsDepots(t *testing.T) {
	x := newFx(t)
	first := x.upload(file(t, "coparn_release.edi"))
	ms, err := x.s.Upload(ctxOf("A"), "c.edi", file(t, "coparn_cancel.edi"), "u2", []string{"d2"})
	if err != nil || len(ms) != 1 || ms[0].Status != "error" || ms[0].OrderID != "" || ms[0].DepotID != "" ||
		!slices.Contains(codes(ms[0].Problems), "E-DEPOT") || !strings.Contains(ms[0].Problems[0].Text, "outside your depots") {
		t.Fatalf("limited cancel = %+v, %v", ms, err)
	}
	if o, _ := x.order(first.OrderID, "d1"); o.Status != "open" {
		t.Fatalf("order = %+v", o)
	}
	if m, err := x.s.Process(ctxOf("A"), ms[0].ID, "", "", "u1", nil); err != nil || m.Status != "done" {
		t.Fatalf("full Process = %+v, %v", m, err)
	}
	if o, _ := x.order(first.OrderID, "d1"); o.Status != "cancelled" {
		t.Fatalf("order = %+v", o)
	}

	y := newFx(t) // one depot, d1: the fallback names it
	if ms, err := y.s.Upload(ctxOf("A"), "r.edi", file(t, "coparn_release.edi"), "u2", []string{"d2"}); err != nil ||
		ms[0].Status != "error" || ms[0].OrderID != "" || !slices.Contains(codes(ms[0].Problems), "E-DEPOT") {
		t.Fatalf("limited release = %+v, %v", ms, err)
	}
}
