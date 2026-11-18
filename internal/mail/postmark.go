package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Postmark struct {
	token, from, baseURL string
	client               *http.Client
}

func NewPostmark(token, from, baseURL string) *Postmark {
	if baseURL == "" {
		baseURL = "https://api.postmarkapp.com"
	}
	return &Postmark{token: token, from: from, baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: 30 * time.Second}}
}

func (p *Postmark) Name() string { return "postmark" }

type pmAttachment struct{ Name, Content, ContentType string }

func (p *Postmark) Send(ctx context.Context, m Outgoing) error {
	atts := make([]pmAttachment, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		atts = append(atts, pmAttachment{a.Name, base64.StdEncoding.EncodeToString(a.Data), a.ContentType})
	}
	body, err := json.Marshal(map[string]any{
		"From": p.from, "To": hdr(strings.Join(m.To, ",")), "Subject": hdr(m.Subject),
		"TextBody": m.Text, "MessageStream": "outbound", "Attachments": atts,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/email", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Postmark-Server-Token", p.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		ErrorCode int
		Message   string
	}
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode/100 != 2 || r.ErrorCode != 0 {
		msg := r.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("postmark: status %d code %d: %s", resp.StatusCode, r.ErrorCode, msg)
	}
	return nil
}

// ParsePostmarkInbound reads Postmark's inbound webhook JSON.
func ParsePostmarkInbound(body []byte) (Inbound, error) {
	var w struct {
		FromFull    struct{ Email string }
		ToFull      []struct{ Email string }
		Subject     string
		TextBody    string
		Headers     []struct{ Name, Value string }
		Attachments []struct{ Name, Content, ContentType string }
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return Inbound{}, fmt.Errorf("postmark inbound: %w", err)
	}
	in := Inbound{From: w.FromFull.Email, Subject: w.Subject, Text: w.TextBody}
	for _, t := range w.ToFull {
		in.To = append(in.To, t.Email)
	}
	in.Trusted = trusted(in.From, w.Headers)
	for _, a := range w.Attachments {
		data, err := base64.StdEncoding.DecodeString(a.Content)
		if err != nil {
			return Inbound{}, fmt.Errorf("postmark inbound attachment %q: %w", a.Name, err)
		}
		in.Attachments = append(in.Attachments, Attachment{Name: a.Name, ContentType: a.ContentType, Data: data})
	}
	return in, nil
}

var spfSender = regexp.MustCompile(`(?i)(?:envelope-from|smtp\.mailfrom)\s*=\s*"?([^;\s"]+)`)
var dkimDomain = regexp.MustCompile(`(?i)header\.d\s*=\s*"?([^;\s"]+)`)

func domainOf(addr string) string {
	addr = strings.Trim(strings.ToLower(strings.TrimSpace(addr)), "<>")
	return addr[strings.LastIndex(addr, "@")+1:]
}

// trusted decides whether the sender's identity is authenticated for the From
// domain. Postmark's Headers carry every header of the message, including ones the
// sender added, so only the FIRST (top-most, i.e. added by the receiving MTA)
// occurrence of each auth header counts, and each signal must align with the From
// domain: a sender passing SPF/DKIM for their own domain must not be able to claim a
// line's address. X-Spam-Tests SPF_PASS is not aligned, so it alone does not trust.
func trusted(from string, headers []struct{ Name, Value string }) bool {
	fd := domainOf(from)
	if fd == "" {
		return false
	}
	seen := map[string]bool{}
	for _, h := range headers {
		name := strings.ToLower(h.Name)
		if seen[name] {
			continue
		}
		seen[name] = true
		v := strings.TrimSpace(h.Value)
		switch name {
		case "x-spam-tests":
			for _, tok := range strings.Split(v, ",") {
				if strings.EqualFold(strings.TrimSpace(tok), "DKIM_VALID_AU") {
					return true
				}
			}
		case "authentication-results":
			for _, seg := range strings.Split(v, ";") {
				m := dkimDomain.FindStringSubmatch(seg)
				if m != nil && strings.Contains(strings.ToLower(seg), "dkim=pass") {
					d := strings.ToLower(m[1])
					if d == fd || strings.HasSuffix(fd, "."+d) {
						return true
					}
				}
			}
		case "received-spf":
			if len(v) >= 4 && strings.EqualFold(v[:4], "pass") {
				if m := spfSender.FindStringSubmatch(v); m != nil && domainOf(m[1]) == fd {
					return true
				}
			}
		}
	}
	return false
}
