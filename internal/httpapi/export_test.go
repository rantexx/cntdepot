package httpapi

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

const stockHeader = "number,type_code,line_code,customer_code,full_empty,in_date,block,row,tier,grade\n"

// uploadCSV posts content as the multipart field "file" of path.
func (u *user) uploadCSV(path, content string) (int, []byte) {
	u.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "stock.csv")
	if err != nil {
		u.t.Fatal(err)
	}
	_, _ = fw.Write([]byte(content))
	mw.Close()
	return do(u.t, u.request("POST", path, &buf, mw.FormDataContentType()))
}

func (u *user) get(path string) (*http.Response, []byte) {
	u.t.Helper()
	resp, err := http.DefaultClient.Do(u.request("GET", path, nil, ""))
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestImportBadRowImportsNothing(t *testing.T) {
	e := newYardEnv(t)
	office := e.as(t, "A", "office")
	office.must(201, "POST", "/api/lines", `{"code":"MSC","name":"MSC"}`)

	status, body := office.uploadCSV("/api/import/stock", stockHeader+
		"MSCU1234566,45G1,MSC,,E,2026-01-05,,,,\n"+
		"CSQU3054383,,,,,05.01.2026,,,,\n"+
		"TGHU1234568,,,,,2026-01-05,,,,\n") // wrong check digit on row 3
	rej := decode[struct {
		Error struct{ Code string }
		Rows  []struct {
			Row            int
			Field, Message string
		}
	}](t, body)
	if status != 422 || rej.Error.Code != "E4220" || len(rej.Rows) != 1 || rej.Rows[0].Row != 3 || rej.Rows[0].Field != "number" {
		t.Fatalf("bad file = %d %s; want 422 E4220 listing row 3", status, body)
	}
	if total := decode[struct{ Total int }](t, office.must(200, "GET", "/api/visits", "")).Total; total != 0 {
		t.Fatalf("%d visits created by a rejected file", total)
	}

	// The good file imports, and the same file again is rejected: the numbers are in the yard.
	good := stockHeader + "MSCU1234566,45G1,MSC,,E,2026-01-05,,,,\nCSQU3054383,,,,,05.01.2026,,,,\n"
	status, body = office.uploadCSV("/api/import/stock", good)
	if status != 200 || decode[struct{ Imported int }](t, body).Imported != 2 {
		t.Fatalf("good file = %d %s", status, body)
	}
	if status, body = office.uploadCSV("/api/import/stock", good); status != 422 {
		t.Fatalf("same file again = %d %s; want 422", status, body)
	}
	// The gate role may not import.
	if status, body = e.as(t, "A", "gate").uploadCSV("/api/import/stock", good); status != 403 {
		t.Fatalf("gate imports = %d %s; want 403", status, body)
	}
}

func TestImportLimits(t *testing.T) {
	e := newYardEnv(t)
	office := e.as(t, "A", "office")
	if status, body := office.uploadCSV("/api/import/stock", stockHeader+strings.Repeat(",,,,,,,,,\n", 20001)); status != 413 {
		t.Errorf("20001 rows = %d %s; want 413", status, body)
	}
	if status, body := office.uploadCSV("/api/import/stock", stockHeader+strings.Repeat("x", 5<<20)); status != 413 {
		t.Errorf("over 5 MiB = %d %s; want 413", status, body)
	}
	if status, body := office.call("POST", "/api/import/stock", `{}`); status != 400 {
		t.Errorf("no upload = %d %s; want 400", status, body)
	}
}

func TestImportOrdersOverHTTP(t *testing.T) {
	e := newYardEnv(t)
	office := e.as(t, "A", "office")
	status, body := office.uploadCSV("/api/import/orders", "kind,reference,line_code,customer_code,valid_until,number,type_code,full_empty,quantity\n"+
		"release,R-1,,,2026-12-31,,45G1,,2\nrelease,R-1,,,,MSCU1234566,,,\n")
	if status != 200 || decode[struct{ Imported int }](t, body).Imported != 1 {
		t.Fatalf("import = %d %s", status, body)
	}
	orders := decode[[]struct{ Reference string }](t, office.must(200, "GET", "/api/orders", ""))
	if len(orders) != 1 || orders[0].Reference != "R-1" {
		t.Fatalf("orders = %+v", orders)
	}
}

func TestImportTemplates(t *testing.T) {
	e := newYardEnv(t)
	office := e.as(t, "A", "office")
	for name, header := range map[string]string{"stock": "number,type_code,line_code,customer_code,full_empty,in_date,block,row,tier,grade",
		"orders": "kind,reference,line_code,customer_code,valid_until,number,type_code,full_empty,quantity"} {
		resp, body := office.get("/api/import/templates/" + name + ".csv")
		lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(body), "\r", "")), "\n")
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") ||
			!bytes.HasPrefix(body, []byte("\xef\xbb\xbf")) || len(lines) != 2 || strings.TrimPrefix(lines[0], "\ufeff") != header {
			t.Errorf("template %s = %d %q %q", name, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}
	// The example row imports as it is.
	_, body := office.get("/api/import/templates/stock.csv")
	if status, got := office.uploadCSV("/api/import/stock", string(body)); status != 422 || !strings.Contains(string(got), "line_code") {
		t.Errorf("stock example = %d %s; want it to name a line that does not exist yet", status, got)
	}
	if resp, _ := office.get("/api/import/templates/nope.csv"); resp.StatusCode != 404 {
		t.Errorf("unknown template = %d; want 404", resp.StatusCode)
	}
}

func TestExportVisitsCSVHasBOMAndScope(t *testing.T) {
	e := newYardEnv(t)
	office, officeB := e.as(t, "A", "office"), e.as(t, "B", "office")
	office.must(201, "POST", "/api/lines", `{"code":"MSC","name":"MSC"}`)
	status, body := office.uploadCSV("/api/import/stock", stockHeader+
		"MSCU1234566,45G1,MSC,,E,2026-01-05,=SUM(A1),\"3\",\"tier, two\",\n"+
		"CSQU3054383,,,,,2026-01-06,@cmd,,,\n")
	if status != 200 {
		t.Fatalf("import = %d %s", status, body)
	}
	// Another company's stock, and company A's stock at its other depot, stay out.
	if status, body = officeB.uploadCSV("/api/import/stock", stockHeader+"TGHU1234567,,,,,2026-01-05,,,,\n"); status != 200 {
		t.Fatalf("import B = %d %s", status, body)
	}
	e.exec(t, "A", `INSERT INTO depots (company_id, id, name) VALUES ('A', 'd2', 'D2')`)
	e.exec(t, "A", `INSERT INTO visits (company_id, id, depot_id, number, in_at) VALUES ('A', 'v-d2', 'd2', 'TEST0000001', now())`)

	resp, got := office.get("/api/export/visits.csv")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "visits.csv") {
		t.Fatalf("export = %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
	}
	if !bytes.HasPrefix(got, []byte("\xef\xbb\xbf")) {
		t.Fatalf("no BOM: %q", got)
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(got), "\ufeff"), "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "number,") {
		t.Fatalf("export = %q; want a header and the 2 containers of company A at depot d1", text)
	}
	for _, want := range []string{"MSCU1234566", "CSQU3054383", `'=SUM(A1)`, `'@cmd`, `"tier, two"`} {
		if !strings.Contains(text, want) {
			t.Errorf("export lacks %q:\n%s", want, text)
		}
	}
	for _, not := range []string{"TGHU1234567", "TEST0000001", ",=SUM", ",@cmd"} {
		if strings.Contains(text, not) {
			t.Errorf("export has %q:\n%s", not, text)
		}
	}

	// Filters as the list's; the gate role may not export; an unknown name is 404.
	if _, got := office.get("/api/export/visits.csv?q=CSQU"); strings.Contains(string(got), "MSCU1234566") || !strings.Contains(string(got), "CSQU3054383") {
		t.Errorf("filtered export:\n%s", got)
	}
	if resp, _ := e.as(t, "A", "gate").get("/api/export/visits.csv"); resp.StatusCode != 403 {
		t.Errorf("gate exports = %d; want 403", resp.StatusCode)
	}
	if resp, _ := office.get("/api/export/nope.csv"); resp.StatusCode != 404 {
		t.Errorf("unknown export = %d; want 404", resp.StatusCode)
	}
	for _, name := range []string{"gate-log", "orders", "ageing"} {
		resp, got := office.get(fmt.Sprintf("/api/export/%s.csv", name))
		if resp.StatusCode != 200 || !bytes.HasPrefix(got, []byte("\xef\xbb\xbf")) {
			t.Errorf("export %s = %d %q", name, resp.StatusCode, got)
		}
	}
}

func TestReportsOverHTTP(t *testing.T) {
	e := newYardEnv(t)
	office := e.as(t, "A", "office")
	office.uploadCSV("/api/import/stock", stockHeader+"MSCU1234566,,,,,2026-01-05,,,,\n")
	d := decode[struct {
		InToday, OutToday, OpenHolds, OpenFlags int
		InYardByLine                            []struct{ Count int }
	}](t, office.must(200, "GET", "/api/reports/dashboard", ""))
	if len(d.InYardByLine) != 1 || d.InYardByLine[0].Count != 1 {
		t.Errorf("dashboard = %+v", d)
	}
	a := decode[[]struct {
		Total   int
		Buckets map[string]int
	}](t, office.must(200, "GET", "/api/reports/ageing", ""))
	if len(a) != 1 || a[0].Total != 1 || a[0].Buckets["90+"] != 1 {
		t.Errorf("ageing = %+v", a)
	}
	office.must(200, "GET", "/api/reports/gate-log?from=2026-01-01&to=2026-12-31", "")
	if status, _ := e.as(t, "A", "gate").call("GET", "/api/reports/dashboard", ""); status != 403 {
		t.Errorf("gate reads the dashboard = %d; want 403", status)
	}
}
