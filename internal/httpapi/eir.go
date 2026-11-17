package httpapi

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"html/template"
	"log"
	"net/http"
	"time"
	_ "time/tzdata" // depot zones work on minimal images

	"depot/internal/yard"
)

// The EIR page is public: its only credential is the token in the link. It
// shows the one move the token opens, and nothing else.

//go:embed eir.html
var eirHTML string

var eirTemplate = template.Must(template.New("eir").Parse(eirHTML))

var eirText = map[string]map[string]string{
	"en": {
		"title": "Equipment Interchange Receipt", "voided": "VOIDED", "corrected": "CORRECTED", "direction": "Direction",
		"time": "Time", "iso": "ISO type", "fullempty": "Full / empty", "seal": "Seal", "condition": "Condition",
		"truck": "Truck", "trailer": "Trailer", "driver": "Driver", "haulier": "Haulier", "damages": "Damages",
		"nodamage": "No damage recorded.", "damagephoto": "Damage photo", "containerphotos": "Container photos", "containerphoto": "Container photo", "signature": "Signature",
		"in": "Gate in", "out": "Gate out", "F": "Full", "E": "Empty", "sound": "Sound", "damaged": "Damaged",
	},
	"tr": {
		"title": "Ekipman Teslim Belgesi", "voided": "İPTAL EDİLDİ", "corrected": "DÜZELTİLDİ", "direction": "Yön",
		"time": "Zaman", "iso": "ISO tipi", "fullempty": "Dolu / boş", "seal": "Mühür", "condition": "Durum",
		"truck": "Çekici", "trailer": "Dorse", "driver": "Sürücü", "haulier": "Nakliyeci", "damages": "Hasarlar",
		"nodamage": "Hasar kaydı yok.", "damagephoto": "Hasar fotoğrafı", "containerphotos": "Konteyner fotoğrafları", "containerphoto": "Konteyner fotoğrafı", "signature": "İmza",
		"in": "Giriş", "out": "Çıkış", "F": "Dolu", "E": "Boş", "sound": "Sağlam", "damaged": "Hasarlı",
	},
}

func eirHeaders(w http.ResponseWriter, nonce string) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	csp := "default-src 'none'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	if nonce != "" {
		csp += "; style-src 'nonce-" + nonce + "'"
	}
	h.Set("Content-Security-Policy", csp)
}

// eirFail answers 404 for an unknown or expired token or photo, with no detail.
func eirFail(w http.ResponseWriter, err error) {
	if errors.Is(err, yard.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	log.Printf("eir: %v", err)
	w.WriteHeader(http.StatusInternalServerError)
}

func (s *Server) eirPage(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	eirHeaders(w, nonce)
	token := r.PathValue("token")
	e, err := s.opts.Yard.OpenEIR(r.Context(), token)
	if err != nil {
		eirFail(w, err)
		return
	}
	lang := "en"
	if r.URL.Query().Get("lang") == "tr" {
		lang = "tr"
	}
	l := eirText[lang]
	owner, check := e.Number, ""
	if len(owner) == 11 { // owner code and serial, with the check digit in its own box
		owner, check = owner[:10], owner[10:]
	}
	word := func(k string) string { // a stored code shown in words; other values as stored
		if v, ok := l[k]; ok {
			return v
		}
		return k
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = eirTemplate.Execute(w, map[string]any{
		"E": e, "L": l, "Lang": lang, "Nonce": nonce, "Token": token, "Owner": owner, "Check": check,
		"Direction": word(e.Direction), "FullEmpty": word(e.FullEmpty), "Condition": word(e.Condition),
		"When": whenIn(e.At, e.Timezone),
	})
	if err != nil {
		log.Printf("eir: render: %v", err)
	}
}

func (s *Server) eirPhoto(w http.ResponseWriter, r *http.Request) {
	eirHeaders(w, "")
	digest, err := s.opts.Yard.EIRPhotoDigest(r.Context(), r.PathValue("token"), r.PathValue("photoId"))
	if err != nil {
		eirFail(w, err)
		return
	}
	data, err := s.opts.Blobs.Get(digest)
	if err != nil { // a plain empty 404, never the API's JSON error
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeBlob(w, data, 1200)
}

// whenIn shows t in the depot's time zone with its abbreviation (UTC when the zone is unknown).
func whenIn(t time.Time, zone string) string {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}
