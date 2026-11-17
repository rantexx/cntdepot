package yard

import (
	"testing"
	"time"
	_ "time/tzdata" // the Istanbul case must run on every machine
)

var now = time.Date(2026, 11, 17, 15, 30, 0, 0, time.UTC)

func day(d int) *time.Time {
	t := time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func has(rs []Reason, code string) bool {
	for _, r := range rs {
		if r.Code == code {
			return true
		}
	}
	return false
}

func order(kind string, until *time.Time, items ...ItemFact) OrderFact {
	return OrderFact{OrderID: "o1", Kind: kind, Status: "open", ValidUntil: until, Items: items}
}

func out(items ...ItemFact) Facts {
	return Facts{Direction: "out", Number: "MSKU1234565", TypeCode: "45G1", FullEmpty: "empty", Now: now,
		OpenVisit: &VisitFact{VisitID: "v"}, Orders: []OrderFact{order("release", day(20), items...)}}
}

func TestDecide(t *testing.T) {
	named := ItemFact{ItemID: "named", Number: "msku1234565", FullEmpty: "empty", Remaining: 1}
	qty := ItemFact{ItemID: "qty", TypeCode: "45G0", FullEmpty: "empty", Remaining: 2}
	tests := []struct {
		name      string
		f         Facts
		refuse    []string
		flag      []string
		noRefuse  bool
		item      string
		cands     int
		isBlocked bool
	}{
		{name: "named match", f: out(named), item: "named", noRefuse: true},
		{name: "quantity 45G0 vs 45G1 matches", f: out(qty), item: "qty", noRefuse: true},
		{name: "45R1 container vs 45G1 order", f: func() Facts {
			f := out(ItemFact{ItemID: "q", TypeCode: "45G1", FullEmpty: "empty", Remaining: 1})
			f.TypeCode = "45R1"
			return f
		}(), refuse: []string{"no_order"}},
		{name: "22G1 vs 45G1", f: func() Facts {
			f := out(ItemFact{ItemID: "q", TypeCode: "45G1", FullEmpty: "empty", Remaining: 1})
			f.TypeCode = "22G1"
			return f
		}(), refuse: []string{"no_order"}},
		{name: "named beats quantity", f: out(qty, named), item: "named", noRefuse: true},
		{name: "used-up never matches", f: out(ItemFact{ItemID: "u", TypeCode: "45G1", FullEmpty: "empty", Remaining: 0}),
			refuse: []string{"no_order"}, flag: []string{"item_used_up"}},
		{name: "full/empty mismatch", f: func() Facts { f := out(qty); f.FullEmpty = "full"; return f }(), refuse: []string{"no_order"}},
		{name: "line mismatch", f: func() Facts { f := out(qty); f.Orders[0].LineID = "A"; f.LineID = "B"; return f }(), refuse: []string{"no_order"}},
		{name: "line match", f: func() Facts { f := out(qty); f.Orders[0].LineID = "A"; f.LineID = "A"; return f }(), item: "qty", noRefuse: true},
		{name: "expired order", f: func() Facts { f := out(qty); f.Orders[0].ValidUntil = day(16); return f }(), refuse: []string{"order_expired"}},
		{name: "valid until today is valid", f: func() Facts { f := out(qty); f.Orders[0].ValidUntil = day(17); return f }(), item: "qty", noRefuse: true},
		{name: "order not open refuses", f: func() Facts { f := out(qty); f.Orders[0].Status = "cancelled"; return f }(), item: "qty", refuse: []string{"order_not_open"}},
		{name: "several matches give candidates", f: out(qty, ItemFact{ItemID: "q2", TypeCode: "45G1", FullEmpty: "empty", Remaining: 1}), cands: 2, noRefuse: true},
		{name: "chosen item resolves", f: func() Facts {
			f := out(qty, ItemFact{ItemID: "q2", TypeCode: "45G1", FullEmpty: "empty", Remaining: 1})
			f.ChosenOrderItemID = "q2"
			return f
		}(), item: "q2", noRefuse: true},
		{name: "open hold refuses gate-out", f: func() Facts { f := out(qty); f.OpenHolds = []HoldFact{{HoldID: "h"}}; return f }(),
			refuse: []string{"hold_open"}, item: "qty"},
		{name: "gate-out no open visit", f: func() Facts { f := out(qty); f.OpenVisit = nil; return f }(), flag: []string{"no_open_visit"}, item: "qty", noRefuse: true},
		{name: "gate-out no order", f: func() Facts { f := out(); f.Orders = nil; return f }(), refuse: []string{"no_order"}},
		{name: "gate-in redelivery match", f: Facts{Direction: "in", Number: "MSKU1234565", TypeCode: "45G1", FullEmpty: "empty", Now: now,
			Orders: []OrderFact{order("redelivery", nil, qty)}}, item: "qty", noRefuse: true},
		{name: "gate-in walkin", f: Facts{Direction: "in", Number: "MSKU1234565", TypeCode: "45G1", Now: now, WalkinsAllowed: true},
			flag: []string{"walkin"}, noRefuse: true},
		{name: "gate-in no walkin", f: Facts{Direction: "in", Number: "MSKU1234565", TypeCode: "45G1", Now: now}, refuse: []string{"no_order"}},
		{name: "gate-in with open visit", f: Facts{Direction: "in", Number: "MSKU1234565", Now: now, WalkinsAllowed: true,
			OpenVisit: &VisitFact{VisitID: "v"}}, flag: []string{"missed_gate_out", "walkin"}, noRefuse: true},
		{name: "block list refuses, matching still runs", f: func() Facts {
			f := out(named)
			f.Blocked = &BlockFact{Kind: "container", Value: "MSKU1234565", Reason: "stolen"}
			return f
		}(), refuse: []string{"blocked"}, item: "named", isBlocked: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.f)
			for _, c := range tc.refuse {
				if !has(d.Refusals, c) {
					t.Errorf("want refusal %s, got %+v", c, d.Refusals)
				}
			}
			if tc.noRefuse && len(d.Refusals) > 0 {
				t.Errorf("unexpected refusals %+v", d.Refusals)
			}
			for _, c := range tc.flag {
				if !has(d.Flags, c) {
					t.Errorf("want flag %s, got %+v", c, d.Flags)
				}
			}
			if tc.item != "" && (d.Item == nil || d.Item.ItemID != tc.item) {
				t.Errorf("want item %s, got %+v", tc.item, d.Item)
			}
			if tc.item == "" && d.Item != nil {
				t.Errorf("want no item, got %+v", d.Item)
			}
			if len(d.Candidates) != tc.cands {
				t.Errorf("want %d candidates, got %d", tc.cands, len(d.Candidates))
			}
			if d.Blocked != tc.isBlocked {
				t.Errorf("Blocked = %v", d.Blocked)
			}
		})
	}
}

func TestTypeMatches(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"45G0", "45G1", true}, {"45g0", " 45G1 ", true}, {"45R1", "45G1", false},
		{"22G1", "45G1", false}, {"42G1", "45G1", false}, {"45", "45G1", false}, {"", "", false},
	} {
		if got := TypeMatches(c.a, c.b); got != c.want {
			t.Errorf("TypeMatches(%q,%q)=%v", c.a, c.b, got)
		}
	}
}

func TestDecideFixRound1(t *testing.T) {
	ist, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Fatal("no tzdata:", err)
	}
	qty := ItemFact{ItemID: "qty", TypeCode: "45G0", FullEmpty: "empty", Remaining: 2}
	until := day(17)

	f := out(qty)
	f.Orders[0].ValidUntil = until
	f.Now = time.Date(2026, 11, 18, 0, 30, 0, 0, ist) // 21:30 UTC on the 17th, but 18th locally
	if d := Decide(f); !has(d.Refusals, "order_expired") {
		t.Errorf("00:30 local next day should be expired: %+v", d)
	}
	f.Now = time.Date(2026, 11, 17, 23, 30, 0, 0, ist)
	if d := Decide(f); len(d.Refusals) > 0 || d.Item == nil {
		t.Errorf("23:30 local on valid_until should be valid: %+v", d)
	}

	in := Facts{Direction: "in", Number: "MSKU1234565", TypeCode: "45G1", FullEmpty: "empty", Now: now,
		Orders: []OrderFact{order("redelivery", day(10), qty)}}
	if d := Decide(in); !has(d.Refusals, "order_expired") || has(d.Flags, "walkin") {
		t.Errorf("walk-ins off: want order_expired, got %+v", d)
	}
	in.WalkinsAllowed = true
	d := Decide(in)
	if len(d.Refusals) > 0 || len(d.Flags) != 1 || d.Flags[0] != (Reason{"walkin", "order expired"}) {
		t.Errorf("walk-ins on: want walkin/order expired, got %+v", d)
	}

	n := out(ItemFact{ItemID: "named", Number: "MSKU 123456-5", FullEmpty: "empty", Remaining: 1})
	if d := Decide(n); d.Item == nil || d.Item.ItemID != "named" {
		t.Errorf("normalised number should match: %+v", d)
	}

	cancelled := order("release", day(20), ItemFact{ItemID: "named", Number: "MSKU1234565", FullEmpty: "empty", Remaining: 1})
	cancelled.OrderID, cancelled.Status = "o2", "cancelled"
	g := out(qty)
	g.Orders = append(g.Orders, cancelled)
	if d := Decide(g); d.Item == nil || d.Item.ItemID != "qty" || has(d.Refusals, "order_not_open") {
		t.Errorf("open order must win over cancelled named item: %+v", d)
	}
	g.Orders = g.Orders[1:]
	if d := Decide(g); d.Item == nil || d.Item.ItemID != "named" || !has(d.Refusals, "order_not_open") {
		t.Errorf("only non-open match should refuse: %+v", d)
	}
}
