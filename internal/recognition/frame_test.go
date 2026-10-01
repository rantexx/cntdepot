package recognition

import (
	"errors"
	"math"
	"testing"
)

func TestBuildFrameAddsRepairCandidates(t *testing.T) {
	f := BuildFrame("rear", Reading{Candidates: []RawCandidate{{Number: "CSQU3O54383", Confidence: 0.97}}, Visible: "full"})
	if len(f.Candidates) != 2 {
		t.Fatalf("candidates = %+v; want original plus one repair", f.Candidates)
	}
	if f.Candidates[0].Number != "CSQU3O54383" || f.Candidates[0].CheckDigitValid {
		t.Fatalf("first candidate = %+v; want the invalid original", f.Candidates[0])
	}
	r := f.Candidates[1]
	if r.Number != "CSQU3054383" || !r.CheckDigitValid || r.RepairedFrom != "CSQU3O54383" {
		t.Fatalf("repair = %+v", r)
	}
	if math.Abs(r.Confidence-0.97*0.6) > 1e-9 {
		t.Fatalf("repair confidence = %v; want %v", r.Confidence, 0.97*0.6)
	}
}

func TestBuildFrameNormalizesAndDeduplicates(t *testing.T) {
	f := BuildFrame("left", Reading{Candidates: []RawCandidate{
		{Number: "csqu 305 4383", Confidence: 0.9},
		{Number: "CSQU3054383", Confidence: 0.5},
	}})
	if len(f.Candidates) != 1 || f.Candidates[0].Number != "CSQU3054383" || !f.Candidates[0].CheckDigitValid {
		t.Fatalf("candidates = %+v", f.Candidates)
	}
	if f.Visible != "none" {
		t.Fatalf("visible = %q; want none when the engine left it empty", f.Visible)
	}
}

func TestBuildFrameCleansTypeCodeColorAndConfidence(t *testing.T) {
	f := BuildFrame("rear", Reading{
		Candidates: []RawCandidate{{Number: "CSQU3054383", Confidence: 1.7}, {Number: "TEXU1234569", Confidence: -1}},
		TypeCode:   "45g1",
		Color:      " Blue ",
	})
	if f.TypeCode != "45G1" || f.Color != "blue" {
		t.Fatalf("type code, color = %q, %q", f.TypeCode, f.Color)
	}
	if f.Candidates[0].Confidence != 1 || f.Candidates[1].Confidence != 0 {
		t.Fatalf("confidences = %v, %v; want clamped to 1 and 0", f.Candidates[0].Confidence, f.Candidates[1].Confidence)
	}
	if g := BuildFrame("rear", Reading{TypeCode: "XX"}); g.TypeCode != "" {
		t.Fatalf("invalid type code kept: %q", g.TypeCode)
	}
}

func TestDecodeJSONAcceptsFencesAndProse(t *testing.T) {
	cases := []string{
		"```json\n{\"candidates\":[{\"number\":\"CSQU3054383\",\"confidence\":0.9}],\"visible\":\"full\"}\n```",
		"Here you go: {\"candidates\":[{\"number\":\"CSQU3054383\",\"confidence\":0.9}],\"visible\":\"full\"} done",
	}
	for _, text := range cases {
		var r Reading
		if err := decodeJSON(text, &r); err != nil {
			t.Fatalf("decodeJSON(%q): %v", text, err)
		}
		if len(r.Candidates) != 1 || r.Candidates[0].Number != "CSQU3054383" || r.Visible != "full" {
			t.Fatalf("decodeJSON(%q) = %+v", text, r)
		}
	}
}

func TestDecodeJSONRejectsGarbage(t *testing.T) {
	var r Reading
	if err := decodeJSON("not json at all", &r); !errors.Is(err, ErrUnparseable) {
		t.Fatalf("err = %v; want ErrUnparseable", err)
	}
}
