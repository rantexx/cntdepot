package edi

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	edidb "depot/internal/edi/db"
	"depot/internal/edifact"
	"depot/internal/iso6346"
	"depot/internal/mail"
	"depot/internal/tenancy"
	"depot/internal/yard"
)

// fakeMailer records what it sends and fails while fail is set.
type fakeMailer struct {
	mu   sync.Mutex
	sent []mail.Outgoing
	fail bool
}

func (f *fakeMailer) Send(_ context.Context, m mail.Outgoing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("mail server down")
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeMailer) Name() string { return "fake" }

const (
	lineTo  = "codeco@msc.test"
	otherTo = "codeco@mae.test"
)

// outFx is fx with a fake mailer, a clock the test moves, party DEPOTX, lA
// sending CODECO to lineTo and a second EDI line lX (code MAE) to otherTo.
type outFx struct {
	*fx
	mail *fakeMailer
	at   time.Time
	gate context.Context
}

func newOutFx(t *testing.T) *outFx {
	t.Helper()
	o := &outFx{fx: newFx(t), mail: &fakeMailer{}, at: time.Now(),
		gate: tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", DepotID: "d1", UserID: "clerk"})}
	o.s.mail = o.mail
	o.s.now = func() time.Time { return o.at }
	o.exec(`UPDATE company_edi SET party_id = 'DEPOTX'`)
	o.line(Profile{SenderID: "MSC", AllowedFrom: []string{lineFrom}, RecipientID: "MSC", SendTo: []string{lineTo}})
	o.exec(`INSERT INTO shipping_lines (company_id, id, code, name) VALUES ('A', 'lX', 'MAE', 'Maersk')`)
	o.exec(`INSERT INTO line_edi (company_id, line_id, mode, profile) VALUES ('A', 'lX', 'edi', '{"SendTo": ["` + otherTo + `"]}')`)
	return o
}

// cn is a container number with its check digit.
func cn(first10 string) string { return first10 + strconv.Itoa(iso6346.CheckDigit(first10)) }

func form(id, dir, number, fe, line string) yard.GateForm {
	return yard.GateForm{MoveID: id, Direction: dir, Number: number, TypeCode: "22G1", FullEmpty: fe, Condition: "sound",
		LineID: line, TruckPlate: "34 abc 1234567"}
}

// gateIn saves a gate-in and returns its move id.
func (o *outFx) gateIn(number, fe, line string) string {
	o.t.Helper()
	mv, err := o.moves.CreateMove(o.gate, "1", false, "clerk")
	if err != nil {
		o.t.Fatal(err)
	}
	if r, err := o.y.SaveGateMove(o.gate, form(mv.ID, "in", number, fe, line), "clerk"); err != nil || r.Refused != nil {
		o.t.Fatalf("gate = %+v, %v", r, err)
	}
	return mv.ID
}

func (o *outFx) correct(id string, f yard.GateForm) {
	o.t.Helper()
	if r, err := o.y.CorrectGateMove(o.gate, id, f, "typo", "office"); err != nil || r.Refused != nil {
		o.t.Fatalf("correct = %+v, %v", r, err)
	}
}

func (o *outFx) tick() {
	o.t.Helper()
	if err := o.s.Tick(context.Background()); err != nil {
		o.t.Fatal(err)
	}
}

// sent returns the mails sent so far.
func (o *outFx) sent() []mail.Outgoing {
	o.mail.mu.Lock()
	defer o.mail.mu.Unlock()
	return slices.Clone(o.mail.sent)
}

// codeco reads the one CODECO in mail m.
func codeco(t *testing.T, m mail.Outgoing) (edifact.Interchange, edifact.Message) {
	t.Helper()
	if len(m.Attachments) != 1 || m.Attachments[0].ContentType != "application/EDIFACT" {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	ic, err := edifact.Parse(m.Attachments[0].Data)
	if err != nil || len(ic.Messages) != 1 || !strings.HasPrefix(ic.Messages[0].Type, "CODECO:") {
		t.Fatalf("interchange = %+v, %v", ic, err)
	}
	return ic, ic.Messages[0]
}

func segment(t *testing.T, m edifact.Message, tag string) edifact.Segment {
	t.Helper()
	for _, s := range m.Segments {
		if s.Tag == tag {
			return s
		}
	}
	t.Fatalf("no %s in %+v", tag, m.Segments)
	return edifact.Segment{}
}

// fn is a mail's CODECO function and, for 1 and 5, its RFF+ACW.
func fn(t *testing.T, m mail.Outgoing) string {
	t.Helper()
	_, msg := codeco(t, m)
	f := segment(t, msg, "BGM").Get(2, 0)
	if f != "9" {
		f += " " + segment(t, msg, "RFF").Get(0, 1)
	}
	return f
}

// gateTime is a CODECO's DTM+7.
func gateTime(t *testing.T, m edifact.Message) string {
	t.Helper()
	for _, s := range m.Segments {
		if s.Tag == "DTM" && s.Get(0, 0) == "7" {
			return s.Get(0, 1)
		}
	}
	t.Fatalf("no DTM+7 in %+v", m.Segments)
	return ""
}

// facts are what a cancel must repeat from the message it cancels.
func facts(t *testing.T, m mail.Outgoing) []string {
	t.Helper()
	_, msg := codeco(t, m)
	return []string{segment(t, msg, "BGM").Get(0, 0), segment(t, msg, "BGM").Get(1, 0), segment(t, msg, "EQD").Get(1, 0),
		segment(t, msg, "NAD").Get(1, 0), segment(t, msg, "TDT").Get(7, 0), gateTime(t, msg)}
}

func TestGateSavedQueuesAndSendsCODECO(t *testing.T) {
	o := newOutFx(t)
	id := o.gateIn("MSCU1234566", "E", "lA")
	// The gate time is the form's save time in the depot's zone, not when the move was made.
	o.exec(`UPDATE gate_moves SET created_at = created_at - interval '2 hours'`)
	o.exec(`UPDATE depots SET timezone = 'Europe/Istanbul'`)
	rec, err := o.y.GateRecord(o.gate, id)
	if err != nil {
		t.Fatal(err)
	}
	ist, _ := time.LoadLocation("Europe/Istanbul")
	o.tick()
	sent := o.sent()
	if len(sent) != 1 || !slices.Equal(sent[0].To, []string{lineTo}) || sent[0].Subject != "CODECO D1 MSCU1234566" ||
		!strings.HasPrefix(sent[0].Attachments[0].Name, "codeco-") {
		t.Fatalf("sent = %+v", sent)
	}
	ic, msg := codeco(t, sent[0])
	if ic.Sender != "DEPOTX" || ic.Recipient != "MSC" || ic.Test || ic.Prepared.IsZero() || msg.Ref != ic.ControlRef ||
		sent[0].Attachments[0].Name != "codeco-"+ic.ControlRef+".edi" {
		t.Fatalf("interchange = %+v", ic)
	}
	bgm := segment(t, msg, "BGM")
	if bgm.Get(0, 0) != "109" || bgm.Get(1, 0) != id || bgm.Get(2, 0) != "9" {
		t.Fatalf("BGM = %+v", bgm)
	}
	if n := segment(t, msg, "EQD").Get(1, 0); n != "MSCU1234566" {
		t.Fatalf("EQD number = %q", n)
	}
	if c := segment(t, msg, "NAD").Get(1, 0); c != "MSC" {
		t.Fatalf("NAD = %q", c)
	}
	if p := segment(t, msg, "TDT").Get(7, 0); p != "34ABC1234" {
		t.Fatalf("plate = %q", p)
	}
	if at := gateTime(t, msg); at != rec.FormSavedAt.In(ist).Format("200601021504") {
		t.Fatalf("gate time = %s, form saved %v", at, rec.FormSavedAt)
	}
	out := o.messages("out")
	if len(out) != 1 || out[0].Kind != "codeco" || out[0].Status != "sent" || out[0].SentAt == nil || out[0].MoveID != id ||
		out[0].LineID != "lA" || out[0].DepotID != "d1" || out[0].Function != "9" || out[0].ControlRef != ic.ControlRef {
		t.Fatalf("out = %+v", out)
	}
}

func TestNonEDILineSendsNothing(t *testing.T) {
	o := newOutFx(t)
	o.exec(`UPDATE line_edi SET mode = 'report' WHERE line_id = 'lA'`)
	o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	if len(o.sent()) != 0 || len(o.messages("out")) != 0 {
		t.Fatalf("sent = %+v", o.sent())
	}
}

func TestEmptiesOnlySkipsFull(t *testing.T) {
	o := newOutFx(t)
	o.line(Profile{SenderID: "MSC", RecipientID: "MSC", SendTo: []string{lineTo}, EmptiesOnly: true})
	o.gateIn("MSCU1234566", "F", "lA")
	o.tick()
	if len(o.sent()) != 0 {
		t.Fatalf("a full container was sent: %+v", o.sent())
	}
	o.gateIn(cn("MSCU765432"), "E", "lA")
	o.tick()
	if sent := o.sent(); len(sent) != 1 || !strings.HasSuffix(sent[0].Subject, cn("MSCU765432")) {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestCorrectionSendsReplacement(t *testing.T) {
	o := newOutFx(t)
	id := o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	f := form(id, "in", "MSCU1234566", "E", "lA")
	f.Seal = "S1"
	o.correct(id, f)
	o.tick()
	sent := o.sent()
	if len(sent) != 2 || fn(t, sent[1]) != "5 "+id || segment(t, func() edifact.Message { _, m := codeco(t, sent[1]); return m }(), "SEL").Get(0, 0) != "S1" {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestCorrectionToOtherLineCancelsAndSends(t *testing.T) {
	o := newOutFx(t)
	id := o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	o.correct(id, form(id, "in", "MSCU1234566", "E", "lX"))
	o.tick()
	sent := o.sent()
	if len(sent) != 3 || fn(t, sent[1]) != "1 "+id || sent[1].To[0] != lineTo || fn(t, sent[2]) != "9" || sent[2].To[0] != otherTo {
		t.Fatalf("sent = %+v", sent)
	}
	if _, msg := codeco(t, sent[2]); segment(t, msg, "NAD").Get(1, 0) != "MAE" {
		t.Fatalf("new line's CODECO = %+v", msg)
	}
	if _, msg := codeco(t, sent[1]); segment(t, msg, "NAD").Get(1, 0) != "MSC" {
		t.Fatalf("the cancel lacks the old line's code: %+v", msg)
	}
}

// spec 5.8.3: a new number on the same line cancels the old one and sends the new one.
func TestCorrectionChangesNumberCancelsAndSends(t *testing.T) {
	o := newOutFx(t)
	id := o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	o.correct(id, form(id, "in", cn("MSCU765432"), "E", "lA"))
	o.tick()
	sent := o.sent()
	if len(sent) != 3 || fn(t, sent[1]) != "1 "+id || fn(t, sent[2]) != "9" || sent[1].To[0] != lineTo || sent[2].To[0] != lineTo {
		t.Fatalf("sent = %+v", sent)
	}
	if !slices.Equal(facts(t, sent[1]), facts(t, sent[0])) {
		t.Fatalf("cancel %v, original %v", facts(t, sent[1]), facts(t, sent[0]))
	}
	if _, msg := codeco(t, sent[2]); segment(t, msg, "EQD").Get(1, 0) != cn("MSCU765432") {
		t.Fatalf("the original lacks the new number: %+v", msg)
	}
}

func TestVoidSendsCancel(t *testing.T) {
	o := newOutFx(t)
	id := o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	o.exec(`UPDATE shipping_lines SET code = 'MSX' WHERE id = 'lA'`) // the cancel repeats what was sent, not today's data
	if err := o.y.VoidGateMove(o.gate, id, "wrong truck", "office"); err != nil {
		t.Fatal(err)
	}
	o.tick()
	sent := o.sent()
	if len(sent) != 2 || fn(t, sent[1]) != "1 "+id {
		t.Fatalf("sent = %+v", sent)
	}
	if !slices.Equal(facts(t, sent[1]), facts(t, sent[0])) {
		t.Fatalf("cancel %v, original %v", facts(t, sent[1]), facts(t, sent[0]))
	}
}

func TestNoPartyIDIsDataError(t *testing.T) {
	o := newOutFx(t)
	o.exec(`UPDATE company_edi SET party_id = ''`)
	o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	out := o.messages("out")
	if len(out) != 1 || out[0].Status != "error" || !slices.Equal(codes(out[0].Problems), []string{"E-DATA"}) ||
		out[0].Problems[0].Text != "our EDI party id is not set" || len(o.sent()) != 0 {
		t.Fatalf("out = %+v", out)
	}
}

func TestSenderBacksOffKeepsOrderAndFails(t *testing.T) {
	o := newOutFx(t)
	o.mail.fail = true
	id := o.gateIn("MSCU1234566", "E", "lA")
	f := form(id, "in", "MSCU1234566", "E", "lA")
	f.Seal = "S2"
	o.correct(id, f)
	t0 := o.at
	for i, step := range []time.Duration{0, time.Minute, 6 * time.Minute, 36 * time.Minute, 90 * time.Minute} {
		o.at = t0.Add(step)
		o.tick()
		want := min(i+1, 4) // +90 min is before the hourly retry at +96 min
		if out := o.messages("out"); len(out) != 2 || out[0].Tries != int64(want) || out[0].Status != "pending" ||
			out[1].Tries != 0 || out[1].Status != "pending" {
			t.Fatalf("at +%v: out = %+v", step, out)
		}
	}
	o.at = t0.Add(25 * time.Hour)
	o.tick()
	out := o.messages("out")
	if out[0].Status != "failed" || !slices.Equal(codes(out[0].Problems), []string{"E-SEND"}) || out[1].Status != "pending" || out[1].Tries != 0 {
		t.Fatalf("out = %+v", out)
	}
	o.mail.fail = false
	if err := o.s.Resend(ctxOf("A"), out[0].ID, "u1"); err != nil {
		t.Fatal(err)
	}
	o.tick()
	sent := o.sent()
	if len(sent) != 2 || fn(t, sent[0]) != "9" || fn(t, sent[1]) != "5 "+id {
		t.Fatalf("sent = %+v", sent)
	}
	if out := o.messages("out"); out[0].Status != "sent" || out[1].Status != "sent" {
		t.Fatalf("out = %+v", out)
	}
}

func TestNoAddressFails(t *testing.T) {
	o := newOutFx(t)
	o.line(Profile{SenderID: "MSC", RecipientID: "MSC"})
	o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	if out := o.messages("out"); len(out) != 1 || out[0].Status != "failed" || !slices.Equal(codes(out[0].Problems), []string{"E-NOADDR"}) || len(o.sent()) != 0 {
		t.Fatalf("out = %+v", out)
	}
}

func TestGateWorksWhenEDIBroken(t *testing.T) {
	o := newOutFx(t)
	bg := context.Background()
	tx, err := o.pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(bg)
	if _, err := tx.Exec(bg, `LOCK TABLE edi_messages, line_edi, company_edi IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(o.gate, 2*time.Second)
	defer cancel()
	o.gate = ctx
	o.gateIn("MSCU1234566", "E", "lA")
}

func TestRescanIsIdempotent(t *testing.T) {
	o := newOutFx(t)
	o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	o.tick()
	o.at = o.at.Add(time.Minute)
	o.tick()
	if out := o.messages("out"); len(out) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if n := count(t, ctxOf("A"), o.pool, `SELECT next_ref FROM company_edi`); n != 2 {
		t.Fatalf("next_ref = %d: a rescan took a reference", n)
	}
}

func TestSendTestCODECO(t *testing.T) {
	o := newOutFx(t)
	m, err := o.s.SendTest(ctxOf("A"), "lA", "u1")
	if err != nil || m.Kind != "test" || m.Status != "pending" || m.MoveID != "" || m.LineID != "lA" {
		t.Fatalf("SendTest = %+v, %v", m, err)
	}
	o.tick()
	sent := o.sent()
	if len(sent) != 1 || !strings.HasPrefix(sent[0].Subject, "TEST ") || sent[0].Attachments[0].Name != "test-"+m.ControlRef+".edi" {
		t.Fatalf("sent = %+v", sent)
	}
	ic, msg := codeco(t, sent[0])
	if !ic.Test || segment(t, msg, "BGM").Get(0, 0) != "109" || segment(t, msg, "EQD").Get(1, 0) != cn("TESU123456") {
		t.Fatalf("test CODECO = %+v", ic)
	}
}

func TestTickProcessesStuckInbound(t *testing.T) {
	o := newOutFx(t)
	m, err := o.s.store(ctxOf("A"), "f.edi", file(t, "coparn_release.edi"), "u1", "upload", false)
	if err != nil {
		t.Fatal(err)
	}
	o.tick()
	if got, _ := o.s.Message(ctxOf("A"), m.ID); got.Status != "received" {
		t.Fatalf("processed before a minute: %+v", got)
	}
	o.at = o.at.Add(2 * time.Minute)
	o.tick()
	if got, _ := o.s.Message(ctxOf("A"), m.ID); got.Status != "done" || got.OrderID == "" {
		t.Fatalf("message = %+v", got)
	}
}

// Bad move data is never built into a CODECO; long values are cut to the guide's sizes.
func TestGateMoveGuards(t *testing.T) {
	good := func() (edidb.CodecoMoveRow, LineEDI) {
		return edidb.CodecoMoveRow{ID: strings.Repeat("M", 40), Direction: "in", Number: "MSCU1234566", FullEmpty: "E", Timezone: "UTC",
			TruckPlate: "34 abc 1234567", HaulierCode: strings.Repeat("H", 20), EdiLocation: strings.Repeat("L", 30)}, LineEDI{LineID: "lA", Code: "MSC"}
	}
	mv, l := good()
	g, err := gateMove(mv, l, "9", nil)
	if err != nil || len(g.DocNumber) != 35 || g.TruckPlate != "34ABC1234" || len(g.HaulierCode) != 17 || len(g.Location) != 25 || g.LineCode != "MSC" {
		t.Fatalf("gateMove = %+v, %v", g, err)
	}
	l.Profile.LineCode = "MSCX"
	if g, _ := gateMove(mv, l, "9", nil); g.LineCode != "MSCX" {
		t.Fatalf("profile line code not used: %+v", g)
	}
	for name, bad := range map[string]func(*edidb.CodecoMoveRow, *LineEDI){
		"direction":  func(m *edidb.CodecoMoveRow, _ *LineEDI) { m.Direction = "" },
		"full/empty": func(m *edidb.CodecoMoveRow, _ *LineEDI) { m.FullEmpty = "" },
		"line code":  func(_ *edidb.CodecoMoveRow, l *LineEDI) { l.Code = " " },
	} {
		mv, l := good()
		bad(&mv, &l)
		if _, err := gateMove(mv, l, "9", nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestRunReturnsWhenContextEnds(t *testing.T) {
	o := newOutFx(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { o.s.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestTestProfileSetsTestFlag(t *testing.T) {
	o := newOutFx(t)
	o.line(Profile{SenderID: "MSC", RecipientID: "MSC", SendTo: []string{lineTo}, Test: true})
	o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	sent := o.sent()
	if len(sent) != 1 {
		t.Fatalf("sent = %+v", sent)
	}
	if ic, _ := codeco(t, sent[0]); !ic.Test {
		t.Fatalf("a line in test gets a live CODECO: %+v", ic)
	}
}

// lostFile is a digest whose file is not in the blob store.
const lostFile = "0000000000000000000000000000000000000000000000000000000000000000"

// An event that cannot become a CODECO is an error row; other moves still send.
func TestBadEventDoesNotBlockOthers(t *testing.T) {
	o := newOutFx(t)
	o.exec(`INSERT INTO events (id, company_id, stream, kind, actor, payload, occurred_at)
		VALUES ('ev-ghost', 'A', 'no-such-move', 'move.gate_saved', 'clerk', '{}', now())`)
	bad := o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	o.exec(`UPDATE edi_messages SET blob_digest = $1 WHERE move_id = $2`, lostFile, bad) // the file it sent is lost
	f := form(bad, "in", "MSCU1234566", "E", "lA")
	f.Seal = "S9"
	o.correct(bad, f)
	good := o.gateIn(cn("MSCU765432"), "E", "lA")
	o.tick()
	o.tick() // a rescan adds nothing
	var errs []Message
	for _, m := range o.messages("out") {
		if m.Status == "error" {
			errs = append(errs, m)
		}
	}
	if len(errs) != 2 || !slices.Equal(append(codes(errs[0].Problems), codes(errs[1].Problems)...), []string{"E-DATA", "E-DATA"}) {
		t.Fatalf("error rows = %+v", errs)
	}
	sent := o.sent()
	if len(sent) != 2 || !strings.HasSuffix(sent[1].Subject, cn("MSCU765432")) {
		t.Fatalf("sent = %+v", sent)
	}
	if out := o.messages("out"); out[len(out)-1].MoveID != good || out[len(out)-1].Status != "sent" {
		t.Fatalf("out = %+v", out)
	}
}

// A due message whose file is missing fails; the company's other messages still go.
func TestMissingBlobFailsRowNotCompany(t *testing.T) {
	o := newOutFx(t)
	m, err := o.s.SendTest(ctxOf("A"), "lA", "u1")
	if err != nil {
		t.Fatal(err)
	}
	o.exec(`UPDATE edi_messages SET blob_digest = $1 WHERE id = $2`, lostFile, m.ID)
	o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	if got, _ := o.s.Message(ctxOf("A"), m.ID); got.Status != "failed" || !slices.Equal(codes(got.Problems), []string{"E-SEND"}) ||
		!strings.Contains(got.Problems[0].Text, "file missing") {
		t.Fatalf("test message = %+v", got)
	}
	if sent := o.sent(); len(sent) != 1 || !strings.HasPrefix(sent[0].Subject, "CODECO ") {
		t.Fatalf("sent = %+v", sent)
	}
}

// A stored inbound file that still cannot be processed becomes an error, not a retry forever.
func TestStuckInboundThatFailsBecomesError(t *testing.T) {
	o := newOutFx(t)
	m, err := o.s.store(ctxOf("A"), "f.edi", file(t, "coparn_release.edi"), "u1", "upload", false)
	if err != nil {
		t.Fatal(err)
	}
	o.exec(`UPDATE edi_messages SET blob_digest = $1 WHERE id = $2`, lostFile, m.ID)
	o.at = o.at.Add(2 * time.Minute)
	o.tick()
	if got, _ := o.s.Message(ctxOf("A"), m.ID); got.Status != "error" || len(got.Problems) != 1 || got.Problems[0].Text == "" {
		t.Fatalf("message = %+v", got)
	}
}

// A replacement goes only to a line still on EDI.
func TestReplacementNeedsLineOnEDI(t *testing.T) {
	o := newOutFx(t)
	id := o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	o.exec(`UPDATE line_edi SET mode = 'off' WHERE line_id = 'lA'`)
	f := form(id, "in", "MSCU1234566", "E", "lA")
	f.Seal = "S1"
	o.correct(id, f)
	o.tick()
	if len(o.sent()) != 1 || len(o.messages("out")) != 1 {
		t.Fatalf("out = %+v", o.messages("out"))
	}
}

// A failed CODECO to the old line does not hold back the new line's original.
func TestOldLineFailureDoesNotHoldNewLine(t *testing.T) {
	o := newOutFx(t)
	o.line(Profile{SenderID: "MSC", RecipientID: "MSC"}) // no address: lA's CODECOs fail
	id := o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	o.correct(id, form(id, "in", "MSCU1234566", "E", "lX"))
	o.tick()
	if sent := o.sent(); len(sent) != 1 || sent[0].To[0] != otherTo || fn(t, sent[0]) != "9" {
		t.Fatalf("sent = %+v", sent)
	}
}

// An empties-only line's container corrected to full no longer qualifies:
// the line gets a cancel, not a replacement carrying a full container.
func TestEmptiesOnlyCorrectedToFullCancels(t *testing.T) {
	o := newOutFx(t)
	o.line(Profile{SenderID: "MSC", RecipientID: "MSC", SendTo: []string{lineTo}, EmptiesOnly: true})
	id := o.gateIn("MSCU1234566", "E", "lA")
	o.tick()
	o.correct(id, form(id, "in", "MSCU1234566", "F", "lA"))
	o.tick()
	sent := o.sent()
	if len(sent) != 2 || fn(t, sent[1]) != "1 "+id || !slices.Equal(facts(t, sent[1]), facts(t, sent[0])) {
		t.Fatalf("sent = %+v", sent)
	}
	o.correct(id, form(id, "in", "MSCU1234566", "E", "lA")) // back to empty: an original again
	o.tick()
	if sent := o.sent(); len(sent) != 3 || fn(t, sent[2]) != "9" {
		t.Fatalf("sent = %+v", sent)
	}
}
