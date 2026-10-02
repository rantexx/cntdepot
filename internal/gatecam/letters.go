package gatecam

import (
	"math"

	"depot/internal/recognition"
)

// LetterHeight estimates the height of the container number's letters in
// pixels from the engine's box. A box at least as wide as it is tall is one
// line of text. A taller box is the number written top to bottom, so the
// height is shared by its 11 characters.
func LetterHeight(b *recognition.Box, width, height int) int {
	if b == nil || width <= 0 || height <= 0 {
		return 0
	}
	bw := (b.X1 - b.X0) * float64(width)
	bh := (b.Y1 - b.Y0) * float64(height)
	if bw >= bh {
		return int(math.Round(bh))
	}
	return int(math.Round(bh / 11))
}

// Verdicts on a letter height.
const (
	VerdictGood       = "good"
	VerdictBorderline = "borderline"
	VerdictTooSmall   = "too_small"
)

// Verdict grades a letter height. From 30 px up, numbers read reliably;
// under 20 px they rarely do. Zero means no number was found.
func Verdict(px int) string {
	switch {
	case px <= 0:
		return ""
	case px >= 30:
		return VerdictGood
	case px >= 20:
		return VerdictBorderline
	default:
		return VerdictTooSmall
	}
}
