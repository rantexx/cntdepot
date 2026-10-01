// Package dataset exports verified gate photos for model training.
package dataset

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"depot/internal/blob"
	"depot/internal/iso6346"
	"depot/internal/recognition"
	"depot/internal/store"
)

// Label describes one exported photo.
type Label struct {
	Image         string           `json:"image"` // relative path, forward slashes
	PhotoID       string           `json:"photo_id"`
	MoveID        string           `json:"move_id"`
	Role          string           `json:"role"`
	Number        string           `json:"number"` // confirmed by a person
	TypeCode      string           `json:"type_code"`
	Color         string           `json:"color"`
	CorrectedFrom string           `json:"corrected_from,omitempty"`
	EngineNumber  string           `json:"engine_number,omitempty"` // what the engine read in this photo
	Box           *recognition.Box `json:"box,omitempty"`
	Verified      bool             `json:"verified"`
}

// Summary counts what was exported.
type Summary struct {
	Moves  int
	Photos int
	Boxes  int
}

const readme = `Depot gate dataset export

labels.jsonl  one JSON object per photo: the container number confirmed by a person,
              ISO type code, color, what the engine read, and the number's box when
              the engine gave one.
images/       the ORIGINAL photos exactly as captured (fingerprints match the gate record).
yolo/         one YOLO label per photo that has a box: "class cx cy w h", normalized 0-1.
classes.txt   YOLO class names.

Boxes come from the reading engine, not from a person: check them before training.
Boxes are relative to the photo as displayed (EXIF orientation applied), so apply EXIF
orientation when loading images (for example PIL.ImageOps.exif_transpose).
`

// Export writes every verified, non-practice move into outDir.
func Export(ctx context.Context, st *store.Store, bl *blob.Store, outDir string) (Summary, error) {
	var sum Summary
	for _, dir := range []string{"images", "yolo"} {
		if err := os.MkdirAll(filepath.Join(outDir, dir), 0o755); err != nil {
			return sum, err
		}
	}
	moves, err := st.VerifiedMoves(ctx)
	if err != nil {
		return sum, err
	}
	var labels bytes.Buffer
	enc := json.NewEncoder(&labels)
	for _, m := range moves {
		attempts, err := st.ListAttempts(ctx, m.ID)
		if err != nil {
			return sum, err
		}
		readings := latestReadings(attempts)
		for _, p := range m.Photos {
			data, err := bl.Get(p.SHA256)
			if err != nil {
				return sum, fmt.Errorf("photo %s: %w", p.ID, err)
			}
			name := p.ID + extension(p.MediaType)
			if err := os.WriteFile(filepath.Join(outDir, "images", name), data, 0o644); err != nil {
				return sum, err
			}
			label := Label{
				Image: "images/" + name, PhotoID: p.ID, MoveID: m.ID, Role: p.Role, Number: m.Number,
				TypeCode: m.TypeCode, Color: m.Color, CorrectedFrom: m.CorrectedFrom, Verified: m.Verified,
			}
			if r, ok := readings[p.ID]; ok {
				if len(r.Candidates) > 0 {
					label.EngineNumber = iso6346.Normalize(r.Candidates[0].Number)
				}
				if b := r.Box; b != nil {
					label.Box = b
					line := fmt.Sprintf("0 %.6f %.6f %.6f %.6f\n", (b.X0+b.X1)/2, (b.Y0+b.Y1)/2, b.X1-b.X0, b.Y1-b.Y0)
					if err := os.WriteFile(filepath.Join(outDir, "yolo", p.ID+".txt"), []byte(line), 0o644); err != nil {
						return sum, err
					}
					sum.Boxes++
				}
			}
			if err := enc.Encode(label); err != nil {
				return sum, err
			}
			sum.Photos++
		}
		sum.Moves++
	}
	files := map[string]string{
		"labels.jsonl": labels.String(),
		"classes.txt":  "container_number\n",
		"README.txt":   readme,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(content), 0o644); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// latestReadings returns each photo's most recent successful reading.
func latestReadings(attempts []store.Attempt) map[string]recognition.Reading {
	out := map[string]recognition.Reading{}
	for _, a := range attempts { // oldest first, so later attempts win
		if a.Error != "" {
			continue
		}
		var r recognition.Reading
		if json.Unmarshal(a.Reading, &r) == nil {
			out[a.PhotoID] = r
		}
	}
	return out
}

func extension(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}
