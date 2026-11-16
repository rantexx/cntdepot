package yard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"depot/internal/store"
	"depot/internal/tenancy"
	sqlcdb "depot/internal/yard/db"
)

// hashEIRToken is how a token is stored: the SHA-256 of the token, in hex.
func hashEIRToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateEIRToken makes a shareable EIR link token for a move of the scope's
// depot. The token is shown once; only its hash is stored. It lives for the
// company's eir_ttl_days.
func (s *Service) CreateEIRToken(ctx context.Context, moveID, actor string) (token string, expires time.Time, err error) {
	st, err := s.st.Settings(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	expires = s.st.now().AddDate(0, 0, int(st.EirTtlDays))
	return token, expires, s.st.AddEIRToken(ctx, moveID, hashEIRToken(token), expires, actor)
}

// EIR is one move as the public equipment interchange receipt shows it.
type EIR struct {
	CompanyID, MoveID                                       string
	Company, Depot                                          string
	Direction, Number, TypeCode, FullEmpty, Seal, Condition string
	TruckPlate, TrailerPlate, Driver, Haulier               string
	At                                                      time.Time
	Damages                                                 []EIRDamage
	SignatureID                                             string   // photo id, "" when none
	Timezone                                                string   // the depot's IANA zone
	Photos                                                  []string // container photo ids; none for camera-lane moves
	Corrected                                               bool
	CorrectionReason                                        string
	Voided                                                  bool
	VoidReason                                              string
}

type EIRDamage struct {
	Location, Description string
	PhotoIDs              []string
}

// OpenEIR resolves a token to its one move. ctx needs no scope. Unknown or
// expired is ErrNotFound.
func (s *Service) OpenEIR(ctx context.Context, token string) (EIR, error) {
	company, moveID, err := s.st.LookupEIR(ctx, hashEIRToken(token))
	if err != nil {
		return EIR{}, err
	}
	ctx = tenancy.With(ctx, tenancy.Scope{CompanyID: company}) // no depot: the move is found by its id
	f, err := s.st.GetMoveForm(ctx, moveID)
	if err != nil {
		return EIR{}, err
	}
	m, err := s.moves.GetMove(ctx, moveID)
	if err != nil {
		return EIR{}, notFoundOf(err)
	}
	var hd sqlcdb.EIRHeaderRow
	if err := s.st.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope) (err error) {
		hd, err = q.EIRHeader(ctx, sqlcdb.EIRHeaderParams{CompanyID: sc.CompanyID, ID: moveID})
		return notFound(err)
	}); err != nil {
		return EIR{}, err
	}
	dmg, err := s.st.ListDamages(ctx, moveID)
	if err != nil {
		return EIR{}, err
	}
	e := EIR{
		CompanyID: company, MoveID: moveID, Company: hd.CompanyName, Depot: hd.DepotName, Haulier: hd.HaulierName,
		Direction: m.Direction, Number: m.Number, TypeCode: m.TypeCode, FullEmpty: f.FullEmpty, Seal: f.Seal,
		Condition: f.Condition, TruckPlate: f.TruckPlate, TrailerPlate: f.TrailerPlate, Driver: f.DriverName,
		Timezone: hd.DepotTimezone, Photos: eirContainerPhotos(m), At: m.CreatedAt, SignatureID: deref(f.SignaturePhotoID),
		Corrected: hd.Corrections > 0, CorrectionReason: hd.CorrectionReason, Voided: f.Voided, VoidReason: f.VoidReason,
	}
	if f.FormSavedAt != nil {
		e.At = *f.FormSavedAt
	}
	for _, d := range dmg {
		e.Damages = append(e.Damages, EIRDamage{Location: d.Location, Description: d.Description, PhotoIDs: d.PhotoIds})
	}
	return e, nil
}

func notFoundOf(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// EIRPhotoDigest returns the blob digest of a damage, signature or container photo of the
// token's move. Any other photo, of this move or another, is ErrNotFound.
func (s *Service) EIRPhotoDigest(ctx context.Context, token, photoID string) (string, error) {
	e, err := s.OpenEIR(ctx, token)
	if err != nil {
		return "", err
	}
	shown := photoID == e.SignatureID
	shown = shown || slices.Contains(e.Photos, photoID)
	for _, d := range e.Damages {
		shown = shown || slices.Contains(d.PhotoIDs, photoID)
	}
	if !shown {
		return "", ErrNotFound
	}
	p, err := s.moves.GetPhoto(tenancy.With(ctx, tenancy.Scope{CompanyID: e.CompanyID}), photoID)
	if err != nil {
		return "", notFoundOf(err)
	}
	if p.MoveID != e.MoveID {
		return "", ErrNotFound
	}
	return p.SHA256, nil
}

// eirContainerPhotos are the container photos a phone-taken move shows. Camera
// lane frames can show the driver, so they are never shown.
func eirContainerPhotos(m store.Move) (ids []string) {
	if m.Source != "phone" {
		return nil
	}
	for _, p := range m.Photos {
		switch strings.ToLower(p.Role) {
		case "rear", "front", "left", "right", "door", "side", "other":
			ids = append(ids, p.ID)
		}
	}
	return ids
}
