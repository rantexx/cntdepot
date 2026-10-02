package httpapi

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"net/http"
	"time"

	"depot/internal/camera"
	"depot/internal/gate"
	"depot/internal/gatecam"
	"depot/internal/live"
	"depot/internal/thumb"
)

// cameraView is everything the Camera page shows. It never includes the
// camera password.
type cameraView struct {
	Available bool           `json:"available"`
	Settings  settingsView   `json:"settings"`
	Status    gatecam.Status `json:"status"`
	Live      live.Status    `json:"live"`
}

type settingsView struct {
	Host        string `json:"host"`
	User        string `json:"user"`
	PasswordSet bool   `json:"password_set"`
	Auto        bool   `json:"auto"`
}

func (s *Server) cameraView() cameraView {
	v := cameraView{Status: gatecam.Status{State: gatecam.StateUnconfigured}, Live: live.Status{State: live.StateOff}}
	if s.opts.Camera == nil {
		return v
	}
	set := s.opts.Camera.Settings()
	v.Available = true
	v.Settings = settingsView{Host: set.Host, User: set.User, PasswordSet: set.Password != "", Auto: set.Auto}
	v.Status = s.opts.Camera.Status()
	if s.opts.Live != nil {
		v.Live = s.opts.Live.Status()
	}
	return v
}

func (s *Server) getCamera(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cameraView())
}

func (s *Server) putCamera(w http.ResponseWriter, r *http.Request) {
	if !s.needCamera(w) {
		return
	}
	var u gatecam.Update
	if !decodeJSON(w, r, &u) {
		return
	}
	if _, err := s.opts.Camera.Save(r.Context(), u); err != nil {
		if errors.Is(err, gatecam.ErrBadSettings) {
			writeError(w, http.StatusBadRequest, "E4007", err.Error())
			return
		}
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cameraView())
}

// needCamera answers 503 when the hub runs without camera support.
func (s *Server) needCamera(w http.ResponseWriter) bool {
	if s.opts.Camera == nil {
		writeError(w, http.StatusServiceUnavailable, "E5031", "camera support is not enabled on this hub")
		return false
	}
	return true
}

// currentCamera returns the connected camera, or answers 409 when none is
// set up yet.
func (s *Server) currentCamera(w http.ResponseWriter) (camera.Camera, bool) {
	if !s.needCamera(w) {
		return nil, false
	}
	cam := s.opts.Camera.Camera()
	if cam == nil {
		writeError(w, http.StatusConflict, "E4091", "no camera is set up yet")
		return nil, false
	}
	return cam, true
}

func (s *Server) testCamera(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.currentCamera(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rep := cam.Test(ctx)
	// A wrong password stops polling until the settings change. A test that
	// logs in after that means the password works again, so polling resumes,
	// even when a later step (live video port, still) has something to fix.
	if rep.LoginOK() && s.opts.Camera.Status().State == gatecam.StateAuth {
		s.opts.Camera.Reconnect()
	}
	writeJSON(w, http.StatusOK, rep)
}

// cameraRead is "read this frame": the still, what the engine saw, and how
// tall the number's letters are in the picture.
type cameraRead struct {
	Still    string              `json:"still"`
	Width    int                 `json:"width"`
	Height   int                 `json:"height"`
	Result   gate.TestReadResult `json:"result"`
	LetterPX int                 `json:"letter_px"`
	Verdict  string              `json:"verdict"`
}

func (s *Server) readCamera(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.currentCamera(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	data, err := cam.Snapshot(ctx)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, "E5020", "camera: "+err.Error())
		return
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		writeError(w, http.StatusBadGateway, "E5021", "the camera's still is not a JPEG")
		return
	}
	digest, err := s.opts.Blobs.Put(data)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	res, err := s.opts.Service.ReadStill(r.Context(), data)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	px := gatecam.LetterHeight(res.Attempt.Reading.Box, cfg.Width, cfg.Height)
	writeJSON(w, http.StatusOK, cameraRead{
		Still: digest, Width: cfg.Width, Height: cfg.Height, Result: res, LetterPX: px, Verdict: gatecam.Verdict(px),
	})
}

func (s *Server) captureCamera(w http.ResponseWriter, r *http.Request) {
	if !s.needCamera(w) {
		return
	}
	id, err := s.opts.Camera.Capture(r.Context())
	switch {
	case errors.Is(err, gatecam.ErrNoCamera):
		writeError(w, http.StatusConflict, "E4091", "no camera is set up yet")
	case errors.Is(err, gatecam.ErrBusy):
		writeError(w, http.StatusConflict, "E4092", "the camera is already taking photos of a vehicle")
	case err != nil:
		writeServiceError(w, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"move_id": id})
	}
}

// cameraStill is a fresh still for the live view's fallback when video
// cannot play.
func (s *Server) cameraStill(w http.ResponseWriter, r *http.Request) {
	width, ok := previewWidth(w, r)
	if !ok {
		return
	}
	cam, ok := s.currentCamera(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	data, err := cam.Snapshot(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "E5020", "camera: "+err.Error())
		return
	}
	if width > 0 {
		if small, err := thumb.Scale(data, width); err == nil {
			data = small
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (s *Server) liveWS(w http.ResponseWriter, r *http.Request) {
	if s.opts.Live == nil {
		writeError(w, http.StatusServiceUnavailable, "E5030", "live video is not running")
		return
	}
	s.opts.Live.Handler().ServeHTTP(w, r)
}
