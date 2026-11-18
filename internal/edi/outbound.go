package edi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	edidb "depot/internal/edi/db"
	"depot/internal/edifact"
	"depot/internal/iso6346"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
)

// ErrNotResendable: only an outbound message with a built file is sent again.
var ErrNotResendable = errors.New("only an outbound message with a file can be sent again")

// errQueued rolls back an event whose CODECO another pass queued first.
var errQueued = errors.New("already queued")

// rescan is how far before the last scan events are read again: an event
// committed late by a slow gate transaction is still seen.
const rescan = 5 * time.Minute

// queueGateEvents queues the CODECO of the company's gate events since the
// last scan (spec 5.4, 5.8.3). A rescanned event queues nothing twice.
func (s *Service) queueGateEvents(ctx context.Context) error {
	start := s.now()
	ce, err := s.st.CompanyEDI(ctx)
	if err != nil {
		return err
	}
	ls, err := s.st.lines(ctx)
	if err != nil {
		return err
	}
	lines := map[string]LineEDI{}
	for _, l := range ls {
		lines[l.LineID] = l
	}
	var evs []edidb.GateEventsRow
	if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) (err error) {
		evs, err = q.GateEvents(ctx, edidb.GateEventsParams{CompanyID: sc.CompanyID, Since: ce.EventsScannedAt.Add(-rescan)})
		return err
	}); err != nil {
		return err
	}
	for _, ev := range evs {
		if err := s.queueEvent(ctx, ev, lines, ce.PartyID); err != nil && !errors.Is(err, errQueued) {
			return fmt.Errorf("event %s: %w", ev.ID, err) // the scan stays where it was: this event is tried again
		}
	}
	return s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		return q.SetEventsScanned(ctx, edidb.SetEventsScannedParams{CompanyID: sc.CompanyID, At: start})
	})
}

// send is one CODECO to queue: its line and function.
type send struct{ line, function string }

// plan says what an event sends (spec 5.8.3). prev is the line of the move's
// latest CODECO, "" when there is none or it was a cancel; line is the move's
// line now; renamed says the number differs from the one prev sent; eligible
// says the line takes this move's CODECO. A new line or number cancels prev.
func plan(kind, prev, line string, renamed, eligible bool, onEDI func(string) bool) []send {
	var out []send
	changed := prev != line || renamed
	if prev != "" && (kind == "move.voided" || (kind == "move.corrected" && changed)) && onEDI(prev) {
		out = append(out, send{prev, "1"})
	}
	switch {
	case kind == "move.corrected" && prev != "" && !changed && onEDI(line): // off EDI: eligible is false too, so nothing
		out = append(out, send{line, "5"})
	case kind != "move.voided" && eligible:
		out = append(out, send{line, "9"})
	}
	return out
}

// queueEvent queues the CODECO of one event in one transaction.
func (s *Service) queueEvent(ctx context.Context, ev edidb.GateEventsRow, lines map[string]LineEDI, party string) error {
	return s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		if done, err := q.EventQueued(ctx, edidb.EventQueuedParams{CompanyID: sc.CompanyID, EventID: &ev.ID}); err != nil || done {
			return err
		}
		// An event that can never become a CODECO is an error row: the rescan
		// skips it and the company's later events still go.
		bad := edidb.InsertMessageParams{CompanyID: sc.CompanyID, ID: newID(), Direction: "out", Kind: "codeco", EventID: &ev.ID, Via: "upload"}
		mv, err := q.CodecoMove(ctx, edidb.CodecoMoveParams{CompanyID: sc.CompanyID, ID: ev.Stream})
		if errors.Is(err, pgx.ErrNoRows) {
			return dataError(ctx, q, bad, fmt.Sprintf("event %s names no move %s", ev.ID, ev.Stream))
		}
		if err != nil {
			return err
		}
		bad.MoveID, bad.DepotID = &mv.ID, &mv.DepotID
		prev, sent := "", edifact.Message{} // prev's line and the CODECO it sent
		last, err := q.LastCodeco(ctx, edidb.LastCodecoParams{CompanyID: sc.CompanyID, MoveID: &mv.ID})
		switch {
		case err == nil && last.Function != "1":
			prev = str(last.LineID)
			if sent, err = s.sentCODECO(last.BlobDigest); err != nil {
				bad.LineID = last.LineID
				return dataError(ctx, q, bad, "the CODECO sent before cannot be read: "+err.Error())
			}
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		line := str(mv.LineID)
		l, ok := lines[line]
		eligible := ok && l.Mode == "edi" && !mv.IsTest && (!l.Profile.EmptiesOnly || mv.FullEmpty == "E")
		renamed := prev != "" && segmentOf(sent, "EQD").Get(1, 0) != edifact.Text(mv.Number)
		sends := plan(ev.Kind, prev, line, renamed, eligible, func(id string) bool { return lines[id].Mode == "edi" })
		if len(sends) == 0 {
			return nil
		}
		ds, err := q.MoveDamages(ctx, edidb.MoveDamagesParams{CompanyID: sc.CompanyID, MoveID: mv.ID})
		if err != nil {
			return err
		}
		var dmg []edifact.Damage
		for _, d := range ds {
			dmg = append(dmg, edifact.Damage{Location: d.Location, Description: d.Description})
		}
		for _, sd := range sends { // plan puts a cancel first: its ULID, made first, sends it first
			l := lines[sd.line]
			row := edidb.InsertMessageParams{CompanyID: sc.CompanyID, ID: newID(), Direction: "out", Kind: "codeco",
				LineID: &l.LineID, DepotID: &mv.DepotID, MoveID: &mv.ID, EventID: &ev.ID, Function: sd.function, Via: "upload"}
			if sd.function == "1" { // a cancel repeats the facts it cancels, not today's
				err = s.queue(ctx, q, row, l.Profile, party, l.Profile.Test, func(ref string) edifact.Message { return edifact.CancelCODECO(ref, sent) })
			} else if g, gerr := gateMove(mv, l, sd.function, dmg); gerr != nil {
				err = dataError(ctx, q, row, gerr.Error())
			} else {
				err = s.queue(ctx, q, row, l.Profile, party, l.Profile.Test, func(ref string) edifact.Message { return edifact.CODECO(ref, g) })
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// sentCODECO reads the CODECO a message sent from its stored file.
func (s *Service) sentCODECO(digest string) (edifact.Message, error) {
	data, err := s.blob.Get(digest)
	if err != nil {
		return edifact.Message{}, err
	}
	ic, err := edifact.Parse(data)
	if err == nil && len(ic.Messages) != 1 {
		err = fmt.Errorf("stored CODECO %s holds %d messages", digest, len(ic.Messages))
	}
	if err != nil {
		return edifact.Message{}, err
	}
	return ic.Messages[0], nil
}

func segmentOf(m edifact.Message, tag string) edifact.Segment {
	for _, s := range m.Segments {
		if s.Tag == tag {
			return s
		}
	}
	return edifact.Segment{}
}

// dataError stores row as an error for the screen: it is never sent.
func dataError(ctx context.Context, q *edidb.Queries, row edidb.InsertMessageParams, text string) error {
	row.Status, row.Problems = "error", problemsJSON([]edifact.Problem{{Code: "E-DATA", Text: text}})
	return inserted(q.InsertMessage(ctx, row))
}

func inserted(n int64, err error) error {
	if err == nil && n == 0 {
		return errQueued
	}
	return err
}

// queue builds the interchange of one message from the company's party id
// to the line, stores it and inserts row as pending, due now. Without our
// party id no valid interchange can be built: the row is an error.
func (s *Service) queue(ctx context.Context, q *edidb.Queries, row edidb.InsertMessageParams, p Profile, party string, test bool, build func(ref string) edifact.Message) error {
	if strings.TrimSpace(party) == "" {
		return dataError(ctx, q, row, "our EDI party id is not set")
	}
	ref, err := s.st.NextRef(ctx, q)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	data := edifact.Interchange{
		Sender: edifact.Text(party), Recipient: edifact.Text(p.RecipientID), RecipientQual: edifact.Text(p.RecipientQual),
		ControlRef: ref, Prepared: now, Test: test, Messages: []edifact.Message{build(ref)},
	}.Encode()
	digest, err := s.blob.Put(data)
	if err != nil {
		return err
	}
	row.Status, row.ControlRef, row.MessageRef, row.ContentHash, row.BlobDigest = "pending", ref, ref, digest, digest
	row.FileName, row.Problems, row.NextAttemptAt = row.Kind+"-"+ref+".edi", problemsJSON(nil), &now
	return inserted(q.InsertMessage(ctx, row))
}

// gateMove is the CODECO content of move mv for line l, cut to the sizes
// of the SMDG guide. Data no CODECO can carry is an error.
func gateMove(mv edidb.CodecoMoveRow, l LineEDI, function string, dmg []edifact.Damage) (edifact.GateMove, error) {
	code := strings.TrimSpace(cmp.Or(l.Profile.LineCode, l.Code)) // NAD is mandatory (CODECO p.24)
	switch {
	case mv.Direction != "in" && mv.Direction != "out":
		return edifact.GateMove{}, fmt.Errorf("direction %q is not in or out", mv.Direction)
	case mv.FullEmpty != "E" && mv.FullEmpty != "F":
		return edifact.GateMove{}, fmt.Errorf("full/empty %q is not E or F", mv.FullEmpty)
	case code == "":
		return edifact.GateMove{}, errors.New("the line has no code")
	}
	loc, err := time.LoadLocation(mv.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return edifact.GateMove{
		Function: function, Direction: mv.Direction, DocNumber: cut(mv.ID, 35), Number: mv.Number,
		TypeCode: l.Profile.theirs(mv.TypeCode), FullEmpty: mv.FullEmpty, Seal: mv.Seal,
		TruckPlate:  cut(strings.ReplaceAll(edifact.Text(mv.TruckPlate), " ", ""), 9),
		HaulierCode: cut(edifact.Text(mv.HaulierCode), 17), Condition: mv.Condition, OrderRef: mv.OrderRef, LineCode: code,
		Location: cut(edifact.Text(mv.EdiLocation), 25), At: mv.GateAt.In(loc), Damages: dmg,
	}, nil
}

// cut shortens UNOA (ASCII) text to n characters.
func cut(s string, n int) string { return s[:min(len(s), n)] }

// Resend sends an outbound message again: it becomes pending with no tries.
func (s *Service) Resend(ctx context.Context, id, actor string) error {
	m, err := s.Message(ctx, id)
	if err != nil {
		return err
	}
	if m.Direction != "out" || m.BlobDigest == "" {
		return ErrNotResendable
	}
	if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		return one(q.Resend(ctx, edidb.ResendParams{CompanyID: sc.CompanyID, ID: id, Now: ptr(s.now())}))
	}); err != nil {
		return err
	}
	log.Printf("edi: %s resent message %s", actor, id)
	return nil
}

func ptr[T any](v T) *T { return &v }

// testNumber is the made-up container of a test CODECO.
var testNumber = "TESU123456" + strconv.Itoa(iso6346.CheckDigit("TESU123456"))

// SendTest queues a test CODECO to a line on EDI (spec 5.8.7): a made-up
// gate-in with the UNB test flag, linked to no move.
func (s *Service) SendTest(ctx context.Context, lineID, actor string) (Message, error) {
	ce, err := s.st.CompanyEDI(ctx)
	if err != nil {
		return Message{}, err
	}
	ls, err := s.st.lines(ctx)
	if err != nil {
		return Message{}, err
	}
	i := slices.IndexFunc(ls, func(l LineEDI) bool { return l.LineID == lineID })
	if i < 0 {
		return Message{}, ErrNotFound
	}
	l, id := ls[i], newID()
	g, err := gateMove(edidb.CodecoMoveRow{ID: id, Direction: "in", Number: testNumber, TypeCode: "22G1", FullEmpty: "E",
		Condition: "sound", GateAt: s.now(), Timezone: "UTC"}, l, "9", nil)
	if err != nil {
		return Message{}, err
	}
	if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		row := edidb.InsertMessageParams{CompanyID: sc.CompanyID, ID: id, Direction: "out", Kind: "test", LineID: &l.LineID, Function: "9", Via: "upload"}
		return s.queue(ctx, q, row, l.Profile, ce.PartyID, true, func(ref string) edifact.Message { return edifact.CODECO(ref, g) })
	}); err != nil {
		return Message{}, err
	}
	log.Printf("edi: %s queued test CODECO %s to line %s", actor, id, lineID)
	return s.Message(ctx, id)
}
