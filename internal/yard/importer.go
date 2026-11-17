package yard

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"depot/internal/iso6346"
	"depot/internal/tenancy"
	sqlcdb "depot/internal/yard/db"

	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/text/encoding/charmap"
)

// Imports read a CSV file (Excel's "CSV" or "CSV UTF-8", ',' or ';' separated)
// and apply it all or nothing in one company transaction.

const (
	MaxImportRows = 20000
	maxRowErrors  = 200
)

var (
	StockColumns = []string{"number", "type_code", "line_code", "customer_code", "full_empty", "in_date", "block", "row", "tier", "grade"}
	OrderColumns = []string{"kind", "reference", "line_code", "customer_code", "valid_until", "number", "type_code", "full_empty", "quantity"}

	ErrImportTooLarge = errors.New("the file has more than 20000 rows")
)

// RowError is one problem in an import file. Row is the 1-based data row
// (the header is not counted); 0 is the header.
type RowError struct {
	Row            int
	Field, Message string
}

// ImportError is a rejected file: nothing was imported. It lists at most 200 problems.
type ImportError struct{ Rows []RowError }

func (e *ImportError) Error() string {
	return fmt.Sprintf("import rejected: %d problem rows, first: row %d %s: %s", len(e.Rows), e.Rows[0].Row, e.Rows[0].Field, e.Rows[0].Message)
}

type problems []RowError

func (p *problems) add(row int, field, msg string) {
	if len(*p) < maxRowErrors {
		*p = append(*p, RowError{row, field, msg})
	}
}

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	slices.SortStableFunc(p, func(a, b RowError) int { return a.Row - b.Row })
	return &ImportError{Rows: p}
}

type record struct {
	n int
	v map[string]string
}

func (r record) get(col string) string { return r.v[col] }

// readTable parses data; blank rows are skipped but counted.
func readTable(data []byte, cols []string) ([]record, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) { // Excel's plain "CSV" in Turkish is Windows-1254
		dec, err := charmap.Windows1254.NewDecoder().Bytes(data)
		if err != nil || bytes.ContainsRune(dec, utf8.RuneError) {
			return nil, &ImportError{Rows: []RowError{{0, "file", "the file's text encoding is not recognised: save it as CSV UTF-8"}}}
		}
		data = dec
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	comma := ','
	if bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
		comma = ';'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.TrimLeadingSpace = comma, -1, true
	head, err := r.Read()
	if err != nil {
		return nil, &ImportError{Rows: []RowError{{0, "header", "the file has no header row"}}}
	}
	idx := map[string]int{}
	for i, h := range head {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	var p problems
	for _, c := range cols {
		if _, ok := idx[c]; !ok {
			p.add(0, c, "missing column")
		}
	}
	if len(p) > 0 {
		return nil, p.err()
	}
	var out []record
	for n := 1; ; n++ {
		cells, err := r.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, &ImportError{Rows: []RowError{{n, "", "malformed CSV: " + err.Error()}}}
		}
		if n > MaxImportRows {
			return nil, ErrImportTooLarge
		}
		rec, blank := record{n: n, v: map[string]string{}}, true
		for _, c := range cols {
			if i := idx[c]; i < len(cells) {
				rec.v[c] = strings.TrimSpace(cells[i])
				blank = blank && rec.v[c] == ""
			}
		}
		if !blank {
			out = append(out, rec)
		}
	}
}

func parseDate(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "2.1.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func tooLong(s string) bool { return utf8.RuneCountInString(s) > 64 }

// code resolves an optional line or customer code to an id.
func code(p *problems, r record, col string, ids map[string]string) *string {
	c := strings.ToUpper(r.get(col))
	if c == "" {
		return nil
	}
	id, ok := ids[c]
	if !ok {
		p.add(r.n, col, "no such code in this company: "+c)
		return nil
	}
	return &id
}

func inverse(rows []sqlcdb.LineCodesRow) map[string]string {
	m := map[string]string{}
	for _, r := range rows {
		m[r.Code] = r.ID
	}
	return m
}

func fullEmpty(p *problems, r record) string {
	switch fe := strings.ToUpper(r.get("full_empty")); fe {
	case "":
		return "E"
	case "E", "F":
		return fe
	}
	p.add(r.n, "full_empty", "E (empty) or F (full)")
	return "E"
}

func (s *Service) codeMaps(ctx context.Context, q *sqlcdb.Queries, company string) (lines, customers map[string]string, err error) {
	ls, err := q.LineCodes(ctx, company)
	if err != nil {
		return nil, nil, err
	}
	cs, err := q.CustomerCodes(ctx, company)
	if err != nil {
		return nil, nil, err
	}
	conv := make([]sqlcdb.LineCodesRow, len(cs))
	for i, c := range cs {
		conv[i] = sqlcdb.LineCodesRow{ID: c.ID, Code: c.Code}
	}
	return inverse(ls), inverse(conv), nil
}

func needDepot(ctx context.Context) error {
	if sc, _ := tenancy.From(ctx); sc.DepotID == "" {
		return tenancy.ErrNoScope
	}
	return nil
}

// ImportStock creates an imported in-yard visit per row at the scope's depot
// (in_at is in_date at 00:00 depot-local) and returns how many. A file with
// any bad row creates nothing and returns an *ImportError; more than
// MaxImportRows rows is ErrImportTooLarge.
func (s *Service) ImportStock(ctx context.Context, data []byte, actor string) (int, error) {
	if err := needDepot(ctx); err != nil {
		return 0, err
	}
	recs, err := readTable(data, StockColumns)
	if err != nil {
		return 0, err
	}
	n := 0
	err = s.tx(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ *[]note) error {
		tz, err := q.DepotTimezone(ctx, sqlcdb.DepotTimezoneParams{CompanyID: sc.CompanyID, ID: sc.DepotID})
		if err != nil {
			return notFound(err)
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return err
		}
		lines, customers, err := s.codeMaps(ctx, q, sc.CompanyID)
		if err != nil {
			return err
		}
		numbers := make([]string, len(recs))
		for i, r := range recs {
			numbers[i] = iso6346.Normalize(r.get("number"))
		}
		taken, err := q.InYardAmong(ctx, sqlcdb.InYardAmongParams{CompanyID: sc.CompanyID, Numbers: numbers})
		if err != nil {
			return err
		}
		now := s.st.now().In(loc)
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		var p problems
		seen := map[string]int{}
		visits := make([]sqlcdb.InsertVisitParams, 0, len(recs))
		for i, r := range recs {
			v := sqlcdb.InsertVisitParams{CompanyID: sc.CompanyID, ID: newID(), DepotID: sc.DepotID, Number: numbers[i], Status: "in_yard",
				Imported: true, Block: r.get("block"), RowNo: r.get("row"), Tier: r.get("tier"), Grade: strings.ToUpper(r.get("grade"))}
			switch prev, dup := seen[v.Number]; {
			case v.Number == "":
				p.add(r.n, "number", "required")
			case !iso6346.IsValid(v.Number):
				p.add(r.n, "number", "not a valid container number (ISO 6346 check digit)")
			case dup:
				p.add(r.n, "number", fmt.Sprintf("the same container as row %d", prev))
			case slices.Contains(taken, v.Number):
				p.add(r.n, "number", "already in the yard")
			}
			seen[v.Number] = r.n
			if t := r.get("type_code"); t != "" {
				if v.TypeCode = iso6346.Normalize(t); !iso6346.ValidTypeCode(v.TypeCode) {
					p.add(r.n, "type_code", "not an ISO type code such as 45G1")
				}
			}
			v.LineID, v.CustomerID = code(&p, r, "line_code", lines), code(&p, r, "customer_code", customers)
			v.FullEmpty = fullEmpty(&p, r)
			if !slices.Contains([]string{"", "A", "B", "C", "FOOD"}, v.Grade) {
				p.add(r.n, "grade", "one of A, B, C, FOOD")
			}
			for _, f := range []string{"block", "row", "tier"} {
				if tooLong(r.get(f)) {
					p.add(r.n, f, "at most 64 characters")
				}
			}
			d, ok := parseDate(r.get("in_date"))
			switch {
			case r.get("in_date") == "":
				p.add(r.n, "in_date", "required")
			case !ok:
				p.add(r.n, "in_date", "use YYYY-MM-DD or DD.MM.YYYY")
			case d.After(today):
				p.add(r.n, "in_date", "in the future")
			default:
				v.InAt = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			}
			visits = append(visits, v)
		}
		if err := p.err(); err != nil {
			return err
		}
		for _, v := range visits {
			if err := q.InsertVisit(ctx, v); err != nil {
				var pg *pgconn.PgError
				if errors.As(err, &pg) && pg.Code == "23505" { // another save took the number meanwhile
					return ErrVisitConflict
				}
				return err
			}
		}
		n = len(visits)
		return s.event(ctx, q, sc, newID(), "stock.imported", actor, map[string]any{"visits": n})
	})
	return n, err
}

type importOrder struct {
	first            int
	kind, reference  string
	line, customer   *string
	lineC, customerC string
	until            *time.Time
	untilC           string
	items            []sqlcdb.InsertOrderItemParams
}

// ImportOrders creates one order per distinct kind and reference, with a row
// as an item, and returns the number of orders. The line, customer and
// valid_until of an order are those of its first row; later rows may repeat
// them or leave them empty. Rejection is as for ImportStock.
func (s *Service) ImportOrders(ctx context.Context, data []byte, actor string) (int, error) {
	if err := needDepot(ctx); err != nil {
		return 0, err
	}
	recs, err := readTable(data, OrderColumns)
	if err != nil {
		return 0, err
	}
	n := 0
	err = s.tx(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ *[]note) error {
		lines, customers, err := s.codeMaps(ctx, q, sc.CompanyID)
		if err != nil {
			return err
		}
		var p problems
		var orders []*importOrder
		byKey := map[string]*importOrder{}
		for _, r := range recs {
			kind, ref := strings.ToLower(r.get("kind")), r.get("reference")
			if kind != "release" && kind != "redelivery" {
				p.add(r.n, "kind", "release or redelivery")
			}
			if ref == "" || tooLong(ref) {
				p.add(r.n, "reference", "required, at most 64 characters")
			}
			o := byKey[kind+"\x00"+ref]
			if o == nil {
				o = &importOrder{first: r.n, kind: kind, reference: ref, lineC: strings.ToUpper(r.get("line_code")),
					customerC: strings.ToUpper(r.get("customer_code")), untilC: r.get("valid_until")}
				o.line, o.customer = code(&p, r, "line_code", lines), code(&p, r, "customer_code", customers)
				if o.untilC != "" {
					if d, ok := parseDate(o.untilC); ok {
						o.until = &d
					} else {
						p.add(r.n, "valid_until", "use YYYY-MM-DD or DD.MM.YYYY")
					}
				}
				byKey[kind+"\x00"+ref] = o
				orders = append(orders, o)
			} else {
				for _, h := range [][3]string{{"line_code", strings.ToUpper(r.get("line_code")), o.lineC},
					{"customer_code", strings.ToUpper(r.get("customer_code")), o.customerC}, {"valid_until", r.get("valid_until"), o.untilC}} {
					if h[1] != "" && h[1] != h[2] {
						p.add(r.n, h[0], fmt.Sprintf("differs from row %d, the first of this order: set it there", o.first))
					}
				}
			}
			it := sqlcdb.InsertOrderItemParams{CompanyID: sc.CompanyID, Quantity: 1, FullEmpty: fullEmpty(&p, r)}
			number, typ := iso6346.Normalize(r.get("number")), iso6346.Normalize(r.get("type_code"))
			if (number == "") == (typ == "") {
				p.add(r.n, "number", "a container number or an ISO type code, exactly one of the two")
			}
			if number != "" {
				if !iso6346.IsValid(number) {
					p.add(r.n, "number", "not a valid container number (ISO 6346 check digit)")
				}
				it.Number = &number
			}
			if typ != "" {
				if !iso6346.ValidTypeCode(typ) {
					p.add(r.n, "type_code", "not an ISO type code such as 45G1")
				}
				it.TypeCode = &typ
			}
			if qs := r.get("quantity"); qs != "" {
				qty, err := strconv.ParseInt(qs, 10, 32)
				switch {
				case err != nil || qty < 1:
					p.add(r.n, "quantity", "a whole number, at least 1")
				case number != "" && qty > 1:
					p.add(r.n, "quantity", "a named container is quantity 1")
				default:
					it.Quantity = qty
				}
			}
			o.items = append(o.items, it)
		}
		if err := p.err(); err != nil {
			return err
		}
		for _, o := range orders {
			id := newID()
			if err := q.InsertOrder(ctx, sqlcdb.InsertOrderParams{CompanyID: sc.CompanyID, ID: id, DepotID: sc.DepotID, Kind: o.kind,
				Reference: o.reference, LineID: o.line, CustomerID: o.customer, ValidUntil: o.until, CreatedBy: actor}); err != nil {
				return err
			}
			for _, it := range o.items {
				it.ID, it.OrderID = newID(), id
				if err := q.InsertOrderItem(ctx, it); err != nil {
					return err
				}
			}
		}
		n = len(orders)
		return s.event(ctx, q, sc, newID(), "orders.imported", actor, map[string]any{"orders": n})
	})
	return n, err
}

// Template returns an import file's header row and an example row.
func Template(name string) (header, example []string, ok bool) {
	switch name {
	case "stock":
		return StockColumns, []string{"MSCU1234566", "45G1", "MSC", "", "E", "2026-01-31", "A", "3", "2", ""}, true
	case "orders":
		return OrderColumns, []string{"release", "REL-1001", "MSC", "", "2026-12-31", "", "45G1", "E", "2"}, true
	}
	return nil, nil, false
}
