package camera

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reolink talks to a Reolink camera's HTTP API (/cgi-bin/api.cgi). New
// firmware ships with HTTP and RTSP switched off: they must be turned on in
// the camera (Settings → Network → Advanced → Port Settings).
//
// A wrong password stops it: after a rejected Login every call fails with
// that ErrAuth without contacting the camera, so polling cannot lock the
// account. Only Test tries again. New settings mean a new Reolink, and Close
// logs the old one out.
type Reolink struct {
	host, user, password string
	client               *http.Client
	now                  func() time.Time

	loginMu sync.Mutex // one login at a time: the camera limits sessions

	mu       sync.Mutex
	token    string
	tokenEnd time.Time
	authErr  error  // the camera rejected the user name or password
	closed   bool   // Close was called: no new sessions
	kind     string // detection kind, once known
	raw      string // last API answer, token and password removed
}

var _ Camera = (*Reolink)(nil)

// NewReolink describes the camera at s.Host. Nothing is sent until the first
// call.
func NewReolink(s Settings) *Reolink {
	return &Reolink{
		host: s.Host, user: s.User, password: s.Password,
		client: &http.Client{Timeout: 10 * time.Second},
		now:    time.Now,
	}
}

// Reolink rspCodes.
const (
	rspMaxSession   = -5 // "max session": every session the camera allows is taken
	rspLoginFirst   = -6 // "please login first": the session expired
	rspNotSupported = -9 // "not support": this camera has no such command
)

// logoutTimeout bounds the best-effort Logout before a new Login and in Close.
const logoutTimeout = 2 * time.Second

// errClosed is a call after Close. It is never ErrAuth: a replaced camera
// says nothing about the password.
var errClosed = fmt.Errorf("%w: the connection to the camera was closed", ErrUnreachable)

type apiReply struct {
	Cmd   string          `json:"cmd"`
	Code  int             `json:"code"`
	Value json.RawMessage `json:"value"`
	Error *struct {
		Detail  string `json:"detail"`
		RspCode int    `json:"rspCode"`
	} `json:"error"`
}

func (r apiReply) expired() bool { return r.Error != nil && r.Error.RspCode == rspLoginFirst }

func (r apiReply) detail() string {
	if r.Error == nil {
		return fmt.Sprintf("code %d", r.Code)
	}
	return fmt.Sprintf("%s (rspCode %d)", r.Error.Detail, r.Error.RspCode)
}

// err turns a failed reply to cmd into an *apiError.
func (r apiReply) err(cmd string) error {
	e := &apiError{Cmd: cmd, Detail: fmt.Sprintf("code %d", r.Code)}
	if r.Error != nil {
		e.RspCode, e.Detail = r.Error.RspCode, r.Error.Detail
	}
	return e
}

// apiError is a command the camera answered with an error code. It is an
// ErrProtocol.
type apiError struct {
	Cmd     string
	RspCode int // 0 when the camera gave none
	Detail  string
}

func (e *apiError) Error() string {
	if e.RspCode == 0 {
		return fmt.Sprintf("%v: %s failed: %s", ErrProtocol, e.Cmd, e.Detail)
	}
	return fmt.Sprintf("%v: %s failed: %s (rspCode %d)", ErrProtocol, e.Cmd, e.Detail, e.RspCode)
}

func (e *apiError) Unwrap() error { return ErrProtocol }

// rspCode is err's rspCode, or 0 when the camera did not answer with one.
func rspCode(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.RspCode
	}
	return 0
}

// sessionsFull is a Login answered with "max session": every session the
// camera allows is taken. Sessions time out, so it passes like a dropped
// connection and is no reason to stop polling.
type sessionsFull struct{ detail string }

func (e *sessionsFull) Error() string   { return errSessionsFull.Error() + "; camera said: " + e.detail }
func (e *sessionsFull) Unwrap() []error { return []error{errSessionsFull, ErrUnreachable} }

func (c *Reolink) endpoint(q url.Values) string {
	return "http://" + c.host + "/cgi-bin/api.cgi?" + q.Encode()
}

// cmd runs one API command, logging in first when needed and once more if
// the camera says the session expired.
func (c *Reolink) cmd(ctx context.Context, name string, param, out any) error {
	stale := ""
	for try := 0; ; try++ {
		token, err := c.login(ctx, stale)
		if err != nil {
			return err
		}
		reply, err := c.post(ctx, name, token, param)
		if err != nil {
			return err
		}
		if reply.Code != 0 {
			if reply.expired() && try == 0 {
				stale = token
				continue
			}
			return reply.err(name)
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(reply.Value, out); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrProtocol, name, err)
		}
		return nil
	}
}

// rejected is why no call may log in: Close, or the remembered ErrAuth of a
// wrong password. nil means none.
func (c *Reolink) rejected() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errClosed
	}
	return c.authErr
}

// login returns a valid session token. stale is the token the camera has
// just refused, or "" when there is none. It logs in when there is no token,
// when the current one is about to expire, or when the current one is stale;
// a token another call has fetched since stale was handed out is reused, so
// a burst of expired calls logs in once.
func (c *Reolink) login(ctx context.Context, stale string) (string, error) {
	if err := c.rejected(); err != nil {
		return "", err // a wrong password: never ask again with it
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	return c.loginLocked(ctx, stale)
}

// retryLogin is the setup test's login: a person asked, so a password the
// camera rejected earlier is tried once more.
func (c *Reolink) retryLogin(ctx context.Context) (string, error) {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	c.mu.Lock()
	c.authErr = nil
	c.mu.Unlock()
	return c.loginLocked(ctx, "")
}

// loginLocked is login with loginMu held.
func (c *Reolink) loginLocked(ctx context.Context, stale string) (string, error) {
	c.mu.Lock()
	token, valid, rejected, closed := c.token, c.token != "" && c.now().Before(c.tokenEnd), c.authErr, c.closed
	c.mu.Unlock()
	if closed {
		return "", errClosed
	}
	if rejected != nil {
		return "", rejected
	}
	if valid && token != stale {
		return token, nil
	}
	if token != "" {
		// Free the old session first: the camera allows only a few.
		c.logout(ctx, token)
		c.mu.Lock()
		c.token, c.tokenEnd = "", time.Time{}
		c.mu.Unlock()
	}
	param := map[string]any{"User": map[string]any{"userName": c.user, "password": c.password}}
	reply, err := c.post(ctx, "Login", "", param)
	if err != nil {
		return "", err
	}
	if reply.Code != 0 {
		if reply.Error != nil && reply.Error.RspCode == rspMaxSession {
			return "", &sessionsFull{detail: reply.detail()}
		}
		// Anything else counts as a wrong password: polling on would risk
		// the camera locking the account.
		err := fmt.Errorf("%w: %s", ErrAuth, reply.detail())
		c.mu.Lock()
		c.authErr = err
		c.mu.Unlock()
		return "", err
	}
	var v struct {
		Token struct {
			LeaseTime int    `json:"leaseTime"`
			Name      string `json:"name"`
		} `json:"Token"`
	}
	if err := json.Unmarshal(reply.Value, &v); err != nil || v.Token.Name == "" {
		return "", fmt.Errorf("%w: login answer has no token", ErrProtocol)
	}
	lease := max(time.Duration(v.Token.LeaseTime)*time.Second, 2*time.Minute)
	c.mu.Lock()
	if c.closed {
		// Closed while logging in: nobody will use or log out this session.
		c.mu.Unlock()
		c.logout(context.WithoutCancel(ctx), v.Token.Name)
		return "", errClosed
	}
	c.token, c.tokenEnd = v.Token.Name, c.now().Add(lease-time.Minute) // renew a minute early
	c.mu.Unlock()
	return v.Token.Name, nil
}

// Close logs the session out, best effort and within logoutTimeout, and
// makes every later call fail without contacting the camera. It does not
// wait for calls in flight: one that logs in meanwhile logs its session out.
func (c *Reolink) Close() {
	c.mu.Lock()
	token := c.token
	c.closed, c.token, c.tokenEnd = true, "", time.Time{}
	c.mu.Unlock()
	c.logout(context.Background(), token)
}

// logout ends the session of token, best effort. It is never sent without a
// token: some firmware misbehaves after a Logout without one.
func (c *Reolink) logout(ctx context.Context, token string) {
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, logoutTimeout)
	defer cancel()
	_, _ = c.post(ctx, "Logout", token, map[string]any{})
}

// post sends one command and returns the camera's first reply.
func (c *Reolink) post(ctx context.Context, name, token string, param any) (apiReply, error) {
	q := url.Values{"cmd": {name}}
	if token != "" {
		q.Set("token", token)
	}
	body, err := json.Marshal([]any{map[string]any{"cmd": name, "action": 0, "param": param}})
	if err != nil {
		return apiReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(q), bytes.NewReader(body))
	if err != nil {
		return apiReply{}, c.unreachable(err) // a bad-URL error would quote the token
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return apiReply{}, c.unreachable(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return apiReply{}, c.unreachable(err)
	}
	c.remember(name, data)
	if res.StatusCode != http.StatusOK {
		return apiReply{}, fmt.Errorf("%w: %s: HTTP %d", ErrProtocol, name, res.StatusCode)
	}
	var replies []apiReply
	if err := json.Unmarshal(data, &replies); err != nil || len(replies) == 0 {
		return apiReply{}, fmt.Errorf("%w: %s: not a camera API answer", ErrProtocol, name)
	}
	return replies[0], nil
}

// unreachable wraps a transport error. The URL is dropped from it because it
// carries the session token.
func (c *Reolink) unreachable(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%w: %s: %v", ErrUnreachable, c.host, err)
}

// Redaction of raw answers works on JSON keys, not on the password's text:
// replacing the password wherever it appears would also mangle, and so
// reveal, any value that happens to contain it ("v3.1.0.2368" with password
// "2368").
var (
	passwordRE = regexp.MustCompile(`(?i)("password"\s*:\s*")(?:[^"\\]|\\.)*(")`)
	tokenRE    = regexp.MustCompile(`("name"\s*:\s*")(?:[^"\\]|\\.)*(")`) // Login's Token.name
)

// remember keeps the camera's last answer for the "raw answer" view, with
// passwords and the session token removed.
func (c *Reolink) remember(cmd string, data []byte) {
	if len(data) > 64<<10 {
		data = data[:64<<10] // the view shows 4000 bytes; spare the regexps a huge non-JPEG
	}
	raw := passwordRE.ReplaceAllString(string(data), "${1}***${2}")
	if cmd == "Login" {
		raw = tokenRE.ReplaceAllString(raw, "${1}***${2}")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		raw = strings.ReplaceAll(raw, c.token, "***") // tokens are long and random
	}
	// Cut only after redacting, so no secret is cut in half and left showing.
	if len(raw) > 4000 {
		raw = strings.ToValidUTF8(raw[:4000], "") + "…"
	}
	c.raw = cmd + ": " + raw
}

func (c *Reolink) lastRaw() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.raw
}

// Snapshot fetches a full-resolution JPEG.
func (c *Reolink) Snapshot(ctx context.Context) ([]byte, error) {
	stale := ""
	for try := 0; ; try++ {
		token, err := c.login(ctx, stale)
		if err != nil {
			return nil, err
		}
		q := url.Values{"cmd": {"Snap"}, "channel": {"0"}, "rs": {strconv.FormatUint(rand.Uint64(), 36)}, "token": {token}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(q), nil)
		if err != nil {
			return nil, c.unreachable(err) // a bad-URL error would quote the token
		}
		res, err := c.client.Do(req)
		if err != nil {
			return nil, c.unreachable(err)
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 20<<20))
		res.Body.Close()
		if err != nil {
			return nil, c.unreachable(err)
		}
		if res.StatusCode == http.StatusOK && http.DetectContentType(data) == "image/jpeg" {
			return data, nil
		}
		// Failures come back as JSON, such as an expired session.
		c.remember("Snap", data)
		var replies []apiReply
		if json.Unmarshal(data, &replies) == nil && len(replies) > 0 && replies[0].Code != 0 {
			if replies[0].expired() && try == 0 {
				stale = token
				continue
			}
			return nil, replies[0].err("Snap")
		}
		return nil, fmt.Errorf("%w: Snap: HTTP %d, %d bytes, not a JPEG", ErrProtocol, res.StatusCode, len(data))
	}
}

// Detect asks whether a vehicle is in the camera's detection zone. Cameras
// that say for certain they have no vehicle detection answer with plain
// motion detection instead. A camera that has reported vehicles keeps doing
// so: a later failure is an error, not a reason to switch.
func (c *Reolink) Detect(ctx context.Context) (Detection, error) {
	if c.getKind() != KindMotion {
		d, unsupported, err := c.detectVehicle(ctx)
		if !unsupported || !c.fallBackToMotion() {
			return d, err
		}
	}
	var md struct {
		State int `json:"state"`
	}
	if err := c.cmd(ctx, "GetMdState", map[string]any{"channel": 0}, &md); err != nil {
		return Detection{}, err
	}
	return Detection{Present: md.State == 1, Kind: KindMotion}, nil
}

// detectVehicle asks GetAiState. unsupported (always with an error) means the
// camera said for certain that it has no vehicle detection: "not support",
// or an answer without vehicle state.
func (c *Reolink) detectVehicle(ctx context.Context) (d Detection, unsupported bool, err error) {
	var v map[string]json.RawMessage
	if err := c.cmd(ctx, "GetAiState", map[string]any{"channel": 0}, &v); err != nil {
		return Detection{}, rspCode(err) == rspNotSupported, err
	}
	present, supported, err := vehicleState(v)
	if err != nil {
		return Detection{}, false, err
	}
	if !supported {
		return Detection{}, true, fmt.Errorf("%w: GetAiState: the camera reports no vehicle detection", ErrProtocol)
	}
	c.setKind(KindVehicle)
	return Detection{Present: present, Kind: KindVehicle}, false, nil
}

func (c *Reolink) getKind() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kind
}

func (c *Reolink) setKind(k string) {
	c.mu.Lock()
	c.kind = k
	c.mu.Unlock()
}

// fallBackToMotion switches to motion detection, unless vehicle detection
// has been seen working.
func (c *Reolink) fallBackToMotion() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kind == KindVehicle {
		return false
	}
	c.kind = KindMotion
	return true
}

// vehicleState reads GetAiState in either shape that firmware uses:
// {"vehicle": {"alarm_state": 1, "support": 1}} or {"vehicle_state": 1}.
// supported is false when the answer has neither, or "support": 0. err means
// the answer could not be read.
func vehicleState(v map[string]json.RawMessage) (present, supported bool, err error) {
	if raw, ok := v["vehicle"]; ok {
		var o struct {
			AlarmState int `json:"alarm_state"`
			Support    int `json:"support"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return false, false, fmt.Errorf("%w: GetAiState: vehicle: %v", ErrProtocol, err)
		}
		return o.AlarmState == 1, o.Support != 0, nil
	}
	if raw, ok := v["vehicle_state"]; ok {
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return false, false, fmt.Errorf("%w: GetAiState: vehicle_state: %v", ErrProtocol, err)
		}
		return n == 1, true, nil
	}
	return false, false, nil
}

// Info reads model, firmware, still size, RTSP state and detection kind.
func (c *Reolink) Info(ctx context.Context) (Info, error) {
	info, _, err := c.readInfo(ctx)
	return info, err
}

// readInfo is Info, plus why the port settings could not be read, if they
// could not: then Info.RTSP is false without RTSP being off.
func (c *Reolink) readInfo(ctx context.Context) (info Info, portsErr, err error) {
	var dev struct {
		DevInfo struct {
			Model   string `json:"model"`
			FirmVer string `json:"firmVer"`
		} `json:"DevInfo"`
	}
	if err := c.cmd(ctx, "GetDevInfo", map[string]any{}, &dev); err != nil {
		return info, nil, err
	}
	info.Model, info.Firmware = dev.DevInfo.Model, dev.DevInfo.FirmVer
	var ports struct {
		NetPort struct {
			RtspEnable int `json:"rtspEnable"`
		} `json:"NetPort"`
	}
	if portsErr = c.cmd(ctx, "GetNetPort", map[string]any{}, &ports); portsErr == nil {
		info.RTSP = ports.NetPort.RtspEnable == 1
	}
	var enc struct {
		Enc struct {
			MainStream struct {
				Size string `json:"size"`
			} `json:"mainStream"`
		} `json:"Enc"`
	}
	if err := c.cmd(ctx, "GetEnc", map[string]any{"channel": 0}, &enc); err == nil {
		info.Width, info.Height = parseSize(enc.Enc.MainStream.Size)
	}
	if d, err := c.Detect(ctx); err == nil {
		info.Detection = d.Kind
	}
	return info, portsErr, nil
}

// parseSize reads Reolink's "3840*2160".
func parseSize(s string) (int, int) {
	a, b, ok := strings.Cut(s, "*")
	if !ok {
		return 0, 0
	}
	w, _ := strconv.Atoi(a)
	h, _ := strconv.Atoi(b)
	return w, h
}

// reach checks that something answers HTTP at the camera's address.
func (c *Reolink) reach(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.host+"/", nil)
	if err != nil {
		return c.unreachable(err)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return c.unreachable(err)
	}
	res.Body.Close()
	return nil
}

// Test checks the connection step by step, so the Camera page can say
// exactly what to fix. It is the one call that tries a rejected password
// again; it keeps a session that is still valid.
func (c *Reolink) Test(ctx context.Context) (r Report) {
	defer func() { r.Raw = c.lastRaw() }()
	step := func(name string, err error, detail string) bool {
		s := Step{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			s.Detail = advice(name, err)
		}
		r.Steps = append(r.Steps, s)
		return err == nil
	}
	if !step("reach", c.reach(ctx), "http://"+c.host) {
		return r
	}
	if _, err := c.retryLogin(ctx); !step("login", err, c.user) {
		return r
	}
	info, portsErr, err := c.readInfo(ctx)
	r.Info = info
	if !step("info", err, strings.TrimSpace(info.Model+" "+info.Firmware)) {
		return r
	}
	switch {
	case portsErr != nil:
		step("ports", fmt.Errorf("%w: %w", errPortsUnread, portsErr), "")
	case !info.RTSP:
		step("ports", errRTSPOff, "")
	default:
		step("ports", nil, "RTSP on")
	}
	switch info.Detection {
	case KindVehicle:
		step("detect", nil, "vehicle detection")
	case KindMotion:
		step("detect", nil, "motion only: this camera cannot tell vehicles from other movement")
	default:
		step("detect", errNoDetection, "")
	}
	still, err := c.Snapshot(ctx)
	detail := ""
	if err == nil {
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(still)); err == nil {
			detail = fmt.Sprintf("%d×%d, %d KB", cfg.Width, cfg.Height, len(still)/1024)
		}
	}
	step("snapshot", err, detail)
	return r
}

// LiveSource is the H.264 sub stream: small, and playable in every browser.
func (c *Reolink) LiveSource() string {
	host := c.host
	if h, _, err := net.SplitHostPort(c.host); err == nil {
		host = h // the HTTP port; RTSP is on 554
	}
	u := url.URL{Scheme: "rtsp", User: url.UserPassword(c.user, c.password), Host: net.JoinHostPort(host, "554"), Path: "/h264Preview_01_sub"}
	return u.String()
}
