package recognition

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const geminiOK = `{"candidates":[{"content":{"role":"model","parts":[
{"text":"{\"candidates\":[{\"number\":\"CSQU3054383\",\"confidence\":0.93}],\"type_code\":\"45G1\",\"color\":\"blue\",\"box_2d\":[200,100,300,500],\"visible\":\"full\",\"notes\":\"\"}"}]},
"finishReason":"STOP"}],
"usageMetadata":{"promptTokenCount":1100,"candidatesTokenCount":60,"thoughtsTokenCount":40,"totalTokenCount":1200}}`

// fakeGemini answers with the given status and bodies in turn (the last one repeats).
func fakeGemini(t *testing.T, status []int, bodies []string, inspect func(r *http.Request, payload map[string]any)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if want := "/v1beta/models/" + DefaultGeminiModel + ":generateContent"; r.URL.Path != want {
			t.Errorf("path = %s; want %s", r.URL.Path, want)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if inspect != nil {
			inspect(r, payload)
		}
		i := min(n, len(bodies)-1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status[min(n, len(status)-1)])
		_, _ = io.WriteString(w, bodies[i])
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func testGemini(srv *httptest.Server, retries int) *Gemini {
	return NewGemini(GeminiConfig{APIKey: "test-key", ThinkingLevel: "low", BaseURL: srv.URL, Retries: retries})
}

// dig walks nested JSON maps and arrays: dig(m, "a", 0, "b").
func dig(node any, path ...any) any {
	for _, p := range path {
		switch key := p.(type) {
		case string:
			m, ok := node.(map[string]any)
			if !ok {
				return nil
			}
			node = m[key]
		case int:
			a, ok := node.([]any)
			if !ok || key >= len(a) {
				return nil
			}
			node = a[key]
		}
	}
	return node
}

var jpeg = Image{Data: []byte("\xff\xd8\xff\xe0fake-jpeg"), MediaType: "image/jpeg"}

func TestGeminiSendsImageSchemaAndThinking(t *testing.T) {
	srv, _ := fakeGemini(t, []int{200}, []string{geminiOK}, func(r *http.Request, p map[string]any) {
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key = %q", got)
		}
		if got := dig(p, "contents", 0, "parts", 0, "inlineData", "mimeType"); got != "image/jpeg" {
			t.Errorf("image mime type = %v", got)
		}
		if got := dig(p, "contents", 0, "parts", 0, "inlineData", "data"); got != base64.StdEncoding.EncodeToString(jpeg.Data) {
			t.Errorf("image data = %v", got)
		}
		if got := dig(p, "contents", 0, "parts", 1, "text"); got != Prompt {
			t.Errorf("second part is not the prompt")
		}
		if got := dig(p, "generationConfig", "responseMimeType"); got != "application/json" {
			t.Errorf("responseMimeType = %v", got)
		}
		if got := dig(p, "generationConfig", "responseSchema", "type"); got != "OBJECT" {
			t.Errorf("responseSchema.type = %v", got)
		}
		if got := dig(p, "generationConfig", "thinkingConfig", "thinkingLevel"); got != "low" {
			t.Errorf("thinkingLevel = %v", got)
		}
	})
	att, err := testGemini(srv, 0).Read(context.Background(), jpeg)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	r := att.Reading
	if len(r.Candidates) != 1 || r.Candidates[0].Number != "CSQU3054383" || r.TypeCode != "45G1" || r.Color != "blue" {
		t.Fatalf("reading = %+v", r)
	}
	if r.Box == nil || *r.Box != (Box{X0: 0.1, Y0: 0.2, X1: 0.5, Y1: 0.3}) {
		t.Fatalf("box = %+v; want x0 0.1 y0 0.2 x1 0.5 y1 0.3", r.Box)
	}
	if att.Usage.InputTokens != 1100 || att.Usage.OutputTokens != 100 || att.Engine != "gemini" || att.Model != DefaultGeminiModel {
		t.Fatalf("attempt = %+v", att)
	}
}

func TestGeminiSkipsThoughtParts(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"Looking at the doors...","thought":true},
{"text":"{\"candidates\":[{\"number\":\"CSQU3054383\",\"confidence\":0.9}],\"type_code\":\"\",\"color\":\"\",\"box_2d\":null,\"visible\":\"full\",\"notes\":\"\"}"}]},"finishReason":"STOP"}]}`
	srv, _ := fakeGemini(t, []int{200}, []string{body}, nil)
	att, err := testGemini(srv, 0).Read(context.Background(), jpeg)
	if err != nil || att.Reading.Candidates[0].Number != "CSQU3054383" || att.Reading.Box != nil {
		t.Fatalf("Read = %+v, %v", att.Reading, err)
	}
}

func TestGeminiInvalidKeyIsAuthError(t *testing.T) {
	body := `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`
	srv, _ := fakeGemini(t, []int{400}, []string{body}, nil)
	if _, err := testGemini(srv, 2).Read(context.Background(), jpeg); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v; want ErrAuth", err)
	}
}

func TestGeminiOverloadIsUnavailable(t *testing.T) {
	srv, calls := fakeGemini(t, []int{503}, []string{`{"error":{"code":503,"status":"UNAVAILABLE"}}`}, nil)
	if _, err := testGemini(srv, 0).Read(context.Background(), jpeg); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v; want ErrUnavailable", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d; want 1 with no retries", calls.Load())
	}
}

func TestGeminiRetriesTemporaryFailures(t *testing.T) {
	srv, calls := fakeGemini(t, []int{503, 200}, []string{`{"error":{"code":503}}`, geminiOK}, nil)
	att, err := testGemini(srv, 1).Read(context.Background(), jpeg)
	if err != nil || att.Reading.Candidates[0].Number != "CSQU3054383" {
		t.Fatalf("Read = %+v, %v", att.Reading, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d; want 2", calls.Load())
	}
}

func TestGeminiBlockedIsRefused(t *testing.T) {
	srv, _ := fakeGemini(t, []int{200}, []string{`{"promptFeedback":{"blockReason":"SAFETY"}}`}, nil)
	if _, err := testGemini(srv, 0).Read(context.Background(), jpeg); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v; want ErrRefused", err)
	}
}

func TestGeminiMaxTokensIsTruncated(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"text":"{\"candidates\":["}]},"finishReason":"MAX_TOKENS"}]}`
	srv, _ := fakeGemini(t, []int{200}, []string{body}, nil)
	if _, err := testGemini(srv, 0).Read(context.Background(), jpeg); !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v; want ErrTruncated", err)
	}
}
