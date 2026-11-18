package edi

import (
	"strings"
	"testing"
	"time"
)

const reportTo = "ops@msc.test"

// reportFx makes lA a line without EDI (mode report) at depot d1 in zone tz.
func reportFx(t *testing.T, tz string) *outFx {
	o := newOutFx(t)
	o.line(Profile{ReportTo: []string{reportTo}})
	o.exec(`UPDATE line_edi SET mode = 'report' WHERE line_id = 'lA'`)
	o.exec(`UPDATE depots SET timezone = $1`, tz)
	return o
}

// moveAt gates in a container and sets the time of its form.
func (o *outFx) moveAt(number, at string) string {
	id := o.gateIn(number, "E", "lA")
	o.exec(`UPDATE gate_moves SET form_saved_at = $1::timestamptz WHERE id = $2`, at, id)
	return id
}

func (o *outFx) reports(status string) int {
	return count(o.t, ctxOf("A"), o.pool, `SELECT count(*) FROM edi_messages WHERE kind = 'report' AND status = '`+status+`'`)
}

func TestReportSentOncePerDay(t *testing.T) {
	o := reportFx(t, "UTC")
	o.moveAt(cn("MSCU123457"), "2026-11-17T10:00:00Z")
	voided := o.moveAt(cn("MSCU123458"), "2026-11-17T11:00:00Z")
	o.exec(`UPDATE gate_moves SET voided = true WHERE id = $1`, voided)
	o.moveAt(cn("MSCU123459"), "2026-11-18T02:00:00Z")
	o.at = time.Date(2026, 11, 18, 6, 1, 0, 0, time.UTC)
	o.tick()
	sent := o.sent()
	if len(sent) != 1 || sent[0].To[0] != reportTo || sent[0].Subject != "MSC gate moves D1 2026-11-17" {
		t.Fatalf("sent = %+v", sent)
	}
	csv := string(sent[0].Attachments[0].Data)
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(csv, bom)), "\r\n")
	if !strings.HasPrefix(csv, bom) || len(lines) != 2 || !strings.HasPrefix(lines[0], "Time;Direction;Container;Type;Full/Empty;Truck;Haulier;Seal;Condition") ||
		!strings.HasPrefix(lines[1], "2026-11-17 10:00;in;"+cn("MSCU123457")) {
		t.Fatalf("csv = %q", csv)
	}
	if o.reports("sent") != 1 {
		t.Fatal("no sent report row")
	}
	o.tick()
	if len(o.sent()) != 1 {
		t.Fatalf("second tick sent again: %d mails", len(o.sent()))
	}
	o.at = o.at.Add(24*time.Hour - 2*time.Hour) // next day 04:01: before report time, nothing due
	o.tick()
	if len(o.sent()) != 1 {
		t.Fatal("sent before report time")
	}
}

func TestReportUsesDepotTimezone(t *testing.T) {
	o := reportFx(t, "Asia/Dubai")
	o.moveAt(cn("MSCU123457"), "2026-11-17T21:30:00Z")   // 01:30 on the 18th in Dubai
	o.at = time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC) // 07:00 on the 19th in Dubai
	o.tick()
	sent := o.sent()
	if len(sent) != 1 || sent[0].Subject != "MSC gate moves D1 2026-11-18" || !strings.Contains(string(sent[0].Attachments[0].Data), "2026-11-18 01:30;") {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestNoMovesNoMail(t *testing.T) {
	o := reportFx(t, "UTC")
	o.at = time.Date(2026, 11, 18, 6, 1, 0, 0, time.UTC)
	o.tick()
	o.tick()
	if len(o.sent()) != 0 || o.reports("ignored") != 1 {
		t.Fatalf("sent %d, ignored rows %d", len(o.sent()), o.reports("ignored"))
	}
}

func TestReportSendFailureIsRecorded(t *testing.T) {
	o := reportFx(t, "UTC")
	o.moveAt(cn("MSCU123457"), "2026-11-17T10:00:00Z")
	o.at = time.Date(2026, 11, 18, 6, 1, 0, 0, time.UTC)
	o.mail.fail = true
	o.tick()
	if o.reports("failed") != 1 || len(o.sent()) != 0 {
		t.Fatalf("failed rows %d", o.reports("failed"))
	}
}
