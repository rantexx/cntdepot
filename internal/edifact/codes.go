package edifact

// Codes and qualifiers from the SMDG user manuals, version 2.1-3 (D.00B):
// "COPARN" (May 2016) and "CODECO" (January 2016). Page numbers are the
// printed page numbers of those manuals. APERAK has no SMDG manual and follows
// the UN/EDIFACT D.00B APERAK message.

// Message types for UNH S009. The association code SMDG21 is "SMDG Version 2.1"
// (COPARN p.12, CODECO p.13).
const (
	typeCOPARN = "COPARN:D:00B:UN:SMDG21"
	typeCODECO = "CODECO:D:00B:UN:SMDG21"
	typeAPERAK = "APERAK:D:00B:UN"
)

// coparnKind maps the COPARN BGM document name code (e1001, COPARN p.13) to an
// order kind. Codes for equipment leaving the depot are releases; codes for
// equipment arriving are redeliveries. Codes not listed (125 empty equipment
// advice, 127 special service instruction, 264 movement instruction) are not
// orders for a depot and are refused.
var coparnKind = map[string]string{
	"108": "release",    // Transport equipment empty release instruction
	"134": "release",    // Transport equipment on-hire request
	"135": "release",    // Transport equipment on-hire order
	"144": "release",    // Empty container disposition order
	"104": "redelivery", // Transport equipment impending arrival advice
	"126": "redelivery", // Transport equipment acceptance order
	"136": "redelivery", // Transport equipment off-hire request
	"170": "redelivery", // Cargo acceptance order
}

// Message function codes (e1225, COPARN p.13-14, CODECO p.14). For 1 and 5 the
// previous message's reference goes in a header RFF+ACW.
const (
	funcCancel   = "1"
	funcReplace  = "5"
	funcOriginal = "9"
)

// orderRefQualifiers are the header RFF qualifiers (e1153, COPARN p.16) that can
// carry the order reference, in order of preference. ACW (previous message)
// is never the order reference. With none of these, BGM e1004 is used.
var orderRefQualifiers = []string{
	"ATY", // Transport equipment release order reference
	"ATX", // Transport equipment acceptance order reference
	"RE",  // Release number
	"BN",  // Booking reference number
	"AKA", // Container disposition order reference number
	"ACA", // Cargo acceptance order reference number
	"CN",  // Carrier's reference number
	"AAY", // Carrier's agent reference number
}

// Date/time qualifiers (e2005) and formats (e2379).
const (
	dtmPrepared = "137" // Document/message date/time (COPARN p.15, CODECO p.16)
	dtmExpiry   = "36"  // Expiry date: order validity (COPARN p.17 header, p.34 equipment)
	dtmEffect   = "7"   // Effective date/time: the gate time in CODECO (CODECO p.34)
	fmtDate     = "102" // CCYYMMDD
	fmtDateTime = "203" // CCYYMMDDHHMM
)

// depotLocQualifiers are the LOC qualifiers (e3227) that name the depot in a
// COPARN, in order of preference: in the equipment group (COPARN p.35) and in
// the inland transport group (p.57, 175 only). CODECO writes 175 (p.35).
var depotLocQualifiers = []string{
	"175", // Activity location
	"98",  // Place of empty equipment despatch
	"154", // Place/location of on-hire
	"155", // Place/location of off-hire
}

// Full or empty indicator (e8169, COPARN p.29, CODECO p.30).
var fullEmptyCode = map[string]string{"4": "E", "5": "F"}

// CODECO BGM document name codes (e1001, CODECO p.14). The plan said 34 and 36;
// in D.00B those are "Cargo status" and "Identity card", so the guide's codes win.
const (
	docGateIn  = "109" // Transport movement gate in report
	docGateOut = "111" // Transport movement gate out report
)

// CODECO header RFF qualifiers for the order used (CODECO p.17).
const (
	rffRelease    = "ATY" // gate-out against a release order
	rffAcceptance = "ATX" // gate-in against an acceptance (redelivery) order
	rffPrevious   = "ACW" // reference number to previous message
)

// Other CODECO codes.
const (
	nadLine      = "CF"  // Container operator/lessee (CODECO p.24)
	locActivity  = "175" // Activity location (CODECO p.35)
	ftxGeneral   = "AAI" // General information: free text allowed (CODECO p.42-44)
	damEquipment = "1"   // Equipment damage (CODECO p.47)
	tdtPre       = "10"  // Pre-carriage transport: the truck bringing a gate-in (CODECO p.50)
	tdtOn        = "30"  // On-carriage transport: the truck taking a gate-out (CODECO p.50)
	modeRoad     = "3"   // Road (CODECO p.50)
	cntEquipment = "16"  // Total number of equipment (COPARN p.62)
)

// APERAK (D.00B). BGM e1001 305 "Application error and acknowledgement";
// e1225 29 "Accepted without amendment" or 27 "Not accepted"; FTX AAO
// "Error description (free text)" after each ERC.
const (
	docAPERAK   = "305"
	funcAccept  = "29"
	funcReject  = "27"
	ftxError    = "AAO"
	ercCodeSize = 8 // e9321 is an..8
)
