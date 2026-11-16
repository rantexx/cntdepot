package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"depot/internal/api"
	"depot/internal/yard"

	"github.com/jackc/pgx/v5/pgconn"
)

// The gate form and the yard's records. Records reach the API through conv:
// sqlc rows carry no json tags, and encoding/json matches field names without
// regard to case, so a row's DepotID fills the API's depotId.

// list is s, or an empty slice: a JSON list is [], never null.
func list[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// optText is the trimmed *p, or nil when that is empty.
func optText(p *string) *string {
	if t := strings.TrimSpace(val(p)); t != "" {
		return &t
	}
	return nil
}

// yardErrors map the yard's refusals of a request onto answers; the message is the error's own.
var yardErrors = []struct {
	err    error
	status int
	code   string
}{
	{yard.ErrNotFound, http.StatusNotFound, "E4043"},
	{yard.ErrBadForm, http.StatusBadRequest, "E4000"},
	{yard.ErrReasonRequired, http.StatusBadRequest, "E4000"},
	{yard.ErrBadLink, http.StatusUnprocessableEntity, "E4221"},
	{yard.ErrVisitConflict, http.StatusConflict, "E4100"},
	{yard.ErrDirectionFixed, http.StatusConflict, "E4101"},
	{yard.ErrVoided, http.StatusConflict, "E4102"},
	{yard.ErrNotSaved, http.StatusConflict, "E4103"},
	{yard.ErrVisitMoved, http.StatusConflict, "E4104"},
	{yard.ErrOrderClosed, http.StatusConflict, "E4105"},
}

// yardError is the answer to a yard error, or to a value the schema refuses.
func yardError(err error) *apiError {
	for _, e := range yardErrors {
		if errors.Is(err, e.err) {
			return &apiError{e.status, e.code, err.Error()}
		}
	}
	var pg *pgconn.PgError
	switch {
	case !errors.As(err, &pg):
	case pg.Code == "23514": // check_violation: a kind, grade, mode or quantity out of range
		return &apiError{http.StatusBadRequest, "E4000", "a value is not allowed: " + pg.ConstraintName}
	case pg.Code == "23503": // foreign_key_violation: a line, customer or haulier not of this company
		return &apiError{http.StatusUnprocessableEntity, "E4221", "a linked record does not exist"}
	}
	return nil
}

// gateAnswer is a save's 200 body, or its 409 body when nothing was applied:
// refused (E4098), blocked (E4099), or several order items to choose from (E4098).
func gateAnswer(res yard.Result) (ok api.GateResult, refused *api.GateRefusal, err error) {
	if res.Refused == nil && len(res.Candidates) == 0 {
		ok, err = conv[api.GateResult](map[string]any{"move": res.Move, "visitId": res.VisitID, "flags": list(res.Flags)})
		return ok, nil, err
	}
	code, msg := "E4098", "the gate rules refuse this move"
	v := map[string]any{"refusals": []yard.Reason{}}
	if r := res.Refused; r != nil {
		v["refusals"] = list(r.Reasons)
		if r.Blocked {
			code, msg = "E4099", "the container or its line is on the block list"
		}
		if r.ApprovalID != "" {
			v["approvalId"], v["approvalStatus"] = r.ApprovalID, r.ApprovalStatus
		}
	} else {
		msg = "several order items match: choose one"
	}
	if len(res.Candidates) > 0 {
		v["candidates"] = res.Candidates
	}
	v["error"] = map[string]string{"code": code, "message": msg}
	out, err := conv[api.GateRefusal](v)
	return ok, &out, err
}

func (s *Server) SaveGateForm(ctx context.Context, req api.SaveGateFormRequestObject) (api.SaveGateFormResponseObject, error) {
	f, err := conv[yard.GateForm](req.Body)
	if err != nil {
		return nil, err
	}
	f.MoveID = req.Id
	res, err := s.opts.Yard.SaveGateMove(ctx, f, actor(ctx))
	if err != nil {
		return nil, err
	}
	ok, refused, err := gateAnswer(res)
	if refused != nil {
		return api.SaveGateForm409JSONResponse(*refused), err
	}
	return api.SaveGateForm200JSONResponse(ok), err
}

func (s *Server) CorrectMove(ctx context.Context, req api.CorrectMoveRequestObject) (api.CorrectMoveResponseObject, error) {
	f, err := conv[yard.GateForm](req.Body.Form)
	if err != nil {
		return nil, err
	}
	res, err := s.opts.Yard.CorrectGateMove(ctx, req.Id, f, req.Body.Reason, actor(ctx))
	if err != nil {
		return nil, err
	}
	ok, refused, err := gateAnswer(res)
	if refused != nil {
		return api.CorrectMove409JSONResponse(*refused), err
	}
	return api.CorrectMove200JSONResponse(ok), err
}

func (s *Server) VoidMove(ctx context.Context, req api.VoidMoveRequestObject) (api.VoidMoveResponseObject, error) {
	return api.VoidMove204Response{}, s.opts.Yard.VoidGateMove(ctx, req.Id, req.Body.Reason, actor(ctx))
}

func (s *Server) CreateEirLink(ctx context.Context, req api.CreateEirLinkRequestObject) (api.CreateEirLinkResponseObject, error) {
	token, expires, err := s.opts.Yard.CreateEIRToken(ctx, req.Id, actor(ctx))
	if err != nil {
		return nil, err
	}
	return api.CreateEirLink200JSONResponse{Url: "/eir/" + token, ExpiresAt: expires}, nil
}

func (s *Server) GateSnapshot(ctx context.Context, _ api.GateSnapshotRequestObject) (api.GateSnapshotResponseObject, error) {
	snap, err := s.opts.Yard.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.opts.Yard.Settings(ctx)
	if err != nil {
		return nil, err
	}
	orders := make([]any, len(snap.Orders))
	for i, o := range snap.Orders {
		orders[i] = orderJSON(o.Order, o.Items)
	}
	return conv[api.GateSnapshot200JSONResponse](map[string]any{
		"at": snap.At, "orders": orders, "blocks": list(snap.Blocks), "holds": list(snap.Holds),
		"inYard": list(snap.InYard), "linePrefixes": list(snap.LinePrefixes), "settings": settings,
	})
}

// ---- orders ----

// orderJSON is an order with its items in the API's shape; valid_until is a calendar date.
func orderJSON(o yard.Order, items []yard.OrderItem) any {
	var until *string
	if o.ValidUntil != nil {
		d := o.ValidUntil.Format(time.DateOnly)
		until = &d
	}
	return struct {
		yard.Order
		ValidUntil *string
		Items      []yard.OrderItem
	}{o, until, list(items)}
}

func (s *Server) ListOrders(ctx context.Context, req api.ListOrdersRequestObject) (api.ListOrdersResponseObject, error) {
	p := req.Params
	orders, err := s.opts.Yard.ListOrders(ctx, string(val(p.Kind)), string(val(p.Status)), strings.TrimSpace(val(p.Q)))
	if err != nil {
		return nil, err
	}
	out := make([]any, len(orders))
	for i, o := range orders {
		out[i] = orderJSON(o, nil)
	}
	return conv[api.ListOrders200JSONResponse](out)
}

func (s *Server) order(ctx context.Context, id string) (api.OrderDetail, error) {
	o, items, err := s.opts.Yard.GetOrder(ctx, id)
	if err != nil {
		return api.OrderDetail{}, err
	}
	return conv[api.OrderDetail](orderJSON(o, items))
}

func (s *Server) CreateOrder(ctx context.Context, req api.CreateOrderRequestObject) (api.CreateOrderResponseObject, error) {
	b := req.Body
	o := yard.Order{Kind: string(b.Kind), Reference: strings.TrimSpace(b.Reference), LineID: optText(b.LineId),
		CustomerID: optText(b.CustomerId), Note: strings.TrimSpace(val(b.Note))}
	if o.Reference == "" || len(b.Items) == 0 {
		return nil, fail(http.StatusBadRequest, "E4008", "an order needs a reference and at least one item")
	}
	if b.ValidUntil != nil {
		o.ValidUntil = &b.ValidUntil.Time
	}
	items := make([]yard.OrderItem, len(b.Items))
	for i, it := range b.Items {
		items[i] = yard.OrderItem{Number: optText(it.Number), TypeCode: optText(it.TypeCode),
			FullEmpty: strings.ToUpper(strings.TrimSpace(val(it.FullEmpty))), Quantity: int64(val(it.Quantity))}
	}
	id, err := s.opts.Yard.CreateOrder(ctx, o, items, actor(ctx))
	if err != nil {
		return nil, err
	}
	out, err := s.order(ctx, id)
	return api.CreateOrder201JSONResponse(out), err
}

func (s *Server) GetOrder(ctx context.Context, req api.GetOrderRequestObject) (api.GetOrderResponseObject, error) {
	out, err := s.order(ctx, req.Id)
	return api.GetOrder200JSONResponse(out), err
}

func (s *Server) CancelOrder(ctx context.Context, req api.CancelOrderRequestObject) (api.CancelOrderResponseObject, error) {
	return api.CancelOrder204Response{}, s.opts.Yard.CancelOrder(ctx, req.Id, actor(ctx))
}

// ---- visits and holds ----

func (s *Server) ListVisits(ctx context.Context, req api.ListVisitsRequestObject) (api.ListVisitsResponseObject, error) {
	p := req.Params
	limit, offset := pageOf(p.Page, p.PerPage)
	rows, total, err := s.opts.Yard.FindVisits(ctx, yard.VisitFilter{
		Status: string(val(p.Status)), LineID: strings.TrimSpace(val(p.LineId)), CustomerID: strings.TrimSpace(val(p.CustomerId)),
		TypeCode: val(p.Type), Grade: strings.ToUpper(strings.TrimSpace(val(p.Grade))), OnHold: val(p.Hold), MinDays: val(p.MinDays),
	}, limit, offset)
	if err != nil {
		return nil, err
	}
	return conv[api.ListVisits200JSONResponse](map[string]any{"items": list(rows), "total": total})
}

func (s *Server) visit(ctx context.Context, id string) (api.Visit, error) {
	v, err := s.opts.Yard.GetVisit(ctx, id)
	if err != nil {
		return api.Visit{}, err
	}
	return conv[api.Visit](v)
}

func (s *Server) GetVisit(ctx context.Context, req api.GetVisitRequestObject) (api.GetVisitResponseObject, error) {
	v, err := s.opts.Yard.GetVisit(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	holds, err := s.opts.Yard.ListHolds(ctx, req.Id, false)
	if err != nil {
		return nil, err
	}
	return conv[api.GetVisit200JSONResponse](struct {
		yard.Visit
		Holds []yard.Hold
	}{v, list(holds)})
}

func (s *Server) SetVisitPosition(ctx context.Context, req api.SetVisitPositionRequestObject) (api.SetVisitPositionResponseObject, error) {
	b := req.Body
	trim := strings.TrimSpace
	if err := s.opts.Yard.SetPosition(ctx, req.Id, trim(b.Block), trim(b.Row), trim(b.Tier)); err != nil {
		return nil, err
	}
	v, err := s.visit(ctx, req.Id)
	return api.SetVisitPosition200JSONResponse(v), err
}

func (s *Server) SetVisitGrade(ctx context.Context, req api.SetVisitGradeRequestObject) (api.SetVisitGradeResponseObject, error) {
	if err := s.opts.Yard.SetGrade(ctx, req.Id, strings.ToUpper(strings.TrimSpace(req.Body.Grade))); err != nil {
		return nil, err
	}
	v, err := s.visit(ctx, req.Id)
	return api.SetVisitGrade200JSONResponse(v), err
}

func (s *Server) PlaceHold(ctx context.Context, req api.PlaceHoldRequestObject) (api.PlaceHoldResponseObject, error) {
	reason := strings.TrimSpace(req.Body.Reason)
	if reason == "" {
		return nil, fail(http.StatusBadRequest, "E4008", "a hold needs a reason")
	}
	id, err := s.opts.Yard.PlaceHold(ctx, req.Id, reason, actor(ctx))
	if err != nil {
		return nil, err
	}
	holds, err := s.opts.Yard.ListHolds(ctx, req.Id, true)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(holds, func(h yard.Hold) bool { return h.ID == id })
	if i < 0 {
		return nil, yard.ErrNotFound
	}
	return conv[api.PlaceHold201JSONResponse](holds[i])
}

func (s *Server) ReleaseHold(ctx context.Context, req api.ReleaseHoldRequestObject) (api.ReleaseHoldResponseObject, error) {
	return api.ReleaseHold204Response{}, s.opts.Yard.ReleaseHold(ctx, req.Id, actor(ctx))
}

// ---- block list ----

func (s *Server) ListBlocks(ctx context.Context, _ api.ListBlocksRequestObject) (api.ListBlocksResponseObject, error) {
	blocks, err := s.opts.Yard.ListBlocks(ctx, false)
	if err != nil {
		return nil, err
	}
	return conv[api.ListBlocks200JSONResponse](list(blocks))
}

func (s *Server) block(ctx context.Context, id string) (api.Block, error) {
	blocks, err := s.opts.Yard.ListBlocks(ctx, false)
	if err != nil {
		return api.Block{}, err
	}
	i := slices.IndexFunc(blocks, func(b yard.Block) bool { return b.ID == id })
	if i < 0 {
		return api.Block{}, yard.ErrNotFound
	}
	return conv[api.Block](blocks[i])
}

func (s *Server) CreateBlock(ctx context.Context, req api.CreateBlockRequestObject) (api.CreateBlockResponseObject, error) {
	b := req.Body
	value, reason := strings.TrimSpace(b.Value), strings.TrimSpace(b.Reason)
	if value == "" || reason == "" {
		return nil, fail(http.StatusBadRequest, "E4008", "a block needs a value and a reason")
	}
	id, err := s.opts.Yard.UpsertBlock(ctx, string(b.Kind), value, reason, actor(ctx))
	if err != nil {
		return nil, err
	}
	out, err := s.block(ctx, id)
	return api.CreateBlock201JSONResponse(out), err
}

func (s *Server) SetBlockActive(ctx context.Context, req api.SetBlockActiveRequestObject) (api.SetBlockActiveResponseObject, error) {
	if err := s.opts.Yard.SetBlockActive(ctx, req.Id, req.Body.Active); err != nil {
		return nil, err
	}
	out, err := s.block(ctx, req.Id)
	return api.SetBlockActive200JSONResponse(out), err
}

// ---- approvals, gate flags, settings ----

func (s *Server) ListApprovals(ctx context.Context, req api.ListApprovalsRequestObject) (api.ListApprovalsResponseObject, error) {
	out, err := s.opts.Yard.ListApprovals(ctx, string(val(req.Params.Status)))
	if err != nil {
		return nil, err
	}
	return conv[api.ListApprovals200JSONResponse](list(out))
}

func (s *Server) DecideApproval(ctx context.Context, req api.DecideApprovalRequestObject) (api.DecideApprovalResponseObject, error) {
	return api.DecideApproval204Response{}, s.opts.Yard.DecideApproval(ctx, req.Id, req.Body.Approve, actor(ctx))
}

func (s *Server) ListGateFlags(ctx context.Context, req api.ListGateFlagsRequestObject) (api.ListGateFlagsResponseObject, error) {
	out, err := s.opts.Yard.ListFlags(ctx, val(req.Params.MoveId), val(req.Params.Open))
	if err != nil {
		return nil, err
	}
	return conv[api.ListGateFlags200JSONResponse](list(out))
}

func (s *Server) ResolveGateFlag(ctx context.Context, req api.ResolveGateFlagRequestObject) (api.ResolveGateFlagResponseObject, error) {
	return api.ResolveGateFlag204Response{}, s.opts.Yard.ResolveFlag(ctx, req.Id, actor(ctx))
}

func (s *Server) GetYardSettings(ctx context.Context, _ api.GetYardSettingsRequestObject) (api.GetYardSettingsResponseObject, error) {
	st, err := s.opts.Yard.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return conv[api.GetYardSettings200JSONResponse](st)
}

func (s *Server) SetYardSettings(ctx context.Context, req api.SetYardSettingsRequestObject) (api.SetYardSettingsResponseObject, error) {
	b := req.Body
	if err := s.opts.Yard.SetSettings(ctx, yard.Settings{RefusalMode: b.RefusalMode, WalkinsAllowed: b.WalkinsAllowed,
		EirTtlDays: int64(b.EirTtlDays)}); err != nil {
		return nil, err
	}
	st, err := s.opts.Yard.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return conv[api.SetYardSettings200JSONResponse](st)
}
