package recognition

import (
	"fmt"
	"math"
	"sort"

	"depot/internal/iso6346"
)

// Reconcile turns several photos of one truck into one decision.
//
// Ranking, in order:
//  1. Check digit. Arithmetic beats confidence, always.
//  2. A score built from each photo's confidence and the candidate's rank in
//     that photo, halved for a repaired guess.
//  3. The number itself, so the result never depends on the order the photos
//     arrive in.
//
// The leading number is auto-accepted only when nothing argues against it:
// it must be a direct read (a repaired guess only ever goes to review), no
// photo may lead with a different valid number, no photo may lead with a
// full-length number that is more than one character away from it (a second
// container on the same truck, even if that read fails its check digit), no
// photo may show two valid numbers, no photo may have failed, and its
// confidence must reach the threshold (a NaN threshold never does). It must
// then either be the only photo that read anything or be confirmed by two
// different sides of the container; two photos with the same role (or a
// second photo too partial to read) are not a second opinion.
func Reconcile(frames []Frame, autoAccept float64) Result {
	scores := map[string]float64{}
	direct := map[string]bool{}
	directConf := map[string]float64{}
	repairConf := map[string]float64{}
	repairFrom := map[string]string{}

	framesRead := 0
	var failedPhoto *Frame
	for i, f := range frames {
		if f.Error != "" && failedPhoto == nil {
			failedPhoto = &frames[i]
		}
		if len(f.Candidates) > 0 {
			framesRead++
		}
		for rank, c := range f.Candidates {
			score := c.Confidence / float64(1+rank)
			if c.RepairedFrom != "" {
				score *= 0.5
				if c.Confidence > repairConf[c.Number] {
					repairConf[c.Number] = c.Confidence
				}
				if from, ok := repairFrom[c.Number]; !ok || c.RepairedFrom < from {
					repairFrom[c.Number] = c.RepairedFrom
				}
			} else {
				direct[c.Number] = true
				if c.Confidence > directConf[c.Number] {
					directConf[c.Number] = c.Confidence
				}
			}
			scores[c.Number] += score
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
		if failedPhoto != nil {
			res.Reason = failedPhoto.Error
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
		c := Candidate{Number: n, CheckDigitValid: iso6346.IsValid(n)}
		if direct[n] {
			c.Confidence = directConf[n]
		} else {
			c.Confidence, c.RepairedFrom = repairConf[n], repairFrom[n]
		}
		res.Candidates = append(res.Candidates, c)
	}

	top := res.Candidates[0]
	res.Number, res.CheckDigitValid = top.Number, top.CheckDigitValid

	// What each photo actually read, as opposed to what repairs were guessed.
	agreeing := map[string]bool{}
	conflict, ambiguous := false, false
	for _, f := range frames {
		lead, valid, firstDirect := "", 0, ""
		for _, c := range f.Candidates {
			if c.RepairedFrom != "" {
				continue
			}
			if firstDirect == "" {
				firstDirect = c.Number
			}
			if !c.CheckDigitValid {
				continue
			}
			if lead == "" {
				lead = c.Number
			}
			valid++
		}
		switch {
		case lead == "":
		case lead == top.Number:
			agreeing[f.Role] = true
		default:
			conflict = true
		}
		// A full-length read that is not a one-character misread of the top
		// number is a different container, even if its check digit fails.
		if len(firstDirect) == 11 && differsByMoreThanOne(firstDirect, top.Number) {
			conflict = true
		}
		if valid > 1 {
			ambiguous = true
		}
	}
	agreeingRoles := len(agreeing)

	conf := top.Confidence
	if agreeingRoles > 1 {
		conf = math.Min(1, conf+0.10*float64(agreeingRoles-1))
	}

	switch {
	case !top.CheckDigitValid:
		res.Confidence, res.Decision = conf, DecisionReview
		res.Reason = "check digit failed on every candidate"
	case top.RepairedFrom != "":
		res.Confidence, res.Decision = math.Min(conf, 0.70), DecisionReview
		res.Reason = fmt.Sprintf("read as %s, which fails its check digit; %s is one character away and passes",
			top.RepairedFrom, top.Number)
	case conflict:
		res.Confidence, res.Decision = math.Min(conf, 0.70), DecisionReview
		res.Reason = "photos disagree"
	case ambiguous:
		res.Confidence, res.Decision = math.Min(conf, 0.70), DecisionReview
		res.Reason = "a photo shows more than one valid number"
	case failedPhoto != nil:
		res.Confidence, res.Decision = conf, DecisionReview
		res.Reason = "a photo could not be read: " + failedPhoto.Error
	case !(conf >= autoAccept): // written so a NaN threshold can never auto-accept
		res.Confidence, res.Decision = conf, DecisionReview
		res.Reason = fmt.Sprintf("confidence %.2f below auto-accept threshold", conf)
	case agreeingRoles >= 2 || framesRead <= 1:
		res.Confidence, res.Decision = conf, DecisionAutoAccept
		res.Reason = fmt.Sprintf("check digit valid, %d side(s) agree", max(agreeingRoles, 1))
	default:
		res.Confidence, res.Decision = conf, DecisionReview
		res.Reason = "only one side of the container confirmed the number"
	}
	return res
}

// differsByMoreThanOne reports whether a and b differ in more than one
// character position. Strings of different length always count as different.
func differsByMoreThanOne(a, b string) bool {
	if len(a) != len(b) {
		return true
	}
	diff := 0
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			diff++
			if diff > 1 {
				return true
			}
		}
	}
	return false
}

// majority returns the most common non-empty value; ties go to the value that
// reached the winning count first.
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
