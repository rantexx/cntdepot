package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"depot/internal/api"
	"depot/internal/yard"
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

// yardErrors map the yard's refusals of a request onto answers; the message is the yard error's own.
var yardErrors = []struct {
	err    error
	status int
	code   string
}{
	{yard.ErrNotFound, http.StatusNotFound, "E4043"},
	{yard.ErrInvalid, http.StatusBadRequest, "E4000"},
	{yard.ErrMissingLink, http.StatusUnprocessableEntity, "E4221"},
	{yard.ErrBadForm, http.StatusBadRequest, "E4000"},
	{yard.ErrReasonRequired, http.StatusBadRequest, "E4000"},
	{yard.ErrBadLink, http.StatusUnprocessableEntity, "E4221"},
	{yard.ErrVisitConflict, http.StatusConflict, "E4100"},
	{yard.ErrDirectionFixed, http.StatusConflict, "E4101"},
	{yard.ErrVoided, http.StatusConflict, "E4102"},
	{yard.ErrNotSaved, http.StatusConflict, "E4103"},
	{yard.ErrVisitMoved, http.StatusConflict, "E4104"},
	{yard.ErrOrderClosed, http.StatusConflict, "E4105"},
	{yard.ErrFormSaved, http.StatusConflict, "E4106"},
}

// yardError is the answer to a yard error, or nil. A wrapped one (the
// schema refused a write) is logged: the answer names no table or constraint.
func yardError(err error) *apiError {
	for _, e := range yardErrors {
		if errors.Is(err, e.err) {
			if err != e.err {
				log.Printf("httpapi: %v", err)
			}
			return &apiError{e.status, e.code, e.err.Error()}
		}
	}
	return nil
}

// Request text limits, as api/openapi.yaml states them (the generated server does not check them).
const (
	maxCode = 64  // codes, ids, numbers, plates, seals, positions
	maxName = 200 // names
	maxText = 500 // reasons, notes, damage text
)

// field is one request value to check: at most max characters and, when oneOf is set, one of those.
type field struct {
	name, v string
	max     int
	oneOf   []string
}

// checkFields answers 400 E4000 naming the first field out of bounds.
func checkFields(fs ...field) error {
	for _, f := range fs {
		if n := utf8.RuneCountInString(f.v); n > f.max {
			return fail(http.StatusBadRequest, "E4000", fmt.Sprintf("%s: at most %d characters", f.name, f.max))
		}
		if f.oneOf != nil && !slices.Contains(f.oneOf, f.v) {
			return fail(http.StatusBadRequest, "E4000", fmt.Sprintf("%s: one of %q", f.name, f.oneOf))
		}
	}
	return nil
}

func code(name string, v *string) field { return field{name: name, v: val(v), max: maxCode} }

func checkGateForm(f api.GateForm) error {
	fs := []field{
		{name: "direction", v: string(f.Direction), max: maxCode, oneOf: []string{"in", "out"}},
		{name: "number", v: f.Number, max: maxCode}, code("typeCode", f.TypeCode),
		{name: "fullEmpty", v: strings.ToUpper(strings.TrimSpace(val(f.FullEmpty))), max: maxCode, oneOf: []string{"", "E", "F"}},
		{name: "condition", v: strings.TrimSpace(val(f.Condition)), max: maxCode, oneOf: []string{"", "sound", "damaged"}},
		code("seal", f.Seal), code("lineId", f.LineId), code("customerId", f.CustomerId), code("haulierId", f.HaulierId),
		code("truckPlate", f.TruckPlate), code("trailerPlate", f.TrailerPlate),
		{name: "driverName", v: val(f.DriverName), max: maxName},
		code("signaturePhotoId", f.SignaturePhotoId), code("chosenOrderItemId", f.ChosenOrderItemId), code("approvalId", f.ApprovalId),
	}
	if f.Override != nil {
		fs = append(fs, field{name: "override.reason", v: f.Override.Reason, max: maxText})
	}
	for _, d := range val(f.Damages) {
		if len(d.PhotoIds) < 1 || len(d.PhotoIds) > 3 {
			return fail(http.StatusBadRequest, "E4000", "damages.photoIds: 1 to 3 photos per damage")
		}
		fs = append(fs, field{name: "damages.location", v: d.Location, max: maxText}, field{name: "damages.description", v: val(d.Description), max: maxText})
		for _, p := range d.PhotoIds {
			fs = append(fs, field{name: "damages.photoIds", v: p, max: maxCode})
		}
	}
	return checkFields(fs...)
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
	if err := checkGateForm(*req.Body); err != nil {
		return nil, err
	}
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
	if err := checkGateForm(req.Body.Form); err != nil {
		return nil, err
	}
	if err := checkFields(field{name: "reason", v: req.Body.Reason, max: maxText}); err != nil {
		return nil, err
	}
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

func (s *Server) TurnAwayMove(ctx context.Context, req api.TurnAwayMoveRequestObject) (api.TurnAwayMoveResponseObject, error) {
	fs := []field{{name: "number", v: req.Body.Number, max: maxCode}}
	reasons := make([]yard.Reason, 0, len(req.Body.Reasons))
	for _, r := range req.Body.Reasons {
		fs = append(fs, field{name: "reasons.code", v: r.Code, max: maxCode}, field{name: "reasons.detail", v: val(r.Detail), max: maxText})
		reasons = append(reasons, yard.Reason{Code: r.Code, Detail: val(r.Detail)})
	}
	if err := checkFields(fs...); err != nil {
		return nil, err
	}
	return api.TurnAwayMove204Response{}, s.opts.Yard.TurnAwayMove(ctx, req.Id, req.Body.Number, reasons, actor(ctx))
}

func (s *Server) VoidMove(ctx context.Context, req api.VoidMoveRequestObject) (api.VoidMoveResponseObject, error) {
	if err := checkFields(field{name: "reason", v: req.Body.Reason, max: maxText}); err != nil {
		return nil, err
	}
	return api.VoidMove204Response{}, s.opts.Yard.VoidGateMove(ctx, req.Id, req.Body.Reason, actor(ctx))
}

// formJSON is a move's gate record in the API's shape.
func formJSON(r yard.GateRecord) any {
	return struct {
		yard.MoveForm
		Damages []yard.Damage
		Flags   []yard.Flag
	}{r.MoveForm, list(r.Damages), list(r.Flags)}
}

func (s *Server) GetMoveForm(ctx context.Context, req api.GetMoveFormRequestObject) (api.GetMoveFormResponseObject, error) {
	r, err := s.opts.Yard.GateRecord(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return conv[api.GetMoveForm200JSONResponse](formJSON(r))
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
	fs := []field{{name: "kind", v: string(b.Kind), max: maxCode, oneOf: []string{"release", "redelivery"}},
		{name: "reference", v: o.Reference, max: maxCode}, code("lineId", b.LineId), code("customerId", b.CustomerId),
		{name: "note", v: o.Note, max: maxText}}
	for i, it := range b.Items {
		at := fmt.Sprintf("items[%d]", i)
		number, typeCode := strings.TrimSpace(val(it.Number)), strings.TrimSpace(val(it.TypeCode))
		switch q := val(it.Quantity); {
		case (number == "") == (typeCode == ""):
			return nil, fail(http.StatusBadRequest, "E4000", at+": a container number or an ISO type, exactly one of the two")
		case it.Quantity != nil && q < 1:
			return nil, fail(http.StatusBadRequest, "E4000", at+".quantity: at least 1")
		case number != "" && q > 1:
			return nil, fail(http.StatusBadRequest, "E4000", at+".quantity: a named container is quantity 1")
		}
		fs = append(fs, field{name: at + ".number", v: number, max: maxCode}, field{name: at + ".typeCode", v: typeCode, max: maxCode},
			field{name: at + ".fullEmpty", v: strings.ToUpper(strings.TrimSpace(val(it.FullEmpty))), max: maxCode, oneOf: []string{"", "E", "F"}})
	}
	if err := checkFields(fs...); err != nil {
		return nil, err
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
		Status: string(val(p.Status)), Number: val(p.Q), LineID: strings.TrimSpace(val(p.LineId)), CustomerID: strings.TrimSpace(val(p.CustomerId)),
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
	// form is a move's record; a move at another depot (a gate-out there) is out of scope: null.
	form := func(moveID *string) (any, error) {
		if moveID == nil {
			return nil, nil
		}
		r, err := s.opts.Yard.GateRecord(ctx, *moveID)
		if errors.Is(err, yard.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return formJSON(r), nil
	}
	in, err := form(v.InMoveID)
	if err != nil {
		return nil, err
	}
	out, err := form(v.OutMoveID)
	if err != nil {
		return nil, err
	}
	return conv[api.GetVisit200JSONResponse](struct {
		yard.Visit
		Holds   []yard.Hold
		InForm  any
		OutForm any
	}{v, list(holds), in, out})
}

func (s *Server) SetVisitPosition(ctx context.Context, req api.SetVisitPositionRequestObject) (api.SetVisitPositionResponseObject, error) {
	b := req.Body
	trim := strings.TrimSpace
	if err := checkFields(field{name: "block", v: trim(b.Block), max: maxCode}, field{name: "row", v: trim(b.Row), max: maxCode},
		field{name: "tier", v: trim(b.Tier), max: maxCode}); err != nil {
		return nil, err
	}
	if err := s.opts.Yard.SetPosition(ctx, req.Id, trim(b.Block), trim(b.Row), trim(b.Tier)); err != nil {
		return nil, err
	}
	v, err := s.visit(ctx, req.Id)
	return api.SetVisitPosition200JSONResponse(v), err
}

func (s *Server) SetVisitGrade(ctx context.Context, req api.SetVisitGradeRequestObject) (api.SetVisitGradeResponseObject, error) {
	grade := strings.ToUpper(strings.TrimSpace(req.Body.Grade))
	if err := checkFields(field{name: "grade", v: grade, max: maxCode, oneOf: []string{"", "A", "B", "C", "FOOD"}}); err != nil {
		return nil, err
	}
	if err := s.opts.Yard.SetGrade(ctx, req.Id, grade); err != nil {
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
	if err := checkFields(field{name: "reason", v: reason, max: maxText}); err != nil {
		return nil, err
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
	if err := checkFields(field{name: "kind", v: string(b.Kind), max: maxCode, oneOf: []string{"container", "line"}},
		field{name: "value", v: value, max: maxCode}, field{name: "reason", v: reason, max: maxText}); err != nil {
		return nil, err
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
	note := ""
	if req.Body != nil {
		note = val(req.Body.Note)
	}
	if err := checkFields(field{name: "note", v: note, max: maxText}); err != nil {
		return nil, err
	}
	return api.ResolveGateFlag204Response{}, s.opts.Yard.ResolveFlag(ctx, req.Id, actor(ctx), note)
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
	if err := checkFields(field{name: "refusalMode", v: string(b.RefusalMode), max: maxCode,
		oneOf: []string{"clerk_override", "manager_approval", "none"}}); err != nil {
		return nil, err
	}
	if b.EirTtlDays < 1 {
		return nil, fail(http.StatusBadRequest, "E4000", "eirTtlDays: at least 1")
	}
	if err := s.opts.Yard.SetSettings(ctx, yard.Settings{RefusalMode: string(b.RefusalMode), WalkinsAllowed: b.WalkinsAllowed,
		EirTtlDays: int64(b.EirTtlDays)}); err != nil {
		return nil, err
	}
	st, err := s.opts.Yard.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return conv[api.SetYardSettings200JSONResponse](st)
}
