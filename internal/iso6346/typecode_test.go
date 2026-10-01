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
