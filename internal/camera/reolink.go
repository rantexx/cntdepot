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
type Reolink struct {
	host, user, password string
	client               *http.Client
	now                  func() time.Time

	loginMu sync.Mutex // one login at a time: the camera limits sessions

	mu       sync.Mutex
	token    string
	tokenEnd time.Time
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

// rspLoginFirst is the camera's "please login first": the session expired.
const rspLoginFirst = -6

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

func (c *Reolink) endpoint(q url.Values) string {
	return "http://" + c.host + "/cgi-bin/api.cgi?" + q.Encode()
}

// cmd runs one API command, logging in first when needed and once more if
// the camera says the session expired.
func (c *Reolink) cmd(ctx context.Context, name string, param, out any) error {
	for try := 0; ; try++ {
		token, err := c.login(ctx, try > 0)
		if err != nil {
			return err
		}
		reply, err := c.post(ctx, name, token, param)
		if err != nil {
			return err
		}
		if reply.Code != 0 {
			if reply.expired() && try == 0 {
				continue
			}
			return fmt.Errorf("%w: %s failed: %s", ErrProtocol, name, reply.detail())
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

// login returns a valid session token. It logs in when there is none, when
// the current one is about to expire, or when force is set.
func (c *Reolink) login(ctx context.Context, force bool) (string, error) {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	c.mu.Lock()
	if !force && c.token != "" && c.now().Before(c.tokenEnd) {
		t := c.token
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()
	param := map[string]any{"User": map[string]any{"userName": c.user, "password": c.password}}
	reply, err := c.post(ctx, "Login", "", param)
	if err != nil {
		return "", err
	}
	if reply.Code != 0 {
		return "", fmt.Errorf("%w: %s", ErrAuth, reply.detail())
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
	c.token, c.tokenEnd = v.Token.Name, c.now().Add(lease-time.Minute) // renew a minute early
	c.mu.Unlock()
	return v.Token.Name, nil
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
		return apiReply{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
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

var tokenRE = regexp.MustCompile(`("name"\s*:\s*")[^"]*(")`)

// remember keeps the camera's last answer for the "raw answer" view, with the
// session token and password removed.
func (c *Reolink) remember(cmd string, data []byte) {
	raw := string(data)
	if len(raw) > 4000 {
		raw = raw[:4000] + "…"
	}
	if cmd == "Login" {
		raw = tokenRE.ReplaceAllString(raw, "${1}***${2}")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		raw = strings.ReplaceAll(raw, c.token, "***")
	}
	if c.password != "" {
		raw = strings.ReplaceAll(raw, c.password, "***")
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
	for try := 0; ; try++ {
		token, err := c.login(ctx, try > 0)
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
		if json.Unmarshal(data, &replies) == nil && len(replies) > 0 && replies[0].expired() && try == 0 {
			continue
		}
		return nil, fmt.Errorf("%w: Snap: HTTP %d, %d bytes, not a JPEG", ErrProtocol, res.StatusCode, len(data))
	}
}

// Detect asks whether a vehicle is in the camera's detection zone. Cameras
// without vehicle detection answer with plain motion detection instead.
func (c *Reolink) Detect(ctx context.Context) (Detection, error) {
	c.mu.Lock()
	kind := c.kind
	c.mu.Unlock()
	if kind != KindMotion {
		var v map[string]json.RawMessage
		err := c.cmd(ctx, "GetAiState", map[string]any{"channel": 0}, &v)
		if err != nil && !errors.Is(err, ErrProtocol) {
			return Detection{}, err
		}
		if present, ok := vehicleState(v); err == nil && ok {
			c.setKind(KindVehicle)
			return Detection{Present: present, Kind: KindVehicle}, nil
		}
		c.setKind(KindMotion) // firmware without vehicle detection
	}
	var md struct {
		State int `json:"state"`
	}
	if err := c.cmd(ctx, "GetMdState", map[string]any{"channel": 0}, &md); err != nil {
		return Detection{}, err
	}
	return Detection{Present: md.State == 1, Kind: KindMotion}, nil
}

func (c *Reolink) setKind(k string) {
	c.mu.Lock()
	c.kind = k
	c.mu.Unlock()
}

// vehicleState reads GetAiState in either shape that firmware uses:
// {"vehicle": {"alarm_state": 1, "support": 1}} or {"vehicle_state": 1}.
func vehicleState(v map[string]json.RawMessage) (present, supported bool) {
	if raw, ok := v["vehicle"]; ok {
		var o struct {
			AlarmState int `json:"alarm_state"`
			Support    int `json:"support"`
		}
		if json.Unmarshal(raw, &o) == nil && o.Support == 1 {
			return o.AlarmState == 1, true
		}
		return false, false
	}
	if raw, ok := v["vehicle_state"]; ok {
		var n int
		if json.Unmarshal(raw, &n) == nil {
			return n == 1, true
		}
	}
	return false, false
}

// Info reads model, firmware, still size, RTSP state and detection kind.
func (c *Reolink) Info(ctx context.Context) (Info, error) {
	var info Info
	var dev struct {
		DevInfo struct {
			Model   string `json:"model"`
			FirmVer string `json:"firmVer"`
		} `json:"DevInfo"`
	}
	if err := c.cmd(ctx, "GetDevInfo", map[string]any{}, &dev); err != nil {
		return info, err
	}
	info.Model, info.Firmware = dev.DevInfo.Model, dev.DevInfo.FirmVer
	var ports struct {
		NetPort struct {
			RtspEnable int `json:"rtspEnable"`
		} `json:"NetPort"`
	}
	if err := c.cmd(ctx, "GetNetPort", map[string]any{}, &ports); err == nil {
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
	return info, nil
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
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return c.unreachable(err)
	}
	res.Body.Close()
	return nil
}

// Test checks the connection step by step, so the Camera page can say
// exactly what to fix.
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
	if _, err := c.login(ctx, true); !step("login", err, c.user) {
		return r
	}
	info, err := c.Info(ctx)
	r.Info = info
	if !step("info", err, strings.TrimSpace(info.Model+" "+info.Firmware)) {
		return r
	}
	var portErr error
	if !info.RTSP {
		portErr = errRTSPOff
	}
	step("ports", portErr, "RTSP on")
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
