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
