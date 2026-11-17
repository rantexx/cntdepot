package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"depot/internal/api"
	"depot/internal/yard"
)

// Reports, CSV export and the stock and order imports.

const (
	maxImportFile = 5 << 20 // bytes
	maxExportRows = 100000
	maxGateLog    = 5000
)

func (s *Server) ReportDashboard(ctx context.Context, _ api.ReportDashboardRequestObject) (api.ReportDashboardResponseObject, error) {
	d, err := s.opts.Yard.Dashboard(ctx)
	if err != nil {
		return nil, err
	}
	d.InYardByLine = list(d.InYardByLine)
	return conv[api.ReportDashboard200JSONResponse](d)
}

func (s *Server) ReportAgeing(ctx context.Context, _ api.ReportAgeingRequestObject) (api.ReportAgeingResponseObject, error) {
	rows, err := s.opts.Yard.Ageing(ctx)
	if err != nil {
		return nil, err
	}
	return conv[api.ReportAgeing200JSONResponse](list(rows))
}

func (s *Server) GateLog(ctx context.Context, req api.GateLogRequestObject) (api.GateLogResponseObject, error) {
	var from, to *time.Time
	if p := req.Params.From; p != nil {
		from = &p.Time
	}
	if p := req.Params.To; p != nil {
		to = &p.Time
	}
	rows, err := s.opts.Yard.GateLog(ctx, from, to, maxGateLog)
	if err != nil {
		return nil, err
	}
	return conv[api.GateLog200JSONResponse](list(rows))
}

// ---- imports ----

// uploadedCSV reads the upload's "file" part; more than maxImportFile bytes is 413.
func uploadedCSV(mr *multipart.Reader) ([]byte, error) {
	form, err := mr.ReadForm(maxImportFile)
	if err != nil {
		if tooBig := new(http.MaxBytesError); errors.As(err, &tooBig) {
			return nil, fail(http.StatusRequestEntityTooLarge, "E4130", "the file is over 5 MiB")
		}
		return nil, fail(http.StatusBadRequest, "E4003", "invalid upload: "+err.Error())
	}
	defer form.RemoveAll()
	files := form.File["file"]
	if len(files) == 0 {
		return nil, fail(http.StatusBadRequest, "E4003", "invalid upload: the file part is missing")
	}
	if files[0].Size > maxImportFile {
		return nil, fail(http.StatusRequestEntityTooLarge, "E4130", "the file is over 5 MiB")
	}
	f, err := files[0].Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxImportFile+1))
}
func (s *Server) importResult(n int, err error) (*api.ImportResult, *api.ImportRejected, error) {
	var ie *yard.ImportError
	switch {
	case err == nil:
		return &api.ImportResult{Imported: n}, nil, nil
	case errors.As(err, &ie):
		rej := api.ImportRejected{Rows: make([]api.ImportRowError, len(ie.Rows))}
		rej.Error.Code, rej.Error.Message = "E4220", "the file has rows that cannot be imported; nothing was imported"
		for i, r := range ie.Rows {
			rej.Rows[i] = api.ImportRowError{Row: r.Row, Field: r.Field, Message: r.Message}
		}
		return nil, &rej, nil
	case errors.Is(err, yard.ErrImportTooLarge):
		return nil, nil, fail(http.StatusRequestEntityTooLarge, "E4130", "the file has more than 20000 rows")
	}
	return nil, nil, err
}

func (s *Server) ImportStock(ctx context.Context, req api.ImportStockRequestObject) (api.ImportStockResponseObject, error) {
	data, err := uploadedCSV(req.Body)
	if err != nil {
		return nil, err
	}
	ok, rej, err := s.importResult(s.opts.Yard.ImportStock(ctx, data, actor(ctx)))
	switch {
	case rej != nil:
		return api.ImportStock422JSONResponse(*rej), nil
	case err != nil:
		return nil, err
	}
	return api.ImportStock200JSONResponse(*ok), nil
}

func (s *Server) ImportOrders(ctx context.Context, req api.ImportOrdersRequestObject) (api.ImportOrdersResponseObject, error) {
	data, err := uploadedCSV(req.Body)
	if err != nil {
		return nil, err
	}
	ok, rej, err := s.importResult(s.opts.Yard.ImportOrders(ctx, data, actor(ctx)))
	switch {
	case rej != nil:
		return api.ImportOrders422JSONResponse(*rej), nil
	case err != nil:
		return nil, err
	}
	return api.ImportOrders200JSONResponse(*ok), nil
}

func (s *Server) ImportTemplate(_ context.Context, req api.ImportTemplateRequestObject) (api.ImportTemplateResponseObject, error) {
	name := strings.TrimSuffix(string(req.Name), ".csv")
	header, example, ok := yard.Template(name)
	if !ok || !strings.HasSuffix(string(req.Name), ".csv") {
		return nil, yard.ErrNotFound
	}
	b := csvBytes([][]string{header, example})
	disp := `attachment; filename="` + name + `-template.csv"`
	return api.ImportTemplate200TextcsvResponse{Body: bytes.NewReader(b), ContentLength: int64(len(b)),
		Headers: api.ImportTemplate200ResponseHeaders{ContentDisposition: &disp}}, nil
}

// ---- export ----

// csvBytes is a UTF-8 CSV with a byte order mark (so Excel opens it as
// UTF-8), RFC 4180 quoting and CRLF line ends. A text cell that starts with
// = + - @ (or a tab or return) is prefixed with ' so a spreadsheet never
// runs it as a formula.
func csvBytes(rows [][]string) []byte {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf")
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	for _, row := range rows {
		out := make([]string, len(row))
		for i, c := range row {
			if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
				c = "'" + c
			}
			out[i] = c
		}
		_ = w.Write(out)
	}
	w.Flush()
	return buf.Bytes()
}

func ts(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func orEmpty(p *string) string { return val(p) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func yesNo(b bool) string { return strconv.FormatBool(b) }

// export is GET /api/export/{name}.csv. Each export takes the filters of its list.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutSuffix(r.PathValue("file"), ".csv")
	if !ok {
		writeError(w, http.StatusNotFound, "E4040", "no such export")
		return
	}
	q := r.URL.Query()
	ctx := r.Context()
	var rows [][]string
	var err error
	switch name {
	case "visits":
		rows, err = s.exportVisits(ctx, q.Get)
	case "gate-log":
		rows, err = s.exportGateLog(ctx, q.Get)
	case "orders":
		rows, err = s.exportOrders(ctx, q.Get)
	case "ageing":
		rows, err = s.exportAgeing(ctx)
	default:
		writeError(w, http.StatusNotFound, "E4040", "no such export")
		return
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(csvBytes(rows))
}

func (s *Server) exportVisits(ctx context.Context, get func(string) string) ([][]string, error) {
	minDays, _ := strconv.Atoi(get("minDays"))
	hold, _ := strconv.ParseBool(get("hold"))
	vs, _, err := s.opts.Yard.FindVisits(ctx, yard.VisitFilter{
		Status: get("status"), Number: get("q"), LineID: strings.TrimSpace(get("lineId")), CustomerID: strings.TrimSpace(get("customerId")),
		TypeCode: get("type"), Grade: strings.ToUpper(strings.TrimSpace(get("grade"))), OnHold: hold, MinDays: max(minDays, 0),
	}, maxExportRows, 0)
	if err != nil {
		return nil, err
	}
	lines, customers, err := s.opts.Yard.Codes(ctx)
	if err != nil {
		return nil, err
	}
	out := [][]string{{"number", "type_code", "line_code", "customer_code", "status", "full_empty", "grade", "block", "row", "tier",
		"in_at", "out_at", "imported", "on_hold"}}
	for _, v := range vs {
		out = append(out, []string{v.Number, v.TypeCode, lines[orEmpty(v.LineID)], customers[orEmpty(v.CustomerID)], v.Status, v.FullEmpty,
			v.Grade, v.Block, v.RowNo, v.Tier, ts(&v.InAt), ts(v.OutAt), yesNo(v.Imported), yesNo(v.OnHold)})
	}
	return out, nil
}

func (s *Server) exportGateLog(ctx context.Context, get func(string) string) ([][]string, error) {
	date := func(k string) (*time.Time, error) {
		if get(k) == "" {
			return nil, nil
		}
		t, err := time.Parse(time.DateOnly, get(k))
		if err != nil {
			return nil, fail(http.StatusBadRequest, "E4000", k+": use YYYY-MM-DD")
		}
		return &t, nil
	}
	from, err := date("from")
	if err != nil {
		return nil, err
	}
	to, err := date("to")
	if err != nil {
		return nil, err
	}
	log, err := s.opts.Yard.GateLog(ctx, from, to, maxExportRows)
	if err != nil {
		return nil, err
	}
	out := [][]string{{"at", "direction", "number", "type_code", "line_code", "customer_code", "haulier_code", "truck_plate", "trailer_plate",
		"driver_name", "seal", "full_empty", "condition", "flags", "voided"}}
	for _, g := range log {
		out = append(out, []string{ts(&g.At), g.Direction, g.Number, g.TypeCode, g.LineCode, g.CustomerCode, g.HaulierCode, g.TruckPlate,
			g.TrailerPlate, g.DriverName, g.Seal, g.FullEmpty, g.Condition, itoa(g.Flags), yesNo(g.Voided)})
	}
	return out, nil
}

func (s *Server) exportOrders(ctx context.Context, get func(string) string) ([][]string, error) {
	orders, err := s.opts.Yard.ListOrders(ctx, get("kind"), get("status"), strings.TrimSpace(get("q")))
	if err != nil {
		return nil, err
	}
	items, err := s.opts.Yard.OrderItemsByOrder(ctx)
	if err != nil {
		return nil, err
	}
	lines, customers, err := s.opts.Yard.Codes(ctx)
	if err != nil {
		return nil, err
	}
	out := [][]string{{"kind", "reference", "line_code", "customer_code", "valid_until", "number", "type_code", "full_empty", "quantity",
		"used", "status"}}
	for _, o := range orders {
		until := ""
		if o.ValidUntil != nil {
			until = o.ValidUntil.Format(time.DateOnly)
		}
		head := []string{o.Kind, o.Reference, lines[orEmpty(o.LineID)], customers[orEmpty(o.CustomerID)], until}
		for _, it := range items[o.ID] {
			out = append(out, append(append([]string{}, head...), orEmpty(it.Number), orEmpty(it.TypeCode), it.FullEmpty, itoa(it.Quantity),
				itoa(it.Used), o.Status))
		}
	}
	return out, nil
}

func (s *Server) exportAgeing(ctx context.Context) ([][]string, error) {
	rows, err := s.opts.Yard.Ageing(ctx)
	if err != nil {
		return nil, err
	}
	out := [][]string{append([]string{"line_code"}, append(append([]string{}, yard.AgeingBuckets...), "total")...)}
	for _, r := range rows {
		row := []string{r.LineCode}
		for _, b := range yard.AgeingBuckets {
			row = append(row, itoa(r.Buckets[b]))
		}
		out = append(out, append(row, itoa(r.Total)))
	}
	return out, nil
}
