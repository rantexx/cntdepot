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
	last := now.Day() - 1 // the latest day due: yesterday once past the report time
	if now.Before(time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)) {
		last--
	}
	// The last 3 due days, oldest first: a day missed while the worker was down
	// goes late, once (the unique report row gates each day).
	for back := 2; back >= 0; back-- {
		day := time.Date(now.Year(), now.Month(), last-back, 0, 0, 0, 0, time.UTC) // a calendar date
		if err := s.reportDay(ctx, l, d, loc, day); err != nil {
			return err
		}
	}
	return nil
}

// csvCell keeps a spreadsheet from reading a cell as a formula.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Service) reportDay(ctx context.Context, l LineEDI, d edidb.DepotZonesRow, loc *time.Location, day time.Time) error {
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
		row := []string{r.At.In(loc).Format("2006-01-02 15:04"), r.Direction, r.Number, r.TypeCode, r.FullEmpty,
			r.TruckPlate, r.HaulierCode, r.Seal, r.Condition}
		for i := range row {
			row[i] = csvCell(row[i])
		}
		moves = append(moves, row)
	}
	date := day.Format("2006-01-02")
	row := edidb.InsertReportParams{ID: newID(), Status: "ignored", LineID: &l.LineID, DepotID: &d.ID, ReportDay: &day}
	if len(moves) > 0 {
		var b bytes.Buffer
		b.WriteString(bom)
		w := csv.NewWriter(&b)
		w.Comma, w.UseCRLF = ';', true
		_ = w.Write([]string{"Time", "Direction", "Container", "Type", "Full/Empty", "Truck", "Haulier", "Seal", "Condition"})
		_ = w.WriteAll(moves)
		data := b.Bytes()
		if row.BlobDigest, err = s.blob.Put(data); err != nil {
			return err
		}
		row.Status, row.FileName = "pending", fmt.Sprintf("%s-%s-%s.csv", code, d.ID, date)
	}
	// A pending row is sent, retried and resent by the common sender.
	return s.st.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		row.CompanyID, row.NextAttemptAt = sc.CompanyID, ptr(s.now())
		_, err := q.InsertReport(ctx, row)
		return err
	})
}
