// Package recognition turns photos into container-number decisions: engines
// read single photos, frames clean those readings up, and Reconcile combines
// every photo of one truck into one decision.
package recognition

import "errors"

// RawCandidate is one possible number exactly as an engine reported it.
type RawCandidate struct {
	Number     string  `json:"number"`
	Confidence float64 `json:"confidence"`
}

// Box locates the container number in the image, normalized to 0..1 with
// (X0,Y0) top-left and (X1,Y1) bottom-right.
type Box struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// Reading is what one engine saw in one photo.
type Reading struct {
	Candidates []RawCandidate `json:"candidates"`
	TypeCode   string         `json:"type_code"`
	Color      string         `json:"color"`
	Visible    string         `json:"visible"`
	Box        *Box           `json:"box"`
	Notes      string         `json:"notes"`
}

// Usage is what an engine call cost.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Attempt is one engine run on one photo: what it saw and what it cost.
type Attempt struct {
	Engine        string  `json:"engine"`
	Model         string  `json:"model"`
	PromptVersion string  `json:"prompt_version"`
	Reading       Reading `json:"reading"`
	Raw           string  `json:"raw"`
	Usage         Usage   `json:"usage"`
	LatencyMS     int64   `json:"latency_ms"`
}

// Candidate is a normalized, checked number.
type Candidate struct {
	Number          string  `json:"number"`
	Confidence      float64 `json:"confidence"`
	CheckDigitValid bool    `json:"check_digit_valid"`
	RepairedFrom    string  `json:"repaired_from,omitempty"`
}

// Frame is one photo's cleaned-up reading.
type Frame struct {
	Role       string      `json:"role"`
	Candidates []Candidate `json:"candidates"`
	TypeCode   string      `json:"type_code"`
	Color      string      `json:"color"`
	Visible    string      `json:"visible"`
	Notes      string      `json:"notes"`
	Error      string      `json:"error,omitempty"`
}

// Decision is what Reconcile recommends.
type Decision string

const (
	DecisionAutoAccept Decision = "auto_accept"
	DecisionReview     Decision = "review"
	DecisionFailed     Decision = "failed"
)

// Result is the combined decision for one truck's photos.
type Result struct {
	Number          string      `json:"number"`
	Confidence      float64     `json:"confidence"`
	CheckDigitValid bool        `json:"check_digit_valid"`
	Decision        Decision    `json:"decision"`
	Reason          string      `json:"reason"`
	TypeCode        string      `json:"type_code"`
	Color           string      `json:"color"`
	Candidates      []Candidate `json:"candidates"`
	Frames          []Frame     `json:"frames"`
}

// Engine errors. Engines wrap these so callers can tell temporary trouble
// (ErrUnavailable) from problems a person must fix (ErrAuth).
var (
	ErrUnavailable = errors.New("engine unavailable")
	ErrAuth        = errors.New("engine rejected the credentials")
	ErrBadRequest  = errors.New("engine rejected the request")
	ErrRefused     = errors.New("engine declined to answer")
	ErrUnparseable = errors.New("engine answer could not be parsed")
	ErrTruncated   = errors.New("engine answer was cut off")
)
