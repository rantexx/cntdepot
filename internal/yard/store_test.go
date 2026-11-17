package yard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"depot/internal/db"
	"depot/internal/pgtest"
	"depot/internal/store"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// ctxOf is scoped to company c, depot d1.
func ctxOf(c string) context.Context {
	return tenancy.With(context.Background(), tenancy.Scope{CompanyID: c, DepotID: "d1", UserID: "u1", Role: tenancy.Owner})
}

var ctxA = ctxOf("A")

// openTest returns a yard store on a fresh database with companies A and B,
// each with depots d1 and d2 and a shipping line (A: lA, B: lB).
func openTest(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.NewDB(t)
	bg := context.Background()
	if err := db.Migrate(bg, owner, db.MigrateOptions{AppPassword: "app-pw"}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(bg, db.AppURL(owner, "app-pw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, c := range []string{"A", "B"} {
		if err := db.Global(bg, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `INSERT INTO companies (id, name) VALUES ($1, $1)`, c)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.InCompany(ctxOf(c), pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(bg, `INSERT INTO depots (company_id, id, name) VALUES ($1, 'd1', 'D1'), ($1, 'd2', 'D2')`, c); err != nil {
				return err
			}
			_, err := tx.Exec(bg, `INSERT INTO shipping_lines (company_id, id, code, name) VALUES ($1, 'l'||$1, 'MSC', 'MSC')`, c)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return NewStore(pool), pool
}

func newMove(t *testing.T, pool *pgxpool.Pool, ctx context.Context) string {
	t.Helper()
	m, err := store.New(pool).CreateMove(ctx, "1", false, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func ptr[T any](v T) *T { return &v }

func pgCode(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestYardTablesRespectCompany(t *testing.T) {
	s, pool := openTest(t)
	str := must[string](t)
	moveID := newMove(t, pool, ctxA)
	orderID := str(s.CreateOrder(ctxA, Order{Kind: "release", Reference: "R1", LineID: ptr("lA")},
		[]OrderItem{{TypeCode: ptr("45G1"), Quantity: 2}}, "u1"))
	_, items, err := s.GetOrder(ctxA, orderID)
	if err != nil || len(items) != 1 {
		t.Fatalf("GetOrder: %v %v", items, err)
	}
	visitID := str(s.CreateVisit(ctxA, Visit{Number: "MSCU1234565", TypeCode: "45G1", InMoveID: &moveID, InAt: time.Now()}))
	holdID := str(s.PlaceHold(ctxA, visitID, "customs", "u1"))
	blockID := str(s.UpsertBlock(ctxA, "container", "MSCU1234565", "stolen", "u1"))
	str(s.AddDamage(ctxA, moveID, "door", "dent", []string{"p1"}))
	flagID := str(s.AddFlag(ctxA, moveID, "walkin", ""))
	apprID := str(s.CreateApproval(ctxA, moveID, json.RawMessage(`[{"code":"no_order"}]`), "u1"))
	if err := s.AddEIRToken(ctxA, moveID, "hash-a", time.Now().Add(time.Hour), "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(ctxA, Settings{RefusalMode: "none", WalkinsAllowed: false, EirTtlDays: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLinePrefix(ctxA, "MSCU", "lA"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMoveForm(ctxA, moveID, MoveForm{TruckPlate: "34ABC12", Condition: "sound", FullEmpty: "E", VisitID: &visitID}); err != nil {
		t.Fatal(err)
	}

	// B sees none of A's rows in any new table.
	tables := []string{"release_orders", "order_items", "visits", "holds", "block_list", "damages",
		"gate_flags", "approvals", "eir_tokens", "company_settings", "line_prefixes"}
	b := ctxOf("B")
	for _, tbl := range tables {
		var na, nb int
		for _, c := range []struct {
			ctx context.Context
			n   *int
		}{{ctxA, &na}, {b, &nb}} {
			if err := db.InCompany(c.ctx, pool, func(tx pgx.Tx) error {
				return tx.QueryRow(c.ctx, "SELECT count(*) FROM "+tbl).Scan(c.n)
			}); err != nil {
				t.Fatal(err)
			}
		}
		if na == 0 || nb != 0 {
			t.Errorf("%s: A sees %d rows, B sees %d", tbl, na, nb)
		}
	}

	// B's reads find nothing.
	if _, _, err := s.GetOrder(b, orderID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetOrder = %v", err)
	}
	if got, err := s.ListOrders(b, "", "", ""); err != nil || len(got) != 0 {
		t.Errorf("ListOrders = %v, %v", got, err)
	}
	if _, err := s.GetVisit(b, visitID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetVisit = %v", err)
	}
	if _, err := s.InYardVisit(b, "MSCU1234565"); !errors.Is(err, ErrNotFound) {
		t.Errorf("InYardVisit = %v", err)
	}
	if got, err := s.ListVisits(b, ""); err != nil || len(got) != 0 {
		t.Errorf("ListVisits = %v, %v", got, err)
	}
	if got, err := s.ListHolds(b, "", false); err != nil || len(got) != 0 {
		t.Errorf("ListHolds = %v, %v", got, err)
	}
	if got, err := s.ListBlocks(b, false); err != nil || len(got) != 0 {
		t.Errorf("ListBlocks = %v, %v", got, err)
	}
	if got, err := s.ListDamages(b, moveID); err != nil || len(got) != 0 {
		t.Errorf("ListDamages = %v, %v", got, err)
	}
	if got, err := s.ListFlags(b, "", false); err != nil || len(got) != 0 {
		t.Errorf("ListFlags = %v, %v", got, err)
	}
	if _, err := s.GetApproval(b, apprID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetApproval = %v", err)
	}
	if got, err := s.ListApprovals(b, ""); err != nil || len(got) != 0 {
		t.Errorf("ListApprovals = %v, %v", got, err)
	}
	if got, err := s.Settings(b); err != nil || got.RefusalMode != "clerk_override" || !got.WalkinsAllowed || got.EirTtlDays != 30 {
		t.Errorf("B's settings = %+v, %v; want defaults", got, err)
	}
	if got, err := s.LinePrefixes(b); err != nil || len(got) != 0 {
		t.Errorf("LinePrefixes = %v, %v", got, err)
	}
	if _, err := s.GetMoveForm(b, moveID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMoveForm = %v", err)
	}

	// B's writes by id fail.
	writes := map[string]error{
		"SetOrderStatus":   s.SetOrderStatus(b, orderID, "cancelled"),
		"UseItem":          s.UseItem(b, items[0].ID, 1),
		"SetVisitGrade":    s.SetVisitGrade(b, visitID, "A"),
		"SetVisitPosition": s.SetVisitPosition(b, visitID, "A", "1", "1"),
		"CloseVisit":       s.CloseVisit(b, visitID, nil, time.Now(), "x"),
		"ReleaseHold":      s.ReleaseHold(b, holdID, "u1"),
		"SetBlockActive":   s.SetBlockActive(b, blockID, false),
		"ResolveFlag":      s.ResolveFlag(b, flagID, "u1", ""),
		"DecideApproval":   s.DecideApproval(b, apprID, true, "u1"),
		"SetMoveForm":      s.SetMoveForm(b, moveID, MoveForm{TruckPlate: "X"}),
		"VoidMove":         s.VoidMove(b, moveID, "x"),
		"AddEIRToken":      s.AddEIRToken(b, moveID, "hash-b", time.Now().Add(time.Hour), "u1"),
	}
	for _, f := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"PlaceHold", func() (string, error) { return s.PlaceHold(b, visitID, "x", "u1") }},
		{"AddDamage", func() (string, error) { return s.AddDamage(b, moveID, "x", "", []string{"p"}) }},
		{"AddFlag", func() (string, error) { return s.AddFlag(b, moveID, "x", "") }},
		{"CreateApproval", func() (string, error) { return s.CreateApproval(b, moveID, nil, "u1") }},
	} {
		_, writes[f.name] = f.fn()
	}
	for name, err := range writes {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s from B = %v, want ErrNotFound", name, err)
		}
	}
	if err := s.SetLinePrefix(b, "MSCU", "lA"); err == nil {
		t.Error("B linked a prefix to A's line")
	}
	if err := s.SetSettings(b, Settings{RefusalMode: "manager_approval", WalkinsAllowed: true, EirTtlDays: 1}); err != nil {
		t.Fatal(err)
	}

	// A's rows are untouched.
	if o, items, err := s.GetOrder(ctxA, orderID); err != nil || o.Status != "open" || items[0].Used != 0 {
		t.Errorf("A's order = %+v %+v, %v", o, items, err)
	}
	if v, err := s.GetVisit(ctxA, visitID); err != nil || v.Grade != "" || v.Status != "in_yard" {
		t.Errorf("A's visit = %+v, %v", v, err)
	}
	if got, err := s.Settings(ctxA); err != nil || got.RefusalMode != "none" || got.EirTtlDays != 7 {
		t.Errorf("A's settings = %+v, %v", got, err)
	}
	if f, err := s.GetMoveForm(ctxA, moveID); err != nil || f.TruckPlate != "34ABC12" || f.Voided {
		t.Errorf("A's move form = %+v, %v", f, err)
	}
	if a, err := s.GetApproval(ctxA, apprID); err != nil || a.Status != "pending" {
		t.Errorf("A's approval = %+v, %v", a, err)
	}
}

func TestYardStoreRoundTrip(t *testing.T) {
	s, pool := openTest(t)
	str := must[string](t)
	moveID := newMove(t, pool, ctxA)
	visitID := str(s.CreateVisit(ctxA, Visit{Number: "MSCU1234565", InMoveID: &moveID, InAt: time.Now()}))
	holdID := str(s.PlaceHold(ctxA, visitID, "customs", "u1"))
	if got, _ := s.ListHolds(ctxA, visitID, true); len(got) != 1 {
		t.Fatalf("open holds %v", got)
	}
	if err := s.ReleaseHold(ctxA, holdID, "u2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListHolds(ctxA, visitID, true); len(got) != 0 {
		t.Fatalf("open holds after release %v", got)
	}
	if err := s.ReleaseHold(ctxA, holdID, "u2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second release = %v", err)
	}
	apprID := str(s.CreateApproval(ctxA, moveID, nil, "u1"))
	if err := s.DecideApproval(ctxA, apprID, true, "m1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideApproval(ctxA, apprID, false, "m1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deciding twice = %v", err)
	}
	if a, _ := s.GetApproval(ctxA, apprID); a.Status != "approved" || a.DecidedBy != "m1" {
		t.Fatalf("approval %+v", a)
	}
	// A depot-scoped read does not see another depot's visit; the company-wide one does.
	all := tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", UserID: "u1", Role: tenancy.Owner})
	d2 := tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", DepotID: "d2", UserID: "u1", Role: tenancy.Owner})
	if _, err := s.GetVisit(d2, visitID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("d2 sees d1's visit: %v", err)
	}
	if _, err := s.InYardVisit(all, "MSCU1234565"); err != nil {
		t.Fatalf("company-wide in-yard lookup: %v", err)
	}
	if _, err := s.CreateVisit(all, Visit{Number: "X", InAt: time.Now()}); !errors.Is(err, tenancy.ErrNoScope) {
		t.Fatalf("CreateVisit without depot = %v", err)
	}
	if _, err := s.CreateOrder(all, Order{Kind: "release", Reference: "R"}, nil, "u1"); !errors.Is(err, tenancy.ErrNoScope) {
		t.Fatalf("CreateOrder without depot = %v", err)
	}
	if err := s.CloseVisit(ctxA, visitID, &moveID, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListVisits(ctxA, "in_yard"); len(got) != 0 {
		t.Fatalf("in-yard after close %v", got)
	}
	if err := s.VoidMove(ctxA, moveID, "wrong truck"); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetMoveForm(ctxA, moveID); !f.Voided || f.VoidReason != "wrong truck" {
		t.Fatalf("void not stored: %+v", f)
	}
}

func TestOneOpenVisitPerNumber(t *testing.T) {
	s, _ := openTest(t)
	if _, err := s.CreateVisit(ctxA, Visit{Number: "MSCU1234565", InAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Even at another depot of the company.
	d2 := tenancy.With(context.Background(), tenancy.Scope{CompanyID: "A", DepotID: "d2", UserID: "u1", Role: tenancy.Owner})
	if _, err := s.CreateVisit(d2, Visit{Number: "MSCU1234565", InAt: time.Now()}); pgCode(err) != "23505" {
		t.Fatalf("second in-yard visit = %v, want unique violation", err)
	}
	// Another company may hold the same number; a closed visit does not count.
	if _, err := s.CreateVisit(ctxOf("B"), Visit{Number: "MSCU1234565", InAt: time.Now()}); err != nil {
		t.Fatalf("company B: %v", err)
	}
	if _, err := s.CreateVisit(ctxA, Visit{Number: "MSCU1234565", Status: "out", InAt: time.Now(), OutAt: ptr(time.Now())}); err != nil {
		t.Fatalf("closed visit: %v", err)
	}
}

func TestOrderItemShape(t *testing.T) {
	s, _ := openTest(t)
	for name, it := range map[string]OrderItem{
		"both":            {Number: ptr("MSCU1234565"), TypeCode: ptr("45G1"), Quantity: 1},
		"neither":         {Quantity: 1},
		"named quantity":  {Number: ptr("MSCU1234565"), Quantity: 2},
		"used > quantity": {TypeCode: ptr("45G1"), Quantity: 1, Used: 2},
	} {
		if _, err := s.CreateOrder(ctxA, Order{Kind: "release", Reference: "R"}, []OrderItem{it}, "u1"); pgCode(err) != "23514" {
			t.Errorf("%s: %v, want check violation", name, err)
		}
	}
	if got, _ := s.ListOrders(ctxA, "", "", ""); len(got) != 0 {
		t.Fatalf("a rejected order was half-stored: %v", got)
	}
	id, err := s.CreateOrder(ctxA, Order{Kind: "release", Reference: "R"}, []OrderItem{{TypeCode: ptr("45G1"), Quantity: 1}}, "u1")
	if err != nil {
		t.Fatal(err)
	}
	_, items, _ := s.GetOrder(ctxA, id)
	if err := s.UseItem(ctxA, items[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.UseItem(ctxA, items[0].ID, 1); pgCode(err) != "23514" {
		t.Fatalf("over-use = %v, want check violation", err)
	}
	if err := s.UseItem(ctxA, items[0].ID, -1); err != nil {
		t.Fatalf("release item: %v", err)
	}
}

func TestEIRLookupOnlyLiveTokens(t *testing.T) {
	s, pool := openTest(t)
	live, expired := newMove(t, pool, ctxA), newMove(t, pool, ctxA)
	if err := s.AddEIRToken(ctxA, live, "h-live", time.Now().Add(time.Hour), "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEIRToken(ctxA, expired, "h-old", time.Now().Add(-time.Second), "u1"); err != nil {
		t.Fatal(err)
	}
	// No scope needed: the page has only the token.
	company, move, err := s.LookupEIR(context.Background(), "h-live")
	if err != nil || company != "A" || move != live {
		t.Fatalf("live token = %q %q %v", company, move, err)
	}
	for _, h := range []string{"h-old", "unknown"} {
		if _, _, err := s.LookupEIR(context.Background(), h); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s = %v, want ErrNotFound", h, err)
		}
	}
}
