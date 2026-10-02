package recognition

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultGeminiModel is used unless the operator picks another model.
const DefaultGeminiModel = "gemini-3.8-flash"

const defaultGeminiURL = "https://generativelanguage.googleapis.com"

// GeminiConfig configures the Gemini engine.
type GeminiConfig struct {
	APIKey        string        // required
	Model         string        // empty: DefaultGeminiModel
	ThinkingLevel string        // for example low or high; empty leaves the model default
	BaseURL       string        // empty: the public Gemini API (tests point it at a fake)
	Timeout       time.Duration // per request; zero: 60s
	Retries       int           // extra attempts after temporary failures
}

// Gemini reads photos with the Gemini API.
type Gemini struct {
	cfg    GeminiConfig
	client *http.Client
}

var _ Recognizer = (*Gemini)(nil)

// NewGemini builds the engine.
func NewGemini(cfg GeminiConfig) *Gemini {
	if cfg.Model == "" {
		cfg.Model = DefaultGeminiModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultGeminiURL
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Gemini{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
}

func (g *Gemini) Name() string  { return "gemini" }
func (g *Gemini) Model() string { return g.cfg.Model }

// geminiSchema is the structured-output schema, in Gemini's OpenAPI-style
// responseSchema format.
var geminiSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"candidates": map[string]any{
			"type": "ARRAY",
			"items": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"number":     map[string]any{"type": "STRING"},
					"confidence": map[string]any{"type": "NUMBER"},
				},
				"required": []string{"number", "confidence"},
			},
		},
		"type_code": map[string]any{"type": "STRING"},
		"color":     map[string]any{"type": "STRING"},
		"box_2d":    map[string]any{"type": "ARRAY", "items": map[string]any{"type": "INTEGER"}, "nullable": true},
		"visible":   map[string]any{"type": "STRING", "enum": []string{"full", "partial", "none"}},
		"notes":     map[string]any{"type": "STRING"},
	},
	"required":         []string{"candidates", "type_code", "color", "visible", "notes"},
	"propertyOrdering": []string{"candidates", "type_code", "color", "box_2d", "visible", "notes"},
}

// geminiReading is a Reading in the shape Gemini answers with.
type geminiReading struct {
	Candidates []RawCandidate `json:"candidates"`
	TypeCode   string         `json:"type_code"`
	Color      string         `json:"color"`
	Box2D      []float64      `json:"box_2d"`
	Visible    string         `json:"visible"`
	Notes      string         `json:"notes"`
}

func (r geminiReading) reading() Reading {
	out := Reading{Candidates: r.Candidates, TypeCode: r.TypeCode, Color: r.Color, Visible: r.Visible, Notes: r.Notes}
	if b := r.Box2D; len(b) == 4 && b[0] < b[2] && b[1] < b[3] {
		// [ymin, xmin, ymax, xmax] on a 0-1000 scale → corners on 0-1.
		out.Box = &Box{X0: clamp01(b[1] / 1000), Y0: clamp01(b[0] / 1000), X1: clamp01(b[3] / 1000), Y1: clamp01(b[2] / 1000)}
	}
	return out
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int64 `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

func (g *Gemini) Read(ctx context.Context, img Image) (Attempt, error) {
	att := Attempt{Engine: g.Name(), Model: g.cfg.Model, PromptVersion: PromptVersion}
	start := time.Now()
	body, err := json.Marshal(g.request(img))
	if err != nil {
		return att, err
	}

	var resp geminiResponse
	for try := 0; ; try++ {
		resp = geminiResponse{}
		err = g.post(ctx, body, &resp)
		if err == nil || !errors.Is(err, ErrUnavailable) || try >= g.cfg.Retries {
			break
		}
		select {
		case <-time.After(time.Duration(try+1) * time.Second):
		case <-ctx.Done():
			att.LatencyMS = time.Since(start).Milliseconds()
			return att, ctx.Err()
		}
	}
	att.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return att, err
	}

	// Thinking tokens are billed as output.
	u := resp.UsageMetadata
	att.Usage = Usage{InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount}
	if resp.PromptFeedback.BlockReason != "" {
		return att, fmt.Errorf("%w: blocked (%s)", ErrRefused, resp.PromptFeedback.BlockReason)
	}
	if len(resp.Candidates) == 0 {
		return att, fmt.Errorf("%w: no answer", ErrRefused)
	}
	c := resp.Candidates[0]
	var text strings.Builder
	for _, p := range c.Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	att.Raw = text.String()
	switch c.FinishReason {
	case "", "STOP":
	case "MAX_TOKENS":
		return att, ErrTruncated
	default:
		return att, fmt.Errorf("%w: finish reason %s", ErrRefused, c.FinishReason)
	}

	var gr geminiReading
	if err := decodeJSON(att.Raw, &gr); err != nil {
		return att, err
	}
	att.Reading = gr.reading()
	return att, nil
}

func (g *Gemini) request(img Image) map[string]any {
	config := map[string]any{
		"responseMimeType": "application/json",
		"responseSchema":   geminiSchema,
	}
	if g.cfg.ThinkingLevel != "" {
		config["thinkingConfig"] = map[string]any{"thinkingLevel": g.cfg.ThinkingLevel}
	}
	image := map[string]any{"inlineData": map[string]any{
		"mimeType": img.MediaType,
		"data":     base64.StdEncoding.EncodeToString(img.Data),
	}}
	if img.Detail == DetailUltraHigh {
		// Per-part resolution (Gemini 3): twice the image tokens of the
		// default, for container marks that are small in a wide 4K still.
		image["mediaResolution"] = map[string]any{"level": "MEDIA_RESOLUTION_ULTRA_HIGH"}
	}
	return map[string]any{
		"contents": []any{map[string]any{
			"role":  "user",
			"parts": []any{image, map[string]any{"text": Prompt}},
		}},
		"generationConfig": config,
	}
}

// post sends one request and maps HTTP failures onto engine errors.
func (g *Gemini) post(ctx context.Context, body []byte, out *geminiResponse) error {
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", strings.TrimRight(g.cfg.BaseURL, "/"), g.cfg.Model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.cfg.APIKey)
	res, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if res.StatusCode >= 300 {
		return classifyHTTP(res.StatusCode, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: %v", ErrUnparseable, err)
	}
	return nil
}

// classifyHTTP maps a Gemini error answer onto engine errors. An invalid key
// comes back as 400 INVALID_ARGUMENT, not 401.
func classifyHTTP(status int, body []byte) error {
	text := string(body)
	detail := strings.TrimSpace(text)
	if len(detail) > 200 {
		detail = detail[:200]
	}
	switch {
	case status == 401 || status == 403 || strings.Contains(text, "API_KEY_INVALID") || strings.Contains(text, "API key not valid"):
		return fmt.Errorf("%w: HTTP %d", ErrAuth, status)
	case status == 408 || status == 429 || status >= 500:
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, status)
	default:
		return fmt.Errorf("%w: HTTP %d: %s", ErrBadRequest, status, detail)
	}
}
