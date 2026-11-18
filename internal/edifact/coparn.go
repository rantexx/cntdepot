package edifact

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"depot/internal/iso6346"
)

// Item is one equipment line of an order: a named container (Number set,
// Quantity 1) or a type and quantity. FullEmpty is "E" or "F".
type Item struct {
	Number, TypeCode, FullEmpty string
	Quantity                    int
}

// Order is what one COPARN message asks for.
//   - Function is "9" (original), "5" (replace) or "1" (cancel).
//   - Kind is "release" or "redelivery".
//   - Location is the depot's LOC code, "UNLOCODE" or "UNLOCODE:FACILITY".
//   - ValidUntil is read as UTC wall-clock time; a date without a time means
//     the end of that day (23:59).
type Order struct {
	Function, Kind, Reference, Location string
	ValidUntil                          *time.Time
	Items                               []Item
}

// Problem is one thing wrong with a message. Codes starting with E_ are errors.
type Problem struct{ Code, Text string }

// ReadCOPARN reads one COPARN message. Problems name the segment position,
// counting UNH as segment 1.
func ReadCOPARN(m Message) (Order, []Problem) {
	var (
		o        Order
		problems []Problem
		docID    string
		refs     = map[string]string{}
		item     *Item
		itemPos  int
		locPos   int
	)
	add := func(pos int, s Segment, code, format string, args ...any) {
		problems = append(problems, Problem{code, fmt.Sprintf("segment %d (%s): ", pos, s.Tag) + fmt.Sprintf(format, args...)})
	}
	endItem := func() {
		if item != nil {
			o.Items = append(o.Items, *item)
			item = nil
		}
	}
	if !strings.HasPrefix(m.Type, "COPARN:") {
		problems = append(problems, Problem{"E_TYPE", fmt.Sprintf("message type %q is not COPARN", m.Type)})
	}
	for k, s := range m.Segments {
		pos := k + 2
		switch s.Tag {
		case "BGM":
			doc := s.Get(0, 0)
			if o.Kind = coparnKind[doc]; o.Kind == "" {
				add(pos, s, "E_DOC", "document code %q is not a release or redelivery order", doc)
			}
			docID, o.Function = s.Get(1, 0), s.Get(2, 0)
			if o.Function != funcOriginal && o.Function != funcReplace && o.Function != funcCancel {
				add(pos, s, "E_FUNC", "message function %q is not 9, 5 or 1", o.Function)
			}
		case "RFF":
			if item == nil && refs[s.Get(0, 0)] == "" {
				refs[s.Get(0, 0)] = s.Get(0, 1)
			}
		case "DTM":
			if s.Get(0, 0) != dtmExpiry || o.ValidUntil != nil {
				continue
			}
			t, err := readDate(s.Get(0, 1), s.Get(0, 2))
			if err != nil {
				add(pos, s, "E_DATE", "%v", err)
				continue
			}
			o.ValidUntil = &t
		case "EQD":
			endItem()
			item, itemPos = &Item{Number: s.Get(1, 0), TypeCode: s.Get(2, 0), Quantity: 1}, pos
			if q := s.Get(0, 0); q != "CN" {
				add(pos, s, "E_EQUIP", "equipment qualifier %q is not CN (container)", q)
			}
			if item.Number == "" && item.TypeCode == "" {
				add(pos, s, "E_EQUIP", "equipment has neither a container number nor a type")
			}
			if item.Number != "" {
				if v := iso6346.Validate(item.Number); !v.Valid {
					add(pos, s, "E_DIGIT", "container number %s fails the ISO 6346 check digit", item.Number)
				} else {
					item.Number = v.Normalized
				}
			}
			fe := s.Get(5, 0)
			if item.FullEmpty = fullEmptyCode[fe]; item.FullEmpty == "" {
				add(pos, s, "E_FE", "full or empty indicator %q is not 4 or 5", fe)
			}
		case "EQN":
			if item == nil {
				continue
			}
			q, err := strconv.Atoi(s.Get(0, 0))
			switch {
			case err != nil || q < 1:
				add(pos, s, "E_QTY", "quantity %q is not a positive number", s.Get(0, 0))
			case item.Number != "" && q != 1:
				add(pos, s, "E_QTY", "a named container (segment %d) has quantity %d", itemPos, q)
			default:
				item.Quantity = q
			}
		case "LOC":
			if !slices.Contains(depotLocQualifiers, s.Get(0, 0)) {
				continue
			}
			loc := s.Get(1, 0)
			if f := s.Get(2, 0); f != "" {
				loc += ":" + f
			}
			if o.Location != "" && o.Location != loc {
				add(pos, s, "E_LOC", "location %s differs from %s in segment %d", loc, o.Location, locPos)
				continue
			}
			o.Location, locPos = loc, pos
		}
	}
	endItem()
	for _, q := range orderRefQualifiers {
		if o.Reference = refs[q]; o.Reference != "" {
			break
		}
	}
	if o.Reference == "" {
		o.Reference = docID
	}
	if o.Reference == "" {
		problems = append(problems, Problem{"E_REF", "the message has no order reference (RFF or BGM)"})
	}
	if len(o.Items) == 0 && o.Function != funcCancel {
		problems = append(problems, Problem{"E_EQUIP", "the message has no equipment (EQD)"})
	}
	return o, problems
}

// readDate reads a DTM value in format 102 (CCYYMMDD, taken as 23:59 that
// day) or 203 (CCYYMMDDHHMM).
func readDate(v, format string) (time.Time, error) {
	switch format {
	case fmtDate:
		t, err := time.Parse("20060102", v)
		return t.Add(23*time.Hour + 59*time.Minute), err
	case fmtDateTime, "":
		return time.Parse("200601021504", v)
	}
	return time.Time{}, fmt.Errorf("date format %q is not 102 or 203", format)
}
