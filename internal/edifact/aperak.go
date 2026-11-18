package edifact

import "strings"

// Answer is an APERAK: whether the answered messages were accepted, their
// references (message or interchange references), and the problems found.
type Answer struct {
	Accepted bool
	RefersTo []string
	Problems []Problem
}

// maxHeaderFTX is the D.00B limit on header FTX segments.
const maxHeaderFTX = 9

// APERAK builds an answer with message reference ref. A rejection carries one
// ERC group (code and FTX text) per problem. An acceptance carries no ERC, so
// that a reader never takes it for a rejection; its problems (warnings) go in
// header FTX segments instead.
func APERAK(ref string, a Answer) Message {
	fn := funcReject
	if a.Accepted {
		fn = funcAccept
	}
	segs := []Segment{
		seg("BGM", el(docAPERAK), el(Text(ref)), el(fn)),
		seg("DTM", el(dtmPrepared, now().Format("200601021504"), fmtDateTime)),
	}
	if a.Accepted {
		for _, p := range a.Problems[:min(len(a.Problems), maxHeaderFTX)] { // shortcut: warnings past 9 are dropped
			segs = append(segs, seg("FTX", el(ftxGeneral), nil, nil, el(cut(Text(p.Code+" "+p.Text), 512))))
		}
	}
	for _, r := range a.RefersTo {
		segs = append(segs, seg("RFF", el(rffPrevious, Text(r))))
	}
	if !a.Accepted {
		for _, p := range a.Problems {
			segs = append(segs,
				seg("ERC", el(cut(Text(p.Code), ercCodeSize))),
				seg("FTX", el(ftxError), nil, nil, el(cut(Text(p.Text), 512))))
		}
	}
	return Message{Ref: ref, Type: typeAPERAK, Segments: segs}
}

// ReadAPERAK reads an answer. It is a rejection when BGM's function is 27 or
// it has any ERC segment; each ERC's code and the text of the FTX that follows
// it are a problem. A rejection without ERC takes its FTX texts as problems.
// RefersTo holds the RFF+ACW references before the first ERC.
func ReadAPERAK(m Message) Answer {
	a := Answer{Accepted: true}
	erc := false
	var texts []Problem
	for _, s := range m.Segments {
		switch {
		case s.Tag == "BGM" && s.Get(2, 0) == funcReject:
			a.Accepted = false
		case s.Tag == "ERC":
			a.Accepted, erc = false, true
			a.Problems = append(a.Problems, Problem{Code: s.Get(0, 0)})
		case s.Tag == "RFF" && !erc && s.Get(0, 0) == rffPrevious:
			a.RefersTo = append(a.RefersTo, s.Get(0, 1))
		case s.Tag == "FTX" && len(s.Elements) > 3:
			text := strings.Join(s.Elements[3], " ") // C108: up to five lines of text
			if !erc {
				texts = append(texts, Problem{Text: text})
			} else if p := &a.Problems[len(a.Problems)-1]; p.Text == "" {
				p.Text = text
			}
		}
	}
	if !a.Accepted && !erc {
		a.Problems = texts
	}
	return a
}
