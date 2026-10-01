package recognition

import (
	"strings"
	"unicode/utf8"

	"depot/internal/iso6346"
)

// maxDirectCandidates caps how many distinct numbers one reading may offer.
const maxDirectCandidates = 4

// BuildFrame cleans up one engine reading. A one-character repair that
// satisfies the check digit is surfaced beside the original, because it is
// usually the right answer; repairs are marked with RepairedFrom, and
// Reconcile is what keeps them from auto-accepting. The result does not
// depend on the order the engine listed its candidates in, and a number the
// engine read directly is never replaced by a repair of the same number.
func BuildFrame(role string, r Reading) Frame {
	f := Frame{Role: role, Visible: normalizeVisible(r.Visible), Notes: clip(r.Notes, 200)}
	if tc := iso6346.Normalize(r.TypeCode); iso6346.ValidTypeCode(tc) {
		f.TypeCode = tc
	}
	f.Color = clip(strings.ToLower(strings.TrimSpace(r.Color)), 24)

	// First pass: what the engine read directly. Normalize, drop blanks and
	// merge duplicates keeping the best confidence, in first-seen order.
	var direct []Candidate
	index := map[string]int{}
	for _, rc := range r.Candidates {
		number := iso6346.Normalize(rc.Number)
		if number == "" {
			continue
		}
		conf := clamp01(rc.Confidence)
		if i, ok := index[number]; ok {
			if conf > direct[i].Confidence {
				direct[i].Confidence = conf
			}
			continue
		}
		index[number] = len(direct)
		direct = append(direct, Candidate{Number: number, Confidence: conf, CheckDigitValid: iso6346.IsValid(number)})
	}
	if len(direct) > maxDirectCandidates {
		direct = direct[:maxDirectCandidates]
	}

	// Second pass: every direct read is in before any repair is considered,
	// so a repair can never shadow a number the engine read itself.
	seen := make(map[string]bool, len(direct))
	for _, c := range direct {
		seen[c.Number] = true
	}
	for _, c := range direct {
		f.Candidates = append(f.Candidates, c)
		if c.CheckDigitValid {
			continue
		}
		for _, fixed := range iso6346.RepairCandidates(c.Number) {
			if seen[fixed] {
				continue
			}
			seen[fixed] = true
			f.Candidates = append(f.Candidates, Candidate{
				Number: fixed, Confidence: c.Confidence * 0.6, CheckDigitValid: true, RepairedFrom: c.Number,
			})
		}
	}
	return f
}

// ErrorFrame stands in for a photo whose reading failed.
func ErrorFrame(role, message string) Frame {
	return Frame{Role: role, Visible: "none", Error: message}
}

// normalizeVisible maps an engine's free-text visibility to one of the three
// values the rest of the system understands.
func normalizeVisible(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "full", "partial", "none":
		return v
	}
	return "none"
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
