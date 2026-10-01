# Local Gate Test Kit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A gate OCR system the owner can run on one Windows PC at their own depot: phones photograph containers over Wi-Fi, Gemini reads the container number / ISO type code / color, a person confirms or corrects it, and every verified photo becomes training data.

**Architecture:** One Go program (`depotd`) running in `hub` mode serves a React web app and a JSON API on the LAN. Photos are stored content-addressed on disk, records in an embedded SQLite database (state + append-only events in one transaction). A worker pool sends a downscaled copy of each photo to a `Recognizer` (Gemini, or a fake for tests/demos); results from all photos of a truck are combined by the ported prototype logic (check digit first, then cross-photo agreement, then confidence). This is the spec's gate hub (spec §6) with the cloud parts left out; the cloud foundation (M0) follows in a later plan.

**Tech Stack:** Go ≥ 1.24 (net/http routing, `database/sql`), `modernc.org/sqlite` (pure Go, no C compiler needed), `github.com/oklog/ulid/v2`, the Gemini API over plain HTTPS (no SDK, same as the prototype); React 18+ + TypeScript + Vite; Node ≥ 20.

**Spec:** `docs/superpowers/specs/2026-09-30-gate-ocr-phase-0-1-design.md`

---

## Scope

**In this plan (milestone "M1 + local M2" from spec §14, reordered so the owner can test at their own depot first):**
- ISO 6346 number and size/type-code validation with explained check-digit math and shared test vectors (spec §9.3, §13)
- Recognition core ported from the prototype: frame building, repair candidates, reconciliation (spec §9)
- `Recognizer` interface with a Gemini engine (structured JSON output, native number boxes) and a fake engine (spec §9.1)
- Hub storage: SQLite with state + events written together, content-addressed photo store (spec §7.3, §7.4)
- Gate service: moves, photos, background reading, review and correction, re-read, test read (spec §8, §10.2)
- LAN web app in Turkish and English: guided capture with on-device quality check, review queue, move detail with check-digit math and photo boxes, test-read tool (spec §8.1, §10.2)
- Optional access code for the LAN app
- Training dataset export (images + JSONL labels + YOLO boxes) and an accuracy benchmark over verified moves (spec §9.7, §13 golden set)

**Not in this plan (later plans):** cloud (AWS, Terraform, identity provider, tenants/roles), PIN login and device pairing, pre-advice import, gate policies, push notifications and escalation, slip printing, connectors (Excel/CODECO/webhooks), local offline OCR model, a second cloud engine as backup, COCO export format, fixed cameras and live video, equipment profiles (still awaiting approval).

**Model choice:** the owner chose Gemini. The engine defaults to `gemini-3.8-flash` (Google's most capable Flash model) with thinking level `low`, both overridable (`-model`, `-thinking`, or `DEPOT_GEMINI_MODEL` / `DEPOT_GEMINI_THINKING`). Trying the cheaper `gemini-3.5-flash-lite` for step 1 of the escalation ladder (spec §9.2) is the owner's decision after running `depotd bench` on real photos.

## File map

```
depot-platform/
├── .gitignore
├── go.mod / go.sum                          module "depot"
├── cmd/depotd/main.go                       subcommands: version, hub, export-dataset, bench
├── testdata/vectors/iso6346.json            shared test vectors (Go now, TypeScript later)
├── internal/
│   ├── iso6346/iso6346.go                   normalize, check digit (explained), validate, repair
│   ├── iso6346/typecode.go                  size/type code validation, equipment family
│   ├── recognition/types.go                 Reading, Attempt, Candidate, Frame, Result, errors
│   ├── recognition/frame.go                 BuildFrame, ErrorFrame
│   ├── recognition/parse.go                 decodeJSON (tolerant JSON)
│   ├── recognition/reconcile.go             Reconcile (ported decision logic)
│   ├── recognition/recognizer.go            Recognizer interface, Image, Prompt
│   ├── recognition/fake.go                  Fake engine for tests and demos
│   ├── recognition/gemini.go                Gemini engine
│   ├── blob/blob.go                         content-addressed photo store
│   ├── store/schema.sql                     SQLite schema v1
│   ├── store/store.go                       Open, migrations, transactions, events
│   ├── store/moves.go                       gate moves
│   ├── store/photos.go                      photos
│   ├── store/attempts.go                    recognition attempts
│   ├── store/events.go                      event reads
│   ├── gate/service.go                      orchestration: photos → reading → result → review
│   ├── httpapi/server.go                    routes, JSON helpers, auth, static
│   ├── httpapi/handlers.go                  API handlers
│   ├── httpapi/broker.go                    live updates (Server-Sent Events)
│   ├── webui/webui.go + dist/.gitkeep       embedded built web app
│   ├── dataset/export.go                    training export
│   ├── bench/bench.go                       accuracy benchmark
│   └── testkit/testkit.go                   shared test helpers
├── web/                                     React + TypeScript app (own go.mod keeps node_modules out of Go builds)
│   ├── go.mod, package.json, tsconfig.json, vite.config.ts, index.html
│   └── src/ main.tsx App.tsx api.ts i18n.ts storage.ts live.ts image.ts components.tsx styles.css
│       pages/ Home.tsx Login.tsx Capture.tsx Queue.tsx MoveDetail.tsx TestRead.tsx
└── docs/runbooks/local-test-kit.md          how to run it at the depot
```

All commands below run from the repository root `C:\Users\OZGUN\depot-platform` and work in both PowerShell and Git Bash.

Every commit message ends with the line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` (shown in each commit step).

---

### Task 0: Workstation prerequisites

**Files:** none

- [ ] **Step 1: Go is installed (owner action)**

Go is not installed on this PC. The owner installs it (an agent must not accept installer license terms on the owner's behalf): download the Windows installer for Go 1.24 or newer from https://go.dev/dl/ and run it. Then, in a **new** terminal:

Run: `go version`
Expected: `go version go1.24.x windows/amd64` or newer.

- [ ] **Step 2: Node is available**

Run: `node --version`
Expected: `v20` or newer (this PC has `v24.18.0`).

- [ ] **Step 3: Repository-local git identity**

The PC has no global git identity. Set one for this repository only:

```bash
git config user.name "OZGUN"
git config user.email "cun9182@gmail.com"
```

- [ ] **Step 4: Gemini API key (owner action, needed only from Task 17)**

The owner creates an API key in Google AI Studio (https://aistudio.google.com/apikey) on a Google Cloud project **with billing enabled**: on Google's unpaid tier, submitted content may be used to improve Google's products, which is not acceptable for depot and customer photos (check the current Gemini API terms). The owner stores the key in **their own** terminal, never in chat or in a file in the repo:

```powershell
setx GEMINI_API_KEY "<key from Google AI Studio>"
```

New terminals then see it. All automated tests use fakes and need no key.

---

### Task 1: Go module skeleton

**Files:**
- Create: `go.mod` (via `go mod init`)
- Create: `.gitignore`
- Create: `cmd/depotd/main.go`

- [ ] **Step 1: Initialise the module**

Run: `go mod init depot`
Expected: `go: creating new go.mod: module depot`

- [ ] **Step 2: Write `.gitignore`**

```gitignore
# Go build output
/bin/
*.exe

# Local hub data (database and photos) and dataset exports
/depot-data/
/dataset-export/

# Web
/web/node_modules/
/internal/webui/dist/*
!/internal/webui/dist/.gitkeep
```

- [ ] **Step 3: Write the first `cmd/depotd/main.go`**

```go
package main

import (
	"fmt"
	"os"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: depotd <command> [flags]

commands:
  version          print the version
  hub              run the gate hub (web app + API + reading) on this computer
  export-dataset   write verified photos and labels for model training
  bench            re-read verified moves with an engine and report accuracy`)
}
```

- [ ] **Step 4: Run it**

Run: `go run ./cmd/depotd version`
Expected: `dev`

- [ ] **Step 5: Commit**

```bash
git add go.mod .gitignore cmd/depotd/main.go
git commit -m "chore: add Go module and depotd skeleton" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: ISO 6346 container numbers

**Files:**
- Create: `testdata/vectors/iso6346.json`
- Create: `internal/iso6346/iso6346.go`
- Test: `internal/iso6346/iso6346_test.go`

Background for the engineer: an ISO 6346 number is 3 owner letters + category letter (U, J or Z) + 6 serial digits + 1 check digit. Letters map to values 10–38 skipping 11, 22, 33. Each of the first 10 characters is multiplied by 2^position (1, 2, 4 … 512); the sum mod 11 is the check digit, and a remainder of 10 becomes 0. The reference number `CSQU3054383` has sum 6185, remainder 3.

- [ ] **Step 1: Write the shared test vectors**

`testdata/vectors/iso6346.json`:

```json
{
  "numbers": [
    {"input": "CSQU3054383", "normalized": "CSQU3054383", "well_formed": true, "valid": true, "expected_check_digit": 3},
    {"input": "csqu 305 4383", "normalized": "CSQU3054383", "well_formed": true, "valid": true, "expected_check_digit": 3},
    {"input": "CSQU3054384", "normalized": "CSQU3054384", "well_formed": true, "valid": false, "expected_check_digit": 3},
    {"input": "ABCU1234560", "normalized": "ABCU1234560", "well_formed": true, "valid": true, "expected_check_digit": 0},
    {"input": "ABCU1234160", "normalized": "ABCU1234160", "well_formed": true, "valid": true, "expected_check_digit": 0, "note": "sum 4454, remainder 10 maps to check digit 0"},
    {"input": "TEXU1234569", "normalized": "TEXU1234569", "well_formed": true, "valid": true, "expected_check_digit": 9},
    {"input": "CSQX3054383", "normalized": "CSQX3054383", "well_formed": false, "valid": false},
    {"input": "CSQU305438", "normalized": "CSQU305438", "well_formed": false, "valid": false},
    {"input": "", "normalized": "", "well_formed": false, "valid": false}
  ],
  "repairs": [
    {"input": "CSQU3O54383", "contains": "CSQU3054383", "note": "letter O read in place of zero"},
    {"input": "C5QU3054383", "contains": "CSQU3054383", "note": "digit 5 read in place of S"},
    {"input": "CSQU3054383", "empty": true, "note": "valid numbers need no repair"}
  ],
  "type_codes": [
    {"input": "45G1", "valid": true, "family": "general"},
    {"input": "22r1", "valid": true, "family": "reefer"},
    {"input": "42U1", "valid": true, "family": "open_top"},
    {"input": "42P3", "valid": true, "family": "flat_rack"},
    {"input": "22T6", "valid": true, "family": "tank"},
    {"input": "4G1", "valid": false, "family": "unknown"},
    {"input": "45X1", "valid": false, "family": "unknown"}
  ]
}
```

- [ ] **Step 2: Write the failing tests**

`internal/iso6346/iso6346_test.go`:

```go
package iso6346

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type vectorFile struct {
	Numbers []struct {
		Input              string `json:"input"`
		Normalized         string `json:"normalized"`
		WellFormed         bool   `json:"well_formed"`
		Valid              bool   `json:"valid"`
		ExpectedCheckDigit *int   `json:"expected_check_digit"`
	} `json:"numbers"`
	Repairs []struct {
		Input    string `json:"input"`
		Contains string `json:"contains"`
		Empty    bool   `json:"empty"`
	} `json:"repairs"`
	TypeCodes []struct {
		Input  string `json:"input"`
		Valid  bool   `json:"valid"`
		Family string `json:"family"`
	} `json:"type_codes"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "iso6346.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectorFile
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return v
}

func TestNumberVectors(t *testing.T) {
	for _, tc := range loadVectors(t).Numbers {
		got := Validate(tc.Input)
		if got.Normalized != tc.Normalized || got.WellFormed != tc.WellFormed || got.Valid != tc.Valid {
			t.Errorf("Validate(%q) = normalized %q, well_formed %v, valid %v; want %q, %v, %v",
				tc.Input, got.Normalized, got.WellFormed, got.Valid, tc.Normalized, tc.WellFormed, tc.Valid)
		}
		if tc.ExpectedCheckDigit != nil {
			if got.ExpectedCheckDigit == nil || *got.ExpectedCheckDigit != *tc.ExpectedCheckDigit {
				t.Errorf("Validate(%q) expected check digit = %v; want %d", tc.Input, got.ExpectedCheckDigit, *tc.ExpectedCheckDigit)
			}
		}
	}
}

func TestRepairVectors(t *testing.T) {
	for _, tc := range loadVectors(t).Repairs {
		got := RepairCandidates(tc.Input)
		if tc.Empty && len(got) != 0 {
			t.Errorf("RepairCandidates(%q) = %v; want none", tc.Input, got)
		}
		if tc.Contains != "" && !slices.Contains(got, tc.Contains) {
			t.Errorf("RepairCandidates(%q) = %v; want it to contain %q", tc.Input, got, tc.Contains)
		}
	}
}

func TestValidateExplainsTheArithmetic(t *testing.T) {
	v := Validate("CSQU3054383")
	if v.Sum != 6185 || v.Remainder != 3 {
		t.Fatalf("sum, remainder = %d, %d; want 6185, 3", v.Sum, v.Remainder)
	}
	if len(v.Steps) != 10 {
		t.Fatalf("len(steps) = %d; want 10", len(v.Steps))
	}
	want := Step{Char: "S", Value: 30, Weight: 2, Product: 60}
	if v.Steps[1] != want {
		t.Fatalf("steps[1] = %+v; want %+v", v.Steps[1], want)
	}
}

func TestGeneratedNumbersRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const categories = "UJZ"
	for i := 0; i < 2000; i++ {
		b := make([]byte, 0, 10)
		for j := 0; j < 3; j++ {
			b = append(b, letters[r.IntN(len(letters))])
		}
		b = append(b, categories[r.IntN(len(categories))])
		for j := 0; j < 6; j++ {
			b = append(b, byte('0'+r.IntN(10)))
		}
		digit := CheckDigit(string(b))
		good := string(b) + string(rune('0'+digit))
		if !IsValid(good) {
			t.Fatalf("%s should be valid", good)
		}
		bad := string(b) + string(rune('0'+(digit+1)%10))
		if IsValid(bad) {
			t.Fatalf("%s should be invalid", bad)
		}
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/iso6346/`
Expected: FAIL to compile with `undefined: Validate` (and the other functions).

- [ ] **Step 4: Implement `internal/iso6346/iso6346.go`**

```go
// Package iso6346 validates container numbers and size/type codes (ISO 6346).
//
// The check digit is arithmetic, not opinion: the rest of the system treats a
// number that fails it as unconfirmed, however confident an engine is.
package iso6346

import (
	"regexp"
	"slices"
	"strings"
)

// letterValue skips multiples of 11 (11, 22, 33), as the standard requires.
var letterValue = map[byte]int{
	'A': 10, 'B': 12, 'C': 13, 'D': 14, 'E': 15, 'F': 16, 'G': 17, 'H': 18,
	'I': 19, 'J': 20, 'K': 21, 'L': 23, 'M': 24, 'N': 25, 'O': 26, 'P': 27,
	'Q': 28, 'R': 29, 'S': 30, 'T': 31, 'U': 32, 'V': 34, 'W': 35, 'X': 36,
	'Y': 37, 'Z': 38,
}

// numberShape: 3-letter owner code, category U/J/Z, 6-digit serial, check digit.
var numberShape = regexp.MustCompile(`^[A-Z]{3}[UJZ][0-9]{7}$`)

var notAlnum = regexp.MustCompile(`[^A-Z0-9]`)

// confusions are characters engines mix up on weathered paint, in both directions.
var confusions = map[byte]byte{
	'0': 'O', 'O': '0', '1': 'I', 'I': '1', '5': 'S', 'S': '5',
	'8': 'B', 'B': '8', '2': 'Z', 'Z': '2', '6': 'G', 'G': '6',
}

// Normalize uppercases and strips everything that is not A-Z or 0-9.
func Normalize(raw string) string {
	return notAlnum.ReplaceAllString(strings.ToUpper(raw), "")
}

// Step is one character's contribution to the check digit sum.
type Step struct {
	Char    string `json:"char"`
	Value   int    `json:"value"`
	Weight  int    `json:"weight"`
	Product int    `json:"product"`
}

// Validation says whether a number is valid and shows the arithmetic.
type Validation struct {
	Input              string `json:"input"`
	Normalized         string `json:"normalized"`
	WellFormed         bool   `json:"well_formed"`
	CheckDigitValid    bool   `json:"check_digit_valid"`
	ExpectedCheckDigit *int   `json:"expected_check_digit"`
	Valid              bool   `json:"valid"`
	Steps              []Step `json:"steps,omitempty"`
	Sum                int    `json:"sum"`
	Remainder          int    `json:"remainder"`
}

// CheckDigit returns the check digit for the first ten characters of a
// number. Callers must pass exactly ten characters of A-Z and 0-9.
func CheckDigit(firstTen string) int {
	digit, _, _, _ := explain(firstTen)
	return digit
}

func explain(firstTen string) (digit int, steps []Step, sum int, rem int) {
	steps = make([]Step, 0, len(firstTen))
	for i := 0; i < len(firstTen); i++ {
		ch := firstTen[i]
		value, isLetter := letterValue[ch]
		if !isLetter {
			value = int(ch - '0')
		}
		weight := 1 << i
		steps = append(steps, Step{Char: string(ch), Value: value, Weight: weight, Product: value * weight})
		sum += value * weight
	}
	rem = sum % 11
	digit = rem
	if rem == 10 {
		digit = 0
	}
	return digit, steps, sum, rem
}

// Validate normalizes number and checks its shape and check digit.
func Validate(number string) Validation {
	n := Normalize(number)
	v := Validation{Input: number, Normalized: n}
	if !numberShape.MatchString(n) {
		return v
	}
	v.WellFormed = true
	digit, steps, sum, rem := explain(n[:10])
	v.ExpectedCheckDigit = &digit
	v.Steps, v.Sum, v.Remainder = steps, sum, rem
	v.CheckDigitValid = int(n[10]-'0') == digit
	v.Valid = v.CheckDigitValid
	return v
}

// IsValid reports whether number has the right shape and check digit.
func IsValid(number string) bool { return Validate(number).Valid }

// RepairCandidates returns single-character substitutions that turn a
// check-digit failure into a pass. A one-character slip is the commonest
// misread and the check digit constrains the answer hard, but anything
// returned here is offered to a person, never auto-accepted. At most four;
// several results mean the read is genuinely ambiguous.
//
// The input does not need to be well formed: the commonest misread is a
// letter in the digit run (O for 0), which is exactly what fails the shape.
func RepairCandidates(number string) []string {
	n := Normalize(number)
	if len(n) != 11 || IsValid(n) {
		return nil
	}
	var found []string
	for i := 0; i < len(n) && len(found) < 4; i++ {
		swap, ok := confusions[n[i]]
		if !ok {
			continue
		}
		cand := n[:i] + string(swap) + n[i+1:]
		if IsValid(cand) && !slices.Contains(found, cand) {
			found = append(found, cand)
		}
	}
	return found
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/iso6346/ -run "TestNumberVectors|TestRepairVectors|TestValidateExplainsTheArithmetic|TestGeneratedNumbersRoundTrip" -v`
Expected: PASS for all four tests. (`TestTypeCodeVectors` does not exist yet.)

- [ ] **Step 6: Commit**

```bash
git add testdata/vectors/iso6346.json internal/iso6346/iso6346.go internal/iso6346/iso6346_test.go
git commit -m "feat: ISO 6346 number validation with explained check digit" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: ISO size/type codes and equipment family

**Files:**
- Create: `internal/iso6346/typecode.go`
- Test: `internal/iso6346/typecode_test.go`

Background: a size/type code such as `45G1` is length code, height/width code, **type group letter**, detail. The type group letter tells the equipment family: G general, V ventilated, B bulk, S named cargo, R reefer, H insulated, U open top, P flat rack/platform, T tank, A air/surface.

- [ ] **Step 1: Write the failing test**

`internal/iso6346/typecode_test.go`:

```go
package iso6346

import "testing"

func TestTypeCodeVectors(t *testing.T) {
	for _, tc := range loadVectors(t).TypeCodes {
		if got := ValidTypeCode(tc.Input); got != tc.Valid {
			t.Errorf("ValidTypeCode(%q) = %v; want %v", tc.Input, got, tc.Valid)
		}
		if got := FamilyOf(tc.Input); string(got) != tc.Family {
			t.Errorf("FamilyOf(%q) = %q; want %q", tc.Input, got, tc.Family)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/iso6346/ -run TestTypeCodeVectors`
Expected: FAIL to compile with `undefined: ValidTypeCode`.

- [ ] **Step 3: Implement `internal/iso6346/typecode.go`**

```go
package iso6346

import "regexp"

// Family is the kind of equipment a size/type code describes. Later it
// decides which photos and fields the gate asks for (equipment profiles).
type Family string

const (
	FamilyGeneral    Family = "general"
	FamilyVentilated Family = "ventilated"
	FamilyBulk       Family = "bulk"
	FamilyNamedCargo Family = "named_cargo"
	FamilyReefer     Family = "reefer"
	FamilyInsulated  Family = "insulated"
	FamilyOpenTop    Family = "open_top"
	FamilyFlatRack   Family = "flat_rack"
	FamilyTank       Family = "tank"
	FamilyAirSurface Family = "air_surface"
	FamilyUnknown    Family = "unknown"
)

// typeCodeShape: length code, height/width code, type group letter, detail.
var typeCodeShape = regexp.MustCompile(`^[0-9][0-9A-Z][A-Z][0-9A-Z]$`)

var familyByGroup = map[byte]Family{
	'G': FamilyGeneral, 'V': FamilyVentilated, 'B': FamilyBulk, 'S': FamilyNamedCargo,
	'R': FamilyReefer, 'H': FamilyInsulated, 'U': FamilyOpenTop, 'P': FamilyFlatRack,
	'T': FamilyTank, 'A': FamilyAirSurface,
}

// ValidTypeCode reports whether code is a well-formed size/type code with a
// known type group.
func ValidTypeCode(code string) bool {
	n := Normalize(code)
	if !typeCodeShape.MatchString(n) {
		return false
	}
	_, known := familyByGroup[n[2]]
	return known
}

// FamilyOf returns the equipment family of a size/type code.
func FamilyOf(code string) Family {
	if !ValidTypeCode(code) {
		return FamilyUnknown
	}
	return familyByGroup[Normalize(code)[2]]
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/iso6346/ -v`
Expected: PASS for all five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/iso6346/typecode.go internal/iso6346/typecode_test.go
git commit -m "feat: ISO size/type code validation and equipment family" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 4: Recognition types, frames and tolerant parsing

**Files:**
- Create: `internal/recognition/types.go`
- Create: `internal/recognition/frame.go`
- Create: `internal/recognition/parse.go`
- Test: `internal/recognition/frame_test.go`

A **Reading** is what one engine saw in one photo. A **Frame** is that reading cleaned up: numbers normalized, deduplicated, checked, and one-character repairs added (at 60% of the original confidence, marked `repaired_from`).

- [ ] **Step 1: Write the failing tests**

`internal/recognition/frame_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/recognition/`
Expected: FAIL to compile with `undefined: BuildFrame`.

- [ ] **Step 3: Implement `internal/recognition/types.go`**

```go
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
```

- [ ] **Step 4: Implement `internal/recognition/frame.go`**

```go
package recognition

import (
	"strings"
	"unicode/utf8"

	"depot/internal/iso6346"
)

// BuildFrame cleans up one engine reading. A one-character repair that
// satisfies the check digit is surfaced beside the original, because it is
// usually the right answer; it is marked so it can never auto-accept.
func BuildFrame(role string, r Reading) Frame {
	f := Frame{Role: role, Visible: clip(r.Visible, 16), Notes: clip(r.Notes, 200)}
	if f.Visible == "" {
		f.Visible = "none"
	}
	if tc := iso6346.Normalize(r.TypeCode); iso6346.ValidTypeCode(tc) {
		f.TypeCode = tc
	}
	f.Color = clip(strings.ToLower(strings.TrimSpace(r.Color)), 24)

	raw := r.Candidates
	if len(raw) > 4 {
		raw = raw[:4]
	}
	seen := map[string]bool{}
	for _, rc := range raw {
		number := iso6346.Normalize(rc.Number)
		if number == "" || seen[number] {
			continue
		}
		seen[number] = true
		conf := clamp01(rc.Confidence)
		ok := iso6346.IsValid(number)
		f.Candidates = append(f.Candidates, Candidate{Number: number, Confidence: conf, CheckDigitValid: ok})
		if ok {
			continue
		}
		for _, fixed := range iso6346.RepairCandidates(number) {
			if seen[fixed] {
				continue
			}
			seen[fixed] = true
			f.Candidates = append(f.Candidates, Candidate{
				Number: fixed, Confidence: conf * 0.6, CheckDigitValid: true, RepairedFrom: number,
			})
		}
	}
	return f
}

// ErrorFrame stands in for a photo whose reading failed.
func ErrorFrame(role, message string) Frame {
	return Frame{Role: role, Visible: "none", Error: message}
}

func clamp01(v float64) float64 {
	if !(v >= 0) { // also catches NaN
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
```

- [ ] **Step 5: Implement `internal/recognition/parse.go`**

```go
package recognition

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var fence = regexp.MustCompile("(?i)```(?:json)?")

// decodeJSON recovers one JSON object from engine text into v. Structured
// output makes clean JSON the norm, but a good read should not be thrown away
// over a code fence or a stray word around it.
func decodeJSON(text string, v any) error {
	cleaned := strings.TrimSpace(fence.ReplaceAllString(text, ""))
	if err := json.Unmarshal([]byte(cleaned), v); err == nil {
		return nil
	}
	start, end := strings.Index(cleaned, "{"), strings.LastIndex(cleaned, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(cleaned[start:end+1]), v); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: %.80q", ErrUnparseable, text)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/recognition/ -v`
Expected: PASS for all five tests.

- [ ] **Step 7: Commit**

```bash
git add internal/recognition/types.go internal/recognition/frame.go internal/recognition/parse.go internal/recognition/frame_test.go
git commit -m "feat: recognition frames, repairs and tolerant parsing" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Reconcile several photos into one decision

**Files:**
- Create: `internal/recognition/reconcile.go`
- Test: `internal/recognition/reconcile_test.go`

Ported from the prototype's `reconcile` with the same safety property: **validity is the primary sort key**, so a confident misread never outranks a less confident number whose check digit adds up. Cross-photo agreement counts distinct photo roles. Type code and color are decided by majority.

- [ ] **Step 1: Write the failing tests**

`internal/recognition/reconcile_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/recognition/`
Expected: FAIL to compile with `undefined: Reconcile`.

- [ ] **Step 3: Implement `internal/recognition/reconcile.go`**

```go
package recognition

import (
	"fmt"
	"math"
	"sort"

	"depot/internal/iso6346"
)

// Reconcile turns several photos of one truck into one decision.
//
// Weighting, in order:
//  1. Check digit. Arithmetic beats confidence, always.
//  2. Cross-photo agreement. Different sides reading the same valid number
//     is the strongest evidence short of a person.
//  3. Engine confidence, as a tie-breaker.
//
// A repaired candidate can lead the ranking but never auto-accepts.
func Reconcile(frames []Frame, autoAccept float64) Result {
	scores := map[string]float64{}
	bestConf := map[string]float64{}
	roles := map[string]map[string]bool{}
	repaired := map[string]string{}
	seen := map[string]bool{}

	for _, f := range frames {
		for rank, c := range f.Candidates {
			score := c.Confidence / float64(1+rank)
			if c.CheckDigitValid {
				score *= 3
			}
			if c.RepairedFrom != "" {
				score *= 0.5
			}
			scores[c.Number] += score
			if c.Confidence > bestConf[c.Number] {
				bestConf[c.Number] = c.Confidence
			}
			if !seen[c.Number] {
				seen[c.Number] = true
				repaired[c.Number] = c.RepairedFrom
			}
			if c.RepairedFrom == "" {
				if roles[c.Number] == nil {
					roles[c.Number] = map[string]bool{}
				}
				roles[c.Number][f.Role] = true
			}
		}
	}

	res := Result{
		Frames:   frames,
		TypeCode: majority(frames, func(f Frame) string { return f.TypeCode }),
		Color:    majority(frames, func(f Frame) string { return f.Color }),
	}
	if len(scores) == 0 {
		res.Decision = DecisionFailed
		res.Reason = "no container number legible in any photo"
		for _, f := range frames {
			if f.Error != "" {
				res.Reason = f.Error
				break
			}
		}
		return res
	}

	// Validity is the PRIMARY sort key, not a weighted term: leaving it to a
	// tunable multiplier means one day someone tunes the safety away.
	numbers := make([]string, 0, len(scores))
	for n := range scores {
		numbers = append(numbers, n)
	}
	sort.Slice(numbers, func(i, j int) bool {
		a, b := numbers[i], numbers[j]
		va, vb := iso6346.IsValid(a), iso6346.IsValid(b)
		if va != vb {
			return va
		}
		if scores[a] != scores[b] {
			return scores[a] > scores[b]
		}
		return a < b
	})
	if len(numbers) > 5 {
		numbers = numbers[:5]
	}
	for _, n := range numbers {
		res.Candidates = append(res.Candidates, Candidate{
			Number: n, Confidence: bestConf[n], CheckDigitValid: iso6346.IsValid(n), RepairedFrom: repaired[n],
		})
	}

	top := res.Candidates[0]
	agreement := len(roles[top.Number])
	framesRead := 0
	for _, f := range frames {
		if len(f.Candidates) > 0 {
			framesRead++
		}
	}
	conf := bestConf[top.Number]
	if agreement > 1 {
		conf = math.Min(1, conf+0.10*float64(agreement-1))
	}
	res.Number, res.CheckDigitValid = top.Number, top.CheckDigitValid

	switch {
	case !top.CheckDigitValid:
		res.Confidence, res.Decision = conf, DecisionReview
		res.Reason = "check digit failed on every candidate"
	case top.RepairedFrom != "":
		res.Confidence, res.Decision = math.Min(conf, 0.70), DecisionReview
		res.Reason = fmt.Sprintf("read as %s, which fails its check digit; %s is one character away and passes",
			top.RepairedFrom, top.Number)
	case conf >= autoAccept && (agreement > 1 || framesRead <= 1):
		res.Confidence, res.Decision = conf, DecisionAutoAccept
		res.Reason = fmt.Sprintf("check digit valid, %d photo(s) agree", agreement)
	case framesRead > 1 && agreement == 1:
		res.Confidence, res.Decision = math.Min(conf, 0.70), DecisionReview
		res.Reason = "photos disagree"
	default:
		res.Confidence, res.Decision = conf, DecisionReview
		res.Reason = fmt.Sprintf("confidence %.2f below auto-accept threshold", conf)
	}
	return res
}

// majority returns the most common non-empty value; ties go to the first seen.
func majority(frames []Frame, pick func(Frame) string) string {
	counts := map[string]int{}
	best, bestN := "", 0
	for _, f := range frames {
		v := pick(f)
		if v == "" {
			continue
		}
		counts[v]++
		if counts[v] > bestN {
			best, bestN = v, counts[v]
		}
	}
	return best
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/recognition/ -v`
Expected: PASS for all tests in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/recognition/reconcile.go internal/recognition/reconcile_test.go
git commit -m "feat: reconcile photos into one decision (ported from prototype)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Recognizer interface, prompt and fake engine

**Files:**
- Create: `internal/recognition/recognizer.go`
- Create: `internal/recognition/fake.go`
- Test: `internal/recognition/recognizer_test.go`

The prompt asks for the number's position as `box_2d` = `[ymin, xmin, ymax, xmax]` normalized to 0–1000, the format Gemini is trained on for object detection. Engines convert it to the engine-neutral `Box` (0–1 corners).

- [ ] **Step 1: Write the failing tests**

`internal/recognition/recognizer_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/recognition/`
Expected: FAIL to compile with `undefined: Prompt` and `undefined: Fake`.

- [ ] **Step 3: Implement `internal/recognition/recognizer.go`**

```go
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
```

- [ ] **Step 4: Implement `internal/recognition/fake.go`**

```go
package recognition

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"
)

// Fake is a scripted engine for tests and for running the hub without an API
// key (`depotd hub -engine fake`).
type Fake struct {
	Reading Reading            // returned for any image not in ByImage
	ByImage map[string]Reading // keyed by the image bytes as a string
	Err     error              // when set, every Read fails with it
	Delay   time.Duration      // simulated engine latency
	calls   atomic.Int64
}

func (f *Fake) Name() string  { return "fake" }
func (f *Fake) Model() string { return "fake-1" }

// Calls reports how many reads have been made.
func (f *Fake) Calls() int64 { return f.calls.Load() }

func (f *Fake) Read(ctx context.Context, img Image) (Attempt, error) {
	f.calls.Add(1)
	start := time.Now()
	att := Attempt{Engine: f.Name(), Model: f.Model(), PromptVersion: PromptVersion}
	if f.Delay > 0 {
		select {
		case <-time.After(f.Delay):
		case <-ctx.Done():
			return att, ctx.Err()
		}
	}
	att.LatencyMS = time.Since(start).Milliseconds()
	if f.Err != nil {
		return att, f.Err
	}
	r := f.Reading
	if scripted, ok := f.ByImage[string(img.Data)]; ok {
		r = scripted
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return att, err
	}
	att.Reading, att.Raw = r, string(raw)
	return att, nil
}

// NewDemoFake reads every photo as the ISO reference container, so the whole
// app can be tried end to end without an API key.
func NewDemoFake() *Fake {
	return &Fake{
		Delay: 700 * time.Millisecond,
		Reading: Reading{
			Candidates: []RawCandidate{{Number: "CSQU3054383", Confidence: 0.92}},
			TypeCode:   "45G1",
			Color:      "blue",
			Visible:    "full",
			Box:        &Box{X0: 0.30, Y0: 0.20, X1: 0.62, Y1: 0.30},
			Notes:      "demo engine",
		},
	}
}
```

- [ ] **Step 5: Run the package tests**

Run: `go test ./internal/recognition/ -v`
Expected: PASS for all tests.

- [ ] **Step 6: Commit**

```bash
git add internal/recognition/recognizer.go internal/recognition/fake.go internal/recognition/recognizer_test.go
git commit -m "feat: recognizer interface, reading prompt and fake engine" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Gemini engine

**Files:**
- Create: `internal/recognition/gemini.go`
- Test: `internal/recognition/gemini_test.go`

The engine calls the Gemini REST endpoint `POST {base}/v1beta/models/{model}:generateContent` directly with `net/http`, as the prototype did: no SDK, so nothing to keep up to date. The request carries the image as `inlineData` plus `Prompt`, and asks for structured output (`generationConfig.responseMimeType = "application/json"` with a `responseSchema` in Gemini's OpenAPI-style schema: upper-case types, `nullable`, `propertyOrdering`). Thinking level is configurable (`generationConfig.thinkingConfig.thinkingLevel`, default `low` from the hub flags). The key goes in the `x-goog-api-key` header.

Error mapping (callers act on these):
- invalid key → `ErrAuth` (Gemini answers **400** with `API_KEY_INVALID` / "API key not valid", not 401); 401/403 → `ErrAuth`
- 408 / 429 / 5xx / network failure → `ErrUnavailable`, retried `Retries` times with a 1 s, 2 s … pause
- `promptFeedback.blockReason`, no candidates, or a finish reason other than `STOP` / `MAX_TOKENS` → `ErrRefused`
- `MAX_TOKENS` → `ErrTruncated`
- parts marked `"thought": true` are thinking summaries and are skipped; thinking tokens are billed as output, so they are added to `OutputTokens`.

Tests run against a local fake server, so no key or network is needed.

- [ ] **Step 1: Write the failing tests**

`internal/recognition/gemini_test.go`:

```go
package recognition

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const geminiOK = `{"candidates":[{"content":{"role":"model","parts":[
{"text":"{\"candidates\":[{\"number\":\"CSQU3054383\",\"confidence\":0.93}],\"type_code\":\"45G1\",\"color\":\"blue\",\"box_2d\":[200,100,300,500],\"visible\":\"full\",\"notes\":\"\"}"}]},
"finishReason":"STOP"}],
"usageMetadata":{"promptTokenCount":1100,"candidatesTokenCount":60,"thoughtsTokenCount":40,"totalTokenCount":1200}}`

// fakeGemini answers with the given status and bodies in turn (the last one repeats).
func fakeGemini(t *testing.T, status []int, bodies []string, inspect func(r *http.Request, payload map[string]any)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if want := "/v1beta/models/" + DefaultGeminiModel + ":generateContent"; r.URL.Path != want {
			t.Errorf("path = %s; want %s", r.URL.Path, want)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if inspect != nil {
			inspect(r, payload)
		}
		i := min(n, len(bodies)-1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status[min(n, len(status)-1)])
		_, _ = io.WriteString(w, bodies[i])
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func testGemini(srv *httptest.Server, retries int) *Gemini {
	return NewGemini(GeminiConfig{APIKey: "test-key", ThinkingLevel: "low", BaseURL: srv.URL, Retries: retries})
}

// dig walks nested JSON maps and arrays: dig(m, "a", 0, "b").
func dig(node any, path ...any) any {
	for _, p := range path {
		switch key := p.(type) {
		case string:
			m, ok := node.(map[string]any)
			if !ok {
				return nil
			}
			node = m[key]
		case int:
			a, ok := node.([]any)
			if !ok || key >= len(a) {
				return nil
			}
			node = a[key]
		}
	}
	return node
}

var jpeg = Image{Data: []byte("\xff\xd8\xff\xe0fake-jpeg"), MediaType: "image/jpeg"}

func TestGeminiSendsImageSchemaAndThinking(t *testing.T) {
	srv, _ := fakeGemini(t, []int{200}, []string{geminiOK}, func(r *http.Request, p map[string]any) {
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key = %q", got)
		}
		if got := dig(p, "contents", 0, "parts", 0, "inlineData", "mimeType"); got != "image/jpeg" {
			t.Errorf("image mime type = %v", got)
		}
		if got := dig(p, "contents", 0, "parts", 0, "inlineData", "data"); got != base64.StdEncoding.EncodeToString(jpeg.Data) {
			t.Errorf("image data = %v", got)
		}
		if got := dig(p, "contents", 0, "parts", 1, "text"); got != Prompt {
			t.Errorf("second part is not the prompt")
		}
		if got := dig(p, "generationConfig", "responseMimeType"); got != "application/json" {
			t.Errorf("responseMimeType = %v", got)
		}
		if got := dig(p, "generationConfig", "responseSchema", "type"); got != "OBJECT" {
			t.Errorf("responseSchema.type = %v", got)
		}
		if got := dig(p, "generationConfig", "thinkingConfig", "thinkingLevel"); got != "low" {
			t.Errorf("thinkingLevel = %v", got)
		}
	})
	att, err := testGemini(srv, 0).Read(context.Background(), jpeg)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	r := att.Reading
	if len(r.Candidates) != 1 || r.Candidates[0].Number != "CSQU3054383" || r.TypeCode != "45G1" || r.Color != "blue" {
		t.Fatalf("reading = %+v", r)
	}
	if r.Box == nil || *r.Box != (Box{X0: 0.1, Y0: 0.2, X1: 0.5, Y1: 0.3}) {
		t.Fatalf("box = %+v; want x0 0.1 y0 0.2 x1 0.5 y1 0.3", r.Box)
	}
	if att.Usage.InputTokens != 1100 || att.Usage.OutputTokens != 100 || att.Engine != "gemini" || att.Model != DefaultGeminiModel {
		t.Fatalf("attempt = %+v", att)
	}
}

func TestGeminiSkipsThoughtParts(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"Looking at the doors...","thought":true},
{"text":"{\"candidates\":[{\"number\":\"CSQU3054383\",\"confidence\":0.9}],\"type_code\":\"\",\"color\":\"\",\"box_2d\":null,\"visible\":\"full\",\"notes\":\"\"}"}]},"finishReason":"STOP"}]}`
	srv, _ := fakeGemini(t, []int{200}, []string{body}, nil)
	att, err := testGemini(srv, 0).Read(context.Background(), jpeg)
	if err != nil || att.Reading.Candidates[0].Number != "CSQU3054383" || att.Reading.Box != nil {
		t.Fatalf("Read = %+v, %v", att.Reading, err)
	}
}

func TestGeminiInvalidKeyIsAuthError(t *testing.T) {
	body := `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`
	srv, _ := fakeGemini(t, []int{400}, []string{body}, nil)
	if _, err := testGemini(srv, 2).Read(context.Background(), jpeg); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v; want ErrAuth", err)
	}
}

func TestGeminiOverloadIsUnavailable(t *testing.T) {
	srv, calls := fakeGemini(t, []int{503}, []string{`{"error":{"code":503,"status":"UNAVAILABLE"}}`}, nil)
	if _, err := testGemini(srv, 0).Read(context.Background(), jpeg); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v; want ErrUnavailable", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d; want 1 with no retries", calls.Load())
	}
}

func TestGeminiRetriesTemporaryFailures(t *testing.T) {
	srv, calls := fakeGemini(t, []int{503, 200}, []string{`{"error":{"code":503}}`, geminiOK}, nil)
	att, err := testGemini(srv, 1).Read(context.Background(), jpeg)
	if err != nil || att.Reading.Candidates[0].Number != "CSQU3054383" {
		t.Fatalf("Read = %+v, %v", att.Reading, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d; want 2", calls.Load())
	}
}

func TestGeminiBlockedIsRefused(t *testing.T) {
	srv, _ := fakeGemini(t, []int{200}, []string{`{"promptFeedback":{"blockReason":"SAFETY"}}`}, nil)
	if _, err := testGemini(srv, 0).Read(context.Background(), jpeg); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v; want ErrRefused", err)
	}
}

func TestGeminiMaxTokensIsTruncated(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"{\"candidates\":["}]},"finishReason":"MAX_TOKENS"}]}`
	srv, _ := fakeGemini(t, []int{200}, []string{body}, nil)
	if _, err := testGemini(srv, 0).Read(context.Background(), jpeg); !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v; want ErrTruncated", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/recognition/`
Expected: FAIL to compile with `undefined: NewGemini`.

- [ ] **Step 3: Implement `internal/recognition/gemini.go`**

```go
package recognition

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultGeminiModel is used unless the operator picks another model.
const DefaultGeminiModel = "gemini-3.8-flash"

const defaultGeminiURL = "https://generativelanguage.googleapis.com"

// GeminiConfig configures the Gemini engine.
type GeminiConfig struct {
	APIKey        string        // required
	Model         string        // empty: DefaultGeminiModel
	ThinkingLevel string        // for example low or high; empty leaves the model default
	BaseURL       string        // empty: the public Gemini API (tests point it at a fake)
	Timeout       time.Duration // per request; zero: 60s
	Retries       int           // extra attempts after temporary failures
}

// Gemini reads photos with the Gemini API.
type Gemini struct {
	cfg    GeminiConfig
	client *http.Client
}

// NewGemini builds the engine.
func NewGemini(cfg GeminiConfig) *Gemini {
	if cfg.Model == "" {
		cfg.Model = DefaultGeminiModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultGeminiURL
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Gemini{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
}

func (g *Gemini) Name() string  { return "gemini" }
func (g *Gemini) Model() string { return g.cfg.Model }

// geminiSchema is the structured-output schema, in Gemini's OpenAPI-style
// responseSchema format.
var geminiSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"candidates": map[string]any{
			"type": "ARRAY",
			"items": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"number":     map[string]any{"type": "STRING"},
					"confidence": map[string]any{"type": "NUMBER"},
				},
				"required": []string{"number", "confidence"},
			},
		},
		"type_code": map[string]any{"type": "STRING"},
		"color":     map[string]any{"type": "STRING"},
		"box_2d":    map[string]any{"type": "ARRAY", "items": map[string]any{"type": "INTEGER"}, "nullable": true},
		"visible":   map[string]any{"type": "STRING", "enum": []string{"full", "partial", "none"}},
		"notes":     map[string]any{"type": "STRING"},
	},
	"required":         []string{"candidates", "type_code", "color", "visible", "notes"},
	"propertyOrdering": []string{"candidates", "type_code", "color", "box_2d", "visible", "notes"},
}

// geminiReading is a Reading in the shape Gemini answers with.
type geminiReading struct {
	Candidates []RawCandidate `json:"candidates"`
	TypeCode   string         `json:"type_code"`
	Color      string         `json:"color"`
	Box2D      []float64      `json:"box_2d"`
	Visible    string         `json:"visible"`
	Notes      string         `json:"notes"`
}

func (r geminiReading) reading() Reading {
	out := Reading{Candidates: r.Candidates, TypeCode: r.TypeCode, Color: r.Color, Visible: r.Visible, Notes: r.Notes}
	if b := r.Box2D; len(b) == 4 && b[0] < b[2] && b[1] < b[3] {
		// [ymin, xmin, ymax, xmax] on a 0-1000 scale → corners on 0-1.
		out.Box = &Box{X0: clamp01(b[1] / 1000), Y0: clamp01(b[0] / 1000), X1: clamp01(b[3] / 1000), Y1: clamp01(b[2] / 1000)}
	}
	return out
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int64 `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

func (g *Gemini) Read(ctx context.Context, img Image) (Attempt, error) {
	att := Attempt{Engine: g.Name(), Model: g.cfg.Model, PromptVersion: PromptVersion}
	start := time.Now()
	body, err := json.Marshal(g.request(img))
	if err != nil {
		return att, err
	}

	var resp geminiResponse
	for try := 0; ; try++ {
		resp = geminiResponse{}
		err = g.post(ctx, body, &resp)
		if err == nil || !errors.Is(err, ErrUnavailable) || try >= g.cfg.Retries {
			break
		}
		select {
		case <-time.After(time.Duration(try+1) * time.Second):
		case <-ctx.Done():
			att.LatencyMS = time.Since(start).Milliseconds()
			return att, ctx.Err()
		}
	}
	att.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return att, err
	}

	// Thinking tokens are billed as output.
	u := resp.UsageMetadata
	att.Usage = Usage{InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount}
	if resp.PromptFeedback.BlockReason != "" {
		return att, fmt.Errorf("%w: blocked (%s)", ErrRefused, resp.PromptFeedback.BlockReason)
	}
	if len(resp.Candidates) == 0 {
		return att, fmt.Errorf("%w: no answer", ErrRefused)
	}
	c := resp.Candidates[0]
	var text strings.Builder
	for _, p := range c.Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	att.Raw = text.String()
	switch c.FinishReason {
	case "", "STOP":
	case "MAX_TOKENS":
		return att, ErrTruncated
	default:
		return att, fmt.Errorf("%w: finish reason %s", ErrRefused, c.FinishReason)
	}

	var gr geminiReading
	if err := decodeJSON(att.Raw, &gr); err != nil {
		return att, err
	}
	att.Reading = gr.reading()
	return att, nil
}

func (g *Gemini) request(img Image) map[string]any {
	config := map[string]any{
		"responseMimeType": "application/json",
		"responseSchema":   geminiSchema,
	}
	if g.cfg.ThinkingLevel != "" {
		config["thinkingConfig"] = map[string]any{"thinkingLevel": g.cfg.ThinkingLevel}
	}
	return map[string]any{
		"contents": []any{map[string]any{
			"role": "user",
			"parts": []any{
				map[string]any{"inlineData": map[string]any{
					"mimeType": img.MediaType,
					"data":     base64.StdEncoding.EncodeToString(img.Data),
				}},
				map[string]any{"text": Prompt},
			},
		}},
		"generationConfig": config,
	}
}

// post sends one request and maps HTTP failures onto engine errors.
func (g *Gemini) post(ctx context.Context, body []byte, out *geminiResponse) error {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", strings.TrimRight(g.cfg.BaseURL, "/"), g.cfg.Model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.cfg.APIKey)
	res, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if res.StatusCode >= 300 {
		return classifyHTTP(res.StatusCode, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: %v", ErrUnparseable, err)
	}
	return nil
}

// classifyHTTP maps a Gemini error answer onto engine errors. An invalid key
// comes back as 400 INVALID_ARGUMENT, not 401.
func classifyHTTP(status int, body []byte) error {
	text := string(body)
	detail := strings.TrimSpace(text)
	if len(detail) > 200 {
		detail = detail[:200]
	}
	switch {
	case status == 401 || status == 403 || strings.Contains(text, "API_KEY_INVALID") || strings.Contains(text, "API key not valid"):
		return fmt.Errorf("%w: HTTP %d", ErrAuth, status)
	case status == 408 || status == 429 || status >= 500:
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, status)
	default:
		return fmt.Errorf("%w: HTTP %d: %s", ErrBadRequest, status, detail)
	}
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/recognition/ -v`
Expected: PASS for all tests, including the seven Gemini tests (the retry test takes about one second).

- [ ] **Step 5: Commit**

```bash
git add internal/recognition/gemini.go internal/recognition/gemini_test.go
git commit -m "feat: Gemini reading engine with structured output and native number boxes" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Content-addressed photo store

**Files:**
- Create: `internal/blob/blob.go`
- Test: `internal/blob/blob_test.go`

Photos are stored under their SHA-256 fingerprint (`blobs/ab/cd/<digest>`). The same bytes always get the same name, so three uploads of one photo cost one file, and the fingerprint doubles as tamper evidence (spec §11).

- [ ] **Step 1: Write the failing tests**

`internal/blob/blob_test.go`:

```go
package blob

import (
	"bytes"
	"errors"
	"testing"
)

func TestPutGetRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("photo bytes")
	d1, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 || len(d1) != 64 {
		t.Fatalf("digests %q, %q; want the same 64-char digest", d1, d2)
	}
	got, err := s.Get(d1)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestGetRejectsBadAndMissingDigests(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("../../etc/passwd"); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("err = %v; want ErrBadDigest", err)
	}
	missing := "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := s.Get(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/blob/`
Expected: FAIL to compile with `undefined: Open`.

- [ ] **Step 3: Implement `internal/blob/blob.go`**

```go
// Package blob stores photos under their SHA-256 fingerprint.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

var (
	ErrNotFound  = errors.New("blob not found")
	ErrBadDigest = errors.New("invalid blob digest")
)

var digestShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Store is a folder of content-addressed files.
type Store struct{ dir string }

// Open creates the folder if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Put stores data and returns its digest. Storing the same bytes twice is free.
func (s *Store) Put(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	path := s.path(digest)
	if _, err := os.Stat(path); err == nil {
		return digest, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Write to a temporary file and rename, so a crash never leaves a
	// half-written photo under a real digest.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return digest, nil
}

// Get returns the bytes stored under digest.
func (s *Store) Get(digest string) ([]byte, error) {
	if !digestShape.MatchString(digest) {
		return nil, ErrBadDigest
	}
	data, err := os.ReadFile(s.path(digest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (s *Store) path(digest string) string {
	return filepath.Join(s.dir, digest[:2], digest[2:4], digest)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/blob/ -v`
Expected: PASS for both tests.

- [ ] **Step 5: Commit**

```bash
git add internal/blob/blob.go internal/blob/blob_test.go
git commit -m "feat: content-addressed photo store" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: SQLite store (state + events in one transaction)

**Files:**
- Create: `internal/store/schema.sql`
- Create: `internal/store/store.go`
- Create: `internal/store/moves.go`
- Create: `internal/store/photos.go`
- Create: `internal/store/attempts.go`
- Create: `internal/store/events.go`
- Test: `internal/store/store_test.go`
- Modify: `go.mod`, `go.sum`

Rules from the spec (§7): every row carries `tenant_id`; IDs are ULIDs (time-sortable, safe to create offline); times are UTC; every state change writes the record **and** an append-only event in the same transaction. The database runs on **one connection** (SQLite has one writer and the hub's load is tiny). Consequence for the engineer: never call another store method while a `*sql.Rows` from this store is still open, or it deadlocks. Collect rows, close, then query again.

- [ ] **Step 1: Add dependencies**

Run: `go get modernc.org/sqlite@latest github.com/oklog/ulid/v2@latest`
Expected: both added to `go.mod`.

- [ ] **Step 2: Write the failing tests**

`internal/store/store_test.go`:

```go
package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

var ctx = context.Background()

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "depot.db")
	s, err := Open(path, "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func addPhoto(t *testing.T, s *Store, moveID string) Photo {
	t.Helper()
	p, err := s.AddPhoto(ctx, Photo{MoveID: moveID, Role: "rear", SHA256: "a", AISHA256: "b", MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 10}, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateAndGetMove(t *testing.T) {
	s, _ := openTest(t)
	m, err := s.CreateMove(ctx, "2", true, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMove(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lane != "2" || !got.IsTest || got.Status != StatusCapturing || got.Direction != "in" || len(got.Photos) != 0 {
		t.Fatalf("move = %+v", got)
	}
	if _, err := s.GetMove(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}
}

func TestPhotosAttemptsAndPending(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	p := addPhoto(t, s, m.ID)

	pending, err := s.PhotosWithoutAttempts(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != p.ID {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if _, err := s.AddAttempt(ctx, Attempt{MoveID: m.ID, PhotoID: p.ID, Engine: "fake", Model: "fake-1", PromptVersion: "v", Reading: json.RawMessage(`{"candidates":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PhotosWithoutAttempts(ctx); len(pending) != 0 {
		t.Fatalf("pending after attempt = %+v", pending)
	}
	attempts, err := s.ListAttempts(ctx, m.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Engine != "fake" {
		t.Fatalf("attempts = %+v, %v", attempts, err)
	}
	got, _ := s.GetMove(ctx, m.ID)
	if len(got.Photos) != 1 || got.Photos[0].Role != "rear" {
		t.Fatalf("photos = %+v", got.Photos)
	}
}

func TestResultThenResolveKeepsHistory(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	err := s.SetResult(ctx, m.ID, ReadOutcome{Status: StatusNeedsReview, Number: "CSQU3054384", Decision: "review", Reason: "check digit failed", ResultJSON: []byte(`{"number":"CSQU3054384"}`)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(ctx, m.ID, ResolveUpdate{Status: StatusAccepted, Number: "CSQU3054383", Verified: true, CorrectedFrom: "CSQU3054384", By: "ayse"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != "CSQU3054383" || !got.Verified || got.CorrectedFrom != "CSQU3054384" || got.ResolvedBy != "ayse" {
		t.Fatalf("resolved move = %+v", got)
	}

	events, err := s.Events(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 3 || kinds[0] != "move.created" || kinds[1] != "move.read" || kinds[2] != "move.resolved" {
		t.Fatalf("event kinds = %v", kinds)
	}
	var read map[string]any
	if err := json.Unmarshal(events[1].Payload, &read); err != nil || read["number"] != "CSQU3054384" {
		t.Fatalf("the machine's original read must survive the correction: %s", events[1].Payload)
	}
}

func TestSetResultAfterResolveIsIgnored(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	if _, err := s.Resolve(ctx, m.ID, ResolveUpdate{Status: StatusRefused, By: "ayse"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetResult(ctx, m.ID, ReadOutcome{Status: StatusNeedsReview, Number: "CSQU3054383"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetMove(ctx, m.ID)
	if got.Status != StatusRefused || got.Number != "" {
		t.Fatalf("a person's decision was overwritten: %+v", got)
	}
}

func TestListMovesFiltersAndPages(t *testing.T) {
	s, _ := openTest(t)
	a, _ := s.CreateMove(ctx, "1", false, "clerk")
	b, _ := s.CreateMove(ctx, "1", false, "clerk")
	c, _ := s.CreateMove(ctx, "1", false, "clerk")
	if err := s.SetStatus(ctx, b.ID, StatusNeedsReview); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListMoves(ctx, ListFilter{})
	if err != nil || len(all) != 3 || all[0].ID != c.ID {
		t.Fatalf("all = %v, %v; want 3, newest first", ids(all), err)
	}
	review, _ := s.ListMoves(ctx, ListFilter{Status: StatusNeedsReview})
	if len(review) != 1 || review[0].ID != b.ID {
		t.Fatalf("needs_review = %v", ids(review))
	}
	older, _ := s.ListMoves(ctx, ListFilter{Before: b.ID})
	if len(older) != 1 || older[0].ID != a.ID {
		t.Fatalf("before b = %v", ids(older))
	}
}

func TestVerifiedMovesExcludesTestAndUnverified(t *testing.T) {
	s, _ := openTest(t)
	good, _ := s.CreateMove(ctx, "1", false, "clerk")
	practice, _ := s.CreateMove(ctx, "1", true, "clerk")
	unverified, _ := s.CreateMove(ctx, "1", false, "clerk")
	for _, m := range []Move{good, practice} {
		if _, err := s.Resolve(ctx, m.ID, ResolveUpdate{Status: StatusAccepted, Number: "CSQU3054383", Verified: true, By: "ayse"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Resolve(ctx, unverified.ID, ResolveUpdate{Status: StatusAccepted, Number: "CSQU3054384", By: "ayse"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.VerifiedMoves(ctx)
	if err != nil || len(got) != 1 || got[0].ID != good.ID {
		t.Fatalf("verified = %v, %v", ids(got), err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	s, path := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	s.Close()
	again, err := Open(path, "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.GetMove(ctx, m.ID); err != nil {
		t.Fatalf("move lost after reopen: %v", err)
	}
}

func ids(moves []Move) []string {
	out := []string{}
	for _, m := range moves {
		out = append(out, m.ID)
	}
	return out
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL to compile with `undefined: Open`.

- [ ] **Step 4: Write `internal/store/schema.sql`**

Keep semicolons only at the ends of statements (the migration runner splits on them).

```sql
-- Schema v1 of the local gate hub: a subset of the spec data model
CREATE TABLE gate_moves (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL,
    depot_id          TEXT NOT NULL,
    lane              TEXT NOT NULL DEFAULT '',
    direction         TEXT NOT NULL DEFAULT 'in',
    status            TEXT NOT NULL,
    number            TEXT NOT NULL DEFAULT '',
    type_code         TEXT NOT NULL DEFAULT '',
    color             TEXT NOT NULL DEFAULT '',
    confidence        REAL NOT NULL DEFAULT 0,
    check_digit_valid INTEGER NOT NULL DEFAULT 0,
    decision          TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL DEFAULT '',
    result_json       TEXT NOT NULL DEFAULT '{}',
    verified          INTEGER NOT NULL DEFAULT 0,
    corrected_from    TEXT NOT NULL DEFAULT '',
    resolved_by       TEXT NOT NULL DEFAULT '',
    note              TEXT NOT NULL DEFAULT '',
    is_test           INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
CREATE INDEX gate_moves_status ON gate_moves (status, id);
CREATE TABLE photos (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    move_id       TEXT NOT NULL REFERENCES gate_moves (id),
    role          TEXT NOT NULL,
    sha256        TEXT NOT NULL,
    ai_sha256     TEXT NOT NULL,
    media_type    TEXT NOT NULL,
    ai_media_type TEXT NOT NULL,
    bytes         INTEGER NOT NULL,
    width         INTEGER NOT NULL DEFAULT 0,
    height        INTEGER NOT NULL DEFAULT 0,
    captured_at   TEXT NOT NULL
);
CREATE INDEX photos_move ON photos (move_id, id);
CREATE TABLE recognition_attempts (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    move_id        TEXT NOT NULL REFERENCES gate_moves (id),
    photo_id       TEXT NOT NULL REFERENCES photos (id),
    engine         TEXT NOT NULL,
    model          TEXT NOT NULL,
    prompt_version TEXT NOT NULL,
    reading_json   TEXT NOT NULL,
    raw            TEXT NOT NULL,
    error          TEXT NOT NULL DEFAULT '',
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    latency_ms     INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL
);
CREATE INDEX attempts_move ON recognition_attempts (move_id, id);
CREATE INDEX attempts_photo ON recognition_attempts (photo_id, id);
CREATE TABLE events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    id          TEXT NOT NULL UNIQUE,
    tenant_id   TEXT NOT NULL,
    stream      TEXT NOT NULL,
    kind        TEXT NOT NULL,
    actor       TEXT NOT NULL,
    payload     TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);
CREATE INDEX events_stream ON events (stream, seq)
```

- [ ] **Step 5: Implement `internal/store/store.go`**

```go
// Package store is the hub's SQLite database. Every state change writes the
// record and an append-only event in one transaction (spec §7.3).
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaV1 string

// migrations run in order. Append new versions; never edit a shipped one.
var migrations = []string{schemaV1}

// Move statuses.
const (
	StatusCapturing   = "capturing"
	StatusReading     = "reading"
	StatusNeedsReview = "needs_review"
	StatusAccepted    = "accepted"
	StatusRefused     = "refused"
)

var ErrNotFound = errors.New("not found")

// Store is the hub database.
type Store struct {
	db     *sql.DB
	tenant string
	depot  string
	now    func() time.Time
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path, tenant, depot string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer and the hub's load is tiny.
	// Never call a store method while a *sql.Rows from this store is open.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, tenant: tenant, depot: depot, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("schema_version: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return err
	}
	for v := current; v < len(migrations); v++ {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			for _, stmt := range strings.Split(migrations[v], ";") {
				if strings.TrimSpace(stmt) == "" {
					continue
				}
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, v+1)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) appendEvent(ctx context.Context, tx *sql.Tx, stream, kind, actor string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (id, tenant_id, stream, kind, actor, payload, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID(), s.tenant, stream, kind, actor, string(body), ts(s.now()))
	return err
}

type scanner interface{ Scan(dest ...any) error }

func newID() string { return ulid.Make().String() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 6: Implement `internal/store/moves.go`**

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Move is one container passing the gate.
type Move struct {
	ID              string          `json:"id"`
	Lane            string          `json:"lane"`
	Direction       string          `json:"direction"`
	Status          string          `json:"status"`
	Number          string          `json:"number"`
	TypeCode        string          `json:"type_code"`
	Color           string          `json:"color"`
	Confidence      float64         `json:"confidence"`
	CheckDigitValid bool            `json:"check_digit_valid"`
	Decision        string          `json:"decision"`
	Reason          string          `json:"reason"`
	Result          json.RawMessage `json:"result"`
	Verified        bool            `json:"verified"`
	CorrectedFrom   string          `json:"corrected_from"`
	ResolvedBy      string          `json:"resolved_by"`
	Note            string          `json:"note"`
	IsTest          bool            `json:"is_test"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Photos          []Photo         `json:"photos"`
}

// ReadOutcome is what the engines concluded about a move.
type ReadOutcome struct {
	Status          string
	Number          string
	TypeCode        string
	Color           string
	Confidence      float64
	CheckDigitValid bool
	Decision        string
	Reason          string
	ResultJSON      []byte
}

// ResolveUpdate is a person's decision about a move.
type ResolveUpdate struct {
	Status        string `json:"status"`
	Number        string `json:"number"`
	Verified      bool   `json:"verified"`
	CorrectedFrom string `json:"corrected_from,omitempty"`
	By            string `json:"by"`
	Note          string `json:"note,omitempty"`
}

// ListFilter selects moves, newest first. Before is a move ID (keyset paging).
type ListFilter struct {
	Status string
	Limit  int
	Before string
}

const moveColumns = `id, lane, direction, status, number, type_code, color, confidence, check_digit_valid,
	decision, reason, result_json, verified, corrected_from, resolved_by, note, is_test, created_at, updated_at`

func scanMove(sc scanner) (Move, error) {
	var m Move
	var result, created, updated string
	var cdv, verified, isTest int
	err := sc.Scan(&m.ID, &m.Lane, &m.Direction, &m.Status, &m.Number, &m.TypeCode, &m.Color, &m.Confidence, &cdv,
		&m.Decision, &m.Reason, &result, &verified, &m.CorrectedFrom, &m.ResolvedBy, &m.Note, &isTest, &created, &updated)
	if err != nil {
		return Move{}, err
	}
	if result == "" {
		result = "{}"
	}
	m.Result = json.RawMessage(result)
	m.CheckDigitValid, m.Verified, m.IsTest = cdv == 1, verified == 1, isTest == 1
	m.CreatedAt, m.UpdatedAt = parseTS(created), parseTS(updated)
	m.Photos = []Photo{}
	return m, nil
}

// CreateMove starts a gate-in on a lane.
func (s *Store) CreateMove(ctx context.Context, lane string, isTest bool, actor string) (Move, error) {
	now := s.now()
	m := Move{
		ID: newID(), Lane: lane, Direction: "in", Status: StatusCapturing, Result: json.RawMessage("{}"),
		IsTest: isTest, CreatedAt: now, UpdatedAt: now, Photos: []Photo{},
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO gate_moves (id, tenant_id, depot_id, lane, direction, status, is_test, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, s.tenant, s.depot, lane, m.Direction, m.Status, boolInt(isTest), ts(now), ts(now))
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, m.ID, "move.created", actor, map[string]any{"lane": lane, "is_test": isTest})
	})
	return m, err
}

// GetMove returns a move with its photos.
func (s *Store) GetMove(ctx context.Context, id string) (Move, error) {
	m, err := scanMove(s.db.QueryRowContext(ctx, `SELECT `+moveColumns+` FROM gate_moves WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Move{}, ErrNotFound
	}
	if err != nil {
		return Move{}, err
	}
	m.Photos, err = s.ListPhotos(ctx, id)
	return m, err
}

// ListMoves returns moves newest first, each with its photos.
func (s *Store) ListMoves(ctx context.Context, f ListFilter) ([]Move, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	return s.queryMoves(ctx,
		`SELECT `+moveColumns+` FROM gate_moves
		 WHERE (? = '' OR status = ?) AND (? = '' OR id < ?)
		 ORDER BY id DESC LIMIT ?`,
		f.Status, f.Status, f.Before, f.Before, f.Limit)
}

// VerifiedMoves returns accepted, person-verified, non-practice moves, oldest
// first: the training set and the benchmark set.
func (s *Store) VerifiedMoves(ctx context.Context) ([]Move, error) {
	return s.queryMoves(ctx,
		`SELECT `+moveColumns+` FROM gate_moves
		 WHERE status = 'accepted' AND verified = 1 AND is_test = 0 ORDER BY id`)
}

func (s *Store) queryMoves(ctx context.Context, query string, args ...any) ([]Move, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	moves := []Move{}
	for rows.Next() {
		m, err := scanMove(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		moves = append(moves, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // must be closed before the next query (single connection)
	for i := range moves {
		if moves[i].Photos, err = s.ListPhotos(ctx, moves[i].ID); err != nil {
			return nil, err
		}
	}
	return moves, nil
}

// SetStatus changes the status of a move nobody has resolved yet.
func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gate_moves SET status = ?, updated_at = ? WHERE id = ? AND resolved_by = ''`,
		status, ts(s.now()), id)
	return err
}

// SetResult records what the engines concluded. A move a person has already
// resolved is left alone: the human decision stands.
func (s *Store) SetResult(ctx context.Context, id string, o ReadOutcome) error {
	result := o.ResultJSON
	if len(result) == 0 {
		result = []byte("{}")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE gate_moves SET status = ?, number = ?, type_code = ?, color = ?, confidence = ?,
			 check_digit_valid = ?, decision = ?, reason = ?, result_json = ?, updated_at = ?
			 WHERE id = ? AND resolved_by = ''`,
			o.Status, o.Number, o.TypeCode, o.Color, o.Confidence, boolInt(o.CheckDigitValid),
			o.Decision, o.Reason, string(result), ts(s.now()), id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		return s.appendEvent(ctx, tx, id, "move.read", "engine", map[string]any{
			"status": o.Status, "number": o.Number, "decision": o.Decision, "reason": o.Reason,
			"confidence": o.Confidence, "type_code": o.TypeCode, "color": o.Color,
		})
	})
}

// Resolve records a person's decision.
func (s *Store) Resolve(ctx context.Context, id string, u ResolveUpdate) (Move, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE gate_moves SET status = ?, number = ?, verified = ?, corrected_from = ?, resolved_by = ?,
			 note = ?, updated_at = ? WHERE id = ?`,
			u.Status, u.Number, boolInt(u.Verified), u.CorrectedFrom, u.By, u.Note, ts(s.now()), id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return s.appendEvent(ctx, tx, id, "move.resolved", u.By, u)
	})
	if err != nil {
		return Move{}, err
	}
	return s.GetMove(ctx, id)
}
```

- [ ] **Step 7: Implement `internal/store/photos.go`**

```go
package store

import (
	"context"
	"database/sql"
	"time"
)

// Photo is one picture of a move. SHA256 is the original as captured (kept
// for evidence and training); AISHA256 is the downscaled copy sent to engines.
type Photo struct {
	ID          string    `json:"id"`
	MoveID      string    `json:"move_id"`
	Role        string    `json:"role"`
	SHA256      string    `json:"sha256"`
	AISHA256    string    `json:"ai_sha256"`
	MediaType   string    `json:"media_type"`
	AIMediaType string    `json:"ai_media_type"`
	Bytes       int64     `json:"bytes"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	CapturedAt  time.Time `json:"captured_at"`
}

const photoColumns = `id, move_id, role, sha256, ai_sha256, media_type, ai_media_type, bytes, width, height, captured_at`

func scanPhoto(sc scanner) (Photo, error) {
	var p Photo
	var captured string
	err := sc.Scan(&p.ID, &p.MoveID, &p.Role, &p.SHA256, &p.AISHA256, &p.MediaType, &p.AIMediaType,
		&p.Bytes, &p.Width, &p.Height, &captured)
	p.CapturedAt = parseTS(captured)
	return p, err
}

// AddPhoto records a photo of a move.
func (s *Store) AddPhoto(ctx context.Context, p Photo, actor string) (Photo, error) {
	p.ID = newID()
	if p.CapturedAt.IsZero() {
		p.CapturedAt = s.now()
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO photos (id, tenant_id, move_id, role, sha256, ai_sha256, media_type, ai_media_type,
			 bytes, width, height, captured_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, s.tenant, p.MoveID, p.Role, p.SHA256, p.AISHA256, p.MediaType, p.AIMediaType,
			p.Bytes, p.Width, p.Height, ts(p.CapturedAt))
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, p.MoveID, "photo.added", actor,
			map[string]any{"photo_id": p.ID, "role": p.Role, "sha256": p.SHA256})
	})
	return p, err
}

// ListPhotos returns a move's photos in capture order.
func (s *Store) ListPhotos(ctx context.Context, moveID string) ([]Photo, error) {
	return s.queryPhotos(ctx, `SELECT `+photoColumns+` FROM photos WHERE move_id = ? ORDER BY id`, moveID)
}

// PhotosWithoutAttempts returns photos of open moves that no engine has read
// yet, e.g. because the hub stopped mid-read. Start re-queues them.
func (s *Store) PhotosWithoutAttempts(ctx context.Context) ([]Photo, error) {
	return s.queryPhotos(ctx,
		`SELECT `+photoColumns+` FROM photos p
		 WHERE NOT EXISTS (SELECT 1 FROM recognition_attempts a WHERE a.photo_id = p.id)
		   AND p.move_id IN (SELECT id FROM gate_moves WHERE resolved_by = '')
		 ORDER BY id`)
}

func (s *Store) queryPhotos(ctx context.Context, query string, args ...any) ([]Photo, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	photos := []Photo{}
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}
```

- [ ] **Step 8: Implement `internal/store/attempts.go`**

```go
package store

import (
	"context"
	"encoding/json"
	"time"
)

// Attempt is one engine run on one photo. The table is append-only: raw
// engine output is kept forever beside the resolved value.
type Attempt struct {
	ID            string          `json:"id"`
	MoveID        string          `json:"move_id"`
	PhotoID       string          `json:"photo_id"`
	Engine        string          `json:"engine"`
	Model         string          `json:"model"`
	PromptVersion string          `json:"prompt_version"`
	Reading       json.RawMessage `json:"reading"`
	Raw           string          `json:"raw"`
	Error         string          `json:"error"`
	InputTokens   int64           `json:"input_tokens"`
	OutputTokens  int64           `json:"output_tokens"`
	LatencyMS     int64           `json:"latency_ms"`
	CreatedAt     time.Time       `json:"created_at"`
}

// AddAttempt records an engine run.
func (s *Store) AddAttempt(ctx context.Context, a Attempt) (Attempt, error) {
	a.ID, a.CreatedAt = newID(), s.now()
	if len(a.Reading) == 0 {
		a.Reading = json.RawMessage("{}")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO recognition_attempts (id, tenant_id, move_id, photo_id, engine, model, prompt_version,
		 reading_json, raw, error, input_tokens, output_tokens, latency_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, s.tenant, a.MoveID, a.PhotoID, a.Engine, a.Model, a.PromptVersion,
		string(a.Reading), a.Raw, a.Error, a.InputTokens, a.OutputTokens, a.LatencyMS, ts(a.CreatedAt))
	return a, err
}

// ListAttempts returns a move's engine runs, oldest first.
func (s *Store) ListAttempts(ctx context.Context, moveID string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, move_id, photo_id, engine, model, prompt_version, reading_json, raw, error,
		 input_tokens, output_tokens, latency_ms, created_at
		 FROM recognition_attempts WHERE move_id = ? ORDER BY id`, moveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := []Attempt{}
	for rows.Next() {
		var a Attempt
		var reading, created string
		if err := rows.Scan(&a.ID, &a.MoveID, &a.PhotoID, &a.Engine, &a.Model, &a.PromptVersion, &reading, &a.Raw,
			&a.Error, &a.InputTokens, &a.OutputTokens, &a.LatencyMS, &created); err != nil {
			return nil, err
		}
		a.Reading, a.CreatedAt = json.RawMessage(reading), parseTS(created)
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}
```

- [ ] **Step 9: Implement `internal/store/events.go`**

```go
package store

import (
	"context"
	"encoding/json"
	"time"
)

// Event is one entry of the append-only log.
type Event struct {
	Seq        int64           `json:"seq"`
	ID         string          `json:"id"`
	Stream     string          `json:"stream"`
	Kind       string          `json:"kind"`
	Actor      string          `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

// Events returns a stream's events in order.
func (s *Store) Events(ctx context.Context, stream string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, id, stream, kind, actor, payload, occurred_at FROM events WHERE stream = ? ORDER BY seq`, stream)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		var payload, occurred string
		if err := rows.Scan(&e.Seq, &e.ID, &e.Stream, &e.Kind, &e.Actor, &payload, &occurred); err != nil {
			return nil, err
		}
		e.Payload, e.OccurredAt = json.RawMessage(payload), parseTS(occurred)
		events = append(events, e)
	}
	return events, rows.Err()
}
```

- [ ] **Step 10: Run the tests**

Run: `go test ./internal/store/ -v`
Expected: PASS for all seven tests.

- [ ] **Step 11: Commit**

```bash
git add go.mod go.sum internal/store/
git commit -m "feat: SQLite hub store with state and events in one transaction" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Gate service

**Files:**
- Create: `internal/gate/service.go`
- Test: `internal/gate/service_test.go`

The service ties it together. Adding a photo stores both copies, marks the move `reading` and queues the AI copy for a worker. Each worker reads one photo, stores the attempt, then **recomputes the whole move** from the latest attempt of every photo (one mutex, so two photos of the same truck can't interleave). While any photo is unread the move stays `reading`; afterwards it goes to `needs_review`, or to `accepted` only if auto-accept is enabled and Reconcile says so. Auto-accept is **off by default**: at the owner's depot every read is confirmed by a person, which is what builds verified training data. A person's decision always wins over later engine results.

Resolution rules:
- `accept`: keep the read number; `verified` only if it passes the check digit.
- `correct`: a person types the number; `verified` only if it passes the check digit (an unreadable box can still be recorded, flagged unverified); `corrected_from` keeps the machine's number.
- `refuse`: status `refused`, never verified.

- [ ] **Step 1: Write the failing tests**

`internal/gate/service_test.go`:

```go
package gate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"depot/internal/blob"
	"depot/internal/recognition"
	"depot/internal/store"
)

var ctx = context.Background()

type harness struct {
	svc   *Service
	store *store.Store
	blobs *blob.Store
}

func newHarness(t *testing.T, rec recognition.Recognizer, cfg Config, start bool) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "depot.db"), "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	bl, err := blob.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, bl, rec, cfg, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	if start {
		if err := svc.Start(runCtx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cancel()
		svc.Wait()
		st.Close()
	})
	return &harness{svc: svc, store: st, blobs: bl}
}

func reading(number string, conf float64) recognition.Reading {
	return recognition.Reading{Candidates: []recognition.RawCandidate{{Number: number, Confidence: conf}}, TypeCode: "45G1", Color: "blue", Visible: "full"}
}

func (h *harness) photo(t *testing.T, moveID, role, content string) {
	t.Helper()
	_, err := h.svc.AddPhoto(ctx, PhotoInput{
		MoveID: moveID, Role: role, Actor: "clerk",
		Original: []byte("original-" + content), OriginalType: "image/jpeg",
		AI: []byte(content), AIType: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func waitStatus(t *testing.T, st *store.Store, id, want string) store.Move {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m, err := st.GetMove(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if m.Status == want {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("move %s status = %s; want %s (reason %q)", id, m.Status, want, m.Reason)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgreeingPhotosWaitForAPersonByDefault(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	m, err := h.svc.NewMove(ctx, "1", false, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	h.photo(t, m.ID, "rear", "a")
	h.photo(t, m.ID, "left", "b")
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if got.Number != "CSQU3054383" || got.Decision != string(recognition.DecisionAutoAccept) || got.TypeCode != "45G1" || got.Color != "blue" {
		t.Fatalf("move = %+v", got)
	}
}

func TestAutoAcceptWhenEnabled(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{AutoAcceptEnabled: true}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	h.photo(t, m.ID, "left", "b")
	got := waitStatus(t, h.store, m.ID, store.StatusAccepted)
	if got.Verified {
		t.Fatal("an automatic accept is not a person's verification")
	}
}

func TestEngineErrorGoesToReview(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Err: recognition.ErrUnavailable}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if !strings.Contains(got.Reason, "engine unavailable") {
		t.Fatalf("reason = %q", got.Reason)
	}
}

func TestCorrectionIsRecordedAndVerified(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054384", 0.9)}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)

	got, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "correct", Number: "csqu 305 4383", By: "ayse"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusAccepted || got.Number != "CSQU3054383" || !got.Verified || got.CorrectedFrom != "CSQU3054384" {
		t.Fatalf("move = %+v", got)
	}
}

func TestInvalidCorrectionIsAcceptedButUnverified(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	got, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "correct", Number: "ABCU1111111", By: "ayse"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusAccepted || got.Verified {
		t.Fatalf("move = %+v; want accepted but unverified", got)
	}
}

func TestResolvedMoveIsClosed(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "refuse", By: "ayse"}); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.AddPhoto(ctx, PhotoInput{MoveID: m.ID, Role: "rear", Original: []byte("x"), OriginalType: "image/jpeg", AI: []byte("x"), AIType: "image/jpeg"})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("AddPhoto err = %v; want ErrClosed", err)
	}
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "accept", By: "ayse"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Resolve err = %v; want ErrClosed", err)
	}
}

func TestBadInputsAreRejected(t *testing.T) {
	h := newHarness(t, &recognition.Fake{}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "dance"}); !errors.Is(err, ErrBadAction) {
		t.Fatalf("err = %v; want ErrBadAction", err)
	}
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "correct", Number: " "}); !errors.Is(err, ErrNumberRequired) {
		t.Fatalf("err = %v; want ErrNumberRequired", err)
	}
	if _, err := h.svc.AddPhoto(ctx, PhotoInput{MoveID: m.ID, Role: "rear", Original: []byte("x"), AI: []byte("x"), AIType: "application/pdf"}); !errors.Is(err, ErrBadPhoto) {
		t.Fatalf("err = %v; want ErrBadPhoto", err)
	}
	if _, err := h.svc.Resolve(ctx, "missing", Resolution{Action: "accept"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}
}

func TestStartRereadsPhotosLeftUnread(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	// Simulate a photo saved just before a power cut: stored, never read.
	ai, _ := h.blobs.Put([]byte("a"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: m.ID, Role: "rear", SHA256: ai, AISHA256: ai, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1}, "clerk"); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if fake.Calls() != 1 {
		t.Fatalf("engine calls = %d; want 1", fake.Calls())
	}
}

func TestRereadQueuesEveryPhotoAgain(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if err := h.svc.Reread(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fake.Calls() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if fake.Calls() != 2 {
		t.Fatalf("engine calls = %d; want 2", fake.Calls())
	}
}

func TestTestReadDoesNotStoreAnything(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	res, err := h.svc.TestRead(ctx, []byte("x"), "image/jpeg")
	if err != nil || res.Error != "" {
		t.Fatalf("TestRead = %+v, %v", res, err)
	}
	if len(res.Frame.Candidates) != 1 || !res.Frame.Candidates[0].CheckDigitValid {
		t.Fatalf("frame = %+v", res.Frame)
	}
	moves, _ := h.store.ListMoves(ctx, store.ListFilter{})
	if len(moves) != 0 {
		t.Fatalf("test read created moves: %d", len(moves))
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/gate/`
Expected: FAIL to compile with `undefined: New`.

- [ ] **Step 3: Implement `internal/gate/service.go`**

```go
// Package gate runs gate-ins: photos come in, engines read them in the
// background, and a person confirms, corrects or refuses the result.
package gate

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"

	"depot/internal/blob"
	"depot/internal/iso6346"
	"depot/internal/recognition"
	"depot/internal/store"
)

var (
	ErrNotFound       = errors.New("move not found")
	ErrClosed         = errors.New("move already resolved")
	ErrBadAction      = errors.New("unknown action: use accept, correct or refuse")
	ErrNumberRequired = errors.New("a container number is required")
	ErrBadPhoto       = errors.New("photo missing or not a JPEG, PNG or WebP image")
)

// Config tunes the service.
type Config struct {
	// AutoAcceptEnabled lets confident, check-digit-valid reads skip the
	// person. Off by default: at the owner's depot every read is confirmed.
	AutoAcceptEnabled   bool
	AutoAcceptThreshold float64 // default 0.85
	Workers             int     // parallel engine calls, default 2
}

// Service orchestrates gate-ins.
type Service struct {
	store  *store.Store
	blobs  *blob.Store
	rec    recognition.Recognizer
	cfg    Config
	notify func(moveID string)
	jobs   chan job
	ctx    context.Context
	mu     sync.Mutex // serializes recompute and resolve
	wg     sync.WaitGroup
}

type job struct {
	moveID, photoID, aiSHA, aiType string
}

// New builds the service. notify is called whenever a move changes (live
// screen updates); it may be nil.
func New(st *store.Store, bl *blob.Store, rec recognition.Recognizer, cfg Config, notify func(string)) *Service {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.AutoAcceptThreshold <= 0 {
		cfg.AutoAcceptThreshold = 0.85
	}
	if notify == nil {
		notify = func(string) {}
	}
	return &Service{store: st, blobs: bl, rec: rec, cfg: cfg, notify: notify, jobs: make(chan job, 256), ctx: context.Background()}
}

// Engine names the engine and model in use.
func (s *Service) Engine() (name, model string) { return s.rec.Name(), s.rec.Model() }

// Start launches the workers and re-queues photos left unread by a previous
// run. Workers stop when ctx is cancelled; call Wait afterwards.
func (s *Service) Start(ctx context.Context) error {
	s.ctx = ctx
	for i := 0; i < s.cfg.Workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	pending, err := s.store.PhotosWithoutAttempts(ctx)
	if err != nil {
		return err
	}
	for _, p := range pending {
		s.enqueue(job{moveID: p.MoveID, photoID: p.ID, aiSHA: p.AISHA256, aiType: p.AIMediaType})
	}
	return nil
}

// Wait blocks until the workers have stopped.
func (s *Service) Wait() { s.wg.Wait() }

// NewMove starts a gate-in.
func (s *Service) NewMove(ctx context.Context, lane string, isTest bool, actor string) (store.Move, error) {
	return s.store.CreateMove(ctx, lane, isTest, actor)
}

// PhotoInput is one uploaded photo: the original as captured, and a
// downscaled, correctly oriented copy for engines.
type PhotoInput struct {
	MoveID, Role, Actor string
	Original            []byte
	OriginalType        string
	AI                  []byte
	AIType              string
	Width, Height       int
}

// AddPhoto stores a photo and queues it for reading.
func (s *Service) AddPhoto(ctx context.Context, in PhotoInput) (store.Photo, error) {
	if len(in.Original) == 0 || len(in.AI) == 0 || !engineReadable(in.AIType) {
		return store.Photo{}, ErrBadPhoto
	}
	m, err := s.openMove(ctx, in.MoveID)
	if err != nil {
		return store.Photo{}, err
	}
	origSHA, err := s.blobs.Put(in.Original)
	if err != nil {
		return store.Photo{}, err
	}
	aiSHA, err := s.blobs.Put(in.AI)
	if err != nil {
		return store.Photo{}, err
	}
	role := strings.TrimSpace(in.Role)
	if role == "" {
		role = "other"
	}
	p, err := s.store.AddPhoto(ctx, store.Photo{
		MoveID: m.ID, Role: role, SHA256: origSHA, AISHA256: aiSHA, MediaType: in.OriginalType,
		AIMediaType: in.AIType, Bytes: int64(len(in.Original)), Width: in.Width, Height: in.Height,
	}, in.Actor)
	if err != nil {
		return store.Photo{}, err
	}
	if err := s.store.SetStatus(ctx, m.ID, store.StatusReading); err != nil {
		return store.Photo{}, err
	}
	s.notify(m.ID)
	s.enqueue(job{moveID: m.ID, photoID: p.ID, aiSHA: aiSHA, aiType: in.AIType})
	return p, nil
}

// Reread queues every photo of an open move for another engine run, e.g.
// after an engine outage.
func (s *Service) Reread(ctx context.Context, moveID string) error {
	m, err := s.openMove(ctx, moveID)
	if err != nil {
		return err
	}
	if len(m.Photos) == 0 {
		return nil
	}
	if err := s.store.SetStatus(ctx, m.ID, store.StatusReading); err != nil {
		return err
	}
	s.notify(m.ID)
	for _, p := range m.Photos {
		s.enqueue(job{moveID: m.ID, photoID: p.ID, aiSHA: p.AISHA256, aiType: p.AIMediaType})
	}
	return nil
}

// Resolution is a person's decision.
type Resolution struct {
	Action string `json:"action"` // accept | correct | refuse
	Number string `json:"number"` // for correct
	By     string `json:"by"`
	Note   string `json:"note"`
}

// Resolve records a person's decision about an open move.
func (s *Service) Resolve(ctx context.Context, moveID string, r Resolution) (store.Move, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.openMove(ctx, moveID)
	if err != nil {
		return m, err
	}
	by := strings.TrimSpace(r.By)
	if by == "" {
		by = "operator"
	}
	u := store.ResolveUpdate{By: by, Note: strings.TrimSpace(r.Note)}
	switch r.Action {
	case "accept":
		if m.Number == "" {
			return m, ErrNumberRequired
		}
		u.Status, u.Number, u.Verified = store.StatusAccepted, m.Number, iso6346.IsValid(m.Number)
	case "correct":
		n := iso6346.Normalize(r.Number)
		if n == "" {
			return m, ErrNumberRequired
		}
		u.Status, u.Number, u.Verified = store.StatusAccepted, n, iso6346.IsValid(n)
		if m.Number != "" && m.Number != n {
			u.CorrectedFrom = m.Number
		}
	case "refuse":
		u.Status, u.Number = store.StatusRefused, m.Number
	default:
		return m, ErrBadAction
	}
	out, err := s.store.Resolve(ctx, moveID, u)
	if err == nil {
		s.notify(moveID)
	}
	return out, err
}

// TestReadResult is the outcome of a one-off engine test.
type TestReadResult struct {
	Attempt recognition.Attempt `json:"attempt"`
	Frame   recognition.Frame   `json:"frame"`
	Error   string              `json:"error,omitempty"`
}

// TestRead runs the engine on one photo without storing anything. Engine
// failures are reported in the result, not as an error.
func (s *Service) TestRead(ctx context.Context, ai []byte, aiType string) (TestReadResult, error) {
	if len(ai) == 0 || !engineReadable(aiType) {
		return TestReadResult{}, ErrBadPhoto
	}
	att, err := s.rec.Read(ctx, recognition.Image{Data: ai, MediaType: aiType})
	res := TestReadResult{Attempt: att}
	if err != nil {
		res.Error = err.Error()
		res.Frame = recognition.ErrorFrame("test", err.Error())
		return res, nil
	}
	res.Frame = recognition.BuildFrame("test", att.Reading)
	return res, nil
}

func (s *Service) openMove(ctx context.Context, id string) (store.Move, error) {
	m, err := s.store.GetMove(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if m.ResolvedBy != "" {
		return m, ErrClosed
	}
	return m, nil
}

func (s *Service) enqueue(j job) {
	select {
	case s.jobs <- j:
	case <-s.ctx.Done():
	}
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.jobs:
			s.process(j)
		}
	}
}

func (s *Service) process(j job) {
	ctx := s.ctx
	data, err := s.blobs.Get(j.aiSHA)
	att := recognition.Attempt{Engine: s.rec.Name(), Model: s.rec.Model(), PromptVersion: recognition.PromptVersion}
	if err == nil {
		att, err = s.rec.Read(ctx, recognition.Image{Data: data, MediaType: j.aiType})
	}
	if ctx.Err() != nil {
		return // shutting down: the photo stays unread and Start re-queues it
	}
	reading, _ := json.Marshal(att.Reading)
	row := store.Attempt{
		MoveID: j.moveID, PhotoID: j.photoID, Engine: att.Engine, Model: att.Model, PromptVersion: att.PromptVersion,
		Reading: reading, Raw: att.Raw, InputTokens: att.Usage.InputTokens, OutputTokens: att.Usage.OutputTokens,
		LatencyMS: att.LatencyMS,
	}
	if err != nil {
		row.Error = err.Error()
	}
	if _, err := s.store.AddAttempt(ctx, row); err != nil {
		log.Printf("gate: store attempt for photo %s: %v", j.photoID, err)
		return
	}
	if err := s.recompute(ctx, j.moveID); err != nil {
		log.Printf("gate: recompute move %s: %v", j.moveID, err)
	}
	s.notify(j.moveID)
}

// recompute rebuilds a move's result from the latest attempt of each photo.
func (s *Service) recompute(ctx context.Context, moveID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.store.GetMove(ctx, moveID)
	if err != nil {
		return err
	}
	if m.ResolvedBy != "" {
		return nil
	}
	attempts, err := s.store.ListAttempts(ctx, moveID)
	if err != nil {
		return err
	}
	latest := map[string]store.Attempt{}
	for _, a := range attempts { // oldest first, so later attempts win
		latest[a.PhotoID] = a
	}
	var frames []recognition.Frame
	pending := 0
	for _, p := range m.Photos {
		a, ok := latest[p.ID]
		switch {
		case !ok:
			pending++
		case a.Error != "":
			frames = append(frames, recognition.ErrorFrame(p.Role, a.Error))
		default:
			var r recognition.Reading
			if err := json.Unmarshal(a.Reading, &r); err != nil {
				frames = append(frames, recognition.ErrorFrame(p.Role, err.Error()))
				continue
			}
			frames = append(frames, recognition.BuildFrame(p.Role, r))
		}
	}
	res := recognition.Reconcile(frames, s.cfg.AutoAcceptThreshold)
	status := store.StatusNeedsReview
	switch {
	case pending > 0:
		status = store.StatusReading
	case res.Decision == recognition.DecisionAutoAccept && s.cfg.AutoAcceptEnabled:
		status = store.StatusAccepted
	}
	resultJSON, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return s.store.SetResult(ctx, moveID, store.ReadOutcome{
		Status: status, Number: res.Number, TypeCode: res.TypeCode, Color: res.Color, Confidence: res.Confidence,
		CheckDigitValid: res.CheckDigitValid, Decision: string(res.Decision), Reason: res.Reason, ResultJSON: resultJSON,
	})
}

func engineReadable(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/webp":
		return true
	}
	return false
}
```

Note on `TestStartRereadsPhotosLeftUnread`: the harness is created with `start=false`, so its cleanup cancels a context the service never used; the test's own `runCtx` drives the workers, and the harness cleanup's `svc.Wait()` returns once the deferred `cancel()` has stopped them.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/gate/ -v -race`
Expected: PASS for all ten tests. (`-race` needs CGO on some platforms; if it reports that cgo is required, run without `-race`.)

- [ ] **Step 5: Commit**

```bash
git add internal/gate/
git commit -m "feat: gate service with background reading and human resolution" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: HTTP API, live updates and access code

**Files:**
- Create: `internal/httpapi/server.go`
- Create: `internal/httpapi/handlers.go`
- Create: `internal/httpapi/broker.go`
- Test: `internal/httpapi/server_test.go`

Endpoints:

| Method + path | Purpose |
|---|---|
| `GET /api/health` | engine, model, version, whether an access code is required (always open) |
| `POST /api/login` `{code}` | sets an HttpOnly cookie when the access code matches (always open) |
| `POST /api/moves` `{lane, is_test}` | start a gate-in → 201 move |
| `GET /api/moves?status=&limit=&before=` | list moves newest first |
| `GET /api/moves/{id}` | move + photos + engine attempts |
| `POST /api/moves/{id}/photos` multipart `role`, `width`, `height`, files `original`, `ai` | add a photo → 202 |
| `POST /api/moves/{id}/resolve` `{action, number, by, note}` | person's decision |
| `POST /api/moves/{id}/reread` | read every photo again → 202 |
| `GET /api/blobs/{digest}` | photo bytes (images only; anything else downloads as a file) |
| `GET /api/validate/{number}` | check-digit validation with the arithmetic |
| `POST /api/test-read` multipart file `ai` | run the engine on one photo, store nothing |
| `GET /api/stream` | Server-Sent Events: `event: move` / `data: {"id": ...}` whenever a move changes |

Errors are JSON: `{"error": {"code": "E4041", "message": "move not found"}}`. Code families follow the prototype's catalogue: 4xxx bad request / not found / conflict, 5xxx access, 9xxx internal.

The access code is optional (`-access-code`). Browsers can't add headers to `<img>` or `EventSource` requests, so it is enforced with a cookie: the login handler compares the code in constant time and sets an HttpOnly cookie holding a SHA-256 of the code.

- [ ] **Step 1: Write the failing tests**

`internal/httpapi/server_test.go`:

```go
package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/recognition"
	"depot/internal/store"
)

func newTestServer(t *testing.T, accessCode string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "depot.db"), "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	bl, err := blob.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &recognition.Fake{Reading: recognition.Reading{
		Candidates: []recognition.RawCandidate{{Number: "CSQU3054383", Confidence: 0.9}},
		TypeCode:   "45G1", Color: "blue", Visible: "full",
	}}
	broker := NewBroker()
	svc := gate.New(st, bl, fake, gate.Config{}, broker.Publish)
	runCtx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<html><div id=root></div></html>")}}
	srv := httptest.NewServer(New(Options{
		Service: svc, Store: st, Blobs: bl, Static: static, AccessCode: accessCode, Version: "test",
	}, broker).Handler())
	t.Cleanup(func() {
		srv.Close()
		cancel()
		svc.Wait()
		st.Close()
	})
	return srv
}

func call(t *testing.T, client *http.Client, method, url, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func uploadPhoto(t *testing.T, client *http.Client, base, moveID, original string) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("role", "rear")
	_ = mw.WriteField("width", "1568")
	_ = mw.WriteField("height", "1176")
	for field, content := range map[string]string{"original": original, "ai": "\xff\xd8\xff\xe0ai-copy"} {
		fw, err := mw.CreateFormFile(field, field+".jpg")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(content))
	}
	mw.Close()
	resp, err := client.Post(base+"/api/moves/"+moveID+"/photos", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

type detail struct {
	Status   string `json:"status"`
	Number   string `json:"number"`
	Verified bool   `json:"verified"`
	Photos   []struct {
		SHA256   string `json:"sha256"`
		AISHA256 string `json:"ai_sha256"`
	} `json:"photos"`
	Attempts []struct {
		Engine string `json:"engine"`
	} `json:"attempts"`
}

func createAndRead(t *testing.T, client *http.Client, base, original string) (string, detail) {
	t.Helper()
	status, body := call(t, client, "POST", base+"/api/moves", `{"lane":"2"}`)
	if status != http.StatusCreated {
		t.Fatalf("create move = %d %s", status, body)
	}
	var m struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &m)
	if code := uploadPhoto(t, client, base, m.ID, original); code != http.StatusAccepted {
		t.Fatalf("upload = %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var d detail
		_, body = call(t, client, "GET", base+"/api/moves/"+m.ID, "")
		_ = json.Unmarshal(body, &d)
		if d.Status == "needs_review" {
			return m.ID, d
		}
		if time.Now().After(deadline) {
			t.Fatalf("move never reached needs_review: %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMoveLifecycleOverHTTP(t *testing.T) {
	srv := newTestServer(t, "")
	c := srv.Client()
	id, d := createAndRead(t, c, srv.URL, "\xff\xd8\xff\xe0original")
	if d.Number != "CSQU3054383" || len(d.Photos) != 1 || len(d.Attempts) != 1 || d.Attempts[0].Engine != "fake" {
		t.Fatalf("detail = %+v", d)
	}

	status, body := call(t, c, "GET", srv.URL+"/api/blobs/"+d.Photos[0].AISHA256, "")
	if status != http.StatusOK || !bytes.HasPrefix(body, []byte("\xff\xd8\xff")) {
		t.Fatalf("blob = %d %q", status, body)
	}

	status, body = call(t, c, "POST", srv.URL+"/api/moves/"+id+"/resolve", `{"action":"accept","by":"ayse"}`)
	var resolved detail
	_ = json.Unmarshal(body, &resolved)
	if status != http.StatusOK || resolved.Status != "accepted" || !resolved.Verified {
		t.Fatalf("resolve = %d %s", status, body)
	}
	if status, _ := call(t, c, "POST", srv.URL+"/api/moves/"+id+"/resolve", `{"action":"accept"}`); status != http.StatusConflict {
		t.Fatalf("second resolve = %d; want 409", status)
	}
}

func TestNonImageBlobsDownloadInsteadOfRendering(t *testing.T) {
	srv := newTestServer(t, "")
	_, d := createAndRead(t, srv.Client(), srv.URL, "<html><script>alert(1)</script></html>")
	resp, err := srv.Client().Get(srv.URL + "/api/blobs/" + d.Photos[0].SHA256)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content type = %q; want application/octet-stream", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("content disposition = %q; want attachment", cd)
	}
}

func TestValidateExplainsCheckDigit(t *testing.T) {
	srv := newTestServer(t, "")
	status, body := call(t, srv.Client(), "GET", srv.URL+"/api/validate/CSQU3054383", "")
	var v struct {
		Valid bool  `json:"valid"`
		Steps []any `json:"steps"`
		Sum   int   `json:"sum"`
	}
	_ = json.Unmarshal(body, &v)
	if status != http.StatusOK || !v.Valid || len(v.Steps) != 10 || v.Sum != 6185 {
		t.Fatalf("validate = %d %s", status, body)
	}
}

func TestErrorsAreJSON(t *testing.T) {
	srv := newTestServer(t, "")
	for url, want := range map[string]int{
		srv.URL + "/api/nope":          http.StatusNotFound,
		srv.URL + "/api/moves/missing": http.StatusNotFound,
		srv.URL + "/api/blobs/not-a-digest": http.StatusBadRequest,
	} {
		status, body := call(t, srv.Client(), "GET", url, "")
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if status != want || json.Unmarshal(body, &e) != nil || e.Error.Code == "" {
			t.Errorf("GET %s = %d %s; want %d with a JSON error", url, status, body, want)
		}
	}
}

func TestStaticFallsBackToTheApp(t *testing.T) {
	srv := newTestServer(t, "")
	status, body := call(t, srv.Client(), "GET", srv.URL+"/queue", "")
	if status != http.StatusOK || !strings.Contains(string(body), "root") {
		t.Fatalf("GET /queue = %d %s", status, body)
	}
}

func TestAccessCodeProtectsTheAPI(t *testing.T) {
	srv := newTestServer(t, "1234")
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if status, _ := call(t, c, "GET", srv.URL+"/api/moves", ""); status != http.StatusUnauthorized {
		t.Fatalf("without code = %d; want 401", status)
	}
	if status, _ := call(t, c, "POST", srv.URL+"/api/login", `{"code":"0000"}`); status != http.StatusUnauthorized {
		t.Fatalf("wrong code = %d; want 401", status)
	}
	if status, _ := call(t, c, "POST", srv.URL+"/api/login", `{"code":"1234"}`); status != http.StatusOK {
		t.Fatalf("right code = %d; want 200", status)
	}
	if status, _ := call(t, c, "GET", srv.URL+"/api/moves", ""); status != http.StatusOK {
		t.Fatalf("after login = %d; want 200", status)
	}
	if status, _ := call(t, &http.Client{}, "GET", srv.URL+"/api/health", ""); status != http.StatusOK {
		t.Fatalf("health must stay open for the login screen, got %d", status)
	}
	if status, _ := call(t, &http.Client{}, "GET", srv.URL+"/", ""); status != http.StatusOK {
		t.Fatalf("the app shell must stay open for the login screen, got %d", status)
	}
}

func TestStreamAnnouncesMoveChanges(t *testing.T) {
	srv := newTestServer(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	go func() {
		r, err := srv.Client().Post(srv.URL+"/api/moves", "application/json", strings.NewReader(`{"lane":"1"}`))
		if err == nil {
			r.Body.Close()
		}
	}()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == "event: move" {
			return
		}
	}
	t.Fatal("no move event before timeout")
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/httpapi/`
Expected: FAIL to compile with `undefined: NewBroker`.

- [ ] **Step 3: Implement `internal/httpapi/broker.go`**

```go
package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Broker fans move-change notices out to every open screen.
type Broker struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan string]struct{}{}} }

// Subscribe returns a channel of changed move IDs and a function to stop.
func (b *Broker) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish announces that a move changed. It never blocks: a slow screen
// misses the notice and catches up on the next one.
func (b *Broker) Publish(moveID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- moveID:
		default:
		}
	}
}

// stream serves Server-Sent Events until the client goes away.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "E9001", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, stop := s.broker.Subscribe()
	defer stop()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case id := <-ch:
			fmt.Fprintf(w, "event: move\ndata: {\"id\":%q}\n\n", id)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 4: Implement `internal/httpapi/server.go`**

```go
// Package httpapi serves the hub's JSON API, live updates and the web app.
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/store"
)

// Options wires the server to the rest of the hub.
type Options struct {
	Service    *gate.Service
	Store      *store.Store
	Blobs      *blob.Store
	Static     fs.FS  // built web app; nil serves a "not built" notice
	AccessCode string // empty: no code required
	Version    string
}

// Server is the hub's HTTP front door.
type Server struct {
	opts   Options
	broker *Broker
	mux    *http.ServeMux
	token  string // cookie value proving the access code was entered
}

const cookieName = "depot_access"

// New builds the server. broker must be the one the gate service notifies.
func New(opts Options, broker *Broker) *Server {
	s := &Server{opts: opts, broker: broker, mux: http.NewServeMux()}
	if opts.AccessCode != "" {
		sum := sha256.Sum256([]byte("depot-kit-v1:" + opts.AccessCode))
		s.token = hex.EncodeToString(sum[:])
	}
	s.routes()
	return s
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler { return s.requireAccess(s.mux) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("POST /api/moves", s.createMove)
	s.mux.HandleFunc("GET /api/moves", s.listMoves)
	s.mux.HandleFunc("GET /api/moves/{id}", s.getMove)
	s.mux.HandleFunc("POST /api/moves/{id}/photos", s.addPhoto)
	s.mux.HandleFunc("POST /api/moves/{id}/resolve", s.resolve)
	s.mux.HandleFunc("POST /api/moves/{id}/reread", s.reread)
	s.mux.HandleFunc("GET /api/blobs/{digest}", s.getBlob)
	s.mux.HandleFunc("GET /api/validate/{number}", s.validate)
	s.mux.HandleFunc("POST /api/test-read", s.testRead)
	s.mux.HandleFunc("GET /api/stream", s.stream)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "E4040", "no such API endpoint")
	})
	s.mux.Handle("/", s.static())
}

// requireAccess enforces the optional access code on the API. The app shell,
// health and login stay open so the login screen can load.
func (s *Server) requireAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if s.token == "" || !strings.HasPrefix(p, "/api/") || p == "/api/login" || p == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "E5001", "access code required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// static serves the built web app; unknown paths get index.html because the
// app routes on the URL hash.
func (s *Server) static() http.Handler {
	if s.opts.Static == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("The web app is not built yet. Run: npm --prefix web run build, then rebuild depotd.\n"))
		})
	}
	files := s.opts.Static
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if _, err := fs.Stat(files, name); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r = r.Clone(r.Context())
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: message}})
}

// writeServiceError maps service errors onto HTTP answers.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gate.ErrNotFound), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "E4041", "move not found")
	case errors.Is(err, gate.ErrClosed):
		writeError(w, http.StatusConflict, "E4090", err.Error())
	case errors.Is(err, gate.ErrBadAction), errors.Is(err, gate.ErrNumberRequired), errors.Is(err, gate.ErrBadPhoto):
		writeError(w, http.StatusBadRequest, "E4000", err.Error())
	default:
		log.Printf("httpapi: %v", err)
		writeError(w, http.StatusInternalServerError, "E9000", "internal error; see the hub log")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "E4001", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
```

- [ ] **Step 5: Implement `internal/httpapi/handlers.go`**

```go
package httpapi

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/iso6346"
	"depot/internal/store"
)

const maxUpload = 60 << 20 // one original phone photo plus its AI copy

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	name, model := s.opts.Service.Engine()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": s.opts.Version, "engine": name, "model": model, "auth_required": s.token != "",
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if s.token == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Code), []byte(s.opts.AccessCode)) != 1 {
		writeError(w, http.StatusUnauthorized, "E5002", "wrong access code")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) createMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lane   string `json:"lane"`
		IsTest bool   `json:"is_test"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	lane := strings.TrimSpace(body.Lane)
	if lane == "" {
		lane = "1"
	}
	if len([]rune(lane)) > 32 {
		writeError(w, http.StatusBadRequest, "E4002", "lane name too long")
		return
	}
	m, err := s.opts.Service.NewMove(r.Context(), lane, body.IsTest, "clerk")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	s.broker.Publish(m.ID)
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) listMoves(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	moves, err := s.opts.Store.ListMoves(r.Context(), store.ListFilter{Status: q.Get("status"), Limit: limit, Before: q.Get("before")})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moves": moves})
}

type moveDetail struct {
	store.Move
	Attempts []store.Attempt `json:"attempts"`
}

func (s *Server) getMove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.opts.Store.GetMove(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	attempts, err := s.opts.Store.ListAttempts(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, moveDetail{Move: m, Attempts: attempts})
}

func (s *Server) addPhoto(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "E4003", "invalid upload: "+err.Error())
		return
	}
	original, originalType, err := formFile(r, "original")
	if err != nil {
		writeError(w, http.StatusBadRequest, "E4004", "missing original photo")
		return
	}
	ai, aiType, err := formFile(r, "ai")
	if err != nil {
		writeError(w, http.StatusBadRequest, "E4004", "missing AI copy of the photo")
		return
	}
	width, _ := strconv.Atoi(r.FormValue("width"))
	height, _ := strconv.Atoi(r.FormValue("height"))
	p, err := s.opts.Service.AddPhoto(r.Context(), gate.PhotoInput{
		MoveID: r.PathValue("id"), Role: r.FormValue("role"), Actor: "clerk",
		Original: original, OriginalType: originalType, AI: ai, AIType: aiType, Width: width, Height: height,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, p)
}

// formFile reads one uploaded file and sniffs its type from the bytes,
// never trusting the browser's label.
func formFile(r *http.Request, field string) ([]byte, string, error) {
	f, _, err := r.FormFile(field)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", err
	}
	return data, http.DetectContentType(data), nil
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	var body gate.Resolution
	if !decodeJSON(w, r, &body) {
		return
	}
	m, err := s.opts.Service.Resolve(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) reread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.opts.Service.Reread(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	m, err := s.opts.Store.GetMove(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, m)
}

func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	data, err := s.opts.Blobs.Get(r.PathValue("digest"))
	switch {
	case errors.Is(err, blob.ErrBadDigest):
		writeError(w, http.StatusBadRequest, "E4005", err.Error())
		return
	case errors.Is(err, blob.ErrNotFound):
		writeError(w, http.StatusNotFound, "E4042", err.Error())
		return
	case err != nil:
		writeServiceError(w, err)
		return
	}
	// Originals are stored exactly as uploaded. Only images are shown inline;
	// anything else downloads, so an uploaded file can never run as a page.
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		ct = "application/octet-stream"
		w.Header().Set("Content-Disposition", "attachment")
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, iso6346.Validate(r.PathValue("number")))
}

func (s *Server) testRead(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "E4003", "invalid upload: "+err.Error())
		return
	}
	ai, aiType, err := formFile(r, "ai")
	if err != nil {
		writeError(w, http.StatusBadRequest, "E4004", "missing photo")
		return
	}
	res, err := s.opts.Service.TestRead(r.Context(), ai, aiType)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS for all seven tests.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/
git commit -m "feat: hub HTTP API with live updates and optional access code" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: `depotd hub` command and embedded web app

**Files:**
- Create: `internal/webui/webui.go`
- Create: `internal/webui/dist/.gitkeep` (empty file)
- Modify: `cmd/depotd/main.go` (full replacement below; `export-dataset` and `bench` are wired in Tasks 15 and 16)

- [ ] **Step 1: Create the embed package**

Create the empty file `internal/webui/dist/.gitkeep`, then `internal/webui/webui.go`:

```go
// Package webui embeds the built web app (web/ → npm run build → dist/).
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built web app, or nil if it has not been built yet.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
```

- [ ] **Step 2: Replace `cmd/depotd/main.go`**

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/httpapi"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/webui"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	case "hub":
		err = runHub(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: depotd <command> [flags]

commands:
  version          print the version
  hub              run the gate hub (web app + API + reading) on this computer
  export-dataset   write verified photos and labels for model training
  bench            re-read verified moves with an engine and report accuracy

run "depotd <command> -h" for a command's flags`)
}

// dataFlags locate the hub's database and photos.
type dataFlags struct{ dir, tenant, depot string }

func (d *dataFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&d.dir, "data", "depot-data", "folder for the database and photos")
	fs.StringVar(&d.tenant, "tenant", "local", "company id (stays local until the cloud exists)")
	fs.StringVar(&d.depot, "depot", "own-depot", "depot id")
}

func (d *dataFlags) open() (*store.Store, *blob.Store, error) {
	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(filepath.Join(d.dir, "depot.db"), d.tenant, d.depot)
	if err != nil {
		return nil, nil, err
	}
	bl, err := blob.Open(filepath.Join(d.dir, "blobs"))
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, bl, nil
}

// engineFlags choose and configure the reading engine.
type engineFlags struct{ engine, model, thinking string }

func (e *engineFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&e.engine, "engine", "gemini", "reading engine: gemini | fake")
	fs.StringVar(&e.model, "model", envOr("DEPOT_GEMINI_MODEL", recognition.DefaultGeminiModel), "Gemini model id")
	fs.StringVar(&e.thinking, "thinking", envOr("DEPOT_GEMINI_THINKING", "low"), "Gemini thinking level (empty: model default)")
}

func (e *engineFlags) build() (recognition.Recognizer, error) {
	switch e.engine {
	case "fake":
		return recognition.NewDemoFake(), nil
	case "gemini":
		key := envOr("GEMINI_API_KEY", os.Getenv("GOOGLE_API_KEY"))
		if key == "" {
			return nil, errors.New("GEMINI_API_KEY is not set: set it in your own terminal " +
				"(setx GEMINI_API_KEY ...), open a new terminal, or run with -engine fake")
		}
		return recognition.NewGemini(recognition.GeminiConfig{
			APIKey: key, Model: e.model, ThinkingLevel: e.thinking, Retries: 2,
		}), nil
	default:
		return nil, fmt.Errorf("unknown engine %q (use gemini or fake)", e.engine)
	}
}

func runHub(args []string) error {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	var data dataFlags
	data.register(fs)
	var eng engineFlags
	eng.register(fs)
	addr := fs.String("addr", ":8080", "listen address")
	autoAccept := fs.Bool("auto-accept", false, "accept confident, check-digit-valid reads without a person")
	threshold := fs.Float64("threshold", 0.85, "confidence needed for auto-accept")
	accessCode := fs.String("access-code", os.Getenv("DEPOT_ACCESS_CODE"), "code people must enter to use the app (empty: none)")
	_ = fs.Parse(args)

	rec, err := eng.build()
	if err != nil {
		return err
	}
	st, bl, err := data.open()
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	broker := httpapi.NewBroker()
	svc := gate.New(st, bl, rec, gate.Config{AutoAcceptEnabled: *autoAccept, AutoAcceptThreshold: *threshold}, broker.Publish)
	if err := svc.Start(ctx); err != nil {
		return err
	}

	static := webui.Dist()
	if static == nil {
		log.Print("web app not built yet: run `npm --prefix web run build`, then rebuild depotd")
	}
	server := &http.Server{
		Addr: *addr,
		Handler: httpapi.New(httpapi.Options{
			Service: svc, Store: st, Blobs: bl, Static: static, AccessCode: *accessCode, Version: Version,
		}, broker).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Requests inherit ctx, so live-update streams end on Ctrl+C.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errc := make(chan error, 1)
	go func() { errc <- server.ListenAndServe() }()
	name, model := svc.Engine()
	log.Printf("depotd %s · engine %s (%s) · data %s · auto-accept %v", Version, name, model, data.dir, *autoAccept)
	for _, u := range lanURLs(*addr) {
		log.Printf("open: %s", u)
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	svc.Wait()
	return nil
}

// lanURLs lists the addresses phones on the same network can open.
func lanURLs(addr string) []string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	urls := []string{"http://localhost:" + port}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return urls
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		urls = append(urls, fmt.Sprintf("http://%s:%s", ipnet.IP, port))
	}
	return urls
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 3: Build and run with the fake engine**

Run: `go build -o bin/depotd.exe ./cmd/depotd`
Expected: no output, `bin/depotd.exe` exists.

Run (in a second terminal, leave it running): `bin/depotd.exe hub -engine fake -data depot-data`
Expected log lines include `engine fake (fake-1)`, `web app not built yet`, and `open: http://192.168.x.x:8080`.

Run: `curl.exe -s http://localhost:8080/api/health`
Expected: `{"auth_required":false,"engine":"fake","model":"fake-1","ok":true,"version":"dev"}`

Stop the hub with Ctrl+C. Expected: `shutting down`, then exit.

- [ ] **Step 4: Run every Go test**

Run: `go vet ./...`
Expected: no output.

Run: `go test ./...`
Expected: all packages `ok`.

- [ ] **Step 5: Commit**

```bash
git add cmd/depotd/main.go internal/webui/
git commit -m "feat: depotd hub command serving API, live updates and embedded app" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: Web app scaffold and shared modules

**Files:**
- Create: `web/go.mod`
- Create: `web/package.json` (dependencies added by npm)
- Create: `web/tsconfig.json`
- Create: `web/vite.config.ts`
- Create: `web/index.html`
- Create: `web/src/api.ts`, `web/src/i18n.ts`, `web/src/storage.ts`, `web/src/live.ts`, `web/src/image.ts`, `web/src/components.tsx`, `web/src/styles.css`

The app is plain React + TypeScript with hash routing (`#/capture`, `#/queue`, `#/moves/<id>`, `#/test`), no router or state library. All text goes through `i18n.ts` (Turkish default, English toggle); the English dictionary is typed against the Turkish one, so a missing translation is a compile error.

Phone constraints the code respects: the app is served over plain `http://` on the LAN, which is not a "secure context". So it uses `<input type="file" accept="image/*" capture="environment">` (opens the phone camera at full resolution; works without HTTPS) instead of `getUserMedia`, and does not use `crypto.randomUUID`.

- [ ] **Step 1: Keep `node_modules` out of the Go build**

`web/go.mod` (a nested module makes `go build ./...` and `go test ./...` skip everything under `web/`):

```
module depotweb

go 1.24
```

- [ ] **Step 2: Write `web/package.json`**

```json
{
  "name": "depot-web",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "typecheck": "tsc --noEmit",
    "build": "tsc --noEmit && vite build && node -e \"require('fs').writeFileSync('../internal/webui/dist/.gitkeep', '')\""
  }
}
```

(The last part of `build` restores `.gitkeep`, which Vite deletes when it empties the output folder.)

- [ ] **Step 3: Install dependencies**

Run: `npm --prefix web install react react-dom`
Run: `npm --prefix web install --save-dev vite @vitejs/plugin-react typescript @types/react @types/react-dom`
Expected: both finish without errors; `web/package.json` now lists them and `web/package-lock.json` exists.

- [ ] **Step 4: Write `web/tsconfig.json`**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "isolatedModules": true,
    "skipLibCheck": true,
    "noEmit": true,
    "types": ["vite/client"]
  },
  "include": ["src", "vite.config.ts"]
}
```

- [ ] **Step 5: Write `web/vite.config.ts`**

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build lands inside the Go module so depotd can embed it.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "../internal/webui/dist", emptyOutDir: true },
  server: { proxy: { "/api": "http://localhost:8080" } },
});
```

- [ ] **Step 6: Write `web/index.html`**

```html
<!doctype html>
<html lang="tr">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Depot Gate</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

- [ ] **Step 7: Write `web/src/api.ts`**

```ts
// Typed client for the hub API. Field names match the Go JSON tags.

export type Status = "capturing" | "reading" | "needs_review" | "accepted" | "refused";

export interface Photo {
  id: string;
  move_id: string;
  role: string;
  sha256: string;
  ai_sha256: string;
  media_type: string;
  ai_media_type: string;
  bytes: number;
  width: number;
  height: number;
  captured_at: string;
}

export interface Candidate {
  number: string;
  confidence: number;
  check_digit_valid: boolean;
  repaired_from?: string;
}

export interface Frame {
  role: string;
  candidates: Candidate[] | null;
  type_code: string;
  color: string;
  visible: string;
  notes: string;
  error?: string;
}

export interface Result {
  number: string;
  confidence: number;
  check_digit_valid: boolean;
  decision: string;
  reason: string;
  type_code: string;
  color: string;
  candidates: Candidate[] | null;
  frames: Frame[] | null;
}

export interface Move {
  id: string;
  lane: string;
  direction: string;
  status: Status;
  number: string;
  type_code: string;
  color: string;
  confidence: number;
  check_digit_valid: boolean;
  decision: string;
  reason: string;
  result: Partial<Result>;
  verified: boolean;
  corrected_from: string;
  resolved_by: string;
  note: string;
  is_test: boolean;
  created_at: string;
  updated_at: string;
  photos: Photo[] | null;
}

export interface Box {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
}

export interface Reading {
  candidates: { number: string; confidence: number }[] | null;
  type_code: string;
  color: string;
  visible: string;
  box: Box | null;
  notes: string;
}

export interface Attempt {
  id: string;
  move_id: string;
  photo_id: string;
  engine: string;
  model: string;
  prompt_version: string;
  reading: Partial<Reading>;
  raw: string;
  error: string;
  input_tokens: number;
  output_tokens: number;
  latency_ms: number;
  created_at: string;
}

export interface MoveDetail extends Move {
  attempts: Attempt[] | null;
}

export interface Step {
  char: string;
  value: number;
  weight: number;
  product: number;
}

export interface Validation {
  input: string;
  normalized: string;
  well_formed: boolean;
  check_digit_valid: boolean;
  expected_check_digit: number | null;
  valid: boolean;
  steps?: Step[];
  sum: number;
  remainder: number;
}

export interface TestReadResult {
  attempt: {
    engine: string;
    model: string;
    prompt_version: string;
    reading: Partial<Reading>;
    raw: string;
    usage: { input_tokens: number; output_tokens: number };
    latency_ms: number;
  };
  frame: Frame;
  error?: string;
}

export interface Health {
  ok: boolean;
  version: string;
  engine: string;
  model: string;
  auth_required: boolean;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: "same-origin", ...init });
  if (!res.ok) {
    let code = "E0000";
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: { code?: string; message?: string } };
      code = body.error?.code ?? code;
      message = body.error?.message ?? message;
    } catch {
      // not a JSON error body
    }
    throw new ApiError(res.status, code, message);
  }
  return (await res.json()) as T;
}

function postJSON(body: unknown): RequestInit {
  return { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

export const blobUrl = (digest: string) => `/api/blobs/${digest}`;

export const api = {
  health: () => call<Health>("/api/health"),
  login: (code: string) => call<{ ok: boolean }>("/api/login", postJSON({ code })),
  createMove: (lane: string, isTest: boolean) => call<Move>("/api/moves", postJSON({ lane, is_test: isTest })),
  listMoves: (status: string) =>
    call<{ moves: Move[] }>(`/api/moves?status=${encodeURIComponent(status)}&limit=100`),
  getMove: (id: string) => call<MoveDetail>(`/api/moves/${encodeURIComponent(id)}`),
  uploadPhoto: (id: string, role: string, original: Blob, ai: Blob, width: number, height: number) => {
    const form = new FormData();
    form.append("role", role);
    form.append("width", String(width));
    form.append("height", String(height));
    form.append("original", original, "original");
    form.append("ai", ai, "ai.jpg");
    return call<Photo>(`/api/moves/${encodeURIComponent(id)}/photos`, { method: "POST", body: form });
  },
  resolve: (
    id: string,
    body: { action: "accept" | "correct" | "refuse"; number?: string; by: string; note?: string },
  ) => call<Move>(`/api/moves/${encodeURIComponent(id)}/resolve`, postJSON(body)),
  reread: (id: string) => call<Move>(`/api/moves/${encodeURIComponent(id)}/reread`, { method: "POST" }),
  validate: (number: string) => call<Validation>(`/api/validate/${encodeURIComponent(number)}`),
  testRead: (ai: Blob) => {
    const form = new FormData();
    form.append("ai", ai, "ai.jpg");
    return call<TestReadResult>("/api/test-read", { method: "POST", body: form });
  },
};
```

- [ ] **Step 8: Write `web/src/i18n.ts`**

```ts
import { useSyncExternalStore } from "react";
import { readLocal, writeLocal } from "./storage";

const tr = {
  appTitle: "Kapı OCR",
  navCapture: "Yeni giriş",
  navQueue: "Kuyruk",
  navTest: "Test okuma",
  homeCaptureHint: "Telefonla konteyner fotoğrafı çek",
  homeQueueHint: "Okunan numaraları kontrol et ve onayla",
  homeTestHint: "Tek fotoğrafla okuma motorunu dene",
  lane: "Şerit",
  practice: "Deneme kaydı (eğitim verisine girmez)",
  startMove: "Yeni kamyon başlat",
  newTruck: "Yeni kamyon",
  shotRear: "Arka kapı",
  shotLeft: "Sol yan",
  shotRight: "Sağ yan",
  shotFront: "Ön",
  shotOther: "Diğer",
  uploading: "Yükleniyor…",
  uploaded: "Yüklendi",
  uploadFailed: "Yükleme başarısız",
  retry: "Tekrar dene",
  tooDark: "Fotoğraf çok karanlık",
  tooBlurry: "Fotoğraf bulanık olabilir",
  retakeHint: "gerekirse tekrar çek",
  status_capturing: "Fotoğraf bekleniyor",
  status_reading: "Okunuyor…",
  status_needs_review: "Kontrol gerekli",
  status_accepted: "Onaylandı",
  status_refused: "Reddedildi",
  number: "Konteyner no",
  typeCode: "ISO tip kodu",
  color: "Renk",
  confidence: "Güven",
  reason: "Sebep",
  candidates: "Adaylar",
  repairedFrom: "düzeltme önerisi, okunan:",
  openMove: "Ayrıntıyı aç",
  photos: "Fotoğraflar",
  original: "Orijinal",
  accept: "Onayla",
  correct: "Düzelt ve onayla",
  refuse: "Reddet",
  yourName: "Adınız",
  note: "Not",
  validNumber: "Check digit doğru",
  invalidNumber: "Check digit hatalı",
  checkDigitMath: "Check digit hesabı",
  sum: "Toplam",
  remainder: "Kalan (mod 11)",
  expected: "Beklenen check digit",
  reread: "Tekrar oku",
  verified: "Doğrulandı",
  unverified: "Doğrulanmadı (check digit hatalı)",
  correctedFrom: "Düzeltilen okuma:",
  resolvedBy: "Karar veren:",
  filterNeedsReview: "Kontrol gereken",
  filterAll: "Tümü",
  filterAccepted: "Onaylanan",
  filterRefused: "Reddedilen",
  noMoves: "Kayıt yok",
  choosePhoto: "Fotoğraf çek veya seç",
  engine: "Motor",
  latency: "Süre",
  tokens: "Token (giriş / çıkış)",
  rawAnswer: "Ham cevap",
  error: "Hata",
  accessCode: "Erişim kodu",
  login: "Giriş",
  wrongCode: "Kod hatalı",
  testTag: "deneme",
  noNumber: "Numara okunamadı",
  back: "Geri",
  loading: "Yükleniyor…",
  quality: "Kalite",
};

export type Key = keyof typeof tr;

const en: Record<Key, string> = {
  appTitle: "Gate OCR",
  navCapture: "New gate-in",
  navQueue: "Queue",
  navTest: "Test read",
  homeCaptureHint: "Photograph a container with the phone",
  homeQueueHint: "Check and confirm read numbers",
  homeTestHint: "Try the reading engine on one photo",
  lane: "Lane",
  practice: "Practice record (kept out of training data)",
  startMove: "Start new truck",
  newTruck: "New truck",
  shotRear: "Rear doors",
  shotLeft: "Left side",
  shotRight: "Right side",
  shotFront: "Front",
  shotOther: "Other",
  uploading: "Uploading…",
  uploaded: "Uploaded",
  uploadFailed: "Upload failed",
  retry: "Retry",
  tooDark: "Photo is too dark",
  tooBlurry: "Photo may be blurry",
  retakeHint: "retake if needed",
  status_capturing: "Waiting for photos",
  status_reading: "Reading…",
  status_needs_review: "Needs review",
  status_accepted: "Accepted",
  status_refused: "Refused",
  number: "Container no",
  typeCode: "ISO type code",
  color: "Color",
  confidence: "Confidence",
  reason: "Reason",
  candidates: "Candidates",
  repairedFrom: "suggested fix, read as:",
  openMove: "Open details",
  photos: "Photos",
  original: "Original",
  accept: "Accept",
  correct: "Correct and accept",
  refuse: "Refuse",
  yourName: "Your name",
  note: "Note",
  validNumber: "Check digit valid",
  invalidNumber: "Check digit invalid",
  checkDigitMath: "Check digit math",
  sum: "Sum",
  remainder: "Remainder (mod 11)",
  expected: "Expected check digit",
  reread: "Read again",
  verified: "Verified",
  unverified: "Not verified (check digit fails)",
  correctedFrom: "Corrected from:",
  resolvedBy: "Decided by:",
  filterNeedsReview: "Needs review",
  filterAll: "All",
  filterAccepted: "Accepted",
  filterRefused: "Refused",
  noMoves: "Nothing here",
  choosePhoto: "Take or choose a photo",
  engine: "Engine",
  latency: "Time",
  tokens: "Tokens (in / out)",
  rawAnswer: "Raw answer",
  error: "Error",
  accessCode: "Access code",
  login: "Log in",
  wrongCode: "Wrong code",
  testTag: "practice",
  noNumber: "No number read",
  back: "Back",
  loading: "Loading…",
  quality: "Quality",
};

export type Lang = "tr" | "en";

const dictionaries: Record<Lang, Record<Key, string>> = { tr, en };
const listeners = new Set<() => void>();
let current: Lang = readLocal("lang") === "en" ? "en" : "tr";

function setLang(lang: Lang) {
  current = lang;
  writeLocal("lang", lang);
  document.documentElement.lang = lang;
  listeners.forEach((l) => l());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useT() {
  const lang = useSyncExternalStore(subscribe, () => current);
  const t = (key: Key) => dictionaries[lang][key];
  return { t, lang, setLang };
}
```

- [ ] **Step 9: Write `web/src/storage.ts`**

```ts
// Browser storage can be missing or blocked (private mode, strict settings);
// it only holds conveniences, so failures are ignored.

export function readLocal(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeLocal(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // storage unavailable
  }
}

let counter = 0;

// localId makes keys for list items. crypto.randomUUID needs HTTPS, which the
// LAN app does not have.
export function localId(): string {
  counter += 1;
  return `${Date.now().toString(36)}-${counter}`;
}
```

- [ ] **Step 10: Write `web/src/live.ts`**

```ts
import { useEffect, useRef } from "react";

// useMoveEvents calls onMove with a move ID whenever the hub reports a change.
// EventSource reconnects by itself after network drops.
export function useMoveEvents(onMove: (id: string) => void) {
  const handler = useRef(onMove);
  useEffect(() => {
    handler.current = onMove;
  });
  useEffect(() => {
    const source = new EventSource("/api/stream");
    const listener = (e: MessageEvent<string>) => {
      try {
        const data = JSON.parse(e.data) as { id?: string };
        if (data.id) handler.current(data.id);
      } catch {
        // ignore malformed events
      }
    };
    source.addEventListener("move", listener);
    return () => {
      source.removeEventListener("move", listener);
      source.close();
    };
  }, []);
}
```

- [ ] **Step 11: Write `web/src/image.ts`**

```ts
export interface Quality {
  brightness: number;
  sharpness: number;
  tooDark: boolean;
  tooBlurry: boolean;
}

export interface PreparedPhoto {
  ai: Blob;
  width: number;
  height: number;
  quality: Quality;
}

// Long edge of the copy sent to the engine: plenty for painted numbers, small
// enough to upload fast and keep cost down. Tune with `depotd bench`.
const MAX_EDGE = 1568;

// Starting thresholds; tune them with real gate photos.
const DARK_BELOW = 45;
const BLURRY_BELOW = 60;

// preparePhoto makes the engine copy: EXIF orientation applied, downscaled,
// JPEG. The original file is uploaded untouched beside it.
export async function preparePhoto(file: Blob): Promise<PreparedPhoto> {
  const bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
  const width = bitmap.width;
  const height = bitmap.height;
  const scale = Math.min(1, MAX_EDGE / Math.max(width, height));
  const w = Math.max(1, Math.round(width * scale));
  const h = Math.max(1, Math.round(height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d", { willReadFrequently: true });
  if (!ctx) throw new Error("canvas is not available");
  ctx.drawImage(bitmap, 0, 0, w, h);
  bitmap.close();
  const quality = measure(ctx, w, h);
  const ai = await new Promise<Blob>((resolve, reject) =>
    canvas.toBlob((b) => (b ? resolve(b) : reject(new Error("could not encode photo"))), "image/jpeg", 0.85),
  );
  return { ai, width, height, quality };
}

// measure estimates brightness (mean luma, 0-255) and sharpness (variance of
// the Laplacian) on a half-resolution grayscale copy.
function measure(ctx: CanvasRenderingContext2D, w: number, h: number): Quality {
  const step = 2;
  const sw = Math.floor(w / step);
  const sh = Math.floor(h / step);
  const data = ctx.getImageData(0, 0, w, h).data;
  const gray = new Float32Array(sw * sh);
  let sum = 0;
  for (let y = 0; y < sh; y++) {
    for (let x = 0; x < sw; x++) {
      const i = (y * step * w + x * step) * 4;
      const g = 0.299 * data[i] + 0.587 * data[i + 1] + 0.114 * data[i + 2];
      gray[y * sw + x] = g;
      sum += g;
    }
  }
  const brightness = gray.length ? sum / gray.length : 0;
  let lapSum = 0;
  let lapSq = 0;
  let n = 0;
  for (let y = 1; y < sh - 1; y++) {
    for (let x = 1; x < sw - 1; x++) {
      const c = y * sw + x;
      const lap = gray[c - sw] + gray[c + sw] + gray[c - 1] + gray[c + 1] - 4 * gray[c];
      lapSum += lap;
      lapSq += lap * lap;
      n++;
    }
  }
  const mean = n ? lapSum / n : 0;
  const sharpness = n ? lapSq / n - mean * mean : 0;
  return { brightness, sharpness, tooDark: brightness < DARK_BELOW, tooBlurry: sharpness < BLURRY_BELOW };
}
```

- [ ] **Step 12: Write `web/src/components.tsx`**

```tsx
import { useEffect, useState } from "react";
import { api, type Box, type Candidate, type Status, type Validation } from "./api";
import { useT, type Key } from "./i18n";

export function StatusChip({ status }: { status: Status }) {
  const { t } = useT();
  return <span className={`chip chip-${status}`}>{t(`status_${status}` as Key)}</span>;
}

export function CandidateList({
  candidates,
  onPick,
}: {
  candidates: Candidate[] | null;
  onPick?: (number: string) => void;
}) {
  const { t } = useT();
  if (!candidates || candidates.length === 0) return null;
  return (
    <div>
      <h4>{t("candidates")}</h4>
      <ul className="candidates">
        {candidates.map((c) => (
          <li key={c.number}>
            <button
              type="button"
              className={c.check_digit_valid ? "cand ok" : "cand bad"}
              onClick={() => onPick?.(c.number)}
              disabled={!onPick}
            >
              <span className="mono">{c.number}</span> {Math.round(c.confidence * 100)}%
              {c.repaired_from && (
                <small>
                  {" "}
                  ({t("repairedFrom")} {c.repaired_from})
                </small>
              )}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

// PhotoWithBox draws the engine's number box over the engine copy of a photo.
// Boxes are relative to that copy, which has EXIF orientation applied.
export function PhotoWithBox({ src, box, alt }: { src: string; box: Box | null | undefined; alt: string }) {
  return (
    <div className="photo">
      <img src={src} alt={alt} loading="lazy" />
      {box && (
        <div
          className="box"
          style={{
            left: `${box.x0 * 100}%`,
            top: `${box.y0 * 100}%`,
            width: `${(box.x1 - box.x0) * 100}%`,
            height: `${(box.y1 - box.y0) * 100}%`,
          }}
        />
      )}
    </div>
  );
}

// CheckDigitMath validates a number on the hub and shows the arithmetic.
export function CheckDigitMath({ number }: { number: string }) {
  const { t } = useT();
  const [v, setV] = useState<Validation | null>(null);
  useEffect(() => {
    if (!number.trim()) {
      setV(null);
      return;
    }
    let live = true;
    const timer = window.setTimeout(() => {
      api
        .validate(number)
        .then((res) => {
          if (live) setV(res);
        })
        .catch(() => {
          if (live) setV(null);
        });
    }, 250);
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [number]);
  if (!v) return null;
  return (
    <div className={v.valid ? "math ok" : "math bad"}>
      <strong>{v.valid ? t("validNumber") : t("invalidNumber")}</strong>
      {v.steps && (
        <details>
          <summary>{t("checkDigitMath")}</summary>
          <table>
            <tbody>
              {v.steps.map((s, i) => (
                <tr key={i}>
                  <td className="mono">{s.char}</td>
                  <td>{s.value}</td>
                  <td>× {s.weight}</td>
                  <td>= {s.product}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p>
            {t("sum")}: {v.sum} · {t("remainder")}: {v.remainder} · {t("expected")}: {v.expected_check_digit}
          </p>
        </details>
      )}
    </div>
  );
}
```

- [ ] **Step 13: Write `web/src/styles.css`**

```css
:root {
  --bg: #f4f5f7;
  --card: #ffffff;
  --text: #1d2330;
  --muted: #6b7280;
  --ok: #0f7b3f;
  --bad: #b42318;
  --warn: #b54708;
  --accent: #1d4ed8;
  --border: #d0d5dd;
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  color: var(--text);
  background: var(--bg);
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #111418;
    --card: #1b2028;
    --text: #e6e8eb;
    --muted: #9aa3af;
    --border: #333a45;
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); }
header { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; padding: 10px 16px; background: var(--card); border-bottom: 1px solid var(--border); position: sticky; top: 0; z-index: 2; }
.brand { font-weight: 700; color: var(--text); text-decoration: none; }
nav { display: flex; gap: 12px; flex: 1; flex-wrap: wrap; }
nav a { color: var(--accent); text-decoration: none; }
main { padding: 16px; max-width: 1100px; margin: 0 auto; }
.card { background: var(--card); border: 1px solid var(--border); border-radius: 10px; padding: 16px; margin-bottom: 16px; }
.narrow { max-width: 360px; margin: 40px auto; display: grid; gap: 12px; }
label { display: grid; gap: 4px; margin: 8px 0; }
label.check { display: flex; align-items: center; gap: 8px; }
input { font: inherit; padding: 10px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg); color: var(--text); }
button, .button { font: inherit; padding: 10px 16px; border-radius: 8px; border: 1px solid var(--border); background: var(--card); color: var(--text); cursor: pointer; text-decoration: none; display: inline-block; }
button.primary { background: var(--accent); color: #fff; border-color: var(--accent); }
button.danger { color: var(--bad); border-color: var(--bad); }
button.big { width: 100%; padding: 18px; font-size: 1.2rem; margin-top: 12px; }
button:disabled { opacity: 0.5; cursor: default; }
.row { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 12px; }
.mono { font-family: ui-monospace, "Cascadia Mono", Consolas, monospace; letter-spacing: 0.04em; }
.big-number { font-size: 2.2rem; font-weight: 700; margin: 8px 0; word-break: break-all; }
.big-number.ok { color: var(--ok); }
.big-number.bad { color: var(--bad); }
.big-input { font-size: 1.5rem; }
.muted { color: var(--muted); }
.small { font-size: 0.85rem; }
.strong { font-weight: 700; }
.error { color: var(--bad); }
.warn { color: var(--warn); display: block; }
.chip { display: inline-block; padding: 2px 8px; border-radius: 999px; font-size: 0.8rem; background: var(--border); color: var(--text); }
.chip-needs_review { background: #fef0c7; color: #93370d; }
.chip-accepted { background: #d1fadf; color: #05603a; }
.chip-refused { background: #fee4e2; color: #912018; }
.chip-reading { background: #dbeafe; color: #1e40af; }
.shots { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 10px; margin-bottom: 16px; }
.shot-button { display: flex; align-items: center; justify-content: center; min-height: 72px; background: var(--accent); color: #fff; border-radius: 10px; font-weight: 600; cursor: pointer; text-align: center; padding: 8px; }
.thumbs { display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr)); gap: 8px; }
.thumb img { width: 100%; aspect-ratio: 4 / 3; object-fit: cover; border-radius: 6px; }
.thumb.failed { outline: 2px solid var(--bad); }
.candidates { list-style: none; padding: 0; display: flex; gap: 6px; flex-wrap: wrap; }
.cand.ok { border-color: var(--ok); }
.cand.bad { border-color: var(--bad); }
.math { margin: 8px 0; }
.math.ok strong { color: var(--ok); }
.math.bad strong { color: var(--bad); }
.math td { padding: 0 8px; }
.tabs { display: flex; gap: 6px; margin-bottom: 12px; flex-wrap: wrap; }
.tabs .active { background: var(--accent); color: #fff; }
.queue { list-style: none; padding: 0; display: grid; gap: 8px; }
.queue-row { display: flex; gap: 12px; align-items: center; background: var(--card); border: 1px solid var(--border); border-radius: 10px; padding: 8px; color: var(--text); text-decoration: none; }
.queue-row img, .noimg { width: 96px; height: 72px; object-fit: cover; border-radius: 6px; background: var(--border); flex: none; }
.photo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.photo { position: relative; }
.photo img { width: 100%; display: block; border-radius: 6px; }
.box { position: absolute; border: 3px solid #f79009; border-radius: 4px; pointer-events: none; }
.tiles { display: grid; gap: 12px; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); }
.tile { display: grid; gap: 6px; padding: 24px; background: var(--card); border: 1px solid var(--border); border-radius: 12px; text-decoration: none; color: var(--text); }
.tile b { font-size: 1.3rem; }
figure { margin: 0; }
pre { white-space: pre-wrap; word-break: break-all; font-size: 0.8rem; }
```

- [ ] **Step 14: Typecheck**

Run: `npm --prefix web run typecheck`
Expected: exits 0 with no errors.

- [ ] **Step 15: Commit**

```bash
git add web/go.mod web/package.json web/package-lock.json web/tsconfig.json web/vite.config.ts web/index.html web/src/
git commit -m "feat(web): scaffold, API client, translations, photo preparation" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14: Web pages (capture, queue, move detail, test read)

**Files:**
- Create: `web/src/main.tsx`, `web/src/App.tsx`
- Create: `web/src/pages/Home.tsx`, `web/src/pages/Login.tsx`, `web/src/pages/Capture.tsx`, `web/src/pages/Queue.tsx`, `web/src/pages/MoveDetail.tsx`, `web/src/pages/TestRead.tsx`

Behavior:
- **Capture (phone):** pick a lane, tick "practice" for test records, start a truck. Five big buttons (rear, left, right, front, other) each open the camera. Each photo is prepared (orientation, downscale, brightness/blur check), then the original and the engine copy upload together. A failed upload keeps the photo on screen with a Retry button. The result card updates live (Server-Sent Events, plus a 3-second poll while `reading` as a safety net).
- **Queue (office):** tabs needs-review / all / accepted / refused, live-refreshing, thumbnail + number + status + reason.
- **Move detail:** all photos with the engine's number box drawn on them and each photo's own reading; the combined result; candidate buttons that fill the number field; live check-digit math as you type; one button that says **Accept** when the field equals the read number and **Correct and accept** when it was edited (Enter submits); Refuse; Read again; your name is remembered on this device.
- **Test read:** one photo → engine answer, candidates, check-digit math, type code, color, latency, tokens, raw answer, box overlay. Stores nothing.

- [ ] **Step 1: Write `web/src/main.tsx`**

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
```

- [ ] **Step 2: Write `web/src/App.tsx`**

```tsx
import { useEffect, useState, type ReactNode } from "react";
import { api, ApiError } from "./api";
import { useT } from "./i18n";
import { Capture } from "./pages/Capture";
import { Home } from "./pages/Home";
import { Login } from "./pages/Login";
import { MoveDetail } from "./pages/MoveDetail";
import { Queue } from "./pages/Queue";
import { TestRead } from "./pages/TestRead";

function useHash(): string {
  const [hash, setHash] = useState(() => window.location.hash || "#/");
  useEffect(() => {
    const onChange = () => setHash(window.location.hash || "#/");
    window.addEventListener("hashchange", onChange);
    return () => window.removeEventListener("hashchange", onChange);
  }, []);
  return hash;
}

export function App() {
  const hash = useHash();
  const { t, lang, setLang } = useT();
  const [needLogin, setNeedLogin] = useState(false);

  useEffect(() => {
    api.listMoves("needs_review").catch((e: unknown) => {
      if (e instanceof ApiError && e.status === 401) setNeedLogin(true);
    });
  }, []);

  const [, route = "", id = ""] = hash.split("/");
  let page: ReactNode;
  if (needLogin) page = <Login onDone={() => setNeedLogin(false)} />;
  else if (route === "capture") page = <Capture />;
  else if (route === "queue") page = <Queue />;
  else if (route === "moves") page = <MoveDetail key={id} id={id} />;
  else if (route === "test") page = <TestRead />;
  else page = <Home />;

  return (
    <div>
      <header>
        <a href="#/" className="brand">
          {t("appTitle")}
        </a>
        <nav>
          <a href="#/capture">{t("navCapture")}</a>
          <a href="#/queue">{t("navQueue")}</a>
          <a href="#/test">{t("navTest")}</a>
        </nav>
        <button type="button" onClick={() => setLang(lang === "tr" ? "en" : "tr")}>
          {lang === "tr" ? "EN" : "TR"}
        </button>
      </header>
      <main>{page}</main>
    </div>
  );
}
```

- [ ] **Step 3: Write `web/src/pages/Home.tsx`**

```tsx
import { useT } from "../i18n";

export function Home() {
  const { t } = useT();
  return (
    <div className="tiles">
      <a className="tile" href="#/capture">
        <b>{t("navCapture")}</b>
        <span>{t("homeCaptureHint")}</span>
      </a>
      <a className="tile" href="#/queue">
        <b>{t("navQueue")}</b>
        <span>{t("homeQueueHint")}</span>
      </a>
      <a className="tile" href="#/test">
        <b>{t("navTest")}</b>
        <span>{t("homeTestHint")}</span>
      </a>
    </div>
  );
}
```

- [ ] **Step 4: Write `web/src/pages/Login.tsx`**

```tsx
import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useT } from "../i18n";

export function Login({ onDone }: { onDone: () => void }) {
  const { t } = useT();
  const [code, setCode] = useState("");
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    try {
      await api.login(code);
      onDone();
    } catch {
      setError(t("wrongCode"));
    }
  }

  return (
    <form className="card narrow" onSubmit={submit}>
      <h2>{t("accessCode")}</h2>
      <input type="password" value={code} onChange={(e) => setCode(e.target.value)} autoFocus />
      <button type="submit" className="primary">
        {t("login")}
      </button>
      {error && <p className="error">{error}</p>}
    </form>
  );
}
```

- [ ] **Step 5: Write `web/src/pages/Capture.tsx`**

```tsx
import { useCallback, useEffect, useState } from "react";
import { api, type MoveDetail } from "../api";
import { CandidateList, StatusChip } from "../components";
import { useT, type Key } from "../i18n";
import { preparePhoto, type Quality } from "../image";
import { useMoveEvents } from "../live";
import { localId, readLocal, writeLocal } from "../storage";

const SHOTS: { role: string; label: Key }[] = [
  { role: "rear", label: "shotRear" },
  { role: "left", label: "shotLeft" },
  { role: "right", label: "shotRight" },
  { role: "front", label: "shotFront" },
  { role: "other", label: "shotOther" },
];

interface Shot {
  key: string;
  role: string;
  file: File;
  preview: string;
  status: "uploading" | "done" | "failed";
  quality?: Quality;
  error?: string;
}

export function Capture() {
  const { t } = useT();
  const [lane, setLane] = useState(() => readLocal("lane") ?? "1");
  const [practice, setPractice] = useState(false);
  const [move, setMove] = useState<MoveDetail | null>(null);
  const [shots, setShots] = useState<Shot[]>([]);
  const [error, setError] = useState("");

  const refresh = useCallback(async (id: string) => {
    try {
      setMove(await api.getMove(id));
    } catch (e) {
      setError(String(e));
    }
  }, []);

  useMoveEvents((id) => {
    if (move && id === move.id) void refresh(id);
  });

  // Safety net if live updates are blocked on this network.
  useEffect(() => {
    if (!move || move.status !== "reading") return;
    const timer = window.setInterval(() => void refresh(move.id), 3000);
    return () => window.clearInterval(timer);
  }, [move, refresh]);

  async function start() {
    setError("");
    try {
      writeLocal("lane", lane);
      const created = await api.createMove(lane, practice);
      shots.forEach((s) => URL.revokeObjectURL(s.preview));
      setShots([]);
      await refresh(created.id);
    } catch (e) {
      setError(String(e));
    }
  }

  function patch(key: string, change: Partial<Shot>) {
    setShots((list) => list.map((s) => (s.key === key ? { ...s, ...change } : s)));
  }

  async function upload(moveId: string, shot: Shot) {
    patch(shot.key, { status: "uploading", error: undefined });
    try {
      const prepared = await preparePhoto(shot.file);
      patch(shot.key, { quality: prepared.quality });
      await api.uploadPhoto(moveId, shot.role, shot.file, prepared.ai, prepared.width, prepared.height);
      patch(shot.key, { status: "done" });
    } catch (e) {
      patch(shot.key, { status: "failed", error: String(e) });
    }
  }

  function onFile(role: string, files: FileList | null) {
    const file = files?.[0];
    if (!move || !file) return;
    const shot: Shot = { key: localId(), role, file, preview: URL.createObjectURL(file), status: "uploading" };
    setShots((list) => [...list, shot]);
    void upload(move.id, shot);
  }

  if (!move) {
    return (
      <section className="card">
        <h2>{t("navCapture")}</h2>
        <label>
          {t("lane")}
          <input value={lane} onChange={(e) => setLane(e.target.value)} inputMode="numeric" />
        </label>
        <label className="check">
          <input type="checkbox" checked={practice} onChange={(e) => setPractice(e.target.checked)} />
          {t("practice")}
        </label>
        <button type="button" className="primary big" onClick={() => void start()}>
          {t("startMove")}
        </button>
        {error && <p className="error">{error}</p>}
      </section>
    );
  }

  const open = move.resolved_by === "";
  const numberClass = move.check_digit_valid ? "ok" : move.number ? "bad" : "";
  return (
    <section>
      <div className="card">
        <StatusChip status={move.status} /> {move.is_test && <span className="chip">{t("testTag")}</span>}
        <div className={`big-number mono ${numberClass}`}>{move.number || t("noNumber")}</div>
        <p>
          {t("typeCode")}: <b>{move.type_code || "–"}</b> · {t("color")}: <b>{move.color || "–"}</b>
        </p>
        {move.reason && (
          <p className="muted">
            {t("reason")}: {move.reason}
          </p>
        )}
        <CandidateList candidates={move.result.candidates ?? null} />
        <div className="row">
          <a className="button" href={`#/moves/${move.id}`}>
            {t("openMove")}
          </a>
          <button type="button" onClick={() => void start()}>
            {t("newTruck")}
          </button>
        </div>
      </div>
      {open && (
        <div className="shots">
          {SHOTS.map((s) => (
            <label key={s.role} className="shot-button">
              {t(s.label)}
              <input
                type="file"
                accept="image/*"
                capture="environment"
                hidden
                onChange={(e) => {
                  onFile(s.role, e.target.files);
                  e.target.value = "";
                }}
              />
            </label>
          ))}
        </div>
      )}
      <div className="thumbs">
        {shots.map((s) => (
          <div key={s.key} className={`thumb ${s.status}`}>
            <img src={s.preview} alt={s.role} />
            <small>
              {s.role} · {s.status === "uploading" ? t("uploading") : s.status === "done" ? t("uploaded") : t("uploadFailed")}
            </small>
            {s.quality?.tooDark && (
              <small className="warn">
                {t("tooDark")}, {t("retakeHint")}
              </small>
            )}
            {s.quality?.tooBlurry && (
              <small className="warn">
                {t("tooBlurry")}, {t("retakeHint")}
              </small>
            )}
            {s.status === "failed" && (
              <button type="button" onClick={() => void upload(move.id, s)}>
                {t("retry")}
              </button>
            )}
          </div>
        ))}
      </div>
      {error && <p className="error">{error}</p>}
    </section>
  );
}
```

- [ ] **Step 6: Write `web/src/pages/Queue.tsx`**

```tsx
import { useCallback, useEffect, useState } from "react";
import { api, blobUrl, type Move } from "../api";
import { StatusChip } from "../components";
import { useT, type Key } from "../i18n";
import { useMoveEvents } from "../live";

const FILTERS: { status: string; label: Key }[] = [
  { status: "needs_review", label: "filterNeedsReview" },
  { status: "", label: "filterAll" },
  { status: "accepted", label: "filterAccepted" },
  { status: "refused", label: "filterRefused" },
];

export function Queue() {
  const { t } = useT();
  const [status, setStatus] = useState("needs_review");
  const [moves, setMoves] = useState<Move[]>([]);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      setMoves((await api.listMoves(status)).moves);
      setError("");
    } catch (e) {
      setError(String(e));
    }
  }, [status]);

  useEffect(() => {
    void load();
  }, [load]);
  useMoveEvents(() => void load());

  return (
    <section>
      <div className="tabs">
        {FILTERS.map((f) => (
          <button
            key={f.label}
            type="button"
            className={f.status === status ? "active" : ""}
            onClick={() => setStatus(f.status)}
          >
            {t(f.label)}
          </button>
        ))}
      </div>
      {error && <p className="error">{error}</p>}
      {moves.length === 0 && <p className="muted">{t("noMoves")}</p>}
      <ul className="queue">
        {moves.map((m) => {
          const first = m.photos?.[0];
          return (
            <li key={m.id}>
              <a href={`#/moves/${m.id}`} className="queue-row">
                {first ? <img src={blobUrl(first.ai_sha256)} alt="" loading="lazy" /> : <div className="noimg" />}
                <div>
                  <div className="mono strong">{m.number || t("noNumber")}</div>
                  <StatusChip status={m.status} /> {m.is_test && <span className="chip">{t("testTag")}</span>}
                  <div className="muted small">
                    {new Date(m.created_at).toLocaleString()} · {m.reason}
                  </div>
                </div>
              </a>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
```

- [ ] **Step 7: Write `web/src/pages/MoveDetail.tsx`**

```tsx
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, blobUrl, type Attempt, type MoveDetail as Detail } from "../api";
import { CandidateList, CheckDigitMath, PhotoWithBox, StatusChip } from "../components";
import { useT } from "../i18n";
import { useMoveEvents } from "../live";
import { readLocal, writeLocal } from "../storage";

function latestByPhoto(attempts: Attempt[] | null): Map<string, Attempt> {
  const latest = new Map<string, Attempt>();
  for (const a of attempts ?? []) latest.set(a.photo_id, a); // oldest first, so later wins
  return latest;
}

export function MoveDetail({ id }: { id: string }) {
  const { t } = useT();
  const [move, setMove] = useState<Detail | null>(null);
  const [number, setNumber] = useState("");
  const [by, setBy] = useState(() => readLocal("by") ?? "");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const m = await api.getMove(id);
      setMove(m);
      setNumber((current) => current || m.number);
      setError("");
    } catch (e) {
      setError(String(e));
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);
  useMoveEvents((changed) => {
    if (changed === id) void load();
  });
  useEffect(() => {
    if (!move || move.status !== "reading") return;
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [move, load]);

  async function resolve(action: "accept" | "correct" | "refuse") {
    if (!move) return;
    setBusy(true);
    setError("");
    try {
      writeLocal("by", by);
      await api.resolve(move.id, { action, number: action === "correct" ? number : undefined, by, note });
      await load();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function reread() {
    if (!move) return;
    try {
      await api.reread(move.id);
      await load();
    } catch (e) {
      setError(String(e));
    }
  }

  if (!move) return <p>{error || t("loading")}</p>;

  const latest = latestByPhoto(move.attempts);
  const resolved = move.resolved_by !== "";
  const edited = number.trim().toUpperCase() !== move.number;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void resolve(edited ? "correct" : "accept");
  };

  return (
    <section>
      <a href="#/queue">← {t("back")}</a>
      <div className="card">
        <StatusChip status={move.status} /> {move.is_test && <span className="chip">{t("testTag")}</span>}
        <div className={`big-number mono ${move.check_digit_valid ? "ok" : "bad"}`}>{move.number || t("noNumber")}</div>
        <p>
          {t("typeCode")}: <b>{move.type_code || "–"}</b> · {t("color")}: <b>{move.color || "–"}</b> ·{" "}
          {t("confidence")}: {Math.round(move.confidence * 100)}%
        </p>
        {move.reason && (
          <p className="muted">
            {t("reason")}: {move.reason}
          </p>
        )}
        {resolved ? (
          <div>
            <p>{move.verified ? `✅ ${t("verified")}` : `⚠️ ${t("unverified")}`}</p>
            {move.corrected_from && (
              <p>
                {t("correctedFrom")} <span className="mono">{move.corrected_from}</span>
              </p>
            )}
            <p>
              {t("resolvedBy")} {move.resolved_by}
              {move.note && ` · ${move.note}`}
            </p>
          </div>
        ) : (
          <form onSubmit={submit}>
            <CandidateList candidates={move.result.candidates ?? null} onPick={setNumber} />
            <label>
              {t("number")}
              <input
                className="mono big-input"
                value={number}
                onChange={(e) => setNumber(e.target.value.toUpperCase())}
                autoFocus
              />
            </label>
            <CheckDigitMath number={number} />
            <label>
              {t("yourName")}
              <input value={by} onChange={(e) => setBy(e.target.value)} />
            </label>
            <label>
              {t("note")}
              <input value={note} onChange={(e) => setNote(e.target.value)} />
            </label>
            <div className="row">
              <button type="submit" className="primary" disabled={busy || !number.trim()}>
                {edited ? t("correct") : t("accept")}
              </button>
              <button type="button" className="danger" disabled={busy} onClick={() => void resolve("refuse")}>
                {t("refuse")}
              </button>
              <button type="button" disabled={busy} onClick={() => void reread()}>
                {t("reread")}
              </button>
            </div>
          </form>
        )}
        {error && <p className="error">{error}</p>}
      </div>
      <h3>{t("photos")}</h3>
      <div className="photo-grid">
        {(move.photos ?? []).map((p) => {
          const a = latest.get(p.id);
          const reading = a?.reading;
          return (
            <figure key={p.id} className="card">
              <PhotoWithBox src={blobUrl(p.ai_sha256)} box={reading?.box} alt={p.role} />
              <figcaption>
                <b>{p.role}</b>
                {a?.error && (
                  <p className="error">
                    {t("error")}: {a.error}
                  </p>
                )}
                {a && !a.error && (
                  <p className="small">
                    {(reading?.candidates ?? []).map((c) => `${c.number} ${Math.round(c.confidence * 100)}%`).join(", ") ||
                      t("noNumber")}
                    {reading?.type_code && ` · ${reading.type_code}`}
                    {reading?.color && ` · ${reading.color}`}
                    {` · ${a.engine}/${a.model} · ${a.latency_ms} ms`}
                  </p>
                )}
                <a href={blobUrl(p.sha256)} target="_blank" rel="noreferrer" className="small">
                  {t("original")}
                </a>
              </figcaption>
            </figure>
          );
        })}
      </div>
    </section>
  );
}
```

- [ ] **Step 8: Write `web/src/pages/TestRead.tsx`**

```tsx
import { useState } from "react";
import { api, type TestReadResult } from "../api";
import { CandidateList, CheckDigitMath, PhotoWithBox } from "../components";
import { useT } from "../i18n";
import { preparePhoto, type Quality } from "../image";

export function TestRead() {
  const { t } = useT();
  const [preview, setPreview] = useState("");
  const [quality, setQuality] = useState<Quality | null>(null);
  const [result, setResult] = useState<TestReadResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function onFile(files: FileList | null) {
    const file = files?.[0];
    if (!file) return;
    setBusy(true);
    setError("");
    setResult(null);
    try {
      const prepared = await preparePhoto(file);
      if (preview) URL.revokeObjectURL(preview);
      setPreview(URL.createObjectURL(prepared.ai));
      setQuality(prepared.quality);
      setResult(await api.testRead(prepared.ai));
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  const top = result?.frame.candidates?.[0]?.number ?? "";
  return (
    <section>
      <label className="shot-button">
        {t("choosePhoto")}
        <input
          type="file"
          accept="image/*"
          capture="environment"
          hidden
          onChange={(e) => {
            void onFile(e.target.files);
            e.target.value = "";
          }}
        />
      </label>
      {busy && <p>{t("status_reading")}</p>}
      {error && <p className="error">{error}</p>}
      {quality && (
        <p className="small">
          {t("quality")}: {quality.brightness.toFixed(0)} / {quality.sharpness.toFixed(0)}
          {quality.tooDark && ` · ${t("tooDark")}`}
          {quality.tooBlurry && ` · ${t("tooBlurry")}`}
        </p>
      )}
      {preview && <PhotoWithBox src={preview} box={result?.attempt.reading.box} alt="" />}
      {result && (
        <div className="card">
          {result.error && (
            <p className="error">
              {t("error")}: {result.error}
            </p>
          )}
          <CandidateList candidates={result.frame.candidates} />
          {top && <CheckDigitMath number={top} />}
          <p>
            {t("typeCode")}: <b>{result.frame.type_code || "–"}</b> · {t("color")}: <b>{result.frame.color || "–"}</b>
          </p>
          <p className="small">
            {t("engine")}: {result.attempt.engine}/{result.attempt.model} · {t("latency")}: {result.attempt.latency_ms} ms ·{" "}
            {t("tokens")}: {result.attempt.usage.input_tokens} / {result.attempt.usage.output_tokens}
          </p>
          <details>
            <summary>{t("rawAnswer")}</summary>
            <pre>{result.attempt.raw}</pre>
          </details>
        </div>
      )}
    </section>
  );
}
```

- [ ] **Step 9: Build the web app**

Run: `npm --prefix web run build`
Expected: TypeScript passes, Vite prints the files written to `../internal/webui/dist/`, and `internal/webui/dist/index.html` and `internal/webui/dist/.gitkeep` both exist.

- [ ] **Step 10: Run the hub with the app embedded**

Run: `go build -o bin/depotd.exe ./cmd/depotd`
Run (second terminal): `bin/depotd.exe hub -engine fake -data depot-data`
Run: `curl.exe -s http://localhost:8080/`
Expected: HTML containing `<div id="root"></div>`; the hub log no longer says `web app not built yet`.

Open `http://localhost:8080` in the in-app browser or any browser and check by hand:
1. Home shows three tiles; the TR/EN button switches language.
2. Test read: choose any photo → after about a second the fake engine's answer appears with `CSQU3054383`, a green "Check digit valid", and an orange box over the photo.
3. New gate-in: start a truck, add a photo → status goes `Reading…` → `Needs review` with `CSQU3054383`.
4. Queue shows the truck; open it; change the number to `CSQU3054384` → button says **Correct and accept** and the math says invalid; change it back → **Accept**; press Enter → accepted and verified.

Stop the hub with Ctrl+C.

- [ ] **Step 11: Commit**

The built app is not committed (`.gitignore` keeps only `.gitkeep`).

```bash
git add web/src/ internal/webui/dist/.gitkeep
git commit -m "feat(web): capture, queue, move detail and test-read pages" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 15: Training dataset export

**Files:**
- Create: `internal/testkit/testkit.go`
- Create: `internal/dataset/export.go`
- Test: `internal/dataset/export_test.go`
- Modify: `cmd/depotd/main.go` (add the `export-dataset` command)

Exports every **verified, non-practice** move (spec §9.7): the original photos byte-for-byte (fingerprints still match the gate record), one JSON label per photo, and a YOLO label file for every photo where the engine gave a number box. COCO JSON comes in a later plan. Training consent per company arrives with the cloud tenants; at the owner's own depot consent is implicit.

- [ ] **Step 1: Write the shared test helper `internal/testkit/testkit.go`**

```go
// Package testkit holds helpers shared by tests in several packages.
package testkit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"depot/internal/blob"
	"depot/internal/iso6346"
	"depot/internal/recognition"
	"depot/internal/store"
)

// Open returns a fresh database and photo store in a temporary folder.
func Open(t *testing.T) (*store.Store, *blob.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "depot.db"), "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bl, err := blob.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return st, bl
}

// SeedVerifiedMove stores a move with one photo, as if an engine had read it
// (reading) and a person then confirmed number. The photo's engine copy holds
// exactly aiContent, so a recognition.Fake can be scripted per photo.
func SeedVerifiedMove(t *testing.T, st *store.Store, bl *blob.Store, number, aiContent string, reading recognition.Reading, isTest bool) store.Move {
	t.Helper()
	ctx := context.Background()
	m, err := st.CreateMove(ctx, "1", isTest, "seed")
	if err != nil {
		t.Fatal(err)
	}
	orig, err := bl.Put([]byte("\xff\xd8\xff\xe0original-" + aiContent))
	if err != nil {
		t.Fatal(err)
	}
	ai, err := bl.Put([]byte(aiContent))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.AddPhoto(ctx, store.Photo{
		MoveID: m.ID, Role: "rear", SHA256: orig, AISHA256: ai,
		MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: int64(len(aiContent)),
	}, "seed")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reading)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAttempt(ctx, store.Attempt{
		MoveID: m.ID, PhotoID: p.ID, Engine: "fake", Model: "fake-1",
		PromptVersion: recognition.PromptVersion, Reading: raw, Raw: string(raw),
	}); err != nil {
		t.Fatal(err)
	}
	engineNumber := ""
	if len(reading.Candidates) > 0 {
		engineNumber = iso6346.Normalize(reading.Candidates[0].Number)
	}
	if err := st.SetResult(ctx, m.ID, store.ReadOutcome{
		Status: store.StatusNeedsReview, Number: engineNumber, TypeCode: reading.TypeCode, Color: reading.Color,
	}); err != nil {
		t.Fatal(err)
	}
	update := store.ResolveUpdate{Status: store.StatusAccepted, Number: number, Verified: iso6346.IsValid(number), By: "seed"}
	if engineNumber != "" && engineNumber != number {
		update.CorrectedFrom = engineNumber
	}
	got, err := st.Resolve(ctx, m.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
```

- [ ] **Step 2: Write the failing test**

`internal/dataset/export_test.go`:

```go
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
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/dataset/`
Expected: FAIL to compile with `undefined: Export`.

- [ ] **Step 4: Implement `internal/dataset/export.go`**

```go
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
```

- [ ] **Step 5: Run the test**

Run: `go test ./internal/dataset/ -v`
Expected: PASS.

- [ ] **Step 6: Wire the command into `cmd/depotd/main.go`**

Add `"depot/internal/dataset"` to the imports. In `main`'s switch, after the `case "hub":` branch, add:

```go
	case "export-dataset":
		err = runExport(os.Args[2:])
```

Add this function below `runHub`:

```go
func runExport(args []string) error {
	fs := flag.NewFlagSet("export-dataset", flag.ExitOnError)
	var data dataFlags
	data.register(fs)
	out := fs.String("out", "dataset-export", "folder to write the dataset into")
	_ = fs.Parse(args)

	st, bl, err := data.open()
	if err != nil {
		return err
	}
	defer st.Close()
	sum, err := dataset.Export(context.Background(), st, bl, *out)
	if err != nil {
		return err
	}
	fmt.Printf("exported %d verified moves, %d photos (%d with number boxes) to %s\n", sum.Moves, sum.Photos, sum.Boxes, *out)
	return nil
}
```

- [ ] **Step 7: Build and try it**

Run: `go build -o bin/depotd.exe ./cmd/depotd`
Run: `bin/depotd.exe export-dataset -data depot-data -out dataset-export`
Expected: `exported N verified moves, ...` (N is the number of moves you accepted with a valid number in Task 14's manual check; practice moves are not counted).

- [ ] **Step 8: Commit**

```bash
git add internal/testkit/ internal/dataset/ cmd/depotd/main.go
git commit -m "feat: export verified photos and labels for model training" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 16: Accuracy benchmark

**Files:**
- Create: `internal/bench/bench.go`
- Test: `internal/bench/bench_test.go`
- Modify: `cmd/depotd/main.go` (add the `bench` command)

`depotd bench` re-reads the engine copy of every verified move's photos with the chosen engine and model, combines them exactly like the gate does, and compares with the number a person confirmed. This is the golden-set measurement from spec §13 and how the owner decides model and thinking level (spec §9.2). It calls the paid engine once per photo, so it refuses to run against Gemini without `-yes`.

- [ ] **Step 1: Write the failing tests**

`internal/bench/bench_test.go`:

```go
package bench

import (
	"context"
	"testing"

	"depot/internal/recognition"
	"depot/internal/testkit"
)

func reads(number string) recognition.Reading {
	return recognition.Reading{Candidates: []recognition.RawCandidate{{Number: number, Confidence: 0.9}}}
}

func TestBenchScoresMovesAndPhotos(t *testing.T) {
	st, bl := testkit.Open(t)
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "photo-good", recognition.Reading{}, false)
	testkit.SeedVerifiedMove(t, st, bl, "TEXU1234569", "photo-bad", recognition.Reading{}, false)
	fake := &recognition.Fake{ByImage: map[string]recognition.Reading{
		"photo-good": reads("CSQU3054383"),
		"photo-bad":  reads("CSQU3054383"),
	}}

	rep, err := Run(context.Background(), st, bl, fake, 0.85, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Moves != 2 || rep.MovesCorrect != 1 || rep.Photos != 2 || rep.PhotosCorrect != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Misses) != 1 || rep.Misses[0].Want != "TEXU1234569" || rep.Misses[0].Got != "CSQU3054383" {
		t.Fatalf("misses = %+v", rep.Misses)
	}
	if rep.MoveAccuracy() != 0.5 || rep.PhotoAccuracy() != 0.5 {
		t.Fatalf("accuracy = %v, %v", rep.MoveAccuracy(), rep.PhotoAccuracy())
	}
}

func TestBenchHonoursLimit(t *testing.T) {
	st, bl := testkit.Open(t)
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "a", recognition.Reading{}, false)
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "b", recognition.Reading{}, false)
	rep, err := Run(context.Background(), st, bl, &recognition.Fake{Reading: reads("CSQU3054383")}, 0.85, 1)
	if err != nil || rep.Moves != 1 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
}

func TestBenchCountsEngineErrors(t *testing.T) {
	st, bl := testkit.Open(t)
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "a", recognition.Reading{}, false)
	rep, err := Run(context.Background(), st, bl, &recognition.Fake{Err: recognition.ErrUnavailable}, 0.85, 0)
	if err != nil || rep.Errors != 1 || rep.MovesCorrect != 0 || len(rep.Misses) != 1 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/bench/`
Expected: FAIL to compile with `undefined: Run`.

- [ ] **Step 3: Implement `internal/bench/bench.go`**

```go
// Package bench measures an engine against moves a person has verified.
package bench

import (
	"context"

	"depot/internal/blob"
	"depot/internal/recognition"
	"depot/internal/store"
)

// Miss is a move the engine got wrong.
type Miss struct {
	MoveID string
	Want   string
	Got    string
	Reason string
}

// Report is the benchmark outcome.
type Report struct {
	Moves, MovesCorrect   int
	Photos, PhotosCorrect int
	Errors                int
	InputTokens           int64
	OutputTokens          int64
	TotalLatencyMS        int64
	Misses                []Miss
}

func (r Report) MoveAccuracy() float64  { return ratio(r.MovesCorrect, r.Moves) }
func (r Report) PhotoAccuracy() float64 { return ratio(r.PhotosCorrect, r.Photos) }

// AvgLatencyMS is the mean engine time per photo.
func (r Report) AvgLatencyMS() int64 {
	if r.Photos == 0 {
		return 0
	}
	return r.TotalLatencyMS / int64(r.Photos)
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// Run re-reads up to limit verified moves (0: all) with rec.
func Run(ctx context.Context, st *store.Store, bl *blob.Store, rec recognition.Recognizer, threshold float64, limit int) (Report, error) {
	var rep Report
	moves, err := st.VerifiedMoves(ctx)
	if err != nil {
		return rep, err
	}
	if limit > 0 && len(moves) > limit {
		moves = moves[:limit]
	}
	for _, m := range moves {
		var frames []recognition.Frame
		for _, p := range m.Photos {
			data, err := bl.Get(p.AISHA256)
			if err != nil {
				return rep, err
			}
			att, err := rec.Read(ctx, recognition.Image{Data: data, MediaType: p.AIMediaType})
			rep.Photos++
			rep.InputTokens += att.Usage.InputTokens
			rep.OutputTokens += att.Usage.OutputTokens
			rep.TotalLatencyMS += att.LatencyMS
			if err != nil {
				rep.Errors++
				frames = append(frames, recognition.ErrorFrame(p.Role, err.Error()))
				continue
			}
			f := recognition.BuildFrame(p.Role, att.Reading)
			if len(f.Candidates) > 0 && f.Candidates[0].Number == m.Number {
				rep.PhotosCorrect++
			}
			frames = append(frames, f)
		}
		res := recognition.Reconcile(frames, threshold)
		rep.Moves++
		if res.Number == m.Number {
			rep.MovesCorrect++
		} else {
			rep.Misses = append(rep.Misses, Miss{MoveID: m.ID, Want: m.Number, Got: res.Number, Reason: res.Reason})
		}
	}
	return rep, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/bench/ -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Wire the command into `cmd/depotd/main.go`**

Add `"depot/internal/bench"` to the imports. In `main`'s switch, after the `case "export-dataset":` branch, add:

```go
	case "bench":
		err = runBench(os.Args[2:])
```

Add this function below `runExport`:

```go
func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	var data dataFlags
	data.register(fs)
	var eng engineFlags
	eng.register(fs)
	limit := fs.Int("limit", 0, "only the first N verified moves (0: all)")
	threshold := fs.Float64("threshold", 0.85, "auto-accept threshold used when combining photos")
	yes := fs.Bool("yes", false, "confirm that every verified photo is sent to the paid engine")
	_ = fs.Parse(args)

	if eng.engine != "fake" && !*yes {
		return errors.New("bench sends every verified photo to the engine, which costs money: re-run with -yes")
	}
	rec, err := eng.build()
	if err != nil {
		return err
	}
	st, bl, err := data.open()
	if err != nil {
		return err
	}
	defer st.Close()

	rep, err := bench.Run(context.Background(), st, bl, rec, *threshold, *limit)
	if err != nil {
		return err
	}
	name, model := rec.Name(), rec.Model()
	fmt.Printf("engine      %s (%s)\n", name, model)
	fmt.Printf("moves       %d/%d correct (%.1f%%)\n", rep.MovesCorrect, rep.Moves, 100*rep.MoveAccuracy())
	fmt.Printf("photos      %d/%d correct (%.1f%%), %d engine errors\n", rep.PhotosCorrect, rep.Photos, 100*rep.PhotoAccuracy(), rep.Errors)
	fmt.Printf("tokens      %d in / %d out\n", rep.InputTokens, rep.OutputTokens)
	fmt.Printf("avg latency %d ms per photo\n", rep.AvgLatencyMS())
	for _, m := range rep.Misses {
		fmt.Printf("miss        %s want %s got %q (%s)\n", m.MoveID, m.Want, m.Got, m.Reason)
	}
	return nil
}
```

- [ ] **Step 6: Build and try it with the fake engine**

Run: `go build -o bin/depotd.exe ./cmd/depotd`
Run: `bin/depotd.exe bench -engine fake -data depot-data`
Expected: a report starting `engine      fake (fake-1)`. Moves whose confirmed number is `CSQU3054383` count as correct (the demo engine always reads that).

Run: `bin/depotd.exe bench -data depot-data`
Expected: `error: bench sends every verified photo to the engine, which costs money: re-run with -yes`

- [ ] **Step 7: Commit**

```bash
git add internal/bench/ cmd/depotd/main.go
git commit -m "feat: benchmark an engine against verified moves" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 17: Runbook and first run at the depot

**Files:**
- Create: `docs/runbooks/local-test-kit.md`

- [ ] **Step 1: Write `docs/runbooks/local-test-kit.md`**

````markdown
# Local gate test kit: how to run it

One Windows PC runs `depotd hub`. Phones on the same Wi-Fi open it in the browser,
photograph containers, and an office screen confirms or corrects what was read.
Everything stays on the PC: database and photos are in the `depot-data` folder.

## One-time setup

1. Install Go 1.24+ (https://go.dev/dl/) and Node 20+.
2. Create a Gemini API key in Google AI Studio (https://aistudio.google.com/apikey) on a project
   **with billing enabled**: on the unpaid tier Google may use submitted photos to improve its
   products. In **your own** terminal run `setx GEMINI_API_KEY "<key>"`, then open a new terminal.
   Never paste the key in chat or commit it.
3. Build, from `C:\Users\OZGUN\depot-platform`:

   ```
   npm --prefix web install
   npm --prefix web run build
   go build -o bin/depotd.exe ./cmd/depotd
   ```

## Every day

```
bin/depotd.exe hub -data depot-data -access-code 4821
```

- The log prints addresses such as `open: http://192.168.0.29:8080`. Open that on the phones.
- The first time, Windows Firewall asks whether `depotd.exe` may use the network: choose
  **Private networks** and allow. (Do this yourself; nobody should change firewall settings for you.)
- `-access-code` is optional; when set, every phone and PC asks for it once.
- No API key yet, or just practising? Add `-engine fake`: every photo reads as `CSQU3054383`.
- Stop with Ctrl+C. Photos that were still being read are read again at the next start.

Options: `-model` and `-thinking` choose the Gemini model and thinking level (defaults `gemini-3.8-flash`, `low`;
try `-model gemini-3.5-flash-lite` with `bench` to see whether the cheaper model is accurate enough),
`-auto-accept` lets confident, check-digit-valid reads skip the person (off: keep it off while
collecting training data), `-addr :8080` changes the port.

## Using it

- **Phone, New gate-in:** pick the lane, start a truck, tap **Rear doors** first (number and ISO
  code are clearest there), then the sides. Tick **Practice** for test trucks: they never enter
  the training data. A warning under a photo means it looks too dark or blurry: retake it.
- **Office, Queue:** everything that needs a person. Open a truck, check the photos (the orange
  box shows where the number was read), fix the number if needed (the check-digit math updates as
  you type) and press Enter. Your name is remembered on that device.
- **Test read:** try one photo without creating a record. Shows the raw engine answer, time and tokens.

## Photo tips

Fill the frame with the number, keep the phone level, avoid glare on wet paint, and for flat racks
and tanks photograph the end frame where the number is. Night photos need a light.

## Training data and accuracy

```
bin/depotd.exe export-dataset -data depot-data -out dataset-export
bin/depotd.exe bench -data depot-data -limit 50 -yes
```

`export-dataset` copies every verified, non-practice photo with labels (see `dataset-export/README.txt`).
`bench` re-reads verified trucks with the chosen `-model`/`-thinking` and prints accuracy, tokens and
time: use it to compare settings before changing them. It costs one engine call per photo.

## Backup

Stop the hub and copy the whole `depot-data` folder somewhere safe (external disk or cloud drive).

## Troubleshooting

| Problem | Fix |
|---|---|
| Phone can't open the address | Same Wi-Fi as the PC? Guest Wi-Fi often isolates devices. Firewall allowed for private networks? Is the Wi-Fi marked "Public" in Windows? |
| `GEMINI_API_KEY is not set` | Run `setx ...` in your own terminal and open a new one, or use `-engine fake`. |
| Reason shows `engine rejected the request` with a model name | The model id is wrong or retired: check the current list in the Gemini docs and pass `-model`. If it mentions thinking, run with `-thinking ""`. |
| Reason shows `engine rejected the credentials` | The API key is wrong or revoked: create a new one. |
| Reason shows `engine unavailable` | Internet or Gemini temporarily down (already retried twice): press **Read again** later, or type the number. |
| `The web app is not built yet` | Run `npm --prefix web run build`, then `go build ...` again. |
| Camera opens the front camera | Switch cameras in the phone's camera screen; the app asks for the rear one but some phones ignore it. |
````

- [ ] **Step 2: Full verification**

Run: `go vet ./...`
Expected: no output.

Run: `go test ./...`
Expected: every package `ok`.

Run: `npm --prefix web run build`
Expected: builds without errors.

Run: `go build -o bin/depotd.exe ./cmd/depotd`
Expected: no output.

- [ ] **Step 3: First run with Gemini (owner present)**

With the owner's `GEMINI_API_KEY` set in their terminal:

Run: `bin/depotd.exe hub -data depot-data`
Expected log: `engine gemini (gemini-3.8-flash)`.

On a phone on the same Wi-Fi: open the printed address → **Test read** → photograph a real container door. Expected: the number appears with a green check-digit result, the ISO type code and color are filled, the box sits on the number, and the latency and tokens are shown. Then do one full **New gate-in** with rear + left + right photos and confirm it in the **Queue**.

If the reading is wrong, keep the photo: it is exactly the kind of case the golden set needs.

- [ ] **Step 4: Commit**

```bash
git add docs/runbooks/local-test-kit.md
git commit -m "docs: runbook for the local gate test kit" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
