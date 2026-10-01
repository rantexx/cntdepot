package recognition

import (
	"strings"
	"unicode/utf8"

	"depot/internal/iso6346"
)

// BuildFrame cleans up one engine reading. A one-character repair that
// satisfies the check digit is surfaced beside the original, because it is
// usually the right answer; it is marked so it can never auto-accept.
func BuildFrame(role string, r Reading) Frame {
	f := Frame{Role: role, Visible: clip(r.Visible, 16), Notes: clip(r.Notes, 200)}
	if f.Visible == "" {
		f.Visible = "none"
	}
	if tc := iso6346.Normalize(r.TypeCode); iso6346.ValidTypeCode(tc) {
		f.TypeCode = tc
	}
	f.Color = clip(strings.ToLower(strings.TrimSpace(r.Color)), 24)

	raw := r.Candidates
	if len(raw) > 4 {
		raw = raw[:4]
	}
	seen := map[string]bool{}
	for _, rc := range raw {
		number := iso6346.Normalize(rc.Number)
		if number == "" || seen[number] {
			continue
		}
		seen[number] = true
		conf := clamp01(rc.Confidence)
		ok := iso6346.IsValid(number)
		f.Candidates = append(f.Candidates, Candidate{Number: number, Confidence: conf, CheckDigitValid: ok})
		if ok {
			continue
		}
		for _, fixed := range iso6346.RepairCandidates(number) {
			if seen[fixed] {
				continue
			}
			seen[fixed] = true
			f.Candidates = append(f.Candidates, Candidate{
				Number: fixed, Confidence: conf * 0.6, CheckDigitValid: true, RepairedFrom: number,
			})
		}
	}
	return f
}

// ErrorFrame stands in for a photo whose reading failed.
func ErrorFrame(role, message string) Frame {
	return Frame{Role: role, Visible: "none", Error: message}
}

func clamp01(v float64) float64 {
	if !(v >= 0) { // also catches NaN
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
