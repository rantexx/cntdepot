package yard

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ErrVisitConflict  = errors.New("the container is in the yard again under a newer visit: correct or void that move first")
	ErrDirectionFixed = errors.New("a correction cannot change the direction: void the move and record a new one")
	ErrFormSaved      = errors.New("the move's gate form is saved: the office corrects or voids it")
)

// TurnedAway is the void reason of a move whose truck the gate turned away.
const TurnedAway = "turned away"

// GateForm is the clerk's gate form for one move.
type GateForm struct {
	MoveID, Direction, Number, TypeCode, FullEmpty, Seal, Condition string
	LineID, CustomerID, HaulierID                                   string
	TruckPlate, TrailerPlate, DriverName, SignaturePhotoID          string
	Damages                                                         []DamageInput
	ChosenOrderItemID                                               string
	Override                                                        *Override
	ApprovalID                                                      string
	// SavedOffline: the phone saved the form offline, so the truck has gone through.
	SavedOffline bool
	// SavedBy is the phone's signed-in user when the form was saved: a claim for the event, never trusted.
	SavedBy string
}

type DamageInput struct {
	Location, Description string
	PhotoIDs              []string
}

// Override is the clerk passing a refusal (refusal mode clerk_override).
type Override struct{ Reason string }

// Refusal says why a move was not saved. ApprovalID names the manager
// approval that may pass it (refusal mode manager_approval), and
// ApprovalStatus says whether it is "pending" or "denied".
type Refusal struct {
	Blocked        bool
	Reasons        []Reason
	ApprovalID     string
	ApprovalStatus string
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
		res, err = s.apply(ctx, q, sc, m, f, actor, "", nil, notes)
		return err
	})
	res.Move.ID = f.MoveID
	return s.withMove(ctx, res, err)
}

var errChoose = errors.New("several order items match")

// CorrectGateMove undoes a saved move's visit and order item and applies the
// corrected form; the move's own visit is rewritten in place, so its
// position, grade and holds stay. Refusals do not stop a correction: they
// become override flags with the reason. The direction cannot change.
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
		case f.Direction != m.Direction:
			return ErrDirectionFixed
		}
		own, err := reverse(ctx, q, sc, m, s.st.now())
		if err != nil {
			return err
		}
		if err := q.DeleteDamages(ctx, sqlcdb.DeleteDamagesParams{CompanyID: sc.CompanyID, MoveID: m.ID}); err != nil {
			return err
		}
		if res, err = s.apply(ctx, q, sc, m, f, actor, reason, own, notes); err == nil && len(res.Candidates) > 0 {
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

// VoidGateMove releases the move's order item, reopens the visits it closed,
// closes the visit it opened (close_reason "voided"), and marks it void.
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
		now := s.st.now()
		if _, err := reverse(ctx, q, sc, m, now); err != nil {
			return err
		}
		if err := one(q.VoidMove(ctx, sqlcdb.VoidMoveParams{CompanyID: sc.CompanyID, ID: m.ID, VoidReason: reason, UpdatedAt: now})); err != nil {
			return err
		}
		*notes = append(*notes, note{"move", m.ID})
		return s.event(ctx, q, sc, m.ID, "move.voided", actor, map[string]any{"reason": reason})
	})
}

// TurnAwayMove records that the gate turned the truck away: the move keeps the
// typed number, is void with reason TurnedAway, and gets a turned_away flag
// (the refusal codes) for the office. A repeat is a no-op; a move whose gate
// form was saved is refused (ErrFormSaved), and one voided otherwise is ErrVoided.
// savedBy is the phone's claim of who turned it away, recorded in the event only.
func (s *Service) TurnAwayMove(ctx context.Context, moveID, number string, reasons []Reason, savedBy, actor string) error {
	return s.tx(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, notes *[]note) error {
		m, err := lockMove(ctx, q, sc, moveID)
		switch {
		case err != nil:
			return err
		case m.Voided && m.VoidReason == TurnedAway:
			return nil
		case m.Voided:
			return ErrVoided
		case m.FormSavedAt != nil:
			return ErrFormSaved
		}
		now := s.st.now()
		if err := one(q.TurnAwayMove(ctx, sqlcdb.TurnAwayMoveParams{CompanyID: sc.CompanyID, ID: m.ID,
			Number: iso6346.Normalize(number), Actor: actor, VoidReason: TurnedAway, UpdatedAt: now})); err != nil {
			return err
		}
		if err := one(q.InsertFlag(ctx, sqlcdb.InsertFlagParams{CompanyID: sc.CompanyID, ID: newID(), MoveID: m.ID,
			Code: "turned_away", Detail: strings.Join(reasonCodes(reasons), ",")})); err != nil {
			return err
		}
		*notes = append(*notes, note{"move", m.ID})
		return s.event(ctx, q, sc, m.ID, "move.turned_away", actor, map[string]any{"number": number, "reasons": reasons, "saved_by": savedBy})
	})
}

// DecideApproval approves or denies a pending approval. Besides the
// approval, its move is announced: the gate phone waiting on it follows moves.
func (s *Service) DecideApproval(ctx context.Context, approvalID string, approve bool, actor string) error {
	if err := s.st.DecideApproval(ctx, approvalID, approve, actor); err != nil {
		return err
	}
	a, err := s.st.GetApproval(ctx, approvalID)
	if err != nil {
		return err
	}
	sc, _ := tenancy.From(ctx)
	s.notify(sc.CompanyID, "approval", approvalID)
	s.notify(sc.CompanyID, "move", a.MoveID)
	return nil
}

// GateRecord is a move's gate form as the office reads it back.
type GateRecord struct {
	MoveForm
	Damages []Damage
	Flags   []Flag
}

// GateRecord reads a move's form, damages and flags in one transaction.
func (s *Service) GateRecord(ctx context.Context, moveID string) (r GateRecord, err error) {
	err = s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		if r.MoveForm, err = q.GetMoveForm(ctx, sqlcdb.GetMoveFormParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: moveID}); err != nil {
			return notFound(err)
		}
		if r.Damages, err = q.ListDamages(ctx, sqlcdb.ListDamagesParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, MoveID: moveID}); err != nil {
			return err
		}
		r.Flags, err = q.ListFlags(ctx, sqlcdb.ListFlagsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, MoveID: moveID})
		return err
	})
	return r, err
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
// correction is the office's reason when m is being corrected, else "";
// own is then the move's visit as reverse left it, rewritten in place.
func (s *Service) apply(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, m sqlcdb.LockMoveRow, f GateForm, actor, correction string, own *Visit, notes *[]note) (Result, error) {
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

	refs, err := q.FormRefs(ctx, sqlcdb.FormRefsParams{CompanyID: c, LineID: strings.TrimSpace(f.LineID),
		CustomerID: strings.TrimSpace(f.CustomerID), HaulierID: strings.TrimSpace(f.HaulierID)})
	if err != nil {
		return Result{}, err
	}
	if !refs.LineOk || !refs.CustomerOk || !refs.HaulierOk {
		return Result{}, ErrBadLink
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
	// The open visit, in any depot of the company: a gate-in closes it as a
	// missed gate-out, a gate-out closes it even at another depot (flagged).
	open, err := q.InYardVisit(ctx, sqlcdb.InYardVisitParams{CompanyID: c, Number: number})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	var visit *Visit
	if err == nil {
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
	openRows, err := q.MatchOpenOrderItems(ctx, sqlcdb.MatchOpenOrderItemsParams{CompanyID: c, DepotID: m.DepotID, Kind: kind, Number: number, TypeCode: typeCode})
	if err != nil {
		return Result{}, err
	}
	otherRows, err := q.MatchOtherOrderItems(ctx, sqlcdb.MatchOtherOrderItemsParams{CompanyID: c, DepotID: m.DepotID, Kind: kind, Number: number, TypeCode: typeCode})
	if err != nil {
		return Result{}, err
	}
	items := map[string]sqlcdb.MatchOpenOrderItemsRow{}
	for _, r := range append(openRows, convertRows(otherRows)...) {
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
	if dir == "out" && visit != nil && visit.DepotID != m.DepotID {
		flags = append(flags, Reason{"other_depot", visit.DepotID})
	}
	if len(d.Refusals) > 0 {
		refused := &Refusal{Blocked: d.Blocked, Reasons: d.Refusals}
		overridden := settings.RefusalMode == "clerk_override" && f.Override != nil && strings.TrimSpace(f.Override.Reason) != ""
		pass, prefix := correction, "override:"
		switch {
		case pass != "":
		case f.SavedOffline && (d.Blocked || !overridden): // the truck has gone: record the truth, flag the conflict (spec 3.3, 3.8)
			prefix = "offline:"
		case d.Blocked:
			return Result{Refused: refused}, nil
		case settings.RefusalMode == "clerk_override":
			if f.Override == nil || strings.TrimSpace(f.Override.Reason) == "" {
				return Result{Refused: refused}, nil
			}
			pass = strings.TrimSpace(f.Override.Reason)
		case settings.RefusalMode == "manager_approval":
			a, ok, err := s.approval(ctx, q, sc, m.ID, number, f.ApprovalID, d.Refusals, actor, notes)
			if err != nil {
				return Result{}, err
			}
			if !ok {
				refused.ApprovalID, refused.ApprovalStatus = a.ID, a.Status
				return Result{Refused: refused}, nil
			}
			pass = "approved by " + a.DecidedBy
		default:
			return Result{Refused: refused}, nil
		}
		for _, r := range d.Refusals {
			flags = append(flags, Reason{prefix + r.Code, cmp.Or(pass, r.Detail)})
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
	if dir == "out" && visit == nil { // a gate-out of a container the yard did not know: record its stay as closed
		newVisit.Status, newVisit.InMoveID, newVisit.OutMoveID, newVisit.OutAt, newVisit.CloseReason = "out", nil, &m.ID, &now, "no_open_visit"
	}
	visitID := newVisit.ID
	switch {
	case dir == "in" && visit != nil: // the truth at the gate wins: it left without a gate-out
		err = closeVisit(ctx, q, c, visit.ID, m.ID, now, "missed_gate_out")
	case visit != nil: // a gate-out closes its visit, even at another depot
		why := ""
		if visit.DepotID != m.DepotID {
			why = "other_depot_gate_out"
		}
		visitID = visit.ID
		err = closeVisit(ctx, q, c, visit.ID, m.ID, now, why)
	}
	if err != nil {
		return Result{}, err
	}
	switch {
	case visitID != newVisit.ID: // closed above; a placeholder the move no longer needs is let go
		if own != nil {
			err = q.DetachVisit(ctx, sqlcdb.DetachVisitParams{CompanyID: c, ID: own.ID})
		}
	case own != nil: // a correction keeps its visit
		visitID = own.ID
		err = q.RewriteVisit(ctx, sqlcdb.RewriteVisitParams{CompanyID: c, ID: own.ID, Number: number, TypeCode: typeCode,
			LineID: lineID, CustomerID: customerID, FullEmpty: fullEmpty, Status: newVisit.Status, OutMoveID: newVisit.OutMoveID,
			OutAt: newVisit.OutAt, CloseReason: newVisit.CloseReason})
	default:
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
		SignaturePhotoID: sig, FormSavedAt: now,
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
	kindOf, payload := "move.gate_saved", map[string]any{"form": f, "flags": flags, "visit_id": visitID, "saved_by": f.SavedBy}
	if correction != "" {
		kindOf, payload["reason"] = "move.corrected", correction
	}
	*notes = append(*notes, note{"move", m.ID})
	return Result{VisitID: visitID, Flags: flags}, s.event(ctx, q, sc, m.ID, kindOf, actor, payload)
}

func closeVisit(ctx context.Context, q *sqlcdb.Queries, companyID, id, moveID string, at time.Time, reason string) error {
	return closeVisitBy(ctx, q, companyID, id, &moveID, at, reason)
}

func closeVisitBy(ctx context.Context, q *sqlcdb.Queries, companyID, id string, moveID *string, at time.Time, reason string) error {
	return one(q.CloseVisit(ctx, sqlcdb.CloseVisitParams{CompanyID: companyID, ID: id, OutMoveID: moveID, OutAt: &at, CloseReason: reason}))
}

// approval decides a refused move under manager_approval. It passes (ok)
// with an approved approval of this move whose reasons cover every current
// refusal: the one the clerk names, else any. Otherwise it returns the
// approval to wait for: the named one or a pending one with exactly these
// reasons, else a new pending one.
func (s *Service) approval(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, moveID, number, id string, refusals []Reason, actor string, notes *[]note) (Approval, bool, error) {
	all, err := q.ApprovalsOfMove(ctx, sqlcdb.ApprovalsOfMoveParams{CompanyID: sc.CompanyID, MoveID: moveID})
	if err != nil {
		return Approval{}, false, err
	}
	want := reasonCodes(refusals)
	covers := func(a Approval) bool {
		have := approvalCodes(a)
		return a.Status == "approved" && (a.Number == "" || a.Number == number) &&
			!slices.ContainsFunc(want, func(c string) bool { return !slices.Contains(have, c) })
	}
	same := func(a Approval) bool {
		return (a.Number == "" || a.Number == number) && slices.Equal(approvalCodes(a), want)
	}
	if id != "" {
		i := slices.IndexFunc(all, func(a Approval) bool { return a.ID == id })
		switch {
		case i < 0:
			return Approval{}, false, ErrBadLink // not this move's
		case covers(all[i]):
			return all[i], true, nil
		case all[i].Status != "approved" && same(all[i]):
			return all[i], false, nil // pending or denied
		}
	}
	if i := slices.IndexFunc(all, covers); i >= 0 {
		return all[i], true, nil
	}
	if i := slices.IndexFunc(all, func(a Approval) bool { return a.Status == "pending" && same(a) }); i >= 0 {
		return all[i], false, nil
	}
	body, err := json.Marshal(refusals)
	if err != nil {
		return Approval{}, false, err
	}
	a := Approval{ID: newID(), MoveID: moveID, Number: number, Status: "pending", Reasons: body}
	err = one(q.InsertApproval(ctx, sqlcdb.InsertApprovalParams{CompanyID: sc.CompanyID, ID: a.ID, MoveID: moveID, Number: number,
		Reasons: body, RequestedBy: actor, RequestedAt: s.st.now()}))
	*notes = append(*notes, note{"approval", a.ID})
	return a, false, err
}

func reasonCodes(rs []Reason) []string {
	var out []string
	for _, r := range rs {
		if !slices.Contains(out, r.Code) {
			out = append(out, r.Code)
		}
	}
	slices.Sort(out)
	return out
}

func approvalCodes(a Approval) []string {
	var rs []Reason
	_ = json.Unmarshal(a.Reasons, &rs) // an unreadable row covers nothing
	return reasonCodes(rs)
}

func convertRows(rs []sqlcdb.MatchOtherOrderItemsRow) []sqlcdb.MatchOpenOrderItemsRow {
	out := make([]sqlcdb.MatchOpenOrderItemsRow, len(rs))
	for i, r := range rs {
		out[i] = sqlcdb.MatchOpenOrderItemsRow(r)
	}
	return out
}

// reverse undoes what a saved move did, without deleting any visit: it
// gives its order item back, closes the visit it opened ("voided"), and
// reopens the visits it closed. It returns the move's own visit, if any. A
// visit that cannot be reopened because the container has a newer open visit
// is ErrVisitConflict; the caller's transaction then changes nothing.
func reverse(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, m sqlcdb.LockMoveRow, now time.Time) (*Visit, error) {
	c := sc.CompanyID
	if err := q.LockNumber(ctx, sqlcdb.LockNumberParams{CompanyID: c, Number: m.Number}); err != nil {
		return nil, err
	}
	if m.OrderItemID != nil {
		if err := one(q.UseItem(ctx, sqlcdb.UseItemParams{CompanyID: c, ID: *m.OrderItemID, N: -1})); err != nil {
			return nil, err
		}
	}
	if err := q.UnlinkMove(ctx, sqlcdb.UnlinkMoveParams{CompanyID: c, ID: m.ID}); err != nil {
		return nil, err
	}
	visits, err := q.VisitsOfMove(ctx, sqlcdb.VisitsOfMoveParams{CompanyID: c, MoveID: m.ID})
	if err != nil {
		return nil, err
	}
	// The move's own visit first, so the visits it closed can reopen.
	var own *Visit
	for i, v := range visits {
		switch {
		case deref(v.InMoveID) == m.ID: // opened by this gate-in
			if v.Status != "in_yard" {
				return nil, ErrVisitMoved
			}
			err = closeVisitBy(ctx, q, c, v.ID, nil, now, "voided")
		case placeholder(v): // recorded closed by this gate-out
			err = q.SetVisitCloseReason(ctx, sqlcdb.SetVisitCloseReasonParams{CompanyID: c, ID: v.ID, CloseReason: "voided"})
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		own = &visits[i]
	}
	for _, v := range visits {
		if (own != nil && v.ID == own.ID) || placeholder(v) {
			continue // only real stays reopen, never a placeholder
		}
		_, err := q.InYardVisit(ctx, sqlcdb.InYardVisitParams{CompanyID: c, Number: v.Number})
		if err == nil {
			return nil, ErrVisitConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err := q.ReopenVisit(ctx, sqlcdb.ReopenVisitParams{CompanyID: c, ID: v.ID}); err != nil {
			return nil, err
		}
	}
	return own, nil
}

// placeholder is a visit a gate-out recorded for a container the yard did
// not know: no gate-in and not imported. It is never reopened.
func placeholder(v Visit) bool { return v.InMoveID == nil && !v.Imported }

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

func (s *Service) ListOrders(ctx context.Context, kind, status, q string) ([]Order, error) {
	return s.st.ListOrders(ctx, kind, status, q)
}

func (s *Service) GetOrder(ctx context.Context, id string) (Order, []OrderItem, error) {
	return s.st.GetOrder(ctx, id)
}

func (s *Service) FindVisits(ctx context.Context, f VisitFilter, limit, offset int) ([]VisitRow, int64, error) {
	return s.st.FindVisits(ctx, f, limit, offset)
}

func (s *Service) GetVisit(ctx context.Context, id string) (Visit, error) {
	return s.st.GetVisit(ctx, id)
}

func (s *Service) ListHolds(ctx context.Context, visitID string, openOnly bool) ([]Hold, error) {
	return s.st.ListHolds(ctx, visitID, openOnly)
}

func (s *Service) ListBlocks(ctx context.Context, activeOnly bool) ([]Block, error) {
	return s.st.ListBlocks(ctx, activeOnly)
}

func (s *Service) ListFlags(ctx context.Context, moveID string, openOnly bool) ([]Flag, error) {
	return s.st.ListFlags(ctx, moveID, openOnly)
}

func (s *Service) ResolveFlag(ctx context.Context, id, actor, note string) error {
	return s.st.ResolveFlag(ctx, id, actor, note)
}

func (s *Service) ListApprovals(ctx context.Context, status string) ([]Approval, error) {
	return s.st.ListApprovals(ctx, status)
}

func (s *Service) Settings(ctx context.Context) (Settings, error) { return s.st.Settings(ctx) }

func (s *Service) SetSettings(ctx context.Context, st Settings) error {
	return s.st.SetSettings(ctx, st)
}

func (s *Service) CreateOrder(ctx context.Context, o Order, items []OrderItem, actor string) (string, error) {
	return s.st.CreateOrder(ctx, o, items, actor)
}

// CancelOrder cancels an open order. Its remaining items are closed with it:
// they never match as open again, and a gate move against them is refused
// with order_not_open (passable per the refusal mode).
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

// FindEDIOrder returns the EDI-origin order of a line, kind and reference in
// any depot of the company, or ErrNotFound.
func (s *Service) FindEDIOrder(ctx context.Context, lineID, kind, reference string) (o Order, items []OrderItem, err error) {
	err = s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		if o, err = q.FindEDIOrder(ctx, sqlcdb.FindEDIOrderParams{CompanyID: sc.CompanyID, LineID: &lineID, Kind: kind, Reference: reference}); err != nil {
			return notFound(err)
		}
		items, err = q.ListOrderItems(ctx, sqlcdb.ListOrderItemsParams{CompanyID: sc.CompanyID, OrderID: o.ID})
		return err
	})
	return o, items, err
}

// ReplaceOrder replaces an order's items and validity (spec 5.8 rule 2).
// Named items match by number, quantity items by type and full/empty. What
// would undo gate history is kept and listed in kept: a used item missing from
// items, and a quantity below what was used. Unused missing items are deleted
// and new ones added. The order is done when every item is used up, else open;
// a cancelled order is ErrOrderClosed. An o.EdiMessageID relinks the order.
func (s *Service) ReplaceOrder(ctx context.Context, id string, o Order, items []OrderItem, actor string) (kept []string, err error) {
	err = s.tx(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ *[]note) error {
		cur, err := q.GetOrder(ctx, sqlcdb.GetOrderParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
		if err != nil {
			return notFound(err)
		}
		if cur.Status == "cancelled" {
			return ErrOrderClosed
		}
		old, err := q.ListOrderItems(ctx, sqlcdb.ListOrderItemsParams{CompanyID: sc.CompanyID, OrderID: id})
		if err != nil {
			return err
		}
		for i := range items {
			items[i] = normItem(items[i])
		}
		taken := make([]bool, len(items))
		done := true
		keep := func(it OrderItem) {
			if it.Number != nil {
				kept = append(kept, *it.Number+" already gated")
			} else {
				kept = append(kept, fmt.Sprintf("%s %s: %d used, kept %d", *it.TypeCode, it.FullEmpty, it.Used, it.Used))
			}
		}
		for _, it := range old {
			j := -1
			for k, n := range items {
				if !taken[k] && sameItem(it, n) {
					j = k
					break
				}
			}
			qty := it.Used // missing: kept at what was used, or deleted when unused
			if j >= 0 {
				taken[j], qty = true, items[j].Quantity
			}
			if qty == 0 {
				if err := one(q.DeleteOrderItem(ctx, sqlcdb.DeleteOrderItemParams{CompanyID: sc.CompanyID, OrderID: id, ID: it.ID})); err != nil {
					return err
				}
				continue
			}
			if j < 0 || qty < it.Used {
				keep(it)
				qty = it.Used
			}
			if qty != it.Quantity {
				if err := one(q.SetItemQuantity(ctx, sqlcdb.SetItemQuantityParams{CompanyID: sc.CompanyID, OrderID: id, ID: it.ID, Quantity: qty})); err != nil {
					return err
				}
			}
			done = done && it.Used == qty
		}
		for j, it := range items {
			if !taken[j] {
				if err := insertItem(ctx, q, sc.CompanyID, id, it); err != nil {
					return err
				}
				done = false
			}
		}
		if err := one(q.SetOrderValidUntil(ctx, sqlcdb.SetOrderValidUntilParams{CompanyID: sc.CompanyID, ID: id, ValidUntil: o.ValidUntil})); err != nil {
			return err
		}
		status := "open"
		if done {
			status = "done"
		}
		if err := one(q.SetOrderStatus(ctx, sqlcdb.SetOrderStatusParams{CompanyID: sc.CompanyID, ID: id, Status: status})); err != nil {
			return err
		}
		if o.EdiMessageID != nil {
			if err := one(q.SetOrderSource(ctx, sqlcdb.SetOrderSourceParams{CompanyID: sc.CompanyID, ID: id, Source: cur.Source, EdiMessageID: o.EdiMessageID})); err != nil {
				return err
			}
		}
		return s.event(ctx, q, sc, id, "order.replaced", actor, map[string]any{"kept": kept})
	})
	return kept, err
}

// sameItem says whether replacement item n is order item it: the same number
// (an unused one must keep its full/empty too), or the same type and full/empty.
func sameItem(it, n OrderItem) bool {
	if it.Number != nil {
		return n.Number != nil && *n.Number == *it.Number && (n.FullEmpty == it.FullEmpty || it.Used > 0)
	}
	return n.Number == nil && *n.TypeCode == *it.TypeCode && n.FullEmpty == it.FullEmpty
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
