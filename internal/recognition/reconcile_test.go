package recognition

import (
	"math"
	"strings"
	"testing"
)

func rc(number string, conf float64) RawCandidate {
	return RawCandidate{Number: number, Confidence: conf}
}

func frame(role string, cands ...RawCandidate) Frame {
	return BuildFrame(role, Reading{Candidates: cands, Visible: "full"})
}

func TestAgreementAutoAccepts(t *testing.T) {
	r := Reconcile([]Frame{frame("left", rc("CSQU3054383", 0.86)), frame("right", rc("CSQU3054383", 0.83))}, 0.80)
	if r.Decision != DecisionAutoAccept || r.Number != "CSQU3054383" {
		t.Fatalf("got %s %s; want auto_accept CSQU3054383", r.Decision, r.Number)
	}
	if math.Abs(r.Confidence-0.96) > 1e-9 {
		t.Fatalf("confidence = %v; want 0.96 (best 0.86 + 0.10 for the second photo)", r.Confidence)
	}
}

func TestDisagreementReviews(t *testing.T) {
	r := Reconcile([]Frame{frame("left", rc("CSQU3054383", 0.99)), frame("right", rc("TEXU1234569", 0.99))}, 0.80)
	if r.Decision != DecisionReview || r.Reason != "photos disagree" {
		t.Fatalf("got %s %q; want review, photos disagree", r.Decision, r.Reason)
	}
}

func TestBadCheckDigitReviews(t *testing.T) {
	r := Reconcile([]Frame{frame("left", rc("CSQU3054384", 0.99))}, 0.80)
	if r.Decision != DecisionReview || r.CheckDigitValid {
		t.Fatalf("got %s valid=%v; want review with an invalid number", r.Decision, r.CheckDigitValid)
	}
}

func TestArithmeticBeatsConfidence(t *testing.T) {
	r := Reconcile([]Frame{frame("left", rc("CSQU3054384", 0.95), rc("CSQU3054383", 0.55))}, 0.80)
	if r.Number != "CSQU3054383" {
		t.Fatalf("number = %s; want the valid CSQU3054383", r.Number)
	}
}

func TestRepairSurfacesButNeverAutoAccepts(t *testing.T) {
	r := Reconcile([]Frame{frame("left", rc("CSQU3O54383", 0.97))}, 0.80)
	if r.Number != "CSQU3054383" || r.Decision != DecisionReview {
		t.Fatalf("got %s %s; want review CSQU3054383", r.Decision, r.Number)
	}
	if r.Confidence > 0.70 {
		t.Fatalf("confidence = %v; repaired reads are capped at 0.70", r.Confidence)
	}
}

func TestSinglePhotoCanAutoAccept(t *testing.T) {
	r := Reconcile([]Frame{frame("rear", rc("CSQU3054383", 0.95))}, 0.80)
	if r.Decision != DecisionAutoAccept {
		t.Fatalf("decision = %s; want auto_accept", r.Decision)
	}
}

func TestLowConfidenceReviews(t *testing.T) {
	r := Reconcile([]Frame{frame("left", rc("CSQU3054383", 0.4)), frame("right", rc("CSQU3054383", 0.4))}, 0.80)
	if r.Decision != DecisionReview || !strings.Contains(r.Reason, "below auto-accept threshold") {
		t.Fatalf("got %s %q", r.Decision, r.Reason)
	}
}

func TestBlankFails(t *testing.T) {
	r := Reconcile([]Frame{BuildFrame("left", Reading{Visible: "none"})}, 0.80)
	if r.Decision != DecisionFailed || r.Number != "" {
		t.Fatalf("got %s %q; want failed with no number", r.Decision, r.Number)
	}
}

func TestEngineErrorBecomesReason(t *testing.T) {
	r := Reconcile([]Frame{ErrorFrame("rear", "engine unavailable: HTTP 529")}, 0.80)
	if r.Decision != DecisionFailed || r.Reason != "engine unavailable: HTTP 529" {
		t.Fatalf("got %s %q", r.Decision, r.Reason)
	}
}

func TestTypeCodeAndColorByMajority(t *testing.T) {
	frames := []Frame{
		BuildFrame("rear", Reading{Candidates: []RawCandidate{rc("CSQU3054383", 0.9)}, TypeCode: "45G1", Color: "blue"}),
		BuildFrame("left", Reading{Candidates: []RawCandidate{rc("CSQU3054383", 0.9)}, TypeCode: "45G1", Color: "blue"}),
		BuildFrame("right", Reading{Candidates: []RawCandidate{rc("CSQU3054383", 0.9)}, TypeCode: "22G1"}),
	}
	r := Reconcile(frames, 0.80)
	if r.TypeCode != "45G1" || r.Color != "blue" {
		t.Fatalf("type code, color = %q, %q; want 45G1, blue", r.TypeCode, r.Color)
	}
}

func TestDirectReadBeatsRepairAcrossPhotos(t *testing.T) {
	frames := []Frame{
		frame("rear", rc("CSQU3O54383", 0.97)),
		frame("left", rc("CSQU3054383", 0.95)),
		frame("right", rc("CSQU3054383", 0.95)),
	}
	orders := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, o := range orders {
		in := []Frame{frames[o[0]], frames[o[1]], frames[o[2]]}
		r := Reconcile(in, 0.85)
		if r.Number != "CSQU3054383" || r.Candidates[0].RepairedFrom != "" || r.Decision != DecisionAutoAccept {
			t.Fatalf("order %v: got %s %s repairedFrom=%q; want auto_accept CSQU3054383 as a direct read",
				o, r.Decision, r.Number, r.Candidates[0].RepairedFrom)
		}
	}
}

func TestRepairNeverAutoAcceptsEvenAtLowThreshold(t *testing.T) {
	r := Reconcile([]Frame{frame("rear", rc("CSQU3O54383", 0.97)), frame("left", rc("CSQU3O54383", 0.97))}, 0.1)
	if r.Decision != DecisionReview || r.Confidence > 0.70 {
		t.Fatalf("got %s %v; want review capped at 0.70", r.Decision, r.Confidence)
	}
}

func TestSameSideTwiceIsNotDisagreement(t *testing.T) {
	r := Reconcile([]Frame{frame("other", rc("CSQU3054383", 0.95)), frame("other", rc("CSQU3054383", 0.95))}, 0.85)
	if r.Decision != DecisionReview || r.Reason != "only one side of the container confirmed the number" {
		t.Fatalf("got %s %q", r.Decision, r.Reason)
	}
}

func TestPartialSecondPhotoIsNotDisagreement(t *testing.T) {
	r := Reconcile([]Frame{frame("rear", rc("CSQU3054383", 0.95)), frame("left", rc("CSQU30", 0.5))}, 0.85)
	if r.Decision != DecisionReview || r.Reason != "only one side of the container confirmed the number" {
		t.Fatalf("got %s %q", r.Decision, r.Reason)
	}
}

func TestOutvotedConflictStillReviews(t *testing.T) {
	r := Reconcile([]Frame{
		frame("left", rc("CSQU3054383", 0.9)),
		frame("right", rc("CSQU3054383", 0.9)),
		frame("front", rc("TEXU1234569", 0.99)),
	}, 0.85)
	if r.Decision != DecisionReview || r.Reason != "photos disagree" {
		t.Fatalf("got %s %q; want review, photos disagree", r.Decision, r.Reason)
	}
}

func TestPhotoWithTwoValidNumbersReviews(t *testing.T) {
	r := Reconcile([]Frame{frame("rear", rc("CSQU3054383", 0.95), rc("TEXU1234569", 0.94))}, 0.85)
	if r.Decision != DecisionReview || r.Reason != "a photo shows more than one valid number" {
		t.Fatalf("got %s %q", r.Decision, r.Reason)
	}
}

func TestFailedPhotoBlocksAutoAccept(t *testing.T) {
	r := Reconcile([]Frame{ErrorFrame("rear", "engine unavailable: HTTP 529"), frame("left", rc("CSQU3054383", 0.95))}, 0.85)
	if r.Decision != DecisionReview || !strings.HasPrefix(r.Reason, "a photo could not be read") {
		t.Fatalf("got %s %q", r.Decision, r.Reason)
	}
}

func TestRepairConfidenceDoesNotInflateDirectRead(t *testing.T) {
	r := Reconcile([]Frame{
		frame("left", rc("CSQU3054383", 0.3)),
		frame("right", rc("CSQU3054383", 0.3)),
		frame("front", rc("CSQU3O54383", 1.0)),
	}, 0.85)
	if r.Number != "CSQU3054383" || math.Abs(r.Confidence-0.40) > 1e-9 {
		t.Fatalf("got %s %v; want CSQU3054383 at 0.40 (direct 0.3 + 0.10 for the second side)", r.Number, r.Confidence)
	}
}
