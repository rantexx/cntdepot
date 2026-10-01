package recognition

import "context"

// PromptVersion changes whenever Prompt or an engine's output schema change,
// so stored attempts record exactly which instructions produced them.
const PromptVersion = "reading-v1"

// Image is one photo as sent to an engine.
type Image struct {
	Data      []byte
	MediaType string // image/jpeg, image/png or image/webp
}

// Recognizer reads one photo. Engines plug in behind this interface: Gemini
// now, a backup cloud engine and the owner's own model later.
type Recognizer interface {
	Name() string
	Model() string
	// Read returns what the engine saw. On error the Attempt still carries
	// engine, model and latency, so failures can be logged and costed.
	Read(ctx context.Context, img Image) (Attempt, error)
}

// Prompt is the instruction sent with every photo.
const Prompt = `You are reading a shipping container in a depot gate photograph.

Return these fields:
- candidates: the ISO 6346 container number. It has 11 characters: a 3-letter owner code, the equipment category letter U, J or Z, a 6-digit serial number and a 1-digit check digit (for example CSQU3054383). It is painted on the doors, side panels, end walls or frame, often over rust, dirt, dents or old markings. Give up to 4 candidates, most likely first, each with a confidence from 0 to 1. If no number is legible, return an empty list.
- type_code: the ISO size/type code printed near the number (4 characters such as 45G1, 22R1, 42U1, 22T6), or an empty string if it is not visible.
- color: the main paint color of the container as one plain English word (for example blue, red, white, grey, green, orange, brown, yellow), or an empty string if unclear.
- box_2d: where the container number is in the image, as [ymin, xmin, ymax, xmax] normalized to 0-1000, or null if no number is visible.
- visible: "full" when the whole number can be read, "partial" when part of it is hidden or damaged, "none" otherwise.
- notes: a short phrase, or an empty string.

Rules:
- Read only what is physically painted on the container. Never complete or guess characters you cannot see.
- Ignore truck and trailer plates, placards, weight tables and chassis markings.
- Where a character is ambiguous (0/O, 1/I, 5/S, 8/B, 2/Z, 6/G), give each reading as its own candidate instead of picking one.`
