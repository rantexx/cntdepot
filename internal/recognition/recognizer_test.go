package recognition

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPromptAsksForEverythingTheGateNeeds(t *testing.T) {
	for _, want := range []string{"candidates", "type_code", "color", "box_2d", "visible", "notes", "U, J or Z"} {
		if !strings.Contains(Prompt, want) {
			t.Errorf("Prompt does not mention %q", want)
		}
	}
}

func TestFakeReturnsScriptedReadings(t *testing.T) {
	f := &Fake{
		Reading: Reading{Candidates: []RawCandidate{{Number: "CSQU3054383", Confidence: 0.9}}},
		ByImage: map[string]Reading{"special": {Candidates: []RawCandidate{{Number: "TEXU1234569", Confidence: 0.8}}}},
	}
	var _ Recognizer = f

	a, err := f.Read(context.Background(), Image{Data: []byte("anything"), MediaType: "image/jpeg"})
	if err != nil || a.Reading.Candidates[0].Number != "CSQU3054383" || a.Engine != "fake" {
		t.Fatalf("default read = %+v, %v", a, err)
	}
	b, err := f.Read(context.Background(), Image{Data: []byte("special"), MediaType: "image/jpeg"})
	if err != nil || b.Reading.Candidates[0].Number != "TEXU1234569" {
		t.Fatalf("scripted read = %+v, %v", b, err)
	}
	if f.Calls() != 2 {
		t.Fatalf("calls = %d; want 2", f.Calls())
	}
}

func TestFakeReturnsConfiguredError(t *testing.T) {
	f := &Fake{Err: ErrUnavailable}
	if _, err := f.Read(context.Background(), Image{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v; want ErrUnavailable", err)
	}
}
