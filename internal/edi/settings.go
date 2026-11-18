package edi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	edidb "depot/internal/edi/db"
	"depot/internal/tenancy"
)

// The API's side of EDI: listing, settings and status.

// Invalid is a value the service refuses; its text names the field and why.
type Invalid string

func (e Invalid) Error() string { return string(e) }

// ErrNotOnEDI: a test CODECO goes only to a line on EDI.
var ErrNotOnEDI = errors.New("the line is not on EDI")

var (
	// unoaSafe is what our party id and depot locations may hold: UNOA
	// level A without the characters EDIFACT escapes.
	unoaSafe = regexp.MustCompile(`^[A-Z0-9 .,\-()/=]+$`)
	hhmm     = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// Filter selects messages; an empty field matches every message.
type Filter struct {
	Direction, Kind, Status, LineID string
	From, To                        *time.Time // From inclusive, To exclusive
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListMessages returns a page of the company's messages, newest first, and how many match.
func (s *Service) ListMessages(ctx context.Context, f Filter, limit, offset int) (out []Message, total int, err error) {
	err = s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		p := edidb.ListMessagesParams{CompanyID: sc.CompanyID, Direction: opt(f.Direction), Kind: opt(f.Kind), Status: opt(f.Status),
			LineID: opt(f.LineID), Since: f.From, Until: f.To, Lim: int32(limit), Off: int32(offset)}
		n, err := q.CountMessages(ctx, edidb.CountMessagesParams{CompanyID: p.CompanyID, Direction: p.Direction, Kind: p.Kind,
			Status: p.Status, LineID: p.LineID, Since: p.Since, Until: p.Until})
		if err != nil {
			return err
		}
		rows, err := q.ListMessages(ctx, p)
		for _, r := range rows {
			m, err := messageOf(r)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		total = int(n)
		return err
	})
	return out, total, err
}

// Raw returns a message and its stored file.
func (s *Service) Raw(ctx context.Context, id string) (Message, []byte, error) {
	m, err := s.Message(ctx, id)
	if err != nil {
		return Message{}, nil, err
	}
	if m.BlobDigest == "" { // an outbound message that could not be built has no file
		return Message{}, nil, ErrNotFound
	}
	data, err := s.blob.Get(m.BlobDigest)
	return m, data, err
}

// Lines returns every line of the company with its EDI settings; a line without any is "off".
func (s *Service) Lines(ctx context.Context) (out []LineEDI, err error) {
	err = s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		rows, err := q.AllLines(ctx, sc.CompanyID)
		for _, r := range rows {
			l, err := lineOf(r.LineID, r.Mode, r.Code, r.Profile)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return err
	})
	return out, err
}

// checkAddresses trims each address in place and refuses any that is not a bare e-mail address.
func checkAddresses(field string, list []string) error {
	for i, a := range list {
		a = strings.TrimSpace(a)
		if p, err := mail.ParseAddress(a); err != nil || p.Name != "" || p.Address != a {
			return Invalid(fmt.Sprintf("%s: %q is not an e-mail address", field, a))
		}
		list[i] = a
	}
	return nil
}

// SetLine saves a line's EDI settings after checking them (Invalid says why not).
func (s *Service) SetLine(ctx context.Context, l LineEDI) (LineEDI, error) {
	p := &l.Profile
	for _, f := range []*string{&p.SenderID, &p.SenderQual, &p.RecipientID, &p.RecipientQual, &p.LineCode, &p.ReportTime} {
		*f = strings.TrimSpace(*f)
	}
	if p.ReportTime == "" {
		p.ReportTime = "06:00"
	}
	switch {
	case !slices.Contains([]string{"edi", "report", "off"}, l.Mode):
		return LineEDI{}, Invalid("mode: one of edi, report, off")
	case !hhmm.MatchString(p.ReportTime):
		return LineEDI{}, Invalid("reportTime: use HH:MM, from 00:00 to 23:59")
	case l.Mode == "edi" && p.RecipientID == "":
		return LineEDI{}, Invalid("recipientId: a line on EDI needs the id we send its files to")
	}
	for _, f := range []struct {
		name string
		list []string
	}{{"sendTo", p.SendTo}, {"allowedFrom", p.AllowedFrom}, {"reportTo", p.ReportTo}} {
		if err := checkAddresses(f.name, f.list); err != nil {
			return LineEDI{}, err
		}
	}
	for i, m := range p.TypeMap {
		p.TypeMap[i] = [2]string{strings.ToUpper(strings.TrimSpace(m[0])), strings.ToUpper(strings.TrimSpace(m[1]))}
		if p.TypeMap[i][0] == "" || p.TypeMap[i][1] == "" {
			return LineEDI{}, Invalid("typeMap: every pair needs both type codes")
		}
	}
	if l.Mode == "edi" {
		ce, err := s.st.CompanyEDI(ctx)
		if err != nil {
			return LineEDI{}, err
		}
		if ce.PartyID == "" {
			return LineEDI{}, Invalid("set the company's EDI party id before a line goes on EDI")
		}
	}
	profile, err := json.Marshal(l.Profile)
	if err != nil {
		return LineEDI{}, err
	}
	err = s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		rows, err := q.AllLines(ctx, sc.CompanyID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(rows, func(r edidb.AllLinesRow) bool { return r.LineID == l.LineID })
		if i < 0 {
			return ErrNotFound
		}
		l.Code = rows[i].Code
		for _, r := range rows {
			if l.Mode != "edi" || p.SenderID == "" || r.LineID == l.LineID || r.Mode != "edi" {
				continue
			}
			o, err := lineOf(r.LineID, r.Mode, r.Code, r.Profile)
			if err != nil {
				return err
			}
			if strings.EqualFold(o.Profile.SenderID, p.SenderID) { // inbound files are matched to a line by it
				return Invalid(fmt.Sprintf("senderId: line %s on EDI already uses %q", r.Code, p.SenderID))
			}
		}
		return q.UpsertLineEDI(ctx, edidb.UpsertLineEDIParams{CompanyID: sc.CompanyID, LineID: l.LineID, Mode: l.Mode, Profile: profile})
	})
	return l, err
}

// Status is the EDI screen's header.
type Status struct {
	Mail, InboundAddress, PartyID string
	Problems                      int
}

// Status reports the mail driver, the company's inbound address and party
// id, and how many messages of the last 30 days need a look.
func (s *Service) Status(ctx context.Context) (Status, error) {
	ce, err := s.st.CompanyEDI(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{Mail: s.mail.Name(), PartyID: ce.PartyID}
	if s.cfg.InboundDomain != "" {
		st.InboundAddress = "edi-" + ce.InboundToken + "@" + s.cfg.InboundDomain
	}
	err = s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		n, err := q.CountProblems(ctx, edidb.CountProblemsParams{CompanyID: sc.CompanyID, Since: s.now().AddDate(0, 0, -30)})
		st.Problems = int(n)
		return err
	})
	return st, err
}

// unoa upper-cases v and checks it is at most max UNOA-safe characters ("" allowed).
func unoa(field, v string, max int) (string, error) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if len(v) > max || (v != "" && !unoaSafe.MatchString(v)) {
		return "", Invalid(fmt.Sprintf("%s: at most %d of A-Z 0-9 space . , - ( ) / =", field, max))
	}
	return v, nil
}

// SetPartyID sets our EDI party id, the UNB sender of what we send.
func (s *Service) SetPartyID(ctx context.Context, id string) error {
	id, err := unoa("partyId", id, 35)
	if err != nil {
		return err
	}
	if id == "" {
		return Invalid("partyId: required")
	}
	if _, err := s.st.CompanyEDI(ctx); err != nil {
		return err
	}
	return s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		return one(q.SetPartyID(ctx, edidb.SetPartyIDParams{CompanyID: sc.CompanyID, PartyID: id}))
	})
}

// SetDepotLocation sets a depot's EDI location code ("" clears it).
func (s *Service) SetDepotLocation(ctx context.Context, depotID, location string) error {
	location, err := unoa("location", location, 25)
	if err != nil {
		return err
	}
	return s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		return one(q.SetDepotLocation(ctx, edidb.SetDepotLocationParams{CompanyID: sc.CompanyID, ID: depotID, EdiLocation: location}))
	})
}
