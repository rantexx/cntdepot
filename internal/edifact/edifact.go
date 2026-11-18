// Package edifact reads and writes UN/EDIFACT interchanges and builds or reads
// the three shipping-line messages depotd exchanges: COPARN (orders in), CODECO
// (gate moves out) and APERAK (answers both ways). It is pure: no database.
package edifact

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Segment is one EDIFACT segment. Each element is its list of components; an
// empty element is nil. Trailing empty elements and components are left out.
type Segment struct {
	Tag      string
	Elements [][]string
}

// Get returns component c of element e (both from 0, after the tag), or "".
func (s Segment) Get(e, c int) string {
	if e < len(s.Elements) && c < len(s.Elements[e]) {
		return s.Elements[e][c]
	}
	return ""
}

// Message is one UNH..UNT message. Segments holds what is between UNH and UNT.
// Type is the full UNH S009, for example "CODECO:D:00B:UN:SMDG21".
type Message struct {
	Ref, Type string
	Segments  []Segment
}

// Interchange is one UNB..UNZ interchange.
type Interchange struct {
	Sender, SenderQual, Recipient, RecipientQual, ControlRef string
	Prepared                                                 time.Time
	Test                                                     bool
	Messages                                                 []Message
}

// now is the message preparation clock (DTM+137); tests replace it.
var now = time.Now

type separators struct{ comp, elem, release, term byte }

var defaultSeps = separators{':', '+', '?', '\''}

// Parse reads an interchange. It honours a UNA service string, the release
// character, and CR/LF (or LF) between segments.
func Parse(data []byte) (Interchange, error) {
	data = bytes.TrimLeftFunc(data, func(r rune) bool { return r == 0xFEFF || unicode.IsSpace(r) }) // a BOM or blank lines before UNA/UNB
	seps := defaultSeps
	if bytes.HasPrefix(data, []byte("UNA")) {
		if len(data) < 9 {
			return Interchange{}, errors.New("edifact: short UNA")
		}
		una := data[3:9] // component, element, decimal mark, release, reserved, terminator
		seps = separators{una[0], una[1], una[3], una[5]}
		data = data[9:]
	}
	segs := split(data, seps)
	if len(segs) == 0 || segs[0].Tag != "UNB" {
		return Interchange{}, errors.New("edifact: no UNB segment")
	}
	unb := segs[0]
	ic := Interchange{
		Sender: unb.Get(1, 0), SenderQual: unb.Get(1, 1),
		Recipient: unb.Get(2, 0), RecipientQual: unb.Get(2, 1),
		ControlRef: unb.Get(4, 0), Test: unb.Get(10, 0) == "1",
	}
	if d, tm := unb.Get(3, 0), unb.Get(3, 1); d != "" {
		layout := "0601021504"
		if len(d) == 8 {
			layout = "200601021504"
		}
		p, err := time.Parse(layout, d+tm)
		if err != nil {
			return Interchange{}, fmt.Errorf("edifact: UNB date %q: %w", d+":"+tm, err)
		}
		ic.Prepared = p
	}
	var cur *Message
	closed := false
	for n, s := range segs[1:] {
		switch {
		case closed:
			return Interchange{}, fmt.Errorf("edifact: segment %s after UNZ", s.Tag)
		case s.Tag == "UNH":
			if cur != nil {
				return Interchange{}, fmt.Errorf("edifact: UNH %s before UNT of %s", s.Get(0, 0), cur.Ref)
			}
			cur = &Message{Ref: s.Get(0, 0)}
			if len(s.Elements) > 1 {
				cur.Type = strings.Join(s.Elements[1], ":")
			}
		case s.Tag == "UNT":
			if cur == nil {
				return Interchange{}, errors.New("edifact: UNT without UNH")
			}
			ic.Messages = append(ic.Messages, *cur)
			cur = nil
		case s.Tag == "UNZ":
			if cur != nil {
				return Interchange{}, fmt.Errorf("edifact: message %s has no UNT", cur.Ref)
			}
			closed = true
		case cur == nil:
			return Interchange{}, fmt.Errorf("edifact: segment %d (%s) outside a message", n+2, s.Tag)
		default:
			cur.Segments = append(cur.Segments, s)
		}
	}
	if !closed {
		return Interchange{}, errors.New("edifact: no UNZ segment")
	}
	return ic, nil
}

// split cuts data into segments, elements and components, removing release
// characters and skipping line breaks between segments.
func split(data []byte, sp separators) []Segment {
	var (
		segs  []Segment
		elems [][]string
		comps []string
		cur   []byte
		empty = true // nothing read yet in this segment
	)
	endComp := func() { comps = append(comps, string(cur)); cur = cur[:0] }
	endElem := func() { endComp(); elems = append(elems, trimComps(comps)); comps = nil }
	for i := 0; i < len(data); i++ {
		c := data[i]
		if empty && (c == '\r' || c == '\n') {
			continue
		}
		empty = false
		switch c {
		case sp.release:
			if i+1 < len(data) {
				i++
				cur = append(cur, data[i])
			}
		case sp.comp:
			endComp()
		case sp.elem:
			endElem()
		case sp.term:
			endElem()
			for len(elems) > 1 && elems[len(elems)-1] == nil {
				elems = elems[:len(elems)-1]
			}
			if len(elems[0]) > 0 { // a stray terminator makes no segment
				s := Segment{Tag: elems[0][0]}
				if len(elems) > 1 {
					s.Elements = elems[1:]
				}
				segs = append(segs, s)
			}
			elems, empty = nil, true
		default:
			cur = append(cur, c)
		}
	}
	return segs
}

func trimComps(c []string) []string {
	for len(c) > 0 && c[len(c)-1] == "" {
		c = c[:len(c)-1]
	}
	if len(c) == 0 {
		return nil
	}
	return c
}

// Encode writes the interchange with UNA, UNB (UNOA:2), UNH/UNT and UNZ,
// counting segments, escaping ? ' + : and adding no line breaks. Values must
// already be UNOA text (see Text).
func (i Interchange) Encode() []byte {
	var b bytes.Buffer
	b.WriteString("UNA:+.? '")
	unb := [][]string{{"UNOA", "2"}, {i.Sender, i.SenderQual}, {i.Recipient, i.RecipientQual},
		{i.Prepared.Format("060102"), i.Prepared.Format("1504")}, {i.ControlRef}}
	if i.Test {
		unb = append(unb, nil, nil, nil, nil, nil, []string{"1"})
	}
	writeSeg(&b, Segment{"UNB", unb})
	for _, m := range i.Messages {
		writeSeg(&b, Segment{"UNH", [][]string{{m.Ref}, strings.Split(m.Type, ":")}})
		for _, s := range m.Segments {
			writeSeg(&b, s)
		}
		writeSeg(&b, Segment{"UNT", [][]string{{strconv.Itoa(len(m.Segments) + 2)}, {m.Ref}}})
	}
	writeSeg(&b, Segment{"UNZ", [][]string{{strconv.Itoa(len(i.Messages))}, {i.ControlRef}}})
	return b.Bytes()
}

var escaper = strings.NewReplacer("?", "??", "'", "?'", "+", "?+", ":", "?:")

func writeSeg(b *bytes.Buffer, s Segment) {
	elems := make([]string, 0, len(s.Elements))
	for _, e := range s.Elements {
		comps := make([]string, len(trimComps(e)))
		for k := range comps {
			comps[k] = escaper.Replace(e[k])
		}
		elems = append(elems, strings.Join(comps, ":"))
	}
	for len(elems) > 0 && elems[len(elems)-1] == "" {
		elems = elems[:len(elems)-1]
	}
	b.WriteString(s.Tag)
	for _, e := range elems {
		b.WriteByte('+')
		b.WriteString(e)
	}
	b.WriteByte('\'')
}

// transliterate maps letters outside UNOA to their plain Latin form (Turkish
// first, then the common Western European accents).
var transliterate = strings.NewReplacer(
	"Ş", "S", "Ğ", "G", "İ", "I", "Ü", "U", "Ö", "O", "Ç", "C",
	"À", "A", "Á", "A", "Â", "A", "Ã", "A", "Ä", "A", "Å", "A", "Æ", "AE",
	"È", "E", "É", "E", "Ê", "E", "Ë", "E", "Ì", "I", "Í", "I", "Î", "I", "Ï", "I",
	"Ñ", "N", "Ò", "O", "Ó", "O", "Ô", "O", "Õ", "O", "Ø", "O",
	"Ù", "U", "Ú", "U", "Û", "U", "Ý", "Y", "ß", "SS",
)

// Text makes s valid UNOA (level A) text: upper case, Turkish and Western
// accents transliterated, line breaks and tabs as spaces, and any other
// character as '-'. Separator escaping happens in Encode.
func Text(s string) string {
	s = transliterate.Replace(strings.ToUpper(s)) // ToUpper turns ı and i into I
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune(" .,-()/='+:?!\"%&*;<>", r):
			return r
		case r == '\r' || r == '\n' || r == '\t':
			return ' '
		}
		return '-'
	}, s)
}

// seg builds a segment from elements written as components.
func seg(tag string, elems ...[]string) Segment {
	for k := range elems {
		elems[k] = trimComps(elems[k])
	}
	for len(elems) > 0 && elems[len(elems)-1] == nil {
		elems = elems[:len(elems)-1]
	}
	if len(elems) == 0 {
		elems = nil
	}
	return Segment{Tag: tag, Elements: elems}
}

// el is shorthand for one element's components.
func el(comps ...string) []string { return comps }
