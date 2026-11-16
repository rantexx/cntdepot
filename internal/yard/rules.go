package yard

import (
	"strings"
	"time"

	"depot/internal/iso6346"
)

type Facts struct {
	Direction                   string // "in" or "out"
	Number, TypeCode, FullEmpty string
	LineID                      string
	Now                         time.Time
	Orders                      []OrderFact
	OpenVisit                   *VisitFact
	Blocked                     *BlockFact
	OpenHolds                   []HoldFact
	WalkinsAllowed              bool
	ChosenOrderItemID           string
}

type OrderFact struct {
	OrderID, Kind, LineID, Status string
	ValidUntil                    *time.Time
	Items                         []ItemFact
}

type ItemFact struct {
	ItemID, Number, TypeCode, FullEmpty string
	Remaining                           int
}

type VisitFact struct{ VisitID, DepotID string }
type BlockFact struct{ Kind, Value, Reason string }
type HoldFact struct{ HoldID, Reason string }

type Reason struct{ Code, Detail string }

type MatchedItem struct {
	OrderID, ItemID, Number, TypeCode, FullEmpty string
	Remaining                                    int
}

type Decision struct {
	Blocked    bool
	Refusals   []Reason
	Flags      []Reason
	Item       *MatchedItem
	Candidates []MatchedItem
}

// TypeMatches: same size, height and type-group letter (characters 1-3).
func TypeMatches(orderCode, containerCode string) bool {
	a, b := iso6346.Normalize(orderCode), iso6346.Normalize(containerCode)
	return len(a) >= 3 && len(b) >= 3 && a[:3] == b[:3]
}

func norm(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func itemMatches(f Facts, o OrderFact, it ItemFact) bool {
	if o.LineID != "" && norm(o.LineID) != norm(f.LineID) {
		return false
	}
	if it.FullEmpty != "" && norm(it.FullEmpty) != norm(f.FullEmpty) {
		return false
	}
	if it.Number != "" {
		return norm(it.Number) == norm(f.Number)
	}
	return TypeMatches(it.TypeCode, f.TypeCode)
}

// Decide applies the gate rules (spec 3.2, 3.8). It never drops a move: every
// conflict is a refusal (the clerk may override) or a flag.
func Decide(f Facts) Decision {
	var d Decision
	if f.Blocked != nil {
		d.Blocked = true
		d.Refusals = append(d.Refusals, Reason{"blocked", f.Blocked.Reason})
		return d
	}
	out := f.Direction == "out"
	if out && len(f.OpenHolds) > 0 {
		d.Refusals = append(d.Refusals, Reason{"hold_open", f.OpenHolds[0].Reason})
	}
	if out && f.OpenVisit == nil {
		d.Flags = append(d.Flags, Reason{Code: "no_open_visit"})
	}
	if !out && f.OpenVisit != nil {
		d.Flags = append(d.Flags, Reason{Code: "missed_gate_out"})
	}

	kind := "redelivery"
	if out {
		kind = "release"
	}
	today := time.Date(f.Now.Year(), f.Now.Month(), f.Now.Day(), 0, 0, 0, 0, time.UTC)
	var named, qty []MatchedItem
	var expired, usedUp bool
	notOpen := map[string]bool{}
	for _, o := range f.Orders {
		if o.Kind != kind {
			continue
		}
		for _, it := range o.Items {
			if !itemMatches(f, o, it) {
				continue
			}
			if o.ValidUntil != nil {
				v := o.ValidUntil
				if time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.UTC).Before(today) {
					expired = true
					continue
				}
			}
			if it.Remaining <= 0 {
				usedUp = true
				continue
			}
			m := MatchedItem{o.OrderID, it.ItemID, it.Number, it.TypeCode, it.FullEmpty, it.Remaining}
			if o.Status != "open" {
				notOpen[it.ItemID] = true
			}
			if it.Number != "" {
				named = append(named, m)
			} else {
				qty = append(qty, m)
			}
		}
	}
	matches := qty
	if len(named) > 0 { // a named item beats a quantity item
		matches = named
	}
	if len(matches) > 1 && f.ChosenOrderItemID != "" {
		for _, m := range matches {
			if m.ItemID == f.ChosenOrderItemID {
				matches = []MatchedItem{m}
				break
			}
		}
	}
	switch {
	case len(matches) == 1:
		d.Item = &matches[0]
		if notOpen[matches[0].ItemID] {
			d.Flags = append(d.Flags, Reason{Code: "order_not_open"})
		}
	case len(matches) > 1:
		d.Candidates = matches
	default:
		if usedUp {
			d.Flags = append(d.Flags, Reason{Code: "item_used_up"})
		}
		switch {
		case expired:
			d.Refusals = append(d.Refusals, Reason{Code: "order_expired"})
		case !out && f.WalkinsAllowed:
			d.Flags = append(d.Flags, Reason{Code: "walkin"})
		default:
			d.Refusals = append(d.Refusals, Reason{Code: "no_order"})
		}
	}
	return d
}
