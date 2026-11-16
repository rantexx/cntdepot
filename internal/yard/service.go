package yard

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // depot time zones must load on hosts without a zoneinfo database

	"depot/internal/iso6346"
	"depot/internal/store"
	"depot/internal/tenancy"
	sqlcdb "depot/internal/yard/db"

	"github.com/jackc/pgx/v5"
)

var (
	ErrBadForm        = errors.New("gate form: direction in or out, a container number, full/empty E or F, condition sound or damaged, 1 to 3 photos per damage")
	ErrBadLink        = errors.New("a linked photo, order item or approval does not belong to this move or depot")
	ErrVoided         = errors.New("the move is void")
	ErrNotSaved       = errors.New("the move has no saved gate form")
	ErrReasonRequired = errors.New("a reason is required")
	ErrOrderClosed    = errors.New("the order is not open")
	ErrVisitMoved     = errors.New("the visit this move opened has been closed since: correct or void that move first")
)

// GateForm is the clerk's gate form for one move.
type GateForm struct {
	MoveID, Direction, Number, TypeCode, FullEmpty, Seal, Condition string
	LineID, CustomerID, HaulierID                                   string
	TruckPlate, TrailerPlate, DriverName, SignaturePhotoID          string
	Damages                                                         []DamageInput
	ChosenOrderItemID                                               string
	Override                                                        *Override
	ApprovalID                                                      string
}

type DamageInput struct {
	Location, Description string
	PhotoIDs              []string
}

// Override is the clerk passing a refusal (refusal mode clerk_override).
type Override struct{ Reason string }

// Refusal says why a move was not saved. ApprovalID names the manager
// approval that may pass it (refusal mode manager_approval).
type Refusal struct {
	Blocked    bool
	Reasons    []Reason
	ApprovalID string
}

// Result of a save. Refused or Candidates (several order items match: the
// clerk picks one) mean nothing was applied.
type Result struct {
	Move       store.Move
	VisitID    string
	Flags      []Reason
	Refused    *Refusal
	Candidates []MatchedItem
}

// Service applies gate forms to the yard: visits, order items, flags and
// approvals, each save in one company transaction.
type Service struct {
	st     *Store
	moves  *store.Store
	notify func(companyID, kind, id string)
}

// NewService builds the service. notify tells live screens about a changed
// "move" or "approval"; it may be nil.
func NewService(st *Store, moves *store.Store, notify func(companyID, kind, id string)) *Service {
	if notify == nil {
		notify = func(string, string, string) {}
	}
	return &Service{st: st, moves: moves, notify: notify}
}

type note struct{ kind, id string }

// tx runs fn in one company transaction and sends its notes once committed.
func (s *Service) tx(ctx context.Context, fn func(q *sqlcdb.Queries, sc tenancy.Scope, notes *[]note) error) error {
	var notes []note
	err := s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error { return fn(q, sc, &notes) })
	if err == nil {
		sc, _ := tenancy.From(ctx)
		for _, n := range notes {
			s.notify(sc.CompanyID, n.kind, n.id)
		}
	}
	return err
}

func (s *Service) withMove(ctx context.Context, res Result, err error) (Result, error) {
	if err != nil {
		return Result{}, err
	}
	res.Move, err = s.moves.GetMove(ctx, res.Move.ID)
	return res, err
}

// SaveGateMove applies the clerk's form to its move. A move whose form is
// already saved returns the stored result: a resend applies nothing twice.
func (s *Service) SaveGateMove(ctx context.Context, f GateForm, actor string) (Result, error) {
	var res Result
	err := s.tx(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, notes *[]note) error {
		m, err := lockMove(ctx, q, sc, f.MoveID)
		switch {
		case err != nil:
			return err
		case m.FormSavedAt != nil:
			res, err = storedResult(ctx, q, sc, m)
			return err
		case m.Voided:
			return ErrVoided
		}
		res, err = s.apply(ctx, q, sc, m, f, actor, "", notes)
		return err
	})
	res.Move.ID = f.MoveID
	return s.withMove(ctx, res, err)
}

var errChoose = errors.New("several order items match")

// CorrectGateMove undoes a saved move's visit and order item and applies the
// corrected form. Refusals do not stop a correction: they become override
// flags with the reason.
func (s *Service) CorrectGateMove(ctx context.Context, moveID string, f GateForm, reason, actor string) (Result, error) {
	if reason = strings.TrimSpace(reason); reason == "" {
		return Result{}, ErrReasonRequired
	}
	f.MoveID = moveID
	var res Result
	err := s.tx(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, notes *[]note) error {
		m, err := lockMove(ctx, q, sc, moveID)
		switch {
		case err != nil:
			return err
		case m.Voided:
			return ErrVoided
		case m.FormSavedAt == nil:
			return ErrNotSaved
		}
		if err := reverse(ctx, q, sc, m); err != nil {
			return err
		}
		if err := q.DeleteDamages(ctx, sqlcdb.DeleteDamagesParams{CompanyID: sc.CompanyID, MoveID: m.ID}); err != nil {
			return err
		}
		if res, err = s.apply(ctx, q, sc, m, f, actor, reason, notes); err == nil && len(res.Candidates) > 0 {
			return errChoose // roll the reversal back; the office picks an item
		}
		return err
	})
	if errors.Is(err, errChoose) {
		err = nil
	}
	res.Move.ID = moveID
	return s.withMove(ctx, res, err)
}

// VoidGateMove releases the move's order item, reopens the visit it closed
// or deletes the one it opened, and marks it void.
func (s *Service) VoidGateMove(ctx context.Context, moveID, reason, actor string) error {
	if reason = strings.TrimSpace(reason); reason == "" {
		return ErrReasonRequired
	}
	return s.tx(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, notes *[]note) error {
		m, err := lockMove(ctx, q, sc, moveID)
		if err != nil {
			return err
		}
		if m.Voided {
			return ErrVoided
		}
		if err := reverse(ctx, q, sc, m); err != nil {
			return err
		}
		now := s.st.now()
		if err := one(q.VoidMove(ctx, sqlcdb.VoidMoveParams{CompanyID: sc.CompanyID, ID: m.ID, VoidReason: reason, UpdatedAt: now})); err != nil {
			return err
		}
		*notes = append(*notes, note{"move", m.ID})
		return s.event(ctx, q, sc, m.ID, "move.voided", actor, map[string]any{"reason": reason})
	})
}

// DecideApproval approves or denies a pending approval.
func (s *Service) DecideApproval(ctx context.Context, approvalID string, approve bool, actor string) error {
	if err := s.st.DecideApproval(ctx, approvalID, approve, actor); err != nil {
		return err
	}
	sc, _ := tenancy.From(ctx)
	s.notify(sc.CompanyID, "approval", approvalID)
	return nil
}

func lockMove(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, id string) (sqlcdb.LockMoveRow, error) {
	m, err := q.LockMove(ctx, sqlcdb.LockMoveParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
	return m, notFound(err)
}

func storedResult(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, m sqlcdb.LockMoveRow) (Result, error) {
	res := Result{VisitID: deref(m.VisitID)}
	fl, err := q.ListFlags(ctx, sqlcdb.ListFlagsParams{CompanyID: sc.CompanyID, MoveID: m.ID})
	for _, f := range fl {
		res.Flags = append(res.Flags, Reason{f.Code, f.Detail})
	}
	return res, err
}

func (s *Service) event(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, stream, kind, actor string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return q.InsertEvent(ctx, sqlcdb.InsertEventParams{
		ID: newID(), CompanyID: sc.CompanyID, Stream: stream, Kind: kind, Actor: actor, Payload: body, OccurredAt: s.st.now(),
	})
}

// apply runs the gate rules on f and, unless refused, applies it to move m.
// correction is the office's reason when m is being corrected, else "".
func (s *Service) apply(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, m sqlcdb.LockMoveRow, f GateForm, actor, correction string, notes *[]note) (Result, error) {
	c := sc.CompanyID
	dir, number, typeCode := f.Direction, iso6346.Normalize(f.Number), iso6346.Normalize(f.TypeCode)
	fullEmpty := cmp.Or(norm(f.FullEmpty), "E")
	condition := strings.TrimSpace(f.Condition)
	if (dir != "in" && dir != "out") || number == "" || (fullEmpty != "E" && fullEmpty != "F") ||
		!slices.Contains([]string{"", "sound", "damaged"}, condition) {
		return Result{}, ErrBadForm
	}
	// Every photo named by the form must be this move's.
	photos, err := q.MovePhotoIDs(ctx, sqlcdb.MovePhotoIDsParams{CompanyID: c, MoveID: m.ID})
	if err != nil {
		return Result{}, err
	}
	sig := opt(f.SignaturePhotoID)
	if sig != nil && !slices.Contains(photos, *sig) {
		return Result{}, ErrBadLink
	}
	for _, d := range f.Damages {
		if len(d.PhotoIDs) < 1 || len(d.PhotoIDs) > 3 {
			return Result{}, ErrBadForm
		}
		for _, p := range d.PhotoIDs {
			if !slices.Contains(photos, p) {
				return Result{}, ErrBadLink
			}
		}
	}

	// The facts.
	if err := q.LockNumber(ctx, sqlcdb.LockNumberParams{CompanyID: c, Number: number}); err != nil {
		return Result{}, err
	}
	now := s.st.now()
	loc := time.UTC
	if tz, err := q.DepotTimezone(ctx, sqlcdb.DepotTimezoneParams{CompanyID: c, ID: m.DepotID}); err == nil {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	settings, err := settingsOf(ctx, q, c)
	if err != nil {
		return Result{}, err
	}
	facts := Facts{Direction: dir, Number: number, TypeCode: typeCode, FullEmpty: fullEmpty, Now: now.In(loc),
		WalkinsAllowed: settings.WalkinsAllowed, ChosenOrderItemID: f.ChosenOrderItemID}
	// Gate-in sees an open visit in any depot of the company (missed gate-out);
	// gate-out closes only one in the move's own depot.
	open, err := q.InYardVisit(ctx, sqlcdb.InYardVisitParams{CompanyID: c, Number: number})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	var visit *Visit
	if err == nil && (dir == "in" || open.DepotID == m.DepotID) {
		visit = &open
		facts.OpenVisit = &VisitFact{open.ID, open.DepotID}
	}
	line := strings.TrimSpace(f.LineID)
	if line == "" && visit != nil && dir == "out" {
		line = deref(visit.LineID)
	}
	facts.LineID = line
	block, err := q.MatchBlock(ctx, sqlcdb.MatchBlockParams{CompanyID: c, Number: number, LineID: line})
	switch {
	case err == nil:
		facts.Blocked = &BlockFact{block.Kind, block.Value, block.Reason}
	case !errors.Is(err, pgx.ErrNoRows):
		return Result{}, err
	}
	if dir == "out" && visit != nil {
		holds, err := q.ListHolds(ctx, sqlcdb.ListHoldsParams{CompanyID: c, VisitID: visit.ID, OpenOnly: true})
		if err != nil {
			return Result{}, err
		}
		for _, h := range holds {
			facts.OpenHolds = append(facts.OpenHolds, HoldFact{h.ID, h.Reason})
		}
	}
	kind := map[string]string{"in": "redelivery", "out": "release"}[dir]
	rows, err := q.MatchOrderItems(ctx, sqlcdb.MatchOrderItemsParams{CompanyID: c, DepotID: m.DepotID, Kind: kind, Number: number, TypeCode: typeCode})
	if err != nil {
		return Result{}, err
	}
	items := map[string]sqlcdb.MatchOrderItemsRow{}
	for _, r := range rows {
		it := r.OrderItem
		items[it.ID] = r
		fact := ItemFact{it.ID, deref(it.Number), deref(it.TypeCode), it.FullEmpty, int(it.Quantity - it.Used)}
		if i := slices.IndexFunc(facts.Orders, func(o OrderFact) bool { return o.OrderID == it.OrderID }); i >= 0 {
			facts.Orders[i].Items = append(facts.Orders[i].Items, fact)
			continue
		}
		facts.Orders = append(facts.Orders, OrderFact{it.OrderID, r.Kind, deref(r.LineID), r.Status, r.ValidUntil, []ItemFact{fact}})
	}
	if _, ok := items[f.ChosenOrderItemID]; f.ChosenOrderItemID != "" && !ok {
		return Result{}, ErrBadLink // not an item of this depot's orders that fits the container
	}
	d := Decide(facts)

	// Refusals: blocked never passes at the gate; the others per refusal mode.
	flags := slices.Clone(d.Flags)
	if len(d.Refusals) > 0 {
		refused := &Refusal{Blocked: d.Blocked, Reasons: d.Refusals}
		pass := correction
		switch {
		case pass != "":
		case d.Blocked:
			return Result{Refused: refused}, nil
		case settings.RefusalMode == "clerk_override":
			if f.Override == nil || strings.TrimSpace(f.Override.Reason) == "" {
				return Result{Refused: refused}, nil
			}
			pass = strings.TrimSpace(f.Override.Reason)
		case settings.RefusalMode == "manager_approval":
			a, err := s.approval(ctx, q, sc, m.ID, f.ApprovalID, d.Refusals, actor, notes)
			if err != nil {
				return Result{}, err
			}
			if a.Status != "approved" {
				refused.ApprovalID = a.ID
				return Result{Refused: refused}, nil
			}
			pass = "approved by " + a.DecidedBy
		default:
			return Result{Refused: refused}, nil
		}
		for _, r := range d.Refusals {
			flags = append(flags, Reason{"override:" + r.Code, pass})
		}
	}
	if len(d.Candidates) > 0 {
		return Result{Candidates: d.Candidates}, nil
	}

	// Apply.
	lineID, customerID := opt(line), opt(f.CustomerID)
	var orderID, itemID *string
	if d.Item != nil {
		r := items[d.Item.ItemID]
		orderID, itemID = &r.OrderItem.OrderID, &r.OrderItem.ID
		lineID, customerID = cmp.Or(r.LineID, lineID), cmp.Or(r.CustomerID, customerID)
		if err := one(q.UseItem(ctx, sqlcdb.UseItemParams{CompanyID: c, ID: *itemID, N: 1})); err != nil {
			return Result{}, err
		}
	}
	newVisit := sqlcdb.InsertVisitParams{CompanyID: c, ID: newID(), DepotID: m.DepotID, Number: number, TypeCode: typeCode,
		LineID: lineID, CustomerID: customerID, Status: "in_yard", FullEmpty: fullEmpty, InMoveID: &m.ID, InAt: now}
	visitID := newVisit.ID
	switch {
	case dir == "in":
		if facts.OpenVisit != nil { // the truth at the gate wins: it left without a gate-out
			if err := closeVisit(ctx, q, c, open.ID, m.ID, now, "missed_gate_out"); err != nil {
				return Result{}, err
			}
		}
		err = q.InsertVisit(ctx, newVisit)
	case visit != nil:
		visitID = visit.ID
		err = closeVisit(ctx, q, c, visit.ID, m.ID, now, "")
	default: // a gate-out of a container the yard did not know: record its stay as closed
		newVisit.Status, newVisit.InMoveID, newVisit.OutMoveID, newVisit.OutAt, newVisit.CloseReason = "out", nil, &m.ID, &now, "no_open_visit"
		err = q.InsertVisit(ctx, newVisit)
	}
	if err != nil {
		return Result{}, err
	}
	if err := one(q.SetMoveIdentity(ctx, sqlcdb.SetMoveIdentityParams{CompanyID: c, ID: m.ID, Direction: dir, Number: number,
		TypeCode: typeCode, Verified: iso6346.IsValid(number), Actor: actor})); err != nil {
		return Result{}, err
	}
	if err := one(q.SetMoveForm(ctx, sqlcdb.SetMoveFormParams{
		CompanyID: c, ID: m.ID, VisitID: &visitID, OrderID: orderID, OrderItemID: itemID,
		TruckPlate: strings.TrimSpace(f.TruckPlate), TrailerPlate: strings.TrimSpace(f.TrailerPlate), DriverName: strings.TrimSpace(f.DriverName),
		HaulierID: opt(f.HaulierID), Seal: strings.TrimSpace(f.Seal), FullEmpty: fullEmpty, Condition: condition,
		SignaturePhotoID: sig, FormSavedAt: &now,
	})); err != nil {
		return Result{}, err
	}
	for _, d := range f.Damages {
		if err := one(q.InsertDamage(ctx, sqlcdb.InsertDamageParams{CompanyID: c, ID: newID(), MoveID: m.ID,
			Location: strings.TrimSpace(d.Location), Description: strings.TrimSpace(d.Description), PhotoIds: d.PhotoIDs})); err != nil {
			return Result{}, err
		}
	}
	for _, fl := range flags {
		if err := one(q.InsertFlag(ctx, sqlcdb.InsertFlagParams{CompanyID: c, ID: newID(), MoveID: m.ID, Code: fl.Code, Detail: fl.Detail})); err != nil {
			return Result{}, err
		}
	}
	if lineID != nil && len(number) >= 4 {
		if err := q.UpsertLinePrefix(ctx, sqlcdb.UpsertLinePrefixParams{CompanyID: c, Prefix: number[:4], LineID: *lineID}); err != nil {
			return Result{}, err
		}
	}
	kindOf, payload := "move.gate_saved", map[string]any{"form": f, "flags": flags, "visit_id": visitID}
	if correction != "" {
		kindOf, payload["reason"] = "move.corrected", correction
	}
	*notes = append(*notes, note{"move", m.ID})
	return Result{VisitID: visitID, Flags: flags}, s.event(ctx, q, sc, m.ID, kindOf, actor, payload)
}

func closeVisit(ctx context.Context, q *sqlcdb.Queries, companyID, id, moveID string, at time.Time, reason string) error {
	return one(q.CloseVisit(ctx, sqlcdb.CloseVisitParams{CompanyID: companyID, ID: id, OutMoveID: &moveID, OutAt: &at, CloseReason: reason}))
}

// approval returns the approval that may pass a refused move: the one the
// clerk names (it must be this move's), else the pending one, else a new one.
func (s *Service) approval(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, moveID, id string, reasons []Reason, actor string, notes *[]note) (Approval, error) {
	if id != "" {
		a, err := q.GetApproval(ctx, sqlcdb.GetApprovalParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && a.MoveID != moveID) {
			return a, ErrBadLink
		}
		return a, err
	}
	a, err := q.PendingApproval(ctx, sqlcdb.PendingApprovalParams{CompanyID: sc.CompanyID, MoveID: moveID})
	if !errors.Is(err, pgx.ErrNoRows) {
		return a, err
	}
	body, err := json.Marshal(reasons)
	if err != nil {
		return a, err
	}
	a = Approval{ID: newID(), MoveID: moveID, Status: "pending"}
	err = one(q.InsertApproval(ctx, sqlcdb.InsertApprovalParams{CompanyID: sc.CompanyID, ID: a.ID, MoveID: moveID,
		Reasons: body, RequestedBy: actor, RequestedAt: s.st.now()}))
	*notes = append(*notes, note{"approval", a.ID})
	return a, err
}

// reverse undoes what a saved move did: it gives its order item back,
// deletes the visit it opened (or created closed), and reopens the visits it closed.
func reverse(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, m sqlcdb.LockMoveRow) error {
	c := sc.CompanyID
	if err := q.LockNumber(ctx, sqlcdb.LockNumberParams{CompanyID: c, Number: m.Number}); err != nil {
		return err
	}
	if m.OrderItemID != nil {
		if err := one(q.UseItem(ctx, sqlcdb.UseItemParams{CompanyID: c, ID: *m.OrderItemID, N: -1})); err != nil {
			return err
		}
	}
	if err := q.UnlinkMove(ctx, sqlcdb.UnlinkMoveParams{CompanyID: c, ID: m.ID}); err != nil {
		return err
	}
	visits, err := q.VisitsOfMove(ctx, sqlcdb.VisitsOfMoveParams{CompanyID: c, MoveID: m.ID})
	if err != nil {
		return err
	}
	opened := func(v Visit) bool {
		return deref(v.InMoveID) == m.ID || (v.InMoveID == nil && !v.Imported && v.CloseReason == "no_open_visit")
	}
	// Deletes first, so a reopened visit never meets the one being removed.
	for _, v := range visits {
		if !opened(v) {
			continue
		}
		if deref(v.InMoveID) == m.ID && v.Status != "in_yard" {
			return ErrVisitMoved
		}
		if err := q.DeleteVisit(ctx, sqlcdb.DeleteVisitParams{CompanyID: c, ID: v.ID}); err != nil {
			return err
		}
	}
	for _, v := range visits {
		if !opened(v) {
			if err := q.ReopenVisit(ctx, sqlcdb.ReopenVisitParams{CompanyID: c, ID: v.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func opt(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Records the office keeps. Each runs under ctx's scope like the Store.

func (s *Service) PlaceHold(ctx context.Context, visitID, reason, actor string) (string, error) {
	return s.st.PlaceHold(ctx, visitID, reason, actor)
}

func (s *Service) ReleaseHold(ctx context.Context, holdID, actor string) error {
	return s.st.ReleaseHold(ctx, holdID, actor)
}

func (s *Service) SetPosition(ctx context.Context, visitID, block, row, tier string) error {
	return s.st.SetVisitPosition(ctx, visitID, block, row, tier)
}

func (s *Service) SetGrade(ctx context.Context, visitID, grade string) error {
	return s.st.SetVisitGrade(ctx, visitID, grade)
}

func (s *Service) UpsertBlock(ctx context.Context, kind, value, reason, actor string) (string, error) {
	return s.st.UpsertBlock(ctx, kind, value, reason, actor)
}

func (s *Service) SetBlockActive(ctx context.Context, id string, active bool) error {
	return s.st.SetBlockActive(ctx, id, active)
}

func (s *Service) CreateOrder(ctx context.Context, o Order, items []OrderItem, actor string) (string, error) {
	return s.st.CreateOrder(ctx, o, items, actor)
}

// CancelOrder cancels an open order; its remaining items no longer match at the gate.
func (s *Service) CancelOrder(ctx context.Context, id, actor string) error {
	return s.tx(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ *[]note) error {
		o, err := q.GetOrder(ctx, sqlcdb.GetOrderParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
		if err != nil {
			return notFound(err)
		}
		if o.Status != "open" {
			return ErrOrderClosed
		}
		if err := one(q.SetOrderStatus(ctx, sqlcdb.SetOrderStatusParams{CompanyID: sc.CompanyID, ID: id, Status: "cancelled"})); err != nil {
			return err
		}
		return s.event(ctx, q, sc, id, "order.cancelled", actor, map[string]any{})
	})
}

// SnapshotOrder is an open order with its items.
type SnapshotOrder struct {
	Order
	Items []OrderItem
}

// Snapshot is what a gate phone keeps for working offline (spec 3.8).
type Snapshot struct {
	At           time.Time
	Orders       []SnapshotOrder // the depot's open orders
	Blocks       []Block         // the company's active block list
	Holds        []Hold          // open holds on the depot's visits
	InYard       []Visit         // the depot's in-yard visits
	LinePrefixes []LinePrefix    // the company's, for the line suggestion
}

// Snapshot needs a depot in the scope.
func (s *Service) Snapshot(ctx context.Context) (snap Snapshot, err error) {
	err = s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		c, dep := sc.CompanyID, sc.DepotID
		if dep == "" {
			return tenancy.ErrNoScope
		}
		snap.At = s.st.now()
		orders, err := q.ListOrders(ctx, sqlcdb.ListOrdersParams{CompanyID: c, DepotID: dep, Status: "open"})
		if err != nil {
			return err
		}
		items, err := q.OpenOrderItems(ctx, sqlcdb.OpenOrderItemsParams{CompanyID: c, DepotID: dep})
		if err != nil {
			return err
		}
		for _, o := range orders {
			so := SnapshotOrder{Order: o}
			for _, it := range items {
				if it.OrderID == o.ID {
					so.Items = append(so.Items, it)
				}
			}
			snap.Orders = append(snap.Orders, so)
		}
		if snap.Blocks, err = q.ListBlocks(ctx, sqlcdb.ListBlocksParams{CompanyID: c, ActiveOnly: true}); err != nil {
			return err
		}
		if snap.Holds, err = q.ListHolds(ctx, sqlcdb.ListHoldsParams{CompanyID: c, DepotID: dep, OpenOnly: true}); err != nil {
			return err
		}
		if snap.InYard, err = q.ListVisits(ctx, sqlcdb.ListVisitsParams{CompanyID: c, DepotID: dep, Status: "in_yard"}); err != nil {
			return err
		}
		snap.LinePrefixes, err = q.ListLinePrefixes(ctx, c)
		return err
	})
	return snap, err
}
