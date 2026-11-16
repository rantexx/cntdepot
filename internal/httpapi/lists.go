package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"depot/internal/api"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"
)

// Lines, customers and hauliers are the same list in three tables. The API
// types of the three are identical, so one generic helper serves them all.
// table is always one of these constants, never a value from the request.
const (
	tableLines     = "shipping_lines"
	tableCustomers = "customers"
	tableHauliers  = "hauliers"
)

const listCols = "id, code, name, contact, active"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var (
	errDuplicateCode = fail(http.StatusConflict, "E4095", "that code is already in use")
	errNoSuchRow     = fail(http.StatusNotFound, "E4043", "no such record")
)

// pageOf is a list page's limit and offset: pages count from 1, 50 rows by default, at most 200.
func pageOf(page, perPage *int) (limit, offset int) {
	n, size := max(val(page), 1), val(perPage)
	if size < 1 {
		size = 50
	}
	size = min(size, 200)
	return size, (n - 1) * size
}

// listRows returns a page of rows whose code or name contains q, by code,
// with the total number of matches. Inactive rows are included.
func listRows[T any](ctx context.Context, s *Server, table string, q *string, page, perPage *int) ([]T, int, error) {
	size, offset := pageOf(page, perPage)
	where := " WHERE code ILIKE $1 OR name ILIKE $1"
	var items []T
	var total int
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table+where, likePattern(q)).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+listCols+" FROM "+table+where+" ORDER BY code, id LIMIT $2 OFFSET $3",
			likePattern(q), size, offset)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, pgx.RowToStructByName[T])
		return err
	})
	return items, total, err
}

func createRow[T any](ctx context.Context, s *Server, table string, b api.LineCreate) (T, error) {
	var row T
	code, name := strings.TrimSpace(b.Code), strings.TrimSpace(b.Name)
	if code == "" || name == "" {
		return row, fail(http.StatusBadRequest, "E4008", "code and name are required")
	}
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "INSERT INTO "+table+" (company_id, id, code, name, contact) VALUES ($1, $2, $3, $4, $5) RETURNING "+listCols,
			scopeOf(ctx).CompanyID, ulid.Make().String(), code, name, strings.TrimSpace(val(b.Contact)))
		if err != nil {
			return err
		}
		row, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
		return err
	})
	if isUniqueViolation(err) {
		err = errDuplicateCode
	}
	return row, err
}

func updateRow[T any](ctx context.Context, s *Server, table, id string, b api.LinePatch) (T, error) {
	var row T
	trim := func(p *string) *string {
		if p == nil {
			return nil
		}
		t := strings.TrimSpace(*p)
		return &t
	}
	code, name := trim(b.Code), trim(b.Name)
	if (code != nil && *code == "") || (name != nil && *name == "") {
		return row, fail(http.StatusBadRequest, "E4008", "code and name cannot be empty")
	}
	err := s.inCompany(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "UPDATE "+table+` SET code = coalesce($2, code), name = coalesce($3, name),
			contact = coalesce($4, contact), active = coalesce($5, active) WHERE id = $1 RETURNING `+listCols,
			id, code, name, trim(b.Contact), b.Active)
		if err != nil {
			return err
		}
		row, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
		return err
	})
	switch {
	case isUniqueViolation(err):
		err = errDuplicateCode
	case errors.Is(err, pgx.ErrNoRows):
		err = errNoSuchRow
	}
	return row, err
}

// deactivateRow sets active=false; rows are never deleted.
func deactivateRow(ctx context.Context, s *Server, table, id string) error {
	return s.inCompany(ctx, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, "UPDATE "+table+" SET active = false WHERE id = $1", id)
		if err == nil && t.RowsAffected() == 0 {
			err = errNoSuchRow
		}
		return err
	})
}

func (s *Server) ListLines(ctx context.Context, req api.ListLinesRequestObject) (api.ListLinesResponseObject, error) {
	items, total, err := listRows[api.Line](ctx, s, tableLines, req.Params.Q, req.Params.Page, req.Params.PerPage)
	return api.ListLines200JSONResponse{Items: items, Total: total}, err
}

func (s *Server) CreateLine(ctx context.Context, req api.CreateLineRequestObject) (api.CreateLineResponseObject, error) {
	row, err := createRow[api.Line](ctx, s, tableLines, *req.Body)
	return api.CreateLine201JSONResponse(row), err
}

func (s *Server) UpdateLine(ctx context.Context, req api.UpdateLineRequestObject) (api.UpdateLineResponseObject, error) {
	row, err := updateRow[api.Line](ctx, s, tableLines, req.Id, *req.Body)
	return api.UpdateLine200JSONResponse(row), err
}

func (s *Server) DeactivateLine(ctx context.Context, req api.DeactivateLineRequestObject) (api.DeactivateLineResponseObject, error) {
	return api.DeactivateLine204Response{}, deactivateRow(ctx, s, tableLines, req.Id)
}

func (s *Server) ListCustomers(ctx context.Context, req api.ListCustomersRequestObject) (api.ListCustomersResponseObject, error) {
	items, total, err := listRows[api.Customer](ctx, s, tableCustomers, req.Params.Q, req.Params.Page, req.Params.PerPage)
	return api.ListCustomers200JSONResponse{Items: items, Total: total}, err
}

func (s *Server) CreateCustomer(ctx context.Context, req api.CreateCustomerRequestObject) (api.CreateCustomerResponseObject, error) {
	row, err := createRow[api.Customer](ctx, s, tableCustomers, api.LineCreate(*req.Body))
	return api.CreateCustomer201JSONResponse(row), err
}

func (s *Server) UpdateCustomer(ctx context.Context, req api.UpdateCustomerRequestObject) (api.UpdateCustomerResponseObject, error) {
	row, err := updateRow[api.Customer](ctx, s, tableCustomers, req.Id, api.LinePatch(*req.Body))
	return api.UpdateCustomer200JSONResponse(row), err
}

func (s *Server) DeactivateCustomer(ctx context.Context, req api.DeactivateCustomerRequestObject) (api.DeactivateCustomerResponseObject, error) {
	return api.DeactivateCustomer204Response{}, deactivateRow(ctx, s, tableCustomers, req.Id)
}

func (s *Server) ListHauliers(ctx context.Context, req api.ListHauliersRequestObject) (api.ListHauliersResponseObject, error) {
	items, total, err := listRows[api.Haulier](ctx, s, tableHauliers, req.Params.Q, req.Params.Page, req.Params.PerPage)
	return api.ListHauliers200JSONResponse{Items: items, Total: total}, err
}

func (s *Server) CreateHaulier(ctx context.Context, req api.CreateHaulierRequestObject) (api.CreateHaulierResponseObject, error) {
	row, err := createRow[api.Haulier](ctx, s, tableHauliers, api.LineCreate(*req.Body))
	return api.CreateHaulier201JSONResponse(row), err
}

func (s *Server) UpdateHaulier(ctx context.Context, req api.UpdateHaulierRequestObject) (api.UpdateHaulierResponseObject, error) {
	row, err := updateRow[api.Haulier](ctx, s, tableHauliers, req.Id, api.LinePatch(*req.Body))
	return api.UpdateHaulier200JSONResponse(row), err
}

func (s *Server) DeactivateHaulier(ctx context.Context, req api.DeactivateHaulierRequestObject) (api.DeactivateHaulierResponseObject, error) {
	return api.DeactivateHaulier204Response{}, deactivateRow(ctx, s, tableHauliers, req.Id)
}
