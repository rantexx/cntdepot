// Package iso6346 validates container numbers and size/type codes (ISO 6346).
//
// The check digit is arithmetic, not opinion: the rest of the system treats a
// number that fails it as unconfirmed, however confident an engine is.
package iso6346

import (
	"regexp"
	"slices"
	"strings"
)

// letterValue skips multiples of 11 (11, 22, 33), as the standard requires.
var letterValue = map[byte]int{
	'A': 10, 'B': 12, 'C': 13, 'D': 14, 'E': 15, 'F': 16, 'G': 17, 'H': 18,
	'I': 19, 'J': 20, 'K': 21, 'L': 23, 'M': 24, 'N': 25, 'O': 26, 'P': 27,
	'Q': 28, 'R': 29, 'S': 30, 'T': 31, 'U': 32, 'V': 34, 'W': 35, 'X': 36,
	'Y': 37, 'Z': 38,
}

// numberShape: 3-letter owner code, category U/J/Z, 6-digit serial, check digit.
var numberShape = regexp.MustCompile(`^[A-Z]{3}[UJZ][0-9]{7}$`)

var notAlnum = regexp.MustCompile(`[^A-Z0-9]`)

// confusions are characters engines mix up on weathered paint, in both directions.
var confusions = map[byte]byte{
	'0': 'O', 'O': '0', '1': 'I', 'I': '1', '5': 'S', 'S': '5',
	'8': 'B', 'B': '8', '2': 'Z', 'Z': '2', '6': 'G', 'G': '6',
}

// Normalize uppercases and strips everything that is not A-Z or 0-9.
func Normalize(raw string) string {
	return notAlnum.ReplaceAllString(strings.ToUpper(raw), "")
}

// Step is one character's contribution to the check digit sum.
type Step struct {
	Char    string `json:"char"`
	Value   int    `json:"value"`
	Weight  int    `json:"weight"`
	Product int    `json:"product"`
}

// Validation says whether a number is valid and shows the arithmetic.
type Validation struct {
	Input              string `json:"input"`
	Normalized         string `json:"normalized"`
	WellFormed         bool   `json:"well_formed"`
	CheckDigitValid    bool   `json:"check_digit_valid"`
	ExpectedCheckDigit *int   `json:"expected_check_digit"`
	Valid              bool   `json:"valid"`
	Steps              []Step `json:"steps,omitempty"`
	Sum                int    `json:"sum"`
	Remainder          int    `json:"remainder"`
}

// CheckDigit returns the check digit for the first ten characters of a
// number. Callers must pass exactly ten characters of A-Z and 0-9.
func CheckDigit(firstTen string) int {
	digit, _, _, _ := explain(firstTen)
	return digit
}

func explain(firstTen string) (digit int, steps []Step, sum int, rem int) {
	steps = make([]Step, 0, len(firstTen))
	for i := 0; i < len(firstTen); i++ {
		ch := firstTen[i]
		value, isLetter := letterValue[ch]
		if !isLetter {
			value = int(ch - '0')
		}
		weight := 1 << i
		steps = append(steps, Step{Char: string(ch), Value: value, Weight: weight, Product: value * weight})
		sum += value * weight
	}
	rem = sum % 11
	digit = rem
	if rem == 10 {
		digit = 0
	}
	return digit, steps, sum, rem
}

// Validate normalizes number and checks its shape and check digit.
func Validate(number string) Validation {
	n := Normalize(number)
	v := Validation{Input: number, Normalized: n}
	if !numberShape.MatchString(n) {
		return v
	}
	v.WellFormed = true
	digit, steps, sum, rem := explain(n[:10])
	v.ExpectedCheckDigit = &digit
	v.Steps, v.Sum, v.Remainder = steps, sum, rem
	v.CheckDigitValid = int(n[10]-'0') == digit
	v.Valid = v.CheckDigitValid
	return v
}

// IsValid reports whether number has the right shape and check digit.
func IsValid(number string) bool { return Validate(number).Valid }

// RepairCandidates returns single-character substitutions that turn a
// check-digit failure into a pass. A one-character slip is the commonest
// misread and the check digit constrains the answer hard, but anything
// returned here is offered to a person, never auto-accepted. At most four;
// several results mean the read is genuinely ambiguous.
//
// The input does not need to be well formed: the commonest misread is a
// letter in the digit run (O for 0), which is exactly what fails the shape.
func RepairCandidates(number string) []string {
	n := Normalize(number)
	if len(n) != 11 || IsValid(n) {
		return nil
	}
	var found []string
	for i := 0; i < len(n) && len(found) < 4; i++ {
		swap, ok := confusions[n[i]]
		if !ok {
			continue
		}
		cand := n[:i] + string(swap) + n[i+1:]
		if IsValid(cand) && !slices.Contains(found, cand) {
			found = append(found, cand)
		}
	}
	return found
}
