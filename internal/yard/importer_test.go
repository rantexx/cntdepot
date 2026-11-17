package yard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"depot/internal/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/encoding/charmap"
)

const stockHeader = "number,type_code,line_code,customer_code,full_empty,in_date,block,row,tier,grade\n"

// importFixture is a service on company A (depot d1 in Istanbul, line MSC, customer ACME).
func importFixture(t *testing.T) *fixture {
	t.Helper()
	x := newFixture(t)
	if err := db.InCompany(ctxA, x.st.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctxA, `UPDATE depots SET timezone = 'Europe/Istanbul' WHERE id = 'd1'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctxA, `INSERT INTO customers (company_id, id, code, name) VALUES ('A', 'cA', 'ACME', 'Acme')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *fixture) rowErrors(err error) []RowError {
	x.t.Helper()
	var ie *ImportError
	if !errors.As(err, &ie) {
		x.t.Fatalf("error = %v; want an *ImportError", err)
	}
	return ie.Rows
}

func TestImportStockCreatesImportedVisits(t *testing.T) {
	x := importFixture(t)
	csv := stockHeader +
		"mscu 123456-6,45g1,msc,acme,F,2026-01-05,A,3,2,b\n" +
		"CSQU3054383,,,,,2026-01-06,,,,\n"
	n, err := x.svc.ImportStock(ctxA, []byte(csv), "office")
	if err != nil || n != 2 {
		t.Fatalf("import = %d, %v; want 2, nil", n, err)
	}
	v := must[Visit](t)(x.st.InYardVisit(ctxA, "MSCU1234566"))
	wantAt := time.Date(2026, 1, 4, 21, 0, 0, 0, time.UTC) // 2026-01-05 00:00 in Istanbul (UTC+3)
	if !v.Imported || v.Status != "in_yard" || !v.InAt.Equal(wantAt) || v.TypeCode != "45G1" || v.FullEmpty != "F" ||
		v.Block != "A" || v.RowNo != "3" || v.Tier != "2" || v.Grade != "B" || v.LineID == nil || *v.LineID != "lA" ||
		v.CustomerID == nil || *v.CustomerID != "cA" || v.InMoveID != nil {
		t.Fatalf("visit = %+v", v)
	}
	if v2 := must[Visit](t)(x.st.InYardVisit(ctxA, "CSQU3054383")); v2.FullEmpty != "E" || v2.LineID != nil {
		t.Fatalf("second visit = %+v", v2)
	}

	// A number already in the yard, a duplicate in the file and a future date are row errors.
	future := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	_, err = x.svc.ImportStock(ctxA, []byte(stockHeader+
		"MSCU1234566,,,,,2026-01-05,,,,\n"+
		"TGHU1234567,,,,,"+future+",,,,\n"+
		"TGHU1234567,,,,,2026-01-05,,,,\n"), "office")
	rows := x.rowErrors(err)
	if len(rows) != 3 || rows[0].Row != 1 || rows[0].Field != "number" || rows[1].Row != 2 || rows[1].Field != "in_date" ||
		rows[2].Row != 3 || rows[2].Field != "number" {
		t.Fatalf("rows = %+v", rows)
	}
	// Another company's yard does not matter; no depot in the scope does.
	if _, err := x.svc.ImportStock(tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", Role: tenancy.Owner}),
		[]byte(stockHeader+"TGHU1234567,,,,,2026-01-05,,,,\n"), "office"); !errors.Is(err, tenancy.ErrNoScope) {
		t.Fatalf("no depot: err = %v; want ErrNoScope", err)
	}
}

func TestImportTurkishExcelCSV(t *testing.T) {
	x := importFixture(t)
	csv := "\xef\xbb\xbfnumber;type_code;line_code;customer_code;full_empty;in_date;block;row;tier;grade\r\n" +
		"MSCU1234566;45G1;MSC;;F;31.12.2025;;;;\r\n"
	if n, err := x.svc.ImportStock(ctxA, []byte(csv), "office"); err != nil || n != 1 {
		t.Fatalf("import = %d, %v", n, err)
	}
	v := must[Visit](t)(x.st.InYardVisit(ctxA, "MSCU1234566"))
	if want := time.Date(2025, 12, 30, 21, 0, 0, 0, time.UTC); !v.InAt.Equal(want) || v.FullEmpty != "F" {
		t.Fatalf("visit = %+v; want in_at %v", v, want)
	}
}

func TestImportBadRowImportsNothingAtServiceLevel(t *testing.T) {
	x := importFixture(t)
	_, err := x.svc.ImportStock(ctxA, []byte(stockHeader+
		"MSCU1234566,,,,,2026-01-05,,,,\n"+
		"CSQU3054383,,,,,2026-01-05,,,,\n"+
		"MSCU1234565,,,,,2026-01-05,,,,\n"+ // wrong check digit
		"TGHU1234567,,NOPE,,,2026-13-45,,,,\n"), "office")
	rows := x.rowErrors(err)
	if len(rows) != 3 || rows[0] != (RowError{3, "number", rows[0].Message}) || rows[1].Field != "line_code" || rows[2].Field != "in_date" {
		t.Fatalf("rows = %+v", rows)
	}
	if got := must[[]Visit](t)(x.st.ListVisits(ctxA, "")); len(got) != 0 {
		t.Fatalf("%d visits created by a rejected file", len(got))
	}
}

func TestImportRejectsBadFiles(t *testing.T) {
	x := importFixture(t)
	if _, err := x.svc.ImportStock(ctxA, []byte("number,foo\nMSCU1234566,1\n"), "office"); len(x.rowErrors(err)) == 0 {
		t.Fatal("a file without the required columns was accepted")
	}
	big := stockHeader + strings.Repeat(",,,,,,,,,\n", MaxImportRows+1)
	if _, err := x.svc.ImportStock(ctxA, []byte(big), "office"); !errors.Is(err, ErrImportTooLarge) {
		t.Fatalf("too many rows: err = %v; want ErrImportTooLarge", err)
	}
	// At most 200 problems are listed.
	many := stockHeader + strings.Repeat("BAD,,,,,2026-01-05,,,,\n", 500)
	if _, err := x.svc.ImportStock(ctxA, []byte(many), "office"); len(x.rowErrors(err)) != 200 {
		t.Fatalf("listed %d problems; want 200", len(x.rowErrors(err)))
	}
}

func TestImportOrdersGroupsByReference(t *testing.T) {
	x := importFixture(t)
	csv := "kind;reference;line_code;customer_code;valid_until;number;type_code;full_empty;quantity\n" +
		"release;R-1;MSC;ACME;31.12.2026;MSCU1234566;;E;\n" +
		"release;R-1;;;;;45G1;F;3\n" +
		"redelivery;R-1;MSC;;2026-12-31;;22G1;;2\n" +
		"release;R-2;;;;CSQU3054383;;;\n"
	n, err := x.svc.ImportOrders(ctxA, []byte(csv), "office")
	if err != nil || n != 3 {
		t.Fatalf("import = %d, %v; want 3 orders", n, err)
	}
	orders := must[[]Order](t)(x.st.ListOrders(ctxA, "", "", ""))
	if len(orders) != 3 {
		t.Fatalf("%d orders", len(orders))
	}
	for _, o := range orders {
		_, items, err := x.st.GetOrder(ctxA, o.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case o.Kind == "release" && o.Reference == "R-1":
			if len(items) != 2 || o.LineID == nil || *o.LineID != "lA" || o.CustomerID == nil || o.ValidUntil == nil ||
				o.ValidUntil.Format("2006-01-02") != "2026-12-31" || *items[0].Number != "MSCU1234566" || items[1].Quantity != 3 || items[1].FullEmpty != "F" {
				t.Errorf("release R-1 = %+v %+v", o, items)
			}
		case o.Kind == "redelivery" && o.Reference == "R-1":
			if len(items) != 1 || *items[0].TypeCode != "22G1" || items[0].Quantity != 2 {
				t.Errorf("redelivery R-1 items = %+v", items)
			}
		case o.Reference == "R-2":
			if len(items) != 1 || *items[0].Number != "CSQU3054383" || items[0].Quantity != 1 {
				t.Errorf("R-2 items = %+v", items)
			}
		default:
			t.Errorf("unexpected order %+v", o)
		}
	}

	// Rows of one order that disagree, and a bad item, reject the whole file.
	_, err = x.svc.ImportOrders(ctxA, []byte("kind,reference,line_code,customer_code,valid_until,number,type_code,full_empty,quantity\n"+
		"release,R-9,MSC,,,,45G1,,1\n"+
		"release,R-9,,ACME,,,45G1,,1\n"+ // customer differs from the first row (empty there)
		"release,R-9,,,,MSCU1234566,45G1,,1\n"+ // number and type both
		"release,R-10,,,,MSCU1234566,,,2\n"), "office") // a named container is quantity 1
	rows := x.rowErrors(err)
	if len(rows) != 3 || rows[0].Row != 2 || rows[1].Row != 3 || rows[2].Row != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	if got := must[[]Order](t)(x.st.ListOrders(ctxA, "", "", "R-9")); len(got) != 0 {
		t.Fatalf("a rejected file created %d orders", len(got))
	}
}

func TestAgeingBuckets(t *testing.T) {
	x := importFixture(t)
	now := time.Now()
	loc, _ := time.LoadLocation("Europe/Istanbul")
	y, mo, d0 := now.In(loc).Date()
	// A visit that came in `days` calendar days ago at hh:mm depot-local.
	add := func(n string, days, hh, mm int, line *string) {
		t.Helper()
		must[string](t)(x.st.CreateVisit(ctxA, Visit{Number: n, LineID: line, InAt: time.Date(y, mo, d0-days, hh, mm, 0, 0, loc)}))
	}
	l := ptrTo("lA")
	// The calendar day counts, not 24-hour periods: 23:50 eight days ago is 8 days, 00:10 seven days ago is 7.
	for i, c := range [][3]int{{0, 0, 10}, {7, 0, 10}, {8, 23, 50}, {30, 23, 50}, {31, 0, 10}, {90, 23, 50}, {91, 0, 10}, {400, 12, 0}} {
		add("TEST000000"+string(rune('0'+i)), c[0], c[1], c[2], l)
	}
	add("TEST0000009", 2, 12, 0, nil)
	// A closed visit and another depot's visit do not count.
	id := must[string](t)(x.st.CreateVisit(ctxA, Visit{Number: "TEST0000008", LineID: l, InAt: now.AddDate(0, 0, -50)}))
	if err := x.st.CloseVisit(ctxA, id, nil, now, "x"); err != nil {
		t.Fatal(err)
	}
	must[string](t)(x.st.CreateVisit(ctxD2, Visit{Number: "TESTD000000", LineID: l, InAt: now.AddDate(0, 0, -100)}))

	rows, err := x.svc.Ageing(ctxA)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ageing = %+v, %v", rows, err)
	}
	if r := rows[0]; r.LineID != "" || r.Total != 1 || r.Buckets["0-7"] != 1 {
		t.Errorf("no-line row = %+v", r)
	}
	r := rows[1]
	if r.LineCode != "MSC" || r.Total != 8 || r.Buckets["0-7"] != 2 || r.Buckets["8-30"] != 2 || r.Buckets["31-90"] != 2 || r.Buckets["90+"] != 2 {
		t.Errorf("MSC row = %+v", r)
	}
	// Without a depot in the scope, both depots count.
	all := tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", Role: tenancy.Owner})
	if rows, _ := x.svc.Ageing(all); rows[1].Total != 9 {
		t.Errorf("all depots: MSC total = %d; want 9", rows[1].Total)
	}

	d := must[Dashboard](t)(x.svc.Dashboard(ctxA))
	if d.OpenHolds != 0 || d.OpenFlags != 0 || len(d.InYardByLine) != 2 || d.InYardByLine[1].Count != 8 {
		t.Errorf("dashboard = %+v", d)
	}
}

func TestDashboardAndGateLog(t *testing.T) {
	x := importFixture(t)
	x.save(ctxA, GateForm{Direction: "in", Number: numA, TypeCode: "45G1"})
	x.save(ctxA, GateForm{Direction: "in", Number: numB})
	d := must[Dashboard](t)(x.svc.Dashboard(ctxA))
	if d.InToday != 2 || d.OutToday != 0 || d.OpenFlags != 2 { // two walk-in flags
		t.Fatalf("dashboard = %+v", d)
	}
	today := time.Now().In(time.FixedZone("TRT", 3*3600)).Format("2006-01-02")
	log := must[[]GateLogRow](t)(x.svc.GateLog(ctxA, ptrTo(mustDate(t, today)), ptrTo(mustDate(t, today)), 100))
	if len(log) != 2 || log[0].Direction != "in" || log[0].Flags != 1 {
		t.Fatalf("gate log = %+v", log)
	}
	if log := must[[]GateLogRow](t)(x.svc.GateLog(ctxA, ptrTo(mustDate(t, "2020-01-01")), ptrTo(mustDate(t, "2020-12-31")), 100)); len(log) != 0 {
		t.Fatalf("old gate log = %+v", log)
	}
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestImportTurkishEncodings(t *testing.T) {
	x := importFixture(t)
	utf8CSV := stockHeader + "MSCU1234566,,,,,2026-01-05,İstanbul,ş,,\n"
	// Excel's plain "CSV" in Turkish is Windows-1254.
	cp1254, err := charmap.Windows1254.NewEncoder().String(strings.Replace(utf8CSV, "MSCU1234566", "CSQU3054383", 1))
	if err != nil {
		t.Fatal(err)
	}
	for number, data := range map[string]string{"MSCU1234566": utf8CSV, "CSQU3054383": cp1254} {
		if n, err := x.svc.ImportStock(ctxA, []byte(data), "office"); err != nil || n != 1 {
			t.Fatalf("import %s = %d, %v", number, n, err)
		}
		if v := must[Visit](t)(x.st.InYardVisit(ctxA, number)); v.Block != "İstanbul" || v.RowNo != "ş" {
			t.Errorf("%s stored block %q row %q; want İstanbul and ş", number, v.Block, v.RowNo)
		}
	}
	// Bytes that are neither UTF-8 nor Windows-1254 are a row-0 problem, not a server error.
	_, err = x.svc.ImportStock(ctxA, []byte(stockHeader+"TGHU1234567,,,,,2026-01-05,\x81,,,\n"), "office")
	if rows := x.rowErrors(err); len(rows) != 1 || rows[0].Row != 0 || !strings.Contains(rows[0].Message, "UTF-8") {
		t.Fatalf("rows = %+v", rows)
	}
}
