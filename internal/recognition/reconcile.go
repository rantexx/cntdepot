package recognition

import (
	"fmt"
	"math"
	"sort"

	"depot/internal/iso6346"
)

// Reconcile turns several photos of one truck into one decision.
//
// Weighting, in order:
//  1. Check digit. Arithmetic beats confidence, always.
//  2. Cross-photo agreement. Different sides reading the same valid number
//     is the strongest evidence short of a person.
//  3. Engine confidence, as a tie-breaker.
//
// A repaired candidate can lead the ranking but never auto-accepts.
func Reconcile(frames []Frame, autoAccept float64) Result {
	scores := map[string]float64{}
	bestConf := map[string]float64{}
	roles := map[string]map[string]bool{}
	repaired := map[string]string{}
	seen := map[string]bool{}

	for _, f := range frames {
		for rank, c := range f.Candidates {
			score := c.Confidence / float64(1+rank)
			if c.CheckDigitValid {
				score *= 3
			}
			if c.RepairedFrom != "" {
				score *= 0.5
			}
			scores[c.Number] += score
			if c.Confidence > bestConf[c.Number] {
				bestConf[c.Number] = c.Confidence
			}
			if !seen[c.Number] {
				seen[c.Number] = true
				repaired[c.Number] = c.RepairedFrom
			}
			if c.RepairedFrom == "" {
				if roles[c.Number] == nil {
					roles[c.Number] = map[string]bool{}
				}
				roles[c.Number][f.Role] = true
			}
		}
	}

	res := Result{
		Frames:   frames,
		TypeCode: majority(frames, func(f Frame) string { return f.TypeCode }),
		Color:    majority(frames, func(f Frame) string { return f.Color }),
	}
	if len(scores) == 0 {
		res.Decision = DecisionFailed
		res.Reason = "no container number legible in any photo"
		for _, f := range frames {
			if f.Error != "" {
				res.Reason = f.Error
				break
			}
		}
		return res
	}

	// Validity is the PRIMARY sort key, not a weighted term: leaving it to a
	// tunable multiplier means one day someone tunes the safety away.
	numbers := make([]string, 0, len(scores))
	for n := range scores {
		numbers = append(numbers, n)
	}
	sort.Slice(numbers, func(i, j int) bool {
		a, b := numbers[i], numbers[j]
		va, vb := iso6346.IsValid(a), iso6346.IsValid(b)
		if va != vb {
			return va
		}
		if scores[a] != scores[b] {
			return scores[a] > scores[b]
		}
		return a < b
	})
	if len(numbers) > 5 {
		numbers = numbers[:5]
	}
	for _, n := range numbers {
		res.Candidates = append(res.Candidates, Candidate{
			Number: n, Confidence: bestConf[n], CheckDigitValid: iso6346.IsValid(n), RepairedFrom: repaired[n],
		})
	}

	top := res.Candidates[0]
	agreement := len(roles[top.Number])
	framesRead := 0
	for _, f := range frames {
		if len(f.Candidates) > 0 {
			framesRead++
		}
	}
	conf := bestConf[top.Number]
	if agreement > 1 {
		conf = math.Min(1, conf+0.10*float64(agreement-1))
	}
	res.Number, res.CheckDigitValid = top.Number, top.CheckDigitValid

	switch {
	case !top.CheckDigitValid:
		res.Confidence, res.Decision = conf, DecisionReview
		res.Reason = "check digit failed on every candidate"
	case top.RepairedFrom != "":
		res.Confidence, res.Decision = math.Min(conf, 0.70), DecisionReview
		res.Reason = fmt.Sprintf("read as %s, which fails its check digit; %s is one character away and passes",
			top.RepairedFrom, top.Number)
	case conf >= autoAccept && (agreement > 1 || framesRead <= 1):
		res.Confidence, res.Decision = conf, DecisionAutoAccept
		res.Reason = fmt.Sprintf("check digit valid, %d photo(s) agree", agreement)
	case framesRead > 1 && agreement == 1:
		res.Confidence, res.Decision = math.Min(conf, 0.70), DecisionReview
		res.Reason = "photos disagree"
	default:
		res.Confidence, res.Decision = conf, DecisionReview
		res.Reason = fmt.Sprintf("confidence %.2f below auto-accept threshold", conf)
	}
	return res
}

// majority returns the most common non-empty value; ties go to the first seen.
func majority(frames []Frame, pick func(Frame) string) string {
	counts := map[string]int{}
	best, bestN := "", 0
	for _, f := range frames {
		v := pick(f)
		if v == "" {
			continue
		}
		counts[v]++
		if counts[v] > bestN {
			best, bestN = v, counts[v]
		}
	}
	return best
}
