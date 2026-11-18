package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		"From": p.from, "To": strings.Join(m.To, ","), "Subject": m.Subject,
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
	for _, h := range w.Headers {
		v := strings.ToLower(strings.TrimSpace(h.Value))
		switch strings.ToLower(h.Name) {
		case "received-spf":
			if strings.HasPrefix(v, "pass") {
				in.Trusted = true
			}
		case "authentication-results":
			if strings.Contains(v, "dkim=pass") {
				in.Trusted = true
			}
		}
	}
	for _, a := range w.Attachments {
		data, err := base64.StdEncoding.DecodeString(a.Content)
		if err != nil {
			return Inbound{}, fmt.Errorf("postmark inbound attachment %q: %w", a.Name, err)
		}
		in.Attachments = append(in.Attachments, Attachment{Name: a.Name, ContentType: a.ContentType, Data: data})
	}
	return in, nil
}
