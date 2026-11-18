// Package yard holds the depot's stock: release and redelivery orders,
// visits (a container's stay in the yard), holds, the block list, and the
// gate form's records (damages, flags, approvals, EIR tokens).
//
// Store calls run under the company in the context's tenancy.Scope, which
// row-level security enforces; without one they return tenancy.ErrNoScope.
// An empty scope depot means every depot of the company for reads and
// updates; creating a depot-owned row (an order or a visit) needs a depot.
// A row outside the scope is ErrNotFound.
package yard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"depot/internal/db"
	"depot/internal/iso6346"
	"depot/internal/tenancy"
	sqlcdb "depot/internal/yard/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// The records, as stored. On create, the store fills CompanyID, ID and the
// timestamps; the other zero values take the column defaults noted below.
type (
	Order      = sqlcdb.ReleaseOrder
	OrderItem  = sqlcdb.OrderItem // FullEmpty "" = "E", Quantity 0 = 1
	Visit      = sqlcdb.Visit     // Status "" = "in_yard", FullEmpty "" = "E"
	Hold       = sqlcdb.Hold
	Block      = sqlcdb.BlockList
	Damage     = sqlcdb.Damage
	Flag       = sqlcdb.GateFlag
	Approval   = sqlcdb.Approval
	Settings   = sqlcdb.CompanySetting
	LinePrefix = sqlcdb.LinePrefix
	MoveForm   = sqlcdb.GetMoveFormRow // the gate form columns of a gate move
)

var (
	ErrNotFound = errors.New("not found")
	// The schema refused a write: a check constraint (ErrInvalid) or a
	// reference to a row that does not exist (ErrMissingLink). The database
	// error is wrapped too, for the log.
	ErrInvalid     = errors.New("a value is not allowed")
	ErrMissingLink = errors.New("a linked record does not exist")
)

// Store is the yard's part of the database. The pool must connect as depot_app.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewStore returns a store on pool. The caller owns and closes the pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Store) run(ctx context.Context, fn func(q *sqlcdb.Queries, sc tenancy.Scope) error) error {
	sc, _ := tenancy.From(ctx)
	err := db.InCompany(ctx, s.pool, func(tx pgx.Tx) error { return fn(sqlcdb.New(tx), sc) })
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23514": // check_violation
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %w", ErrMissingLink, err)
		}
	}
	return err
}

func newID() string { return ulid.Make().String() }

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// one maps a write that touched no row to ErrNotFound.
func one(n int64, err error) error {
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func ptrTo[T any](v T) *T { return &v }

func blockValue(kind, value string) string {
	if kind == "container" {
		return iso6346.Normalize(value)
	}
	return norm(value)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// CreateOrder stores an open order with its items in the scope's depot and
// returns its id. Source "" is "manual"; an "edi" order links its message.
func (s *Store) CreateOrder(ctx context.Context, o Order, items []OrderItem, by string) (string, error) {
	id := newID()
	return id, s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		if sc.DepotID == "" {
			return tenancy.ErrNoScope // an order belongs to one depot
		}
		err := q.InsertOrder(ctx, sqlcdb.InsertOrderParams{
			CompanyID: sc.CompanyID, ID: id, DepotID: sc.DepotID, Kind: o.Kind, Reference: o.Reference,
			LineID: o.LineID, CustomerID: o.CustomerID, ValidUntil: o.ValidUntil, Note: o.Note, CreatedBy: by,
		})
		if err == nil && o.Source != "" {
			err = one(q.SetOrderSource(ctx, sqlcdb.SetOrderSourceParams{CompanyID: sc.CompanyID, ID: id, Source: o.Source, EdiMessageID: o.EdiMessageID}))
		}
		for _, it := range items {
			if err != nil {
				return err
			}
			err = insertItem(ctx, q, sc.CompanyID, id, it)
		}
		return err
	})
}

// normItem fills an item's defaults and normalizes it as the gate compares.
func normItem(it OrderItem) OrderItem {
	if it.Quantity == 0 {
		it.Quantity = 1
	}
	if it.Number != nil {
		it.Number = ptrTo(iso6346.Normalize(*it.Number))
	}
	if it.TypeCode != nil {
		it.TypeCode = ptrTo(iso6346.Normalize(*it.TypeCode))
	}
	it.FullEmpty = orDefault(it.FullEmpty, "E")
	return it
}

func insertItem(ctx context.Context, q *sqlcdb.Queries, companyID, orderID string, it OrderItem) error {
	it = normItem(it)
	return q.InsertOrderItem(ctx, sqlcdb.InsertOrderItemParams{
		CompanyID: companyID, ID: newID(), OrderID: orderID, Number: it.Number, TypeCode: it.TypeCode,
		FullEmpty: it.FullEmpty, Quantity: it.Quantity, Used: it.Used,
	})
}

// GetOrder returns an order and its items.
func (s *Store) GetOrder(ctx context.Context, id string) (o Order, items []OrderItem, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		if o, err = q.GetOrder(ctx, sqlcdb.GetOrderParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id}); err != nil {
			return notFound(err)
		}
		items, err = q.ListOrderItems(ctx, sqlcdb.ListOrderItemsParams{CompanyID: sc.CompanyID, OrderID: id})
		return err
	})
	return o, items, err
}

// ListOrders returns orders newest first; an empty kind or status matches
// all, and q matches anywhere in the reference, in any case.
func (s *Store) ListOrders(ctx context.Context, kind, status, q string) (out []Order, err error) {
	err = s.run(ctx, func(qs *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = qs.ListOrders(ctx, sqlcdb.ListOrdersParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, Kind: kind, Status: status, Q: q})
		return err
	})
	return out, err
}

func (s *Store) SetOrderStatus(ctx context.Context, id, status string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.SetOrderStatus(ctx, sqlcdb.SetOrderStatusParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, Status: status}))
	})
}

// UseItem adds n (negative to give back) to an item's used count. The schema
// refuses a count below 0 or above the quantity.
func (s *Store) UseItem(ctx context.Context, itemID string, n int64) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.UseItem(ctx, sqlcdb.UseItemParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: itemID, N: n}))
	})
}

// CreateVisit stores a visit in the scope's depot and returns its id. A
// second in_yard visit of a number in the company violates visits_one_in_yard.
// The number is stored normalised; a zero InAt is now.
func (s *Store) CreateVisit(ctx context.Context, v Visit) (string, error) {
	id := newID()
	if v.InAt.IsZero() {
		v.InAt = s.now()
	}
	return id, s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		if sc.DepotID == "" {
			return tenancy.ErrNoScope // a visit belongs to one depot
		}
		return q.InsertVisit(ctx, sqlcdb.InsertVisitParams{
			CompanyID: sc.CompanyID, ID: id, DepotID: sc.DepotID, Number: iso6346.Normalize(v.Number), TypeCode: v.TypeCode,
			LineID: v.LineID, CustomerID: v.CustomerID, Status: orDefault(v.Status, "in_yard"),
			FullEmpty: orDefault(v.FullEmpty, "E"), Grade: v.Grade, Block: v.Block, RowNo: v.RowNo, Tier: v.Tier,
			InMoveID: v.InMoveID, OutMoveID: v.OutMoveID, InAt: v.InAt, OutAt: v.OutAt, Imported: v.Imported,
			CloseReason: v.CloseReason,
		})
	})
}

func (s *Store) GetVisit(ctx context.Context, id string) (v Visit, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		v, err = q.GetVisit(ctx, sqlcdb.GetVisitParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
		return notFound(err)
	})
	return v, err
}

// InYardVisit returns the open visit of a container number.
func (s *Store) InYardVisit(ctx context.Context, number string) (v Visit, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		v, err = q.InYardVisit(ctx, sqlcdb.InYardVisitParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, Number: iso6346.Normalize(number)})
		return notFound(err)
	})
	return v, err
}

// ListVisits returns visits newest first; an empty status matches all.
func (s *Store) ListVisits(ctx context.Context, status string) (out []Visit, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = q.ListVisits(ctx, sqlcdb.ListVisitsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, Status: status})
		return err
	})
	return out, err
}

// VisitFilter narrows the yard list. Zero values match all; TypeCode
// matches as a prefix ("45" is every 45 type), MinDays is depot-local calendar days in the yard,
// and Number matches anywhere in the container number.
type VisitFilter struct {
	Status, Number, LineID, CustomerID, TypeCode, Grade string
	OnHold                                              bool
	MinDays                                             int
}

// VisitRow is a visit in the yard list, with whether it has an open hold.
type VisitRow = sqlcdb.FindVisitsRow

// FindVisits returns one page of the filtered visits, newest first, and the
// number of all that match.
func (s *Store) FindVisits(ctx context.Context, f VisitFilter, limit, offset int) (out []VisitRow, total int64, err error) {
	now := s.now() // days in the yard are depot-local calendar days at this moment, as in the ageing report
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		p := sqlcdb.CountVisitsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, Status: f.Status, Number: iso6346.Normalize(f.Number), LineID: f.LineID,
			CustomerID: f.CustomerID, TypeCode: iso6346.Normalize(f.TypeCode), Grade: f.Grade, OnHoldOnly: f.OnHold, MinDays: int64(f.MinDays), AtTime: now}
		if total, err = q.CountVisits(ctx, p); err != nil {
			return err
		}
		out, err = q.FindVisits(ctx, sqlcdb.FindVisitsParams{CompanyID: p.CompanyID, DepotID: p.DepotID, Status: p.Status, Number: p.Number,
			LineID: p.LineID, CustomerID: p.CustomerID, TypeCode: p.TypeCode, Grade: p.Grade, OnHoldOnly: p.OnHoldOnly,
			MinDays: p.MinDays, AtTime: p.AtTime, Lim: int64(limit), Off: int64(offset)})
		return err
	})
	return out, total, err
}

// CloseVisit marks an open visit out; a closed one is ErrNotFound. outMoveID is nil when no gate move closed it.
func (s *Store) CloseVisit(ctx context.Context, id string, outMoveID *string, outAt time.Time, reason string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.CloseVisit(ctx, sqlcdb.CloseVisitParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, OutMoveID: outMoveID, OutAt: &outAt, CloseReason: reason,
		}))
	})
}

func (s *Store) SetVisitPosition(ctx context.Context, id, block, row, tier string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.SetVisitPosition(ctx, sqlcdb.SetVisitPositionParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, Block: block, RowNo: row, Tier: tier,
		}))
	})
}

func (s *Store) SetVisitGrade(ctx context.Context, id, grade string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.SetVisitGrade(ctx, sqlcdb.SetVisitGradeParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, Grade: grade}))
	})
}

// PlaceHold puts a hold on a visit and returns its id.
func (s *Store) PlaceHold(ctx context.Context, visitID, reason, by string) (string, error) {
	id := newID()
	return id, s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.InsertHold(ctx, sqlcdb.InsertHoldParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, VisitID: visitID, Reason: reason, PlacedBy: by, PlacedAt: s.now(),
		}))
	})
}

// ReleaseHold releases an open hold; an already released one is ErrNotFound.
func (s *Store) ReleaseHold(ctx context.Context, id, by string) error {
	now := s.now()
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.ReleaseHold(ctx, sqlcdb.ReleaseHoldParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, ReleasedBy: by, ReleasedAt: &now,
		}))
	})
}

// ListHolds returns the holds of a visit ("" = every visit), optionally only open ones.
func (s *Store) ListHolds(ctx context.Context, visitID string, openOnly bool) (out []Hold, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = q.ListHolds(ctx, sqlcdb.ListHoldsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, VisitID: visitID, OpenOnly: openOnly})
		return err
	})
	return out, err
}

// UpsertBlock adds or re-activates a block-list entry (company-wide) and
// returns its id. A container value is a number, a line value a line code;
// both are stored normalised.
func (s *Store) UpsertBlock(ctx context.Context, kind, value, reason, by string) (id string, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		id, err = q.UpsertBlock(ctx, sqlcdb.UpsertBlockParams{
			CompanyID: sc.CompanyID, ID: newID(), Kind: kind, Value: blockValue(kind, value), Reason: reason, CreatedBy: by, CreatedAt: s.now(),
		})
		return err
	})
	return id, err
}

func (s *Store) SetBlockActive(ctx context.Context, id string, active bool) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.SetBlockActive(ctx, sqlcdb.SetBlockActiveParams{CompanyID: sc.CompanyID, ID: id, Active: active}))
	})
}

func (s *Store) ListBlocks(ctx context.Context, activeOnly bool) (out []Block, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = q.ListBlocks(ctx, sqlcdb.ListBlocksParams{CompanyID: sc.CompanyID, ActiveOnly: activeOnly})
		return err
	})
	return out, err
}

// AddDamage records a damage on a gate move (1 to 3 photos) and returns its id.
func (s *Store) AddDamage(ctx context.Context, moveID, location, description string, photoIDs []string) (string, error) {
	id := newID()
	return id, s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.InsertDamage(ctx, sqlcdb.InsertDamageParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, MoveID: moveID, Location: location,
			Description: description, PhotoIds: photoIDs,
		}))
	})
}

func (s *Store) ListDamages(ctx context.Context, moveID string) (out []Damage, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = q.ListDamages(ctx, sqlcdb.ListDamagesParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, MoveID: moveID})
		return err
	})
	return out, err
}

// AddFlag marks a gate move for the office and returns the flag's id.
func (s *Store) AddFlag(ctx context.Context, moveID, code, detail string) (string, error) {
	id := newID()
	return id, s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.InsertFlag(ctx, sqlcdb.InsertFlagParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, MoveID: moveID, Code: code, Detail: detail,
		}))
	})
}

// ListFlags returns the flags of a move ("" = every move), optionally only unresolved ones.
func (s *Store) ListFlags(ctx context.Context, moveID string, openOnly bool) (out []Flag, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = q.ListFlags(ctx, sqlcdb.ListFlagsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, MoveID: moveID, OpenOnly: openOnly})
		return err
	})
	return out, err
}

// ResolveFlag resolves an open flag; an already resolved one is ErrNotFound.
func (s *Store) ResolveFlag(ctx context.Context, id, by, note string) error {
	now := s.now()
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.ResolveFlag(ctx, sqlcdb.ResolveFlagParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, ResolvedBy: by, ResolvedAt: &now, ResolveNote: note,
		}))
	})
}

// CreateApproval asks a manager to pass a refused move and returns its id.
func (s *Store) CreateApproval(ctx context.Context, moveID string, reasons json.RawMessage, by string) (string, error) {
	if len(reasons) == 0 {
		reasons = json.RawMessage("[]")
	}
	id := newID()
	return id, s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.InsertApproval(ctx, sqlcdb.InsertApprovalParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, MoveID: moveID, Reasons: reasons,
			RequestedBy: by, RequestedAt: s.now(),
		}))
	})
}

func (s *Store) GetApproval(ctx context.Context, id string) (a Approval, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		a, err = q.GetApproval(ctx, sqlcdb.GetApprovalParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
		return notFound(err)
	})
	return a, err
}

// ListApprovals returns approvals oldest first; an empty status matches all.
func (s *Store) ListApprovals(ctx context.Context, status string) (out []Approval, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = q.ListApprovals(ctx, sqlcdb.ListApprovalsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, Status: status})
		return err
	})
	return out, err
}

// DecideApproval approves or denies a pending approval; one already decided is ErrNotFound.
func (s *Store) DecideApproval(ctx context.Context, id string, approve bool, by string) error {
	status, now := "denied", s.now()
	if approve {
		status = "approved"
	}
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.DecideApproval(ctx, sqlcdb.DecideApprovalParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id, Status: status, DecidedBy: by, DecidedAt: &now,
		}))
	})
}

// AddEIRToken stores the SHA-256 of an EIR link token for a gate move.
func (s *Store) AddEIRToken(ctx context.Context, moveID, tokenHash string, expires time.Time, by string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.InsertEIRToken(ctx, sqlcdb.InsertEIRTokenParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: newID(), MoveID: moveID, TokenHash: tokenHash,
			ExpiresAt: expires, CreatedBy: by,
		}))
	})
}

// LookupEIR finds the company and move of a live token hash. It needs no
// scope: the public EIR page has only the token. Unknown or expired is ErrNotFound.
func (s *Store) LookupEIR(ctx context.Context, tokenHash string) (companyID, moveID string, err error) {
	err = db.Global(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := sqlcdb.New(tx).EIRLookup(ctx, tokenHash)
		companyID, moveID = r.CompanyID, r.MoveID
		return notFound(err)
	})
	return companyID, moveID, err
}

// Settings returns the company's yard settings, or the defaults when none are saved.
func (s *Store) Settings(ctx context.Context) (st Settings, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		st, err = settingsOf(ctx, q, sc.CompanyID)
		return err
	})
	return st, err
}

func settingsOf(ctx context.Context, q *sqlcdb.Queries, companyID string) (Settings, error) {
	st, err := q.GetSettings(ctx, companyID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The column defaults of company_settings in 002_yard.sql.
		return Settings{CompanyID: companyID, RefusalMode: "clerk_override", WalkinsAllowed: true, EirTtlDays: 30}, nil
	}
	return st, err
}

// SetSettings saves the company's yard settings (st.CompanyID is ignored).
func (s *Store) SetSettings(ctx context.Context, st Settings) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return q.UpsertSettings(ctx, sqlcdb.UpsertSettingsParams{
			CompanyID: sc.CompanyID, RefusalMode: st.RefusalMode, WalkinsAllowed: st.WalkinsAllowed, EirTtlDays: st.EirTtlDays,
		})
	})
}

// SetLinePrefix remembers the line last used for an owner prefix.
func (s *Store) SetLinePrefix(ctx context.Context, prefix, lineID string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return q.UpsertLinePrefix(ctx, sqlcdb.UpsertLinePrefixParams{CompanyID: sc.CompanyID, Prefix: prefix, LineID: lineID})
	})
}

func (s *Store) LinePrefixes(ctx context.Context) (out []LinePrefix, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		out, err = q.ListLinePrefixes(ctx, sc.CompanyID)
		return err
	})
	return out, err
}

// SetMoveForm stores the clerk's gate form on a move and stamps form_saved_at.
// f.ID, f.FormSavedAt, f.Voided and f.VoidReason are ignored.
func (s *Store) SetMoveForm(ctx context.Context, moveID string, f MoveForm) error {
	now := s.now()
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.SetMoveForm(ctx, sqlcdb.SetMoveFormParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: moveID, VisitID: f.VisitID, OrderID: f.OrderID,
			OrderItemID: f.OrderItemID, TruckPlate: f.TruckPlate, TrailerPlate: f.TrailerPlate, DriverName: f.DriverName,
			HaulierID: f.HaulierID, Seal: f.Seal, FullEmpty: f.FullEmpty, Condition: f.Condition,
			SignaturePhotoID: f.SignaturePhotoID, FormSavedAt: now,
		}))
	})
}

func (s *Store) GetMoveForm(ctx context.Context, moveID string) (f MoveForm, err error) {
	err = s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		f, err = q.GetMoveForm(ctx, sqlcdb.GetMoveFormParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: moveID})
		return notFound(err)
	})
	return f, err
}

// VoidMove marks a gate move void with a reason.
func (s *Store) VoidMove(ctx context.Context, moveID, reason string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) error {
		return one(q.VoidMove(ctx, sqlcdb.VoidMoveParams{
			CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: moveID, VoidReason: reason, UpdatedAt: s.now(),
		}))
	})
}
