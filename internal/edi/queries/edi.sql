-- name: GetCompanyEDI :one
SELECT * FROM company_edi WHERE company_id = @company_id;

-- name: InsertCompanyEDI :execrows
INSERT INTO company_edi (company_id, inbound_token) VALUES (@company_id, @inbound_token)
ON CONFLICT (company_id) DO NOTHING;

-- name: TakeNextRef :one
UPDATE company_edi SET next_ref = next_ref + 1 WHERE company_id = @company_id
RETURNING (next_ref - 1)::bigint AS ref;

-- name: InboundCompany :one
SELECT COALESCE(edi_inbound_company(@token::text), '')::text AS company_id;

-- name: EDICompanies :many
SELECT edi_companies()::text AS company_id;
