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

func TestBuildFrameKeepsDirectReadOverRepair(t *testing.T) {
	f := BuildFrame("rear", Reading{Candidates: []RawCandidate{
		{Number: "CSQU3O54383", Confidence: 0.97},
		{Number: "CSQU3054383", Confidence: 0.90},
	}})
	count := 0
	for _, c := range f.Candidates {
		if c.Number != "CSQU3054383" {
			continue
		}
		count++
		if c.Confidence != 0.90 || !c.CheckDigitValid || c.RepairedFrom != "" {
			t.Fatalf("direct read = %+v; want confidence 0.90, valid, not a repair", c)
		}
	}
	if count != 1 {
		t.Fatalf("CSQU3054383 appears %d times in %+v; want exactly once, as the direct read", count, f.Candidates)
	}
}

func TestBuildFrameKeepsBestConfidenceForDuplicates(t *testing.T) {
	f := BuildFrame("rear", Reading{Candidates: []RawCandidate{
		{Number: "CSQU3054383", Confidence: 0.2},
		{Number: "csqu 3054383", Confidence: 0.95},
	}})
	if len(f.Candidates) != 1 || f.Candidates[0].Number != "CSQU3054383" || f.Candidates[0].Confidence != 0.95 {
		t.Fatalf("candidates = %+v; want one candidate at 0.95", f.Candidates)
	}
}

func TestBuildFrameCapsAfterFiltering(t *testing.T) {
	want := []string{"CSQU3054383", "TEXU1234569", "ABCU1234560", "ABCU1234160"}
	raw := []RawCandidate{{Number: "", Confidence: 0.9}, {Number: "", Confidence: 0.9}}
	for _, n := range want {
		raw = append(raw, RawCandidate{Number: n, Confidence: 0.8})
	}
	f := BuildFrame("rear", Reading{Candidates: raw})
	if len(f.Candidates) != 4 {
		t.Fatalf("candidates = %+v; want 4", f.Candidates)
	}
	for i, n := range want {
		if f.Candidates[i].Number != n {
			t.Fatalf("candidate %d = %q; want %q (all: %+v)", i, f.Candidates[i].Number, n, f.Candidates)
		}
	}
}

func TestBuildFrameNormalizesVisible(t *testing.T) {
	if f := BuildFrame("rear", Reading{Visible: " Full "}); f.Visible != "full" {
		t.Fatalf("visible = %q; want full", f.Visible)
	}
	if f := BuildFrame("rear", Reading{Visible: "weird"}); f.Visible != "none" {
		t.Fatalf("visible = %q; want none for an unknown value", f.Visible)
	}
}
