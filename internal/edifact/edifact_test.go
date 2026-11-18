package edifact

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseFile(t *testing.T, name string) Interchange {
	t.Helper()
	i, err := Parse(readFile(t, name))
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return i
}

func fixNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

// One COPARN, written the ways lines really send it. Every spelling carries an
// escaped apostrophe inside free text.
const variantBody = `UNB+UNOA:2+MSC:ZZZ+DEPOTX:ZZZ+261118:0930+20001'UNH+20001+COPARN:D:00B:UN:SMDG21'BGM+108+RLS4471+9'RFF+ATY:RLS4471'EQD+CN+MSCU1234566+22G1:102:5+++4'FTX+AAI+++CALL O?'BRIEN?: GATE 2'UNT+6+20001'UNZ+1+20001'`

func TestParseVariants(t *testing.T) {
	star := strings.NewReplacer("+", "*").Replace(variantBody)
	variants := map[string]string{
		"default":        variantBody,
		"una star":       "UNA:*.? '" + star,
		"crlf":           "UNA:+.? '\r\n" + strings.ReplaceAll(strings.ReplaceAll(variantBody, "?'", "\x00"), "'", "'\r\n"),
		"lf and newline": strings.ReplaceAll(strings.ReplaceAll(variantBody, "?'", "\x00"), "'", "'\n") + "\n",
		"bom":            string(rune(0xFEFF)) + variantBody,
	}
	var want Interchange
	for _, name := range []string{"default", "una star", "crlf", "lf and newline", "bom"} {
		got, err := Parse([]byte(strings.ReplaceAll(variants[name], "\x00", "?'")))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "default" {
			want = got
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s parsed to\n%+v\nwant\n%+v", name, got, want)
		}
	}
	if len(want.Messages) != 1 || want.Sender != "MSC" || want.ControlRef != "20001" {
		t.Fatalf("interchange = %+v", want)
	}
	m := want.Messages[0]
	if m.Type != "COPARN:D:00B:UN:SMDG21" || m.Ref != "20001" || len(m.Segments) != 4 {
		t.Fatalf("message = %+v", m)
	}
	if ftx := m.Segments[3]; ftx.Tag != "FTX" || ftx.Elements[3][0] != "CALL O'BRIEN: GATE 2" {
		t.Fatalf("FTX = %+v", ftx)
	}
}

func TestRoundTrip(t *testing.T) {
	i := Interchange{
		Sender: "DEPOTX", SenderQual: "ZZZ", Recipient: "MSC", ControlRef: "77",
		Prepared: time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC), Test: true,
		Messages: []Message{
			{Ref: "1", Type: "COPARN:D:00B:UN:SMDG21", Segments: []Segment{
				{Tag: "BGM", Elements: [][]string{{"108"}, {"R+1"}, {"9"}}},
				{Tag: "FTX", Elements: [][]string{{"AAI"}, nil, nil, {"A'B", "C:D?"}}},
			}},
			{Ref: "2", Type: "APERAK:D:00B:UN", Segments: []Segment{
				{Tag: "BGM", Elements: [][]string{{"305"}, {"2"}, {"29"}}},
			}},
		},
	}
	enc := i.Encode()
	if bytes.ContainsAny(enc, "\r\n") {
		t.Fatalf("Encode wrote a newline: %q", enc)
	}
	for _, part := range []string{"UNA:+.? 'UNB+UNOA:2+DEPOTX:ZZZ+MSC+261118:0930+77++++++1'", "UNT+4+1'", "UNT+3+2'", "UNZ+2+77'", "R?+1", "A?'B:C?:D??"} {
		if !bytes.Contains(enc, []byte(part)) {
			t.Errorf("encoded %q lacks %q", enc, part)
		}
	}
	got, err := Parse(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, i) {
		t.Fatalf("round trip\n got %+v\nwant %+v", got, i)
	}
}

func TestParseRejectsBrokenStructure(t *testing.T) {
	for name, s := range map[string]string{
		"no UNB":      "UNH+1+COPARN:D:00B:UN'UNT+2+1'",
		"open UNH":    "UNB+UNOA:2+A+B+261118:0930+1'UNH+1+COPARN:D:00B:UN'BGM+108'UNZ+1+1'",
		"no UNZ":      "UNB+UNOA:2+A+B+261118:0930+1'UNH+1+COPARN:D:00B:UN'UNT+2+1'",
		"outside UNH": "UNB+UNOA:2+A+B+261118:0930+1'BGM+108'UNZ+0+1'",
		"empty":       "",
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestReadCOPARNRelease(t *testing.T) {
	i := parseFile(t, "coparn_release.edi")
	o, problems := ReadCOPARN(i.Messages[0])
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	if o.Kind != "release" || o.Function != "9" || o.Reference != "RLS4471" || o.Location != "TRMER" {
		t.Fatalf("order = %+v", o)
	}
	if want := []Item{{Number: "MSCU1234566", TypeCode: "22G1", FullEmpty: "E", Quantity: 1}}; !reflect.DeepEqual(o.Items, want) {
		t.Fatalf("items = %+v", o.Items)
	}
	if o.ValidUntil == nil || !o.ValidUntil.Equal(time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("ValidUntil = %v", o.ValidUntil)
	}

	c, problems := ReadCOPARN(parseFile(t, "coparn_cancel.edi").Messages[0])
	if len(problems) != 0 || c.Function != "1" || c.Kind != "release" || c.Reference != "RLS4471" {
		t.Fatalf("cancel = %+v %+v", c, problems)
	}
}

func TestReadCOPARNQuantity(t *testing.T) {
	o, problems := ReadCOPARN(parseFile(t, "coparn_redelivery_quantity.edi").Messages[0])
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	if o.Kind != "redelivery" || o.Reference != "ACC889" || o.Location != "TRMER" || len(o.Items) != 2 {
		t.Fatalf("order = %+v", o)
	}
	if want := (Item{TypeCode: "22G1", FullEmpty: "E", Quantity: 3}); o.Items[0] != want {
		t.Fatalf("item = %+v", o.Items[0])
	}
	// A date-only validity lasts to the end of that day.
	if o.ValidUntil == nil || !o.ValidUntil.Equal(time.Date(2026, 12, 5, 23, 59, 0, 0, time.UTC)) {
		t.Fatalf("ValidUntil = %v", o.ValidUntil)
	}
}

func TestReadCOPARNProblems(t *testing.T) {
	m := Message{Ref: "1", Type: "COPARN:D:00B:UN:SMDG21", Segments: []Segment{
		{Tag: "BGM", Elements: [][]string{{"108"}, {"R1"}, {"9"}}},                                         // 2
		{Tag: "RFF", Elements: [][]string{{"ATY", "R1"}}},                                                  // 3
		{Tag: "EQD", Elements: [][]string{{"CN"}, {"MSCU1234565"}, {"22G1", "102", "5"}, nil, nil, {"4"}}}, // 4
		{Tag: "EQD", Elements: [][]string{{"CN"}, nil, nil, nil, nil, {"4"}}},                              // 5
	}}
	_, problems := ReadCOPARN(m)
	if len(problems) != 2 {
		t.Fatalf("problems = %+v", problems)
	}
	if p := problems[0]; p.Code != "E-DIGIT" || !strings.Contains(p.Text, "segment 4") || !strings.Contains(p.Text, "MSCU1234565") {
		t.Errorf("check digit problem = %+v", p)
	}
	if p := problems[1]; p.Code != "E-EQUIP" || !strings.Contains(p.Text, "segment 5") {
		t.Errorf("equipment problem = %+v", p)
	}
}

func TestReadCOPARNUnknownDocument(t *testing.T) {
	m := Message{Type: "COPARN:D:00B:UN:SMDG21", Segments: []Segment{
		{Tag: "BGM", Elements: [][]string{{"127"}, {"R1"}, {"9"}}},
		{Tag: "EQD", Elements: [][]string{{"CN"}, {"MSCU1234566"}, nil, nil, nil, {"4"}}},
	}}
	_, problems := ReadCOPARN(m)
	if len(problems) != 1 || problems[0].Code != "E-DOC" {
		t.Fatalf("problems = %+v", problems)
	}
}

func TestReadCOPARNWithoutBGM(t *testing.T) {
	m := Message{Type: "COPARN:D:00B:UN:SMDG21", Segments: []Segment{
		{Tag: "RFF", Elements: [][]string{{"ATY", "R1"}}},
		{Tag: "EQD", Elements: [][]string{{"CN"}, {"MSCU1234566"}, nil, nil, nil, {"4"}}},
	}}
	_, problems := ReadCOPARN(m)
	if len(problems) != 1 || problems[0].Code != "E-BGM" {
		t.Fatalf("problems = %+v", problems)
	}
}

func TestReadCOPARNTypeCodes(t *testing.T) {
	for code, ok := range map[string]bool{"22G1": true, "45R1": true, "L5G1": true, "M2G1": true, "22g1": false, "20DV": true,
		"A2G1": false, "22G": false, "22G10": false, "22_1": false} {
		m := Message{Type: "COPARN:D:00B:UN:SMDG21", Segments: []Segment{
			{Tag: "BGM", Elements: [][]string{{"108"}, {"R1"}, {"9"}}},
			{Tag: "EQD", Elements: [][]string{{"CN"}, nil, {code, "102", "5"}, nil, nil, {"4"}}},
		}}
		_, problems := ReadCOPARN(m)
		if got := len(problems) == 0; got != ok || (!ok && (len(problems) != 1 || problems[0].Code != "E-TYPE")) {
			t.Errorf("type %q: problems = %+v", code, problems)
		}
	}
}

func goldenMove() GateMove {
	return GateMove{
		Function: "9", Direction: "in", DocNumber: "MV20261118001", Number: "MSCU1234566", TypeCode: "22G1",
		FullEmpty: "E", Seal: "S12345", TruckPlate: "34ABC12", HaulierCode: "ACMETR", Condition: "damaged",
		OrderRef: "ACC889", LineCode: "MSC", Location: "TRMER:DEP01",
		At:      time.Date(2026, 11, 18, 14, 5, 0, 0, time.UTC),
		Damages: []Damage{{Location: "door", Description: "dent"}},
	}
}

func TestCODECOGolden(t *testing.T) {
	at := time.Date(2026, 11, 18, 14, 7, 0, 0, time.UTC)
	fixNow(t, at)
	i := Interchange{Sender: "DEPOTX", SenderQual: "ZZZ", Recipient: "MSC", RecipientQual: "ZZZ", ControlRef: "30001",
		Prepared: at, Messages: []Message{CODECO("30001", goldenMove())}}
	got, want := i.Encode(), readFile(t, "codeco_gate_in.edi")
	if !bytes.Equal(got, want) {
		t.Fatalf("CODECO\n got %s\nwant %s", got, want)
	}
}

func TestCODECOGateOutReplace(t *testing.T) {
	fixNow(t, time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC))
	g := goldenMove()
	g.Function, g.Direction, g.FullEmpty, g.Damages, g.Condition = "5", "out", "F", nil, ""
	enc := string(Interchange{Sender: "A", Recipient: "B", ControlRef: "1", Messages: []Message{CODECO("1", g)}}.Encode())
	for _, part := range []string{"BGM+111+MV20261118001+5'", "RFF+ATY:ACC889'", "RFF+ACW:MV20261118001'", "+++5'", "TDT+30++3"} {
		if !strings.Contains(enc, part) {
			t.Errorf("%s lacks %q", enc, part)
		}
	}
}

// A cancel of a sent original or replacement, read back from its file, is
// the message CODECO builds with function 1 from the same facts.
func TestCancelCODECOKeepsSentFacts(t *testing.T) {
	fixNow(t, time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC))
	cancel := goldenMove()
	cancel.Function = "1"
	want := Interchange{ControlRef: "2", Messages: []Message{CODECO("2", cancel)}}.Encode()
	for _, fn := range []string{"9", "5"} {
		g := goldenMove()
		g.Function = fn
		sent, err := Parse(Interchange{ControlRef: "1", Messages: []Message{CODECO("1", g)}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if got := (Interchange{ControlRef: "2", Messages: []Message{CancelCODECO("2", sent.Messages[0])}}.Encode()); !bytes.Equal(got, want) {
			t.Errorf("cancel of %s\n got %s\nwant %s", fn, got, want)
		}
	}
}

func TestCODECOTransliteratesAndEscapes(t *testing.T) {
	if got := Text("ŞÜKRÜ ÇİFTÇİ"); got != "SUKRU CIFTCI" {
		t.Fatalf("Text = %q", got)
	}
	if got := Text("şükrü çiftçi ğö ı_x"); got != "SUKRU CIFTCI GO I-X" {
		t.Fatalf("Text lower = %q", got)
	}
	fixNow(t, time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC))
	g := goldenMove()
	g.Seal = "A'B"
	g.Damages = []Damage{{Location: "kapı", Description: "ŞÜKRÜ ÇİFTÇİ çizdi"}}
	enc := Interchange{Sender: "A", Recipient: "B", ControlRef: "1", Messages: []Message{CODECO("1", g)}}.Encode()
	for _, part := range []string{"SEL+A?'B'", "DAM+1+:::SUKRU CIFTCI CIZDI+:::KAPI'"} {
		if !bytes.Contains(enc, []byte(part)) {
			t.Errorf("%s lacks %q", enc, part)
		}
	}
	for _, c := range enc {
		if c > 0x7e {
			t.Fatalf("non-ASCII byte in %q", enc)
		}
	}
}

func TestAPERAKRoundTrip(t *testing.T) {
	fixNow(t, time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC))
	a := Answer{RefersTo: []string{"20001"}, Problems: []Problem{
		{Code: "E-DIGIT", Text: "segment 4 (EQD): bad check digit"},
		{Code: "E-DEPOT", Text: "no depot for location TRMER"},
	}}
	i := Interchange{Sender: "DEPOTX", Recipient: "MSC", ControlRef: "9", Messages: []Message{APERAK("9", a)}}
	got, err := Parse(i.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages[0].Type != "APERAK:D:00B:UN" {
		t.Fatalf("type = %q", got.Messages[0].Type)
	}
	r := ReadAPERAK(got.Messages[0])
	if r.Accepted || !reflect.DeepEqual(r.RefersTo, []string{"20001"}) || len(r.Problems) != 2 {
		t.Fatalf("answer = %+v", r)
	}
	if r.Problems[0].Text != "SEGMENT 4 (EQD): BAD CHECK DIGIT" || r.Problems[1].Text != "NO DEPOT FOR LOCATION TRMER" {
		t.Fatalf("problems = %+v", r.Problems)
	}

	ok := ReadAPERAK(APERAK("10", Answer{Accepted: true, RefersTo: []string{"20002"}, Problems: []Problem{{Code: "W-KEPT", Text: "kept"}}}))
	if !ok.Accepted || len(ok.Problems) != 0 || ok.RefersTo[0] != "20002" {
		t.Fatalf("accepted answer = %+v", ok)
	}

	line := ReadAPERAK(parseFile(t, "aperak_reject.edi").Messages[0])
	if line.Accepted || !reflect.DeepEqual(line.RefersTo, []string{"30001"}) ||
		!reflect.DeepEqual(line.Problems, []Problem{{"12", "EQUIPMENT NUMBER UNKNOWN"}, {"13", "GATE TIME MISSING"}}) {
		t.Fatalf("line answer = %+v", line)
	}
}

// BGM function 27 rejects even without ERC; its FTX texts are the problems. 29 accepts.
func TestAPERAKFunctionDecides(t *testing.T) {
	msg := func(fn string) Message {
		return Message{Type: typeAPERAK, Segments: []Segment{
			seg("BGM", el(docAPERAK), el("5"), el(fn)),
			seg("FTX", el(ftxError), nil, nil, el("BOOKING CLOSED")),
			seg("RFF", el(rffPrevious, "30001")),
		}}
	}
	r := ReadAPERAK(msg("27"))
	if r.Accepted || !reflect.DeepEqual(r.RefersTo, []string{"30001"}) || !reflect.DeepEqual(r.Problems, []Problem{{"", "BOOKING CLOSED"}}) {
		t.Fatalf("27 without ERC = %+v", r)
	}
	if r := ReadAPERAK(msg("29")); !r.Accepted || len(r.Problems) != 0 || r.RefersTo[0] != "30001" {
		t.Fatalf("29 = %+v", r)
	}
}
