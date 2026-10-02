// Package camera talks to fixed gate cameras: still photos, the camera's own
// vehicle detection, and the address of its live video.
package camera

import (
	"context"
	"errors"
)

// Settings are what a person enters to connect a camera.
type Settings struct {
	Host     string // address, optionally with the HTTP port: 192.168.1.50 or 192.168.1.50:8080
	User     string
	Password string
	Auto     bool // the camera's vehicle detection starts photo bursts
}

// Configured reports whether there is enough to connect.
func (s Settings) Configured() bool { return s.Host != "" && s.User != "" }

// Detection kinds.
const (
	KindVehicle = "vehicle"
	KindMotion  = "motion" // the camera cannot tell vehicles from other movement
)

// Info describes the connected camera.
type Info struct {
	Model     string `json:"model"`
	Firmware  string `json:"firmware"`
	Width     int    `json:"width"`  // main stream, i.e. still size
	Height    int    `json:"height"` //
	RTSP      bool   `json:"rtsp"`   // RTSP switched on (needed for live video)
	Detection string `json:"detection"`
}

// Detection is the camera's answer to "is a vehicle there now?".
type Detection struct {
	Present bool   `json:"present"`
	Kind    string `json:"kind"`
}

// Step is one line of a connection test: reach, login, info, ports, detect
// or snapshot.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report is the outcome of a connection test.
type Report struct {
	Steps []Step `json:"steps"`
	Info  Info   `json:"info"`
	Raw   string `json:"raw"` // the camera's last answer, secrets removed
}

// OK reports whether every step passed.
func (r Report) OK() bool {
	for _, s := range r.Steps {
		if !s.OK {
			return false
		}
	}
	return len(r.Steps) > 0
}

// Camera errors. ErrAuth must stop polling: cameras lock logins after a few
// wrong passwords.
var (
	ErrUnreachable = errors.New("camera unreachable")
	ErrAuth        = errors.New("camera rejected the user name or password")
	ErrProtocol    = errors.New("camera answered something unexpected")

	errRTSPOff     = errors.New("RTSP is off")
	errPortsUnread = errors.New("could not read the port settings")
	errNoDetection = errors.New("the camera did not answer a detection query")
	// errSessionsFull passes on its own: errors carrying it are also
	// ErrUnreachable, never ErrAuth.
	errSessionsFull = errors.New("the camera has too many open sessions: wait a few minutes or restart the camera")
)

// Camera is one fixed camera.
type Camera interface {
	Info(ctx context.Context) (Info, error)
	// Snapshot returns a full-resolution JPEG.
	Snapshot(ctx context.Context) ([]byte, error)
	Detect(ctx context.Context) (Detection, error)
	// LiveSource is the RTSP address of the small live stream, credentials
	// included, or "" when the camera has none.
	LiveSource() string
	// Test checks the connection step by step.
	Test(ctx context.Context) Report
}

// advice turns a failed step into what the person should do.
func advice(step string, err error) string {
	switch {
	case errors.Is(err, errPortsUnread):
		return err.Error() // "could not read the port settings: <why>", whatever the why
	case errors.Is(err, errSessionsFull):
		return "The camera has too many open sessions, for example from the Reolink app, a recorder or another program. " +
			"Wait a few minutes or restart the camera, then test again."
	case errors.Is(err, ErrUnreachable):
		return "Cannot connect. Check the address, that the camera has power and is on this network, " +
			"and that HTTP is on (Reolink app: Settings → Network → Advanced → Port Settings). " + err.Error()
	case errors.Is(err, ErrAuth):
		return "Wrong user name or password. Careful: the camera locks logins after several wrong tries. " + err.Error()
	case errors.Is(err, errRTSPOff):
		return "RTSP is off: turn it on in Port Settings, otherwise there is no live video."
	default:
		return err.Error()
	}
}
