package recognition

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"
)

// Fake is a scripted engine for tests and for running the hub without an API
// key (`depotd hub -engine fake`).
type Fake struct {
	Reading Reading            // returned for any image not in ByImage
	ByImage map[string]Reading // keyed by the image bytes as a string
	Err     error              // when set, every Read fails with it
	Delay   time.Duration      // simulated engine latency
	calls   atomic.Int64
}

func (f *Fake) Name() string  { return "fake" }
func (f *Fake) Model() string { return "fake-1" }

// Calls reports how many reads have been made.
func (f *Fake) Calls() int64 { return f.calls.Load() }

func (f *Fake) Read(ctx context.Context, img Image) (Attempt, error) {
	f.calls.Add(1)
	start := time.Now()
	att := Attempt{Engine: f.Name(), Model: f.Model(), PromptVersion: PromptVersion}
	if f.Delay > 0 {
		select {
		case <-time.After(f.Delay):
		case <-ctx.Done():
			return att, ctx.Err()
		}
	}
	att.LatencyMS = time.Since(start).Milliseconds()
	if f.Err != nil {
		return att, f.Err
	}
	r := f.Reading
	if scripted, ok := f.ByImage[string(img.Data)]; ok {
		r = scripted
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return att, err
	}
	att.Reading, att.Raw = r, string(raw)
	return att, nil
}

// NewDemoFake reads every photo as the ISO reference container, so the whole
// app can be tried end to end without an API key.
func NewDemoFake() *Fake {
	return &Fake{
		Delay: 700 * time.Millisecond,
		Reading: Reading{
			Candidates: []RawCandidate{{Number: "CSQU3054383", Confidence: 0.92}},
			TypeCode:   "45G1",
			Color:      "blue",
			Visible:    "full",
			Box:        &Box{X0: 0.30, Y0: 0.20, X1: 0.62, Y1: 0.30},
			Notes:      "demo engine",
		},
	}
}
