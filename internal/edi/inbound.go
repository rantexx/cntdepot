package edi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	edidb "depot/internal/edi/db"
	"depot/internal/edifact"
	"depot/internal/mail"
	"depot/internal/tenancy"
	"depot/internal/yard"
)

// mailActor is the actor of what a mailed file does.
const mailActor = "edi"

// how is how a file is handled: the office's line and depot (or "" to find
// them), whether it came by mail (and from whom, authenticated or not), and
// who acts.
type how struct {
	lineID, depotID string
	mail, trusted   bool
	from, actor     string
}

// ReceiveMail stores and processes each attachment of a mail to a company's
// edi-<token>@ address. It needs no scope. Mail to no known address is
// dropped with a log line. It fails only when a file could not be stored;
// a file stored but not processed stays "received" for the worker.
func (s *Service) ReceiveMail(ctx context.Context, in mail.Inbound) error {
	company, err := s.inboundCompany(ctx, in.To)
	if err != nil || company == "" {
		if company == "" && err == nil {
			log.Printf("edi: mail from %s to %v: no company has that address, dropped", in.From, in.To)
		}
		return err
	}
	ctx = tenancy.With(ctx, tenancy.Scope{CompanyID: company, UserID: mailActor})
	if len(in.Attachments) == 0 {
		log.Printf("edi: mail from %s to company %s has no attachment, dropped", in.From, company)
	}
	for _, a := range in.Attachments {
		id, digest, err := s.store(ctx, a.Name, a.Data, in.From)
		if err != nil {
			return err
		}
		if _, err := s.process(ctx, id, digest, a.Data, how{mail: true, trusted: in.Trusted, from: in.From, actor: mailActor}); err != nil {
			log.Printf("edi: company %s message %s left received: %v", company, id, err)
		}
	}
	return nil
}

// inboundCompany finds the company of the first edi-<token>@ recipient.
func (s *Service) inboundCompany(ctx context.Context, to []string) (string, error) {
	for _, addr := range to {
		local, _, _ := strings.Cut(addr, "@")
		tok, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(local)), "edi-")
		if !ok || tok == "" {
			continue
		}
		c, err := s.st.InboundCompany(ctx, tok)
		if !errors.Is(err, ErrNotFound) {
			return c, err
		}
	}
	return "", nil
}

// Upload stores and processes a file a signed-in manager uploaded (scoped).
func (s *Service) Upload(ctx context.Context, name string, data []byte, actor string) ([]Message, error) {
	id, digest, err := s.store(ctx, name, data, actor)
	if err != nil {
		return nil, err
	}
	m, err := s.process(ctx, id, digest, data, how{actor: actor})
	if err != nil {
		return nil, err
	}
	return []Message{m}, nil
}

// Process handles an unmatched or error message again with the line and
// depot the office chose ("" to find them as for an upload).
func (s *Service) Process(ctx context.Context, id, lineID, depotID, actor string) (Message, error) {
	m, err := s.Message(ctx, id)
	if err != nil {
		return Message{}, err
	}
	if m.Direction != "in" || (m.Status != "unmatched" && m.Status != "error") {
		return Message{}, ErrNotProcessable
	}
	data, err := s.blob.Get(m.BlobDigest)
	if err != nil {
		return Message{}, err
	}
	return s.process(ctx, id, m.BlobDigest, data, how{lineID: lineID, depotID: depotID, actor: actor})
}

// store keeps the raw file and commits its "received" row before anything
// reads it, so no file is ever lost. The blob digest is the content hash.
func (s *Service) store(ctx context.Context, name string, data []byte, sender string) (id, digest string, err error) {
	if digest, err = s.blob.Put(data); err != nil {
		return "", "", err
	}
	id = newID()
	return id, digest, s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		return q.InsertMessage(ctx, edidb.InsertMessageParams{
			CompanyID: sc.CompanyID, ID: id, Direction: "in", Kind: "unknown", Status: "received",
			ContentHash: digest, BlobDigest: digest, FileName: name, Sender: sender, Problems: problemsJSON(nil),
		})
	})
}

func hasError(ps []edifact.Problem) bool {
	return slices.ContainsFunc(ps, func(p edifact.Problem) bool { return strings.HasPrefix(p.Code, "E") })
}

func kindOf(ic edifact.Interchange) string {
	kind := ""
	for _, m := range ic.Messages {
		k, _, _ := strings.Cut(m.Type, ":")
		k = strings.ToLower(k)
		if (k != "coparn" && k != "aperak") || (kind != "" && k != kind) {
			return "unknown"
		}
		kind = k
	}
	return cmp.Or(kind, "unknown")
}

// process reads stored file id and applies it: the steps of the inbound
// algorithm (parse, line, repeat, orders or answers, APERAK). It returns an
// error only when it could not finish; the row then stays as it was.
func (s *Service) process(ctx context.Context, id, digest string, data []byte, h how) (Message, error) {
	r := edidb.SetInResultParams{ID: id, Kind: "unknown", Status: "error"}
	var problems []edifact.Problem
	ic, err := edifact.Parse(data)
	if err != nil {
		return s.finish(ctx, r, append(problems, edifact.Problem{Code: "E-PARSE", Text: err.Error()}), ic, nil, Profile{})
	}
	r.ControlRef = ic.ControlRef
	if len(ic.Messages) > 0 {
		r.MessageRef = ic.Messages[0].Ref
	}
	if r.Kind = kindOf(ic); r.Kind == "unknown" {
		return s.finish(ctx, r, append(problems, edifact.Problem{Code: "E-TYPE", Text: "the file holds no COPARN or APERAK message, or both"}), ic, nil, Profile{})
	}
	line, ok, err := s.findLine(ctx, ic.Sender, h)
	if err != nil {
		return Message{}, err
	}
	if !ok {
		r.Status = "unmatched"
		return s.finish(ctx, r, nil, ic, nil, Profile{})
	}
	r.LineID = &line.LineID
	if ic.ControlRef != "" {
		var rep edidb.RepeatsOfRow
		if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) (err error) {
			rep, err = q.RepeatsOf(ctx, edidb.RepeatsOfParams{CompanyID: sc.CompanyID, ID: id, LineID: r.LineID, ControlRef: ic.ControlRef, ContentHash: digest})
			return err
		}); err != nil {
			return Message{}, err
		}
		if rep.Same > 0 {
			r.Status = "ignored"
			return s.finish(ctx, r, nil, ic, nil, Profile{})
		}
		if rep.Other > 0 {
			problems = append(problems, edifact.Problem{Code: "W-REPREF", Text: fmt.Sprintf("control reference %s was received before with other content", ic.ControlRef)})
		}
	}
	if r.Kind == "aperak" {
		matched, err := s.answered(ctx, line.LineID, ic)
		if err != nil {
			return Message{}, err
		}
		r.Status = "unmatched"
		if matched {
			r.Status = "done"
		}
		return s.finish(ctx, r, problems, ic, nil, line.Profile)
	}
	var refs []string
	for _, m := range ic.Messages {
		a, err := s.order(ctx, id, line, m, h)
		if err != nil {
			return Message{}, err
		}
		for _, p := range a.problems {
			if len(ic.Messages) > 1 {
				p.Text = "message " + m.Ref + ": " + p.Text
			}
			problems = append(problems, p)
		}
		if a.orderID != "" {
			r.OrderID, r.DepotID = &a.orderID, &a.depotID
		}
		r.Function = a.function
		refs = append(refs, m.Ref)
	}
	if !hasError(problems) {
		r.Status = "done"
	}
	var answer *edifact.Answer
	if line.Profile.SendAPERAK {
		answer = &edifact.Answer{Accepted: !hasError(problems), RefersTo: refs, Problems: problems}
	}
	return s.finish(ctx, r, problems, ic, answer, line.Profile)
}

// finish writes the row's result and, with an answer, queues our APERAK in
// the same transaction. The APERAK goes back to the party that sent the
// file, from the id it was addressed to.
func (s *Service) finish(ctx context.Context, r edidb.SetInResultParams, problems []edifact.Problem, ic edifact.Interchange, answer *edifact.Answer, p Profile) (Message, error) {
	r.Problems = problemsJSON(problems)
	if answer != nil {
		if _, err := s.st.CompanyEDI(ctx); err != nil { // NextRef needs the company's row
			return Message{}, err
		}
	}
	err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		r.CompanyID = sc.CompanyID
		if err := one(q.SetInResult(ctx, r)); err != nil || answer == nil {
			return err
		}
		ref, err := s.st.NextRef(ctx, q)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		data := edifact.Interchange{
			// The answer mirrors the interchange it answers (usual EDI practice), not party_id/RecipientID.
			Sender: edifact.Text(ic.Recipient), SenderQual: edifact.Text(ic.RecipientQual),
			Recipient: edifact.Text(ic.Sender), RecipientQual: edifact.Text(ic.SenderQual),
			ControlRef: ref, Prepared: now, Test: p.Test, Messages: []edifact.Message{edifact.APERAK(ref, *answer)},
		}.Encode()
		digest, err := s.blob.Put(data)
		if err != nil {
			return err
		}
		return q.InsertMessage(ctx, edidb.InsertMessageParams{
			CompanyID: sc.CompanyID, ID: newID(), Direction: "out", Kind: "aperak", Status: "pending",
			LineID: r.LineID, DepotID: r.DepotID, AnswerOf: &r.ID, ControlRef: ref, MessageRef: ref,
			ContentHash: digest, BlobDigest: digest, FileName: "aperak-" + ref + ".edi", Problems: problemsJSON(nil), NextAttemptAt: &now,
		})
	})
	if err != nil {
		return Message{}, err
	}
	return s.Message(ctx, r.ID)
}

func one(n int64, err error) error {
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// findLine finds the line of a file: the office's choice, else the line on
// EDI whose profile SenderID is the UNB sender. Mail must also come from an
// allowed address, authenticated (spec 5.8 rule 5).
func (s *Service) findLine(ctx context.Context, sender string, h how) (LineEDI, bool, error) {
	lines, err := s.st.lines(ctx)
	if err != nil {
		return LineEDI{}, false, err
	}
	for _, l := range lines {
		switch {
		case h.lineID != "":
			if l.LineID == h.lineID {
				return l, true, nil
			}
		case l.Mode != "edi" || l.Profile.SenderID == "" || !strings.EqualFold(l.Profile.SenderID, sender):
		case h.mail && !(h.trusted && slices.ContainsFunc(l.Profile.AllowedFrom, func(a string) bool { return strings.EqualFold(a, h.from) })):
		default:
			return l, true, nil
		}
	}
	if h.lineID != "" {
		return LineEDI{LineID: h.lineID}, true, nil // a line without EDI settings
	}
	return LineEDI{}, false, nil
}

// answered applies an APERAK's answers to our messages to the line and says
// whether any matched.
func (s *Service) answered(ctx context.Context, lineID string, ic edifact.Interchange) (matched bool, err error) {
	err = s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		for _, m := range ic.Messages {
			a := edifact.ReadAPERAK(m)
			refs := slices.DeleteFunc(a.RefersTo, func(r string) bool { return r == "" })
			if len(refs) == 0 {
				continue
			}
			status := "rejected"
			if a.Accepted {
				status = "accepted"
			}
			ids, err := q.MarkAnswered(ctx, edidb.MarkAnsweredParams{CompanyID: sc.CompanyID, LineID: &lineID, Refs: refs, Status: status, Problems: problemsJSON(a.Problems)})
			if err != nil {
				return err
			}
			matched = matched || len(ids) > 0
		}
		return nil
	})
	return matched, err
}

// applied is what one COPARN message did.
type applied struct {
	orderID, depotID, function string
	problems                   []edifact.Problem
}

// order applies one COPARN message to the line's EDI order (spec 5.3, 5.8).
// Yard calls run in the order's depot as the actor.
func (s *Service) order(ctx context.Context, msgID string, line LineEDI, m edifact.Message, h how) (a applied, err error) {
	o, problems := edifact.ReadCOPARN(m)
	a.function, a.problems = o.Function, problems
	if hasError(problems) {
		return a, nil
	}
	add := func(code, format string, args ...any) {
		a.problems = append(a.problems, edifact.Problem{Code: code, Text: fmt.Sprintf(format, args...)})
	}
	sc, _ := tenancy.From(ctx)
	in := func(depot string) context.Context {
		return tenancy.With(ctx, tenancy.Scope{CompanyID: sc.CompanyID, DepotID: depot, UserID: h.actor, Role: sc.Role})
	}
	var items []yard.OrderItem
	for _, it := range o.Items {
		oi := yard.OrderItem{FullEmpty: it.FullEmpty, Quantity: int64(it.Quantity)}
		if it.Number != "" {
			oi.Number = &it.Number
		} else {
			tc := line.Profile.ours(it.TypeCode)
			oi.TypeCode = &tc
		}
		items = append(items, oi)
	}
	cur, _, err := s.yard.FindEDIOrder(in(""), line.LineID, o.Kind, o.Reference)
	switch {
	case errors.Is(err, yard.ErrNotFound):
		cur, err = yard.Order{}, nil
	case err != nil:
		return a, err
	default:
		a.orderID, a.depotID = cur.ID, cur.DepotID
	}
	ds, err := s.depots(ctx)
	if err != nil {
		return a, err
	}
	at := slices.IndexFunc(ds, func(d edidb.DepotLocationsRow) bool {
		return o.Location != "" && strings.EqualFold(d.EdiLocation, o.Location)
	})
	if cur.ID != "" && at >= 0 && ds[at].ID != cur.DepotID { // spec 5.8.1: equipment for another depot
		was := slices.IndexFunc(ds, func(d edidb.DepotLocationsRow) bool { return d.ID == cur.DepotID })
		add("E-DEPOT", "order %s is at depot %s, this file names %s", o.Reference, ds[was].Name, ds[at].Name)
		return a, nil
	}

	if o.Function == "1" {
		switch {
		case cur.ID == "":
			add("E-NOORD", "there is no %s order %s to cancel", o.Kind, o.Reference)
		case cur.Status == "open":
			err = s.yard.CancelOrder(in(cur.DepotID), cur.ID, h.actor)
		case cur.Status == "done":
			add("W-KEPT", "order %s is already used up at the gate, not cancelled", o.Reference)
		} // already cancelled: nothing to do
		return a, err
	}
	if cur.ID != "" { // an original for an existing order is a replacement
		kept, err := s.yard.ReplaceOrder(in(cur.DepotID), cur.ID, yard.Order{ValidUntil: o.ValidUntil, EdiMessageID: &msgID}, items, h.actor)
		if errors.Is(err, yard.ErrOrderClosed) {
			add("E-CLOSED", "order %s is cancelled", o.Reference)
			return a, nil
		}
		for _, k := range kept {
			add("W-KEPT", "%s", k)
		}
		return a, err
	}
	// A new order's depot: the office's choice, else the depot at the
	// order's location, else the company's only depot.
	depot := h.depotID
	switch {
	case depot != "":
	case at >= 0:
		depot = ds[at].ID
	case len(ds) == 1:
		depot = ds[0].ID
	default:
		add("E-DEPOT", "no depot for location %q: set the depot's EDI location, or pick the depot and process again", o.Location)
		return a, nil
	}
	a.depotID = depot
	a.orderID, err = s.yard.CreateOrder(in(depot), yard.Order{
		Kind: o.Kind, Reference: o.Reference, LineID: &line.LineID, ValidUntil: o.ValidUntil, Source: "edi", EdiMessageID: &msgID,
	}, items, h.actor)
	return a, err
}

// depots returns the company's depots with their EDI locations.
func (s *Service) depots(ctx context.Context) (ds []edidb.DepotLocationsRow, err error) {
	err = s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		ds, err = q.DepotLocations(ctx, sc.CompanyID)
		return err
	})
	return ds, err
}
