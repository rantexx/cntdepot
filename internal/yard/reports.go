package yard

import (
	"context"
	"time"

	"depot/internal/tenancy"
	sqlcdb "depot/internal/yard/db"
)

// Reports run under ctx's scope like the Store: one depot, or every depot of
// the company when the scope has none. "Today" is each depot's own calendar day.

// LineCount is the containers in the yard of one line ("" = no line).
type LineCount struct {
	LineID, LineCode string
	Count            int64
}

// Dashboard is the office's front page.
type Dashboard struct {
	InToday, OutToday int64 // saved, not void gate moves today
	InYardByLine      []LineCount
	OpenHolds         int64
	OpenFlags         int64
}

// AgeingBuckets names the buckets of AgeingRow.Buckets, in order.
var AgeingBuckets = []string{"0-7", "8-30", "31-90", "90+"}

// AgeingRow counts the in-yard containers of one line by whole days in the yard.
type AgeingRow struct {
	LineID, LineCode string
	Total            int64
	Buckets          map[string]int64 // keyed by AgeingBuckets
}

// GateLogRow is a saved gate form, with its line, customer and haulier as codes.
type GateLogRow = sqlcdb.GateLogRow

// ageing is the ageing report; at is the moment ages are counted to.
func ageing(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, at time.Time) ([]AgeingRow, error) {
	rows, err := q.AgeingByLine(ctx, sqlcdb.AgeingByLineParams{AtTime: at, CompanyID: sc.CompanyID, DepotID: sc.DepotID})
	out := make([]AgeingRow, len(rows))
	for i, r := range rows {
		out[i] = AgeingRow{LineID: r.LineID, LineCode: r.LineCode, Total: r.D07 + r.D830 + r.D3190 + r.D90Plus,
			Buckets: map[string]int64{"0-7": r.D07, "8-30": r.D830, "31-90": r.D3190, "90+": r.D90Plus}}
	}
	return out, err
}

// Ageing returns the in-yard containers by line and age, lines in code order (no line first).
func (s *Service) Ageing(ctx context.Context) (out []AgeingRow, err error) {
	err = s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) (err error) {
		out, err = ageing(ctx, q, sc, s.st.now())
		return err
	})
	return out, err
}

// Dashboard returns today's gate moves, the yard by line and what is open.
func (s *Service) Dashboard(ctx context.Context) (d Dashboard, err error) {
	err = s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		now := s.st.now()
		c, err := q.GateCountsToday(ctx, sqlcdb.GateCountsTodayParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, AtTime: now})
		if err != nil {
			return err
		}
		d.InToday, d.OutToday = c.InToday, c.OutToday
		rows, err := ageing(ctx, q, sc, now)
		if err != nil {
			return err
		}
		d.InYardByLine = make([]LineCount, len(rows))
		for i, r := range rows {
			d.InYardByLine[i] = LineCount{r.LineID, r.LineCode, r.Total}
		}
		holds, err := q.ListHolds(ctx, sqlcdb.ListHoldsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, OpenOnly: true})
		if err != nil {
			return err
		}
		flags, err := q.ListFlags(ctx, sqlcdb.ListFlagsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, OpenOnly: true})
		d.OpenHolds, d.OpenFlags = int64(len(holds)), int64(len(flags))
		return err
	})
	return d, err
}

// GateLog returns saved gate forms newest first, at most limit. from and to
// (nil: no bound) are depot-local calendar dates, both inclusive; only their
// year, month and day count.
func (s *Service) GateLog(ctx context.Context, from, to *time.Time, limit int) (out []GateLogRow, err error) {
	err = s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = q.GateLog(ctx, sqlcdb.GateLogParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, FromDate: from, ToDate: to, Lim: int64(limit)})
		return err
	})
	return out, err
}

// Codes returns the company's line and customer codes by id, for exports.
func (s *Service) Codes(ctx context.Context) (lines, customers map[string]string, err error) {
	lines, customers = map[string]string{}, map[string]string{}
	err = s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		ls, err := q.LineCodes(ctx, sc.CompanyID)
		if err != nil {
			return err
		}
		for _, l := range ls {
			lines[l.ID] = l.Code
		}
		cs, err := q.CustomerCodes(ctx, sc.CompanyID)
		for _, c := range cs {
			customers[c.ID] = c.Code
		}
		return err
	})
	return lines, customers, err
}

// OrderItemsByOrder returns the items of every order in the scope, by order id.
func (s *Service) OrderItemsByOrder(ctx context.Context) (out map[string][]OrderItem, err error) {
	out = map[string][]OrderItem{}
	err = s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		items, err := q.OrderItemsInScope(ctx, sqlcdb.OrderItemsInScopeParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID})
		for _, it := range items {
			out[it.OrderID] = append(out[it.OrderID], it)
		}
		return err
	})
	return out, err
}
