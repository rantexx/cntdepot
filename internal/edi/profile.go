package edi

import (
	"context"
	"encoding/json"
	"strings"

	edidb "depot/internal/edi/db"
	"depot/internal/tenancy"
)

// Profile is a line's EDI settings (spec 5.2), stored as line_edi.profile.
//   - SenderID/SenderQual: the line's UNB id on the files it sends us.
//   - RecipientID/RecipientQual: the line's UNB id on the files we send it.
//   - LineCode: the line's code in messages; "" is the line's own code.
//   - SendTo: where CODECO and APERAK go; AllowedFrom: who may mail us files.
//   - TypeMap: pairs of {ours, theirs} type codes, used both ways.
//   - ReportTo and ReportTime ("HH:MM", default "06:00"): the daily report of a line without EDI.
type Profile struct {
	SenderID, SenderQual, RecipientID, RecipientQual, LineCode string
	SendTo, AllowedFrom                                        []string
	EmptiesOnly, SendAPERAK, Test                              bool
	TypeMap                                                    [][2]string
	ReportTo                                                   []string
	ReportTime                                                 string
}

// LineEDI is one line's EDI row. Mode is "edi", "report" or "off"; Code is the line's own code.
type LineEDI struct {
	LineID, Mode, Code string
	Profile            Profile
}

// ours maps the line's type code to ours.
func (p Profile) ours(code string) string {
	for _, m := range p.TypeMap {
		if strings.EqualFold(m[1], code) {
			return m[0]
		}
	}
	return code
}

// theirs maps our type code to the line's (spec 5.8.8: the map works both ways).
func (p Profile) theirs(code string) string {
	for _, m := range p.TypeMap {
		if strings.EqualFold(m[0], code) {
			return m[1]
		}
	}
	return code
}

// lines returns the company's line EDI rows.
func (s *Store) lines(ctx context.Context) (out []LineEDI, err error) {
	err = s.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		rows, err := q.ListLineEDI(ctx, sc.CompanyID)
		for _, r := range rows {
			l, err := lineOf(r.LineID, r.Mode, r.Code, r.Profile)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return err
	})
	return out, err
}

// lineOf reads a line_edi row; the report time defaults to 06:00.
func lineOf(lineID, mode, code string, profile []byte) (LineEDI, error) {
	l := LineEDI{LineID: lineID, Mode: mode, Code: code}
	if err := json.Unmarshal(profile, &l.Profile); err != nil {
		return LineEDI{}, err
	}
	if l.Profile.ReportTime == "" {
		l.Profile.ReportTime = "06:00"
	}
	return l, nil
}
