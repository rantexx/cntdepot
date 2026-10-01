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

// typeCodeShape: length code (a digit or a letter, e.g. L for 45 ft),
// height/width code, type group letter, detail.
var typeCodeShape = regexp.MustCompile(`^[0-9A-Z][0-9A-Z][A-Z][0-9A-Z]$`)

var familyByGroup = map[byte]Family{
	'G': FamilyGeneral, 'V': FamilyVentilated, 'B': FamilyBulk, 'S': FamilyNamedCargo,
	'R': FamilyReefer, 'H': FamilyInsulated, 'U': FamilyOpenTop, 'P': FamilyFlatRack,
	'T': FamilyTank, 'A': FamilyAirSurface,
}

// ValidTypeCode reports whether code is a well-formed size/type code with a
// known type group. The input is normalized first (spaces and punctuation
// are ignored).
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
