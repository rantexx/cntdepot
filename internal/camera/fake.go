package camera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"sync"
	"time"
)

// Fake is a scripted camera for tests and for trying the hub without one
// (`depotd hub -fake-camera`).
type Fake struct {
	mu        sync.Mutex
	stills    [][]byte
	next      int
	vehicle   bool
	cycle     time.Duration // when set, a vehicle comes and goes on this period
	start     time.Time
	snapErr   error
	detectErr error
	snaps     int
	detects   int
}

var _ Camera = (*Fake)(nil)

// NewFake serves stills in turn and reports no vehicle until SetVehicle.
func NewFake(stills ...[]byte) *Fake { return &Fake{stills: stills, start: time.Now()} }

// NewDemoFake serves stills in turn (a generated still when none are given)
// and reports a vehicle for 20 seconds out of every 40.
func NewDemoFake(stills ...[]byte) *Fake {
	if len(stills) == 0 {
		stills = [][]byte{DemoStill()}
	}
	f := NewFake(stills...)
	f.cycle = 20 * time.Second
	return f
}

func (f *Fake) SetVehicle(present bool) { f.mu.Lock(); f.vehicle = present; f.mu.Unlock() }
func (f *Fake) SetSnapErr(err error)    { f.mu.Lock(); f.snapErr = err; f.mu.Unlock() }
func (f *Fake) SetDetectErr(err error)  { f.mu.Lock(); f.detectErr = err; f.mu.Unlock() }

// Snaps counts stills served; Detects counts detection queries.
func (f *Fake) Snaps() int   { f.mu.Lock(); defer f.mu.Unlock(); return f.snaps }
func (f *Fake) Detects() int { f.mu.Lock(); defer f.mu.Unlock(); return f.detects }

func (f *Fake) Info(ctx context.Context) (Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detectErr != nil {
		return Info{}, f.detectErr
	}
	return Info{Model: "Fake camera", Firmware: "demo", Width: 1280, Height: 720, Detection: KindVehicle}, nil
}

func (f *Fake) Snapshot(ctx context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapErr != nil {
		return nil, f.snapErr
	}
	if len(f.stills) == 0 {
		return nil, fmt.Errorf("%w: the fake camera has no stills", ErrProtocol)
	}
	s := f.stills[f.next%len(f.stills)]
	f.next++
	f.snaps++
	return s, nil
}

func (f *Fake) Detect(ctx context.Context) (Detection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detects++
	if f.detectErr != nil {
		return Detection{}, f.detectErr
	}
	present := f.vehicle
	if f.cycle > 0 {
		present = (time.Since(f.start)/f.cycle)%2 == 1
	}
	return Detection{Present: present, Kind: KindVehicle}, nil
}

// LiveSource is empty: a fake camera has no video, so pages show stills.
func (f *Fake) LiveSource() string { return "" }

// Close does nothing: a fake camera has no session, and the -fake-camera one
// stays in use whatever happens to the saved settings.
func (f *Fake) Close() {}

func (f *Fake) Test(ctx context.Context) Report {
	info, err := f.Info(ctx)
	if err != nil {
		return Report{Steps: []Step{{Name: "reach", Detail: advice("reach", err)}}}
	}
	snap := Step{Name: "snapshot", OK: true, Detail: "1280×720"}
	f.mu.Lock()
	if f.snapErr != nil {
		snap = Step{Name: "snapshot", Detail: advice("snapshot", f.snapErr)}
	}
	f.mu.Unlock()
	return Report{Info: info, Raw: "fake camera", Steps: []Step{
		{Name: "reach", OK: true, Detail: "fake camera"},
		{Name: "login", OK: true},
		{Name: "info", OK: true, Detail: info.Model},
		{Name: "ports", OK: true, Detail: "a fake camera has no live video"},
		{Name: "detect", OK: true, Detail: "vehicle detection"},
		snap,
	}}
}

// DemoStill is a plain picture standing in for a camera still: a blue
// container side with a white plate where the number would be. The demo
// engine's number box covers the plate.
func DemoStill() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 30, G: 80, B: 160, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(384, 144, 794, 216), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}
