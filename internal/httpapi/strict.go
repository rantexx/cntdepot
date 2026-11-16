package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"depot/internal/api"
	"depot/internal/auth"
	"depot/internal/db"
	"depot/internal/gate"
	"depot/internal/iso6346"
	"depot/internal/store"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

var _ api.StrictServerInterface = (*Server)(nil)

// Who may call an operation, beyond the role permissions of package auth.
const (
	permPublic   auth.Perm = "public"    // anyone, signed in or not
	permSignedIn auth.Perm = "signed-in" // any session, company chosen or not
	permPlatform auth.Perm = "platform"  // platform admins
)

// operationAccess is what each generated operation needs. An operation
// missing here is refused (TestEveryOperationHasAPermission catches it).
var operationAccess = map[string]auth.Perm{
	"GetHealth": permPublic,

	"GetSession": permSignedIn, "SetScope": permSignedIn, "Logout": permSignedIn,

	"ListCompanies": permPlatform, "CreateCompany": permPlatform,

	"ListMoves": auth.PermGate, "CreateMove": auth.PermGate, "GetMove": auth.PermGate, "AddPhoto": auth.PermGate,
	"ResolveMove": auth.PermGate, "EnterNumber": auth.PermGate, "ValidateNumber": auth.PermGate,
	"GetCompany": auth.PermGate, "ListDepots": auth.PermGate,
	"ListLines": auth.PermGate, "ListCustomers": auth.PermGate, "ListHauliers": auth.PermGate,
	"SearchLiners": auth.PermGate, "SearchLocodes": auth.PermGate,

	"RereadMove": auth.PermOffice, "TestRead": auth.PermOffice,
	"CreateLine": auth.PermOffice, "UpdateLine": auth.PermOffice, "DeactivateLine": auth.PermOffice,
	"CreateCustomer": auth.PermOffice, "UpdateCustomer": auth.PermOffice, "DeactivateCustomer": auth.PermOffice,
	"CreateHaulier": auth.PermOffice, "UpdateHaulier": auth.PermOffice, "DeactivateHaulier": auth.PermOffice,

	"SetModules": auth.PermOwner, "CreateDepot": auth.PermOwner,
	"ListMembers": auth.PermOwner, "UpdateMember": auth.PermOwner, "RemoveMember": auth.PermOwner,
	"ListInvitations": auth.PermOwner, "CreateInvitation": auth.PermOwner, "DeleteInvitation": auth.PermOwner,
}

type requestKey struct{}

// access is the strict middleware: it checks operationAccess and puts the
// request in the context for the operations that need its cookie.
func (s *Server) access(f api.StrictHandlerFunc, op string) api.StrictHandlerFunc {
	need, known := operationAccess[op]
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		ctx = context.WithValue(ctx, requestKey{}, r)
		cur, signedIn := auth.CurrentFrom(ctx)
		switch {
		case !known:
			return nil, fail(http.StatusForbidden, "E4030", "this operation is not open to anyone")
		case need == permPublic:
			return f(ctx, w, r, req)
		case !signedIn:
			return nil, fail(http.StatusUnauthorized, "E4010", "sign in first")
		case need == permSignedIn:
			return f(ctx, w, r, req)
		case need == permPlatform:
			if !cur.PlatformAdmin {
				return nil, fail(http.StatusForbidden, "E4030", "platform admins only")
			}
			return f(ctx, w, r, req)
		}
		sc, ok := tenancy.From(ctx)
		if !ok {
			return nil, fail(http.StatusConflict, "E4090", "choose a company")
		}
		if !auth.Can(sc.Role, need) {
			return nil, fail(http.StatusForbidden, "E4030", "your role does not allow this")
		}
		return f(ctx, w, r, req)
	}
}

// current is the signed-in user; access guarantees one for every operation that asks.
func current(ctx context.Context) *auth.Current {
	cur, _ := auth.CurrentFrom(ctx)
	if cur == nil {
		return &auth.Current{}
	}
	return cur
}

func scopeOf(ctx context.Context) tenancy.Scope {
	sc, _ := tenancy.From(ctx)
	return sc
}

// actor names the signed-in user in the event log.
func actor(ctx context.Context) string { return current(ctx).User.Email }

// sessionCookie is the request's session cookie value.
func sessionCookie(ctx context.Context) string {
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		if c, err := r.Cookie(auth.CookieName); err == nil {
			return c.Value
		}
	}
	return ""
}

// conv turns a domain value into its API twin through their shared JSON shape.
func conv[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}

// val is *p, or the zero value when p is nil.
func val[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func (s *Server) inCompany(ctx context.Context, fn func(pgx.Tx) error) error {
	return db.InCompany(ctx, s.opts.Pool, fn)
}

// ---- gate ----

func (s *Server) GetHealth(ctx context.Context, _ api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	name, model := s.opts.Service.Engine()
	return api.GetHealth200JSONResponse{Ok: true, Version: s.opts.Version, Engine: name, Model: model, AuthRequired: true}, nil
}

func (s *Server) ListMoves(ctx context.Context, req api.ListMovesRequestObject) (api.ListMovesResponseObject, error) {
	p := req.Params
	moves, err := s.opts.Store.ListMoves(ctx, store.ListFilter{
		Status: val(p.Status), Source: val(p.Source), Limit: val(p.Limit), Before: val(p.Before),
	})
	if err != nil {
		return nil, err
	}
	return conv[api.ListMoves200JSONResponse](map[string]any{"moves": moves})
}

func (s *Server) CreateMove(ctx context.Context, req api.CreateMoveRequestObject) (api.CreateMoveResponseObject, error) {
	lane := strings.TrimSpace(val(req.Body.Lane))
	if lane == "" {
		lane = "1"
	}
	if len([]rune(lane)) > 32 {
		return nil, fail(http.StatusBadRequest, "E4002", "lane name too long")
	}
	m, err := s.opts.Service.NewMove(ctx, lane, val(req.Body.IsTest), actor(ctx))
	if err != nil {
		return nil, err
	}
	s.broker.Publish(scopeOf(ctx).CompanyID, m.ID)
	return conv[api.CreateMove201JSONResponse](m)
}

func (s *Server) GetMove(ctx context.Context, req api.GetMoveRequestObject) (api.GetMoveResponseObject, error) {
	m, err := s.opts.Store.GetMove(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	attempts, err := s.opts.Store.ListAttempts(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return conv[api.GetMove200JSONResponse](struct {
		store.Move
		Attempts []store.Attempt `json:"attempts"`
	}{m, attempts})
}

func (s *Server) EnterNumber(ctx context.Context, req api.EnterNumberRequestObject) (api.EnterNumberResponseObject, error) {
	m, err := s.opts.Service.ManualEntry(ctx, req.Id, req.Body.Number, actor(ctx))
	if err != nil {
		return nil, err
	}
	return conv[api.EnterNumber200JSONResponse](m)
}

const maxUpload = 60 << 20 // one original phone photo plus its AI copy

// readForm reads a whole multipart upload; call RemoveAll on the form when done.
func readForm(mr *multipart.Reader) (*multipart.Form, error) {
	form, err := mr.ReadForm(16 << 20)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "E4003", "invalid upload: "+err.Error())
	}
	return form, nil
}

// formFile reads one uploaded file and sniffs its type from the bytes,
// never trusting the browser's label.
func formFile(form *multipart.Form, field string) ([]byte, string, error) {
	files := form.File[field]
	if len(files) == 0 {
		return nil, "", errors.New("missing")
	}
	f, err := files[0].Open()
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", err
	}
	return data, http.DetectContentType(data), nil
}

func formValue(form *multipart.Form, key string) string {
	if v := form.Value[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (s *Server) AddPhoto(ctx context.Context, req api.AddPhotoRequestObject) (api.AddPhotoResponseObject, error) {
	form, err := readForm(req.Body)
	if err != nil {
		return nil, err
	}
	defer form.RemoveAll()
	original, originalType, err := formFile(form, "original")
	if err != nil {
		return nil, fail(http.StatusBadRequest, "E4004", "missing original photo")
	}
	ai, aiType, err := formFile(form, "ai")
	if err != nil {
		return nil, fail(http.StatusBadRequest, "E4004", "missing AI copy of the photo")
	}
	width, _ := strconv.Atoi(formValue(form, "width"))
	height, _ := strconv.Atoi(formValue(form, "height"))
	p, err := s.opts.Service.AddPhoto(ctx, gate.PhotoInput{
		MoveID: req.Id, Role: formValue(form, "role"), Actor: actor(ctx),
		Original: original, OriginalType: originalType, AI: ai, AIType: aiType, Width: width, Height: height,
	})
	if err != nil {
		return nil, err
	}
	return conv[api.AddPhoto202JSONResponse](p)
}

func (s *Server) ResolveMove(ctx context.Context, req api.ResolveMoveRequestObject) (api.ResolveMoveResponseObject, error) {
	b := req.Body
	m, err := s.opts.Service.Resolve(ctx, req.Id, gate.Resolution{
		Action: string(b.Action), Number: val(b.Number), By: b.By, Note: val(b.Note),
	})
	if err != nil {
		return nil, err
	}
	return conv[api.ResolveMove200JSONResponse](m)
}

func (s *Server) RereadMove(ctx context.Context, req api.RereadMoveRequestObject) (api.RereadMoveResponseObject, error) {
	if err := s.opts.Service.Reread(ctx, req.Id); err != nil {
		return nil, err
	}
	m, err := s.opts.Store.GetMove(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return conv[api.RereadMove202JSONResponse](m)
}

func (s *Server) ValidateNumber(ctx context.Context, req api.ValidateNumberRequestObject) (api.ValidateNumberResponseObject, error) {
	return conv[api.ValidateNumber200JSONResponse](iso6346.Validate(req.Number))
}

func (s *Server) TestRead(ctx context.Context, req api.TestReadRequestObject) (api.TestReadResponseObject, error) {
	form, err := readForm(req.Body)
	if err != nil {
		return nil, err
	}
	defer form.RemoveAll()
	ai, aiType, err := formFile(form, "ai")
	if err != nil {
		return nil, fail(http.StatusBadRequest, "E4004", "missing photo")
	}
	res, err := s.opts.Service.TestRead(ctx, ai, aiType)
	if err != nil {
		return nil, err
	}
	return conv[api.TestRead200JSONResponse](res)
}

// ---- session ----

// session describes cur; current is set when cur has a scope.
func (s *Server) session(ctx context.Context, cur *auth.Current) (api.Session, error) {
	companies := []map[string]any{}
	for _, m := range cur.Memberships {
		companies = append(companies, map[string]any{"id": m.CompanyID, "name": m.CompanyName, "role": m.Role})
	}
	v := map[string]any{
		"user":      map[string]string{"id": cur.User.ID, "email": cur.User.Email, "name": cur.User.Name},
		"companies": companies, "platformAdmin": cur.PlatformAdmin,
	}
	if sc := cur.Scope; sc != nil {
		mods, err := s.opts.Store.CompanyModules(tenancy.With(ctx, *sc))
		if err != nil {
			return api.Session{}, err
		}
		perms := []auth.Perm{}
		for _, p := range []auth.Perm{auth.PermGate, auth.PermOffice, auth.PermManage, auth.PermOwner} {
			if auth.Can(sc.Role, p) {
				perms = append(perms, p)
			}
		}
		var depot any // null: all depots
		if sc.DepotID != "" {
			depot = sc.DepotID
		}
		v["current"] = map[string]any{"companyId": sc.CompanyID, "depotId": depot, "role": sc.Role,
			"modules": append([]string{}, mods...), "permissions": perms}
	}
	return conv[api.Session](v)
}

func (s *Server) GetSession(ctx context.Context, _ api.GetSessionRequestObject) (api.GetSessionResponseObject, error) {
	out, err := s.session(ctx, current(ctx))
	return api.GetSession200JSONResponse(out), err
}

func (s *Server) SetScope(ctx context.Context, req api.SetScopeRequestObject) (api.SetScopeResponseObject, error) {
	cookie := sessionCookie(ctx)
	err := s.opts.Sessions.SetScope(ctx, cookie, req.Body.CompanyId, req.Body.DepotId)
	if errors.Is(err, auth.ErrNotMember) {
		return nil, fail(http.StatusForbidden, "E4031", "you are not a member of that company or depot")
	}
	if errors.Is(err, auth.ErrNoSession) {
		return nil, fail(http.StatusUnauthorized, "E4010", "sign in first")
	}
	if err != nil {
		return nil, err
	}
	cur, err := s.opts.Sessions.Resolve(ctx, cookie)
	if err != nil {
		return nil, err
	}
	out, err := s.session(ctx, &cur)
	return api.SetScope200JSONResponse(out), err
}

// logoutResponse clears the session cookie as it answers 204.
type logoutResponse struct {
	s *Server
	r *http.Request
}

func (l logoutResponse) VisitLogoutResponse(w http.ResponseWriter) error {
	l.s.opts.Sessions.SetCookie(w, l.r, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) Logout(ctx context.Context, _ api.LogoutRequestObject) (api.LogoutResponseObject, error) {
	if err := s.opts.Sessions.Logout(ctx, sessionCookie(ctx)); err != nil {
		return nil, err
	}
	r, _ := ctx.Value(requestKey{}).(*http.Request)
	return logoutResponse{s: s, r: r}, nil
}

// ---- company and depots ----

// knownModules are the modules a company can switch on.
var knownModules = []string{gate.ModuleOCR}

func checkModules(mods []string) error {
	for _, m := range mods {
		if !slices.Contains(knownModules, m) {
			return fail(http.StatusBadRequest, "E4012", "unknown module "+strconv.Quote(m)+"; known: "+strings.Join(knownModules, ", "))
		}
	}
	return nil
}

func (s *Server) company(ctx context.Context, id string) (api.Company, error) {
	var c api.Company
	err := db.Global(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT id, name, country, modules FROM companies WHERE id = $1", id).
			Scan(&c.Id, &c.Name, &c.Country, &c.Modules)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = fail(http.StatusNotFound, "E4043", "no such company")
	}
	return c, err
}

func (s *Server) GetCompany(ctx context.Context, _ api.GetCompanyRequestObject) (api.GetCompanyResponseObject, error) {
	c, err := s.company(ctx, scopeOf(ctx).CompanyID)
	return api.GetCompany200JSONResponse(c), err
}

func (s *Server) SetModules(ctx context.Context, req api.SetModulesRequestObject) (api.SetModulesResponseObject, error) {
	mods := append([]string{}, req.Body.Modules...)
	if err := checkModules(mods); err != nil {
		return nil, err
	}
	slices.Sort(mods)
	mods = slices.Compact(mods)
	id := scopeOf(ctx).CompanyID
	err := db.Global(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE companies SET modules = $2 WHERE id = $1", id, mods)
		return err
	})
	if err != nil {
		return nil, err
	}
	c, err := s.company(ctx, id)
	return api.SetModules200JSONResponse(c), err
}

func (s *Server) ListDepots(ctx context.Context, _ api.ListDepotsRequestObject) (api.ListDepotsResponseObject, error) {
	// A member limited to some depots sees only those.
	var allowed []string
	for _, m := range current(ctx).Memberships {
		if m.CompanyID == scopeOf(ctx).CompanyID {
			allowed = m.DepotIDs
		}
	}
	out := api.ListDepots200JSONResponse{}
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT id, name, country, timezone FROM depots ORDER BY name, id")
		if err != nil {
			return err
		}
		depots, err := pgx.CollectRows(rows, pgx.RowToStructByName[api.Depot])
		for _, d := range depots {
			if allowed == nil || slices.Contains(allowed, d.Id) {
				out = append(out, d)
			}
		}
		return err
	})
	return out, err
}

func (s *Server) CreateDepot(ctx context.Context, req api.CreateDepotRequestObject) (api.CreateDepotResponseObject, error) {
	d := api.Depot{Id: ulid.Make().String(), Name: strings.TrimSpace(req.Body.Name),
		Country: strings.TrimSpace(req.Body.Country), Timezone: strings.TrimSpace(req.Body.Timezone)}
	if d.Name == "" {
		return nil, fail(http.StatusBadRequest, "E4008", "a depot needs a name")
	}
	if d.Timezone == "" {
		d.Timezone = "UTC"
	}
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO depots (company_id, id, name, country, timezone) VALUES ($1, $2, $3, $4, $5)",
			scopeOf(ctx).CompanyID, d.Id, d.Name, d.Country, d.Timezone)
		return err
	})
	return api.CreateDepot201JSONResponse(d), err
}

// ---- reference data ----

// likePattern matches q anywhere, with q's own % and _ taken literally.
func likePattern(q *string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(val(q))) + "%"
}

// searchRef finds up to 20 rows whose code or name contains q, rows matching
// on the code first. table and cols are fixed by the caller, never the request.
func searchRef[T any](ctx context.Context, s *Server, table, cols string, q *string) ([]T, error) {
	out := []T{}
	err := db.Global(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+cols+" FROM "+table+
			" WHERE code ILIKE $1 OR name ILIKE $1 ORDER BY code ILIKE $1 DESC, code LIMIT 20", likePattern(q))
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByName[T])
		return err
	})
	return out, err
}

func (s *Server) SearchLiners(ctx context.Context, req api.SearchLinersRequestObject) (api.SearchLinersResponseObject, error) {
	out, err := searchRef[api.Liner](ctx, s, "ref_smdg_liners", "code, name", req.Params.Q)
	return api.SearchLiners200JSONResponse(out), err
}

func (s *Server) SearchLocodes(ctx context.Context, req api.SearchLocodesRequestObject) (api.SearchLocodesResponseObject, error) {
	out, err := searchRef[api.Locode](ctx, s, "ref_locodes", "code, name, country", req.Params.Q)
	return api.SearchLocodes200JSONResponse(out), err
}
