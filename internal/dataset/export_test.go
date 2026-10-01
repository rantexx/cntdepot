package dataset

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"depot/internal/recognition"
	"depot/internal/testkit"
)

func TestExportWritesImagesLabelsAndYOLO(t *testing.T) {
	st, bl := testkit.Open(t)
	m := testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "ai-1", recognition.Reading{
		Candidates: []recognition.RawCandidate{{Number: "CSQU3054384", Confidence: 0.9}},
		TypeCode:   "45G1", Color: "blue",
		Box: &recognition.Box{X0: 0.1, Y0: 0.2, X1: 0.5, Y1: 0.3},
	}, false)
	testkit.SeedVerifiedMove(t, st, bl, "TEXU1234569", "ai-2", recognition.Reading{}, true) // practice: excluded

	out := t.TempDir()
	sum, err := Export(context.Background(), st, bl, out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Moves != 1 || sum.Photos != 1 || sum.Boxes != 1 {
		t.Fatalf("summary = %+v", sum)
	}

	raw, err := os.ReadFile(filepath.Join(out, "labels.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var label Label
	if err := json.Unmarshal(bytes.TrimSpace(raw), &label); err != nil {
		t.Fatalf("labels.jsonl: %v (%s)", err, raw)
	}
	if label.MoveID != m.ID || label.Number != "CSQU3054383" || label.EngineNumber != "CSQU3054384" ||
		label.CorrectedFrom != "CSQU3054384" || label.TypeCode != "45G1" || label.Color != "blue" || !label.Verified {
		t.Fatalf("label = %+v", label)
	}

	img, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(label.Image)))
	if err != nil || !bytes.Equal(img, []byte("\xff\xd8\xff\xe0original-ai-1")) {
		t.Fatalf("image = %q, %v; want the original bytes", img, err)
	}
	yolo, err := os.ReadFile(filepath.Join(out, "yolo", label.PhotoID+".txt"))
	if err != nil || string(yolo) != "0 0.300000 0.250000 0.400000 0.100000\n" {
		t.Fatalf("yolo = %q, %v", yolo, err)
	}
	if _, err := os.Stat(filepath.Join(out, "classes.txt")); err != nil {
		t.Fatalf("classes.txt: %v", err)
	}
}
