package dataset

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"depot/internal/blob"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testdir"
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

	out := testdir.New(t)
	sum, err := Export(testkit.Ctx(), st, bl, out)
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
		label.CorrectedFrom != "CSQU3054384" || label.TypeCode != "45G1" || label.Color != "blue" || !label.Verified ||
		label.Source != "phone" || !label.Read {
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

func TestExportSkipsBoxesAroundADifferentNumber(t *testing.T) {
	st, bl := testkit.Open(t)
	// The engine boxed some other number (a trailer plate, the next container
	// in the stack), not the one the person confirmed.
	m := testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "ai-1", recognition.Reading{
		Candidates: []recognition.RawCandidate{{Number: "TEXU1234569", Confidence: 0.9}},
		TypeCode:   "45G1", Color: "blue",
		Box: &recognition.Box{X0: 0.1, Y0: 0.2, X1: 0.5, Y1: 0.3},
	}, false)

	out := testdir.New(t)
	sum, err := Export(testkit.Ctx(), st, bl, out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Moves != 1 || sum.Photos != 1 || sum.Boxes != 0 {
		t.Fatalf("summary = %+v; want the photo exported without a box", sum)
	}
	raw, err := os.ReadFile(filepath.Join(out, "labels.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var label Label
	if err := json.Unmarshal(bytes.TrimSpace(raw), &label); err != nil {
		t.Fatalf("labels.jsonl: %v (%s)", err, raw)
	}
	if label.MoveID != m.ID || label.Number != "CSQU3054383" || label.EngineNumber != "TEXU1234569" || label.Box != nil {
		t.Fatalf("label = %+v; want engine number kept and no box", label)
	}
	if _, err := os.Stat(filepath.Join(out, "yolo", label.PhotoID+".txt")); !os.IsNotExist(err) {
		t.Fatalf("yolo file for a box around another number: stat err = %v; want not exist", err)
	}
}

func TestBoxMatchesTheConfirmedNumber(t *testing.T) {
	for _, tc := range []struct {
		engine, confirmed string
		want              bool
	}{
		{"CSQU3054383", "CSQU3054383", true},
		{"CSQU3054384", "CSQU3054383", true},  // one character misread
		{"CSQU3054384", "CSQU305438", false},  // different length
		{"CSQU3054394", "CSQU3054383", false}, // two characters differ
		{"TEXU1234569", "CSQU3054383", false}, // another container
		{"", "CSQU3054383", false},            // the engine read no number
	} {
		if got := boxMatches(tc.engine, tc.confirmed); got != tc.want {
			t.Errorf("boxMatches(%q, %q) = %v; want %v", tc.engine, tc.confirmed, got, tc.want)
		}
	}
}

func TestReadmeSaysTypeAndColorAreTheEnginesReading(t *testing.T) {
	st, bl := testkit.Open(t)
	out := testdir.New(t)
	if _, err := Export(testkit.Ctx(), st, bl, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "README.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "type_code and color are the engine's reading") {
		t.Fatalf("README.txt does not warn about type_code and color:\n%s", raw)
	}
}

// seedCameraMove stores a verified camera move: one still that was read, and
// unread stills (deferred, as a camera visit leaves them). Every still has a
// reading whose box surrounds the confirmed number, so only the deferred flag
// can keep a still out of yolo/.
func seedCameraMove(t *testing.T, st *store.Store, bl *blob.Store, number string, unread int) (store.Move, store.Photo, []store.Photo) {
	t.Helper()
	ctx := testkit.Ctx()
	m, err := st.CreateMoveWithSource(ctx, "1", store.SourceCamera, false, "camera")
	if err != nil {
		t.Fatal(err)
	}
	reading, err := json.Marshal(recognition.Reading{
		Candidates: []recognition.RawCandidate{{Number: number, Confidence: 0.9}},
		TypeCode:   "45G1", Color: "blue",
		Box: &recognition.Box{X0: 0.1, Y0: 0.2, X1: 0.5, Y1: 0.3},
	})
	if err != nil {
		t.Fatal(err)
	}
	add := func(i int, deferred bool) store.Photo {
		orig, err := bl.Put([]byte("\xff\xd8\xff\xe0still-" + string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		p, err := st.AddPhoto(ctx, store.Photo{
			MoveID: m.ID, Role: "camera", SHA256: orig, AISHA256: orig,
			MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 10, Deferred: deferred,
		}, "camera")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddAttempt(ctx, store.Attempt{
			MoveID: m.ID, PhotoID: p.ID, Engine: "fake", Model: "fake-1",
			PromptVersion: recognition.PromptVersion, Reading: reading, Raw: string(reading),
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	read := add(0, false)
	var stills []store.Photo
	for i := range unread {
		stills = append(stills, add(i+1, true))
	}
	if err := st.SetResult(ctx, m.ID, store.ReadOutcome{Status: store.StatusNeedsReview, Number: number, TypeCode: "45G1", Color: "blue"}); err != nil {
		t.Fatal(err)
	}
	if m, err = st.Resolve(ctx, m.ID, store.ResolveUpdate{Status: store.StatusAccepted, Number: number, Verified: true, By: "seed"}); err != nil {
		t.Fatal(err)
	}
	return m, read, stills
}

func TestExportKeepsUnreadCameraStillsApart(t *testing.T) {
	st, bl := testkit.Open(t)
	m, read, stills := seedCameraMove(t, st, bl, "CSQU3054383", 2)

	out := testdir.New(t)
	sum, err := Export(testkit.Ctx(), st, bl, out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Moves != 1 || sum.Photos != 3 || sum.Boxes != 1 || sum.Unread != 2 {
		t.Fatalf("summary = %+v; want 3 photos, 1 box, 2 unread", sum)
	}

	raw, err := os.ReadFile(filepath.Join(out, "labels.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]Label{}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var l Label
		if err := json.Unmarshal(line, &l); err != nil {
			t.Fatalf("labels.jsonl: %v (%s)", err, line)
		}
		labels[l.PhotoID] = l
	}
	if l := labels[read.ID]; l.Image != "images/"+read.ID+".jpg" || !l.Read || l.Source != "camera" || l.Box == nil || l.MoveID != m.ID {
		t.Fatalf("read still label = %+v", l)
	}
	if _, err := os.Stat(filepath.Join(out, "yolo", read.ID+".txt")); err != nil {
		t.Fatalf("yolo label for the read still: %v", err)
	}
	for _, p := range stills {
		l := labels[p.ID]
		if l.Image != "images-unread/"+p.ID+".jpg" || l.Read || l.Source != "camera" || l.Box != nil || l.EngineNumber != "" ||
			l.Number != "CSQU3054383" || l.MoveID != m.ID {
			t.Errorf("unread still label = %+v; want images-unread/, read false, no box, the move's number", l)
		}
		if _, err := os.Stat(filepath.Join(out, "images-unread", p.ID+".jpg")); err != nil {
			t.Errorf("unread still not in images-unread/: %v", err)
		}
		if _, err := os.Stat(filepath.Join(out, "images", p.ID+".jpg")); !os.IsNotExist(err) {
			t.Errorf("unread still in images/: stat err = %v; want not exist", err)
		}
		if _, err := os.Stat(filepath.Join(out, "yolo", p.ID+".txt")); !os.IsNotExist(err) {
			t.Errorf("yolo label for an unread still: stat err = %v; want not exist", err)
		}
	}
}

func TestReadmeExplainsUnreadCameraStills(t *testing.T) {
	st, bl := testkit.Open(t)
	out := testdir.New(t)
	if _, err := Export(testkit.Ctx(), st, bl, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "README.txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(raw)), " ") // line breaks fall anywhere
	for _, want := range []string{
		"images-unread/", "show the same truck seconds apart", "confirmed number applies to the truck, but the still may not show it",
		"They have no boxes and no YOLO labels", "Keep them out of YOLO training unless you label them",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("README.txt does not say %q:\n%s", want, raw)
		}
	}
	if fi, err := os.Stat(filepath.Join(out, "images-unread")); err != nil || !fi.IsDir() {
		t.Fatalf("images-unread/ = %v; want the folder even when empty", err)
	}
}
