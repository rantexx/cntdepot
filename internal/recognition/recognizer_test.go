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
	att, err := f.Read(context.Background(), Image{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v; want ErrUnavailable", err)
	}
	if att.Engine != "fake" || att.Model != "fake-1" || att.PromptVersion != PromptVersion {
		t.Fatalf("attempt on error = %+v; want engine, model and prompt version populated", att)
	}
}

func TestFakeHonoursCancelledContext(t *testing.T) {
	f := &Fake{Reading: Reading{Candidates: []RawCandidate{{Number: "CSQU3054383", Confidence: 0.9}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	att, err := f.Read(ctx, Image{Data: []byte("anything"), MediaType: "image/jpeg"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	if att.Engine != "fake" || att.Model != "fake-1" {
		t.Fatalf("attempt on cancel = %+v; want engine and model populated", att)
	}
}
