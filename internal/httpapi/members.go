package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/mail"
	"strings"

	"depot/internal/api"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

// checkBody caps request bodies, and makes member and invitation writes name
// depotIds outright: a forgotten key must not silently mean "all depots".
func checkBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(64 << 10)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			limit = maxUpload
			if strings.HasPrefix(r.Pattern, "POST /api/import/") {
				limit = maxImportFile + 64<<10
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if r.Pattern == "POST /api/invitations" || r.Pattern == "PATCH /api/members/{userId}" {
			data, err := io.ReadAll(r.Body)
			var keys map[string]json.RawMessage
			if err != nil || json.Unmarshal(data, &keys) != nil {
				writeError(w, http.StatusBadRequest, "E4001", "invalid JSON body")
				return
			}
			if _, ok := keys["depotIds"]; !ok {
				writeError(w, http.StatusBadRequest, "E4009", "depotIds is required: null for all depots, or a list of depot ids")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		next.ServeHTTP(w, r)
	})
}

// checkAccess validates a role and depot list from the client.
func checkAccess(role api.Role, depots *[]string) error {
	if !role.Valid() {
		return fail(http.StatusBadRequest, "E4009", "role must be owner, manager, office or gate")
	}
	if depots != nil && len(*depots) == 0 {
		return fail(http.StatusBadRequest, "E4009", "depotIds must name at least one depot, or be null for all depots")
	}
	return nil
}

var errLastOwner = fail(http.StatusConflict, "E4096", "the company must keep at least one owner")

// keepAnOwner fails the transaction when the company has no owner left.
func keepAnOwner(ctx context.Context, tx pgx.Tx) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM memberships WHERE role = 'owner')").Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errLastOwner
	}
	return nil
}

// optIDs is a nullable depot list as the API sends it.
func optIDs(ids []string) *[]string {
	if ids == nil {
		return nil
	}
	return &ids
}

const memberQuery = `SELECT m.user_id, u.email, u.name, m.role, m.depot_ids
	FROM memberships m JOIN users u ON u.id = m.user_id`

func scanMember(row pgx.Row) (api.Member, error) {
	var m api.Member
	var ids []string
	err := row.Scan(&m.UserId, &m.Email, &m.Name, &m.Role, &ids)
	m.DepotIds = optIDs(ids)
	return m, err
}

func (s *Server) ListMembers(ctx context.Context, _ api.ListMembersRequestObject) (api.ListMembersResponseObject, error) {
	out := api.ListMembers200JSONResponse{}
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, memberQuery+" ORDER BY u.email, m.user_id")
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (api.Member, error) { return scanMember(r) })
		return err
	})
	return out, err
}

func (s *Server) UpdateMember(ctx context.Context, req api.UpdateMemberRequestObject) (api.UpdateMemberResponseObject, error) {
	b := req.Body
	if err := checkAccess(b.Role, b.DepotIds); err != nil {
		return nil, err
	}
	var m api.Member
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, "UPDATE memberships SET role = $2, depot_ids = $3 WHERE user_id = $1", req.UserId, b.Role, val(b.DepotIds))
		if err == nil && t.RowsAffected() == 0 {
			err = errNoSuchRow
		}
		if err == nil {
			err = keepAnOwner(ctx, tx)
		}
		if err == nil {
			m, err = scanMember(tx.QueryRow(ctx, memberQuery+" WHERE m.user_id = $1", req.UserId))
		}
		return err
	})
	return api.UpdateMember200JSONResponse(m), err
}

func (s *Server) RemoveMember(ctx context.Context, req api.RemoveMemberRequestObject) (api.RemoveMemberResponseObject, error) {
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, "DELETE FROM memberships WHERE user_id = $1", req.UserId)
		if err == nil && t.RowsAffected() == 0 {
			err = errNoSuchRow
		}
		if err == nil {
			err = keepAnOwner(ctx, tx)
		}
		return err
	})
	return api.RemoveMember204Response{}, err
}

func (s *Server) ListInvitations(ctx context.Context, _ api.ListInvitationsRequestObject) (api.ListInvitationsResponseObject, error) {
	out := api.ListInvitations200JSONResponse{}
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT id, email, role, depot_ids, created_at FROM invitations WHERE used_at IS NULL ORDER BY created_at, id")
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (i api.Invitation, err error) {
			var ids []string
			err = r.Scan(&i.Id, &i.Email, &i.Role, &ids, &i.CreatedAt)
			i.DepotIds = optIDs(ids)
			return i, err
		})
		return err
	})
	return out, err
}

func (s *Server) CreateInvitation(ctx context.Context, req api.CreateInvitationRequestObject) (api.CreateInvitationResponseObject, error) {
	b := req.Body
	if err := checkAccess(b.Role, b.DepotIds); err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(b.Email))
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return nil, fail(http.StatusBadRequest, "E4008", "a plain email address is required")
	}
	inv := api.Invitation{Id: ulid.Make().String(), Email: email, Role: b.Role, DepotIds: b.DepotIds}
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO invitations (company_id, id, email, role, depot_ids, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
			scopeOf(ctx).CompanyID, inv.Id, email, b.Role, val(b.DepotIds), current(ctx).User.ID).Scan(&inv.CreatedAt)
	})
	return api.CreateInvitation201JSONResponse(inv), err
}

func (s *Server) DeleteInvitation(ctx context.Context, req api.DeleteInvitationRequestObject) (api.DeleteInvitationResponseObject, error) {
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, "DELETE FROM invitations WHERE id = $1 AND used_at IS NULL", req.Id)
		if err == nil && t.RowsAffected() == 0 {
			err = errNoSuchRow
		}
		return err
	})
	return api.DeleteInvitation204Response{}, err
}
