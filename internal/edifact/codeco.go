package edifact

import (
	"strings"
	"time"
)

// Damage is one damage noted at the gate, as free text.
type Damage struct{ Location, Description string }

// GateMove is one gate-in or gate-out to report in a CODECO.
//   - Function is "9", "5" or "1". For 5 and 1, DocNumber must be the same as in
//     the message being replaced or cancelled: it is sent as RFF+ACW.
//   - Direction is "in" or "out"; FullEmpty is "E" or "F".
//   - Location is the depot's code, "UNLOCODE" or "UNLOCODE:FACILITY".
//   - At is the gate time, written in its own time zone.
type GateMove struct {
	Function, Direction, DocNumber, Number, TypeCode, FullEmpty, Seal, TruckPlate, HaulierCode, Condition, OrderRef, LineCode, Location string
	At                                                                                                                                  time.Time
	Damages                                                                                                                             []Damage
}

// CODECO builds the gate message for g with message reference ref, following
// the SMDG CODECO 2.1 segment order (CODECO p.8-9).
func CODECO(ref string, g GateMove) Message {
	doc, rff, stage := docGateIn, rffAcceptance, tdtPre
	if g.Direction == "out" {
		doc, rff, stage = docGateOut, rffRelease, tdtOn
	}
	fe := "4"
	if g.FullEmpty == "F" {
		fe = "5"
	}
	segs := []Segment{
		seg("BGM", el(doc), el(Text(g.DocNumber)), el(g.Function)),
		seg("DTM", el(dtmPrepared, now().Format("200601021504"), fmtDateTime)),
	}
	if g.OrderRef != "" {
		segs = append(segs, seg("RFF", el(rff, Text(g.OrderRef))))
	}
	if g.Function == funcReplace || g.Function == funcCancel {
		segs = append(segs, seg("RFF", el(rffPrevious, Text(g.DocNumber))))
	}
	if g.LineCode != "" {
		segs = append(segs, seg("NAD", el(nadLine), el(Text(g.LineCode), "160", "ZZZ")))
	}
	segs = append(segs,
		seg("EQD", el("CN"), el(Text(g.Number)), el(Text(g.TypeCode), "102", "5"), nil, nil, el(fe)),
		seg("DTM", el(dtmEffect, g.At.Format("200601021504"), fmtDateTime)),
	)
	if g.Location != "" {
		code, facility, _ := strings.Cut(Text(g.Location), ":")
		loc := seg("LOC", el(locActivity), el(code, "139", "6"))
		if facility != "" {
			loc.Elements = append(loc.Elements, el(facility, "72", "SMD"))
		}
		segs = append(segs, loc)
	}
	if g.Seal != "" {
		segs = append(segs, seg("SEL", el(Text(g.Seal))))
	}
	if g.Condition != "" {
		segs = append(segs, seg("FTX", el(ftxGeneral), nil, nil, el(Text(g.Condition))))
	}
	for _, d := range g.Damages {
		// shortcut: free text only (e7500/e7502, an..35); ISO 9897 codes when the gate records them.
		segs = append(segs, seg("DAM", el(damEquipment), el("", "", "", cut(Text(d.Description), 35)), el("", "", "", cut(Text(d.Location), 35))))
	}
	if g.TruckPlate != "" || g.HaulierCode != "" {
		segs = append(segs, seg("TDT", el(stage), nil, el(modeRoad), nil, el(Text(g.HaulierCode)), nil, nil, el(Text(g.TruckPlate))))
	}
	segs = append(segs, seg("CNT", el(cntEquipment, "1")))
	return Message{Ref: ref, Type: typeCODECO, Segments: segs}
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// CancelCODECO builds the cancellation (function 1) of CODECO message prev
// with message reference ref: prev's facts as sent, prepared now, naming
// prev's document number in RFF+ACW (CODECO p.14, p.17).
func CancelCODECO(ref string, prev Message) Message {
	var segs []Segment
	doc := ""
	for _, s := range prev.Segments {
		switch {
		case s.Tag == "BGM":
			doc = s.Get(1, 0)
			s = seg("BGM", el(s.Get(0, 0)), el(doc), el(funcCancel))
		case s.Tag == "DTM" && s.Get(0, 0) == dtmPrepared:
			s = seg("DTM", el(dtmPrepared, now().Format("200601021504"), fmtDateTime))
		case s.Tag == "RFF" && s.Get(0, 0) == rffPrevious:
			continue // a replacement's own ACW: added again below
		case s.Tag != "DTM" && s.Tag != "RFF" && doc != "": // the header ends: ACW goes last in it, as CODECO writes it
			segs, doc = append(segs, seg("RFF", el(rffPrevious, doc))), ""
		}
		segs = append(segs, s)
	}
	return Message{Ref: ref, Type: prev.Type, Segments: segs}
}
