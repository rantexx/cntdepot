package httpapi

import (
	"context"
	"errors"
	"net/http"

	"depot/internal/api"
	"depot/internal/platform"
)

func platformError(err error) error {
	switch {
	case errors.Is(err, platform.ErrNotAdmin):
		return fail(http.StatusForbidden, "E4030", "platform admins only")
	case errors.Is(err, platform.ErrInvalid):
		return fail(http.StatusBadRequest, "E4008", "a company needs a name and a valid owner email")
	}
	return err
}

func (s *Server) ListCompanies(ctx context.Context, _ api.ListCompaniesRequestObject) (api.ListCompaniesResponseObject, error) {
	list, err := platform.ListCompanies(ctx, s.opts.Pool)
	if err != nil {
		return nil, platformError(err)
	}
	out := api.ListCompanies200JSONResponse{}
	for _, c := range list {
		out = append(out, api.Company{Id: c.ID, Name: c.Name, Country: c.Country, Modules: append([]string{}, c.Modules...)})
	}
	return out, nil
}

func (s *Server) CreateCompany(ctx context.Context, req api.CreateCompanyRequestObject) (api.CreateCompanyResponseObject, error) {
	b := req.Body
	if err := checkModules(b.Modules); err != nil {
		return nil, err
	}
	id, err := platform.CreateCompany(ctx, s.opts.Pool, b.Name, b.Country, b.OwnerEmail, b.Modules)
	if err != nil {
		return nil, platformError(err)
	}
	c, err := s.company(ctx, id)
	return api.CreateCompany201JSONResponse(c), err
}
