package edi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	edidb "depot/internal/edi/db"
	"depot/internal/edifact"
	"depot/internal/mail"
	"depot/internal/tenancy"
)

const bom = "\xef\xbb\xbf"

// reports sends the daily gate-move CSV (spec 5.8) of every line without EDI
// (mode report, with an address) for every depot of the company: the previous
// full depot-local day, once the depot's clock is past the line's report time.
// One line or depot failing does not stop the others.
func (s *Service) reports(ctx context.Context, sc tenancy.Scope) error {
	ls, err := s.st.lines(ctx)
	if err != nil {
		return err
	}
	var depots []edidb.DepotZonesRow
	if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) (err error) {
		depots, err = q.DepotZones(ctx, sc.CompanyID)
		return err
	}); err != nil {
		return err
	}
	var errs []error
	for _, l := range ls {
		if l.Mode != "report" || len(l.Profile.ReportTo) == 0 {
			continue
		}
		for _, d := range depots {
			dsc := sc
			dsc.DepotID = d.ID
			if err := s.reportOne(tenancy.With(ctx, dsc), l, d); err != nil {
				log.Printf("edi: report of line %s at depot %s: %v", l.Code, d.ID, err)
				errs = append(errs, fmt.Errorf("report %s at %s: %w", l.Code, d.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Service) reportOne(ctx context.Context, l LineEDI, d edidb.DepotZonesRow) error {
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := s.now().In(loc)
	hh, mm, ok := strings.Cut(l.Profile.ReportTime, ":")
	var h, m int
	if _, err := fmt.Sscanf(hh+" "+mm, "%d %d", &h, &m); !ok || err != nil {
		h, m = 6, 0
	}
	if now.Before(time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)) {
		return nil
	}
	day := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, time.UTC) // a calendar date
	var done bool
	if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) (err error) {
		done, err = q.ReportDone(ctx, edidb.ReportDoneParams{CompanyID: sc.CompanyID, LineID: &l.LineID, DepotID: &d.ID, ReportDay: &day})
		return err
	}); err != nil || done {
		return err
	}
	// shortcut: a depot with over 10000 moves of all lines in a day loses the oldest from the report; raise when a depot nears it.
	log10k, err := s.yard.GateLog(ctx, &day, &day, 10000)
	if err != nil {
		return err
	}
	code := strings.ToUpper(strings.TrimSpace(l.Code))
	var moves [][]string
	for i := len(log10k) - 1; i >= 0; i-- { // oldest first
		r := log10k[i]
		if r.Voided || strings.ToUpper(strings.TrimSpace(r.LineCode)) != code {
			continue
		}
		moves = append(moves, []string{r.At.In(loc).Format("2006-01-02 15:04"), r.Direction, r.Number, r.TypeCode, r.FullEmpty,
			r.TruckPlate, r.HaulierCode, r.Seal, r.Condition})
	}
	date := day.Format("2006-01-02")
	row := edidb.InsertReportParams{ID: newID(), Status: "ignored", LineID: &l.LineID, DepotID: &d.ID, ReportDay: &day}
	var data []byte
	if len(moves) > 0 {
		var b bytes.Buffer
		b.WriteString(bom)
		w := csv.NewWriter(&b)
		w.Comma, w.UseCRLF = ';', true
		_ = w.Write([]string{"Time", "Direction", "Container", "Type", "Full/Empty", "Truck", "Haulier", "Seal", "Condition"})
		_ = w.WriteAll(moves)
		data = b.Bytes()
		if row.BlobDigest, err = s.blob.Put(data); err != nil {
			return err
		}
		row.Status, row.FileName = "pending", fmt.Sprintf("gate-moves-%s-%s-%s.csv", code, d.ID, date)
	}
	var first bool
	if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		row.CompanyID = sc.CompanyID
		n, err := q.InsertReport(ctx, row)
		first = n == 1
		return err
	}); err != nil || !first || len(moves) == 0 {
		return err
	}
	subject := fmt.Sprintf("%s gate moves %s %s", code, d.Name, date)
	sendErr := s.mail.Send(ctx, mail.Outgoing{To: l.Profile.ReportTo, Subject: subject, Text: subject,
		Attachments: []mail.Attachment{{Name: row.FileName, ContentType: "text/csv; charset=utf-8", Data: data}}})
	res := edidb.SetReportResultParams{ID: row.ID, Status: "sent", SentAt: ptr(s.now()), Problems: problemsJSON(nil)}
	if sendErr != nil {
		res.Status, res.SentAt = "failed", nil
		res.Problems = problemsJSON([]edifact.Problem{{Code: "E-SEND", Text: sendErr.Error()}})
	}
	if err := s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		res.CompanyID = sc.CompanyID
		return q.SetReportResult(ctx, res)
	}); err != nil {
		return err
	}
	if sendErr != nil {
		log.Printf("edi: report %s not sent: %v", row.ID, sendErr) // recorded as failed; not retried
	}
	return nil
}
