package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"depot/internal/bench"
	"depot/internal/blob"
	"depot/internal/camera"
	"depot/internal/dataset"
	"depot/internal/gate"
	"depot/internal/gatecam"
	"depot/internal/httpapi"
	"depot/internal/live"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/webui"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "dev"

// The camera watcher drives gate-ins through the gate service.
var _ gatecam.Gate = (*gate.Service)(nil)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	case "hub":
		err = runHub(os.Args[2:])
	case "export-dataset":
		err = runExport(os.Args[2:])
	case "bench":
		err = runBench(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: depotd <command> [flags]

commands:
  version          print the version
  hub              run the gate hub (web app + API + reading) on this computer
  export-dataset   write verified photos and labels for model training
  bench            re-read verified moves with an engine and report accuracy

run "depotd <command> -h" for a command's flags`)
}

// dataFlags locate the hub's database and photos.
type dataFlags struct{ dir, tenant, depot string }

func (d *dataFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&d.dir, "data", "depot-data", "folder for the database and photos")
	fs.StringVar(&d.tenant, "tenant", "local", "company id (stays local until the cloud exists)")
	fs.StringVar(&d.depot, "depot", "own-depot", "depot id")
}

func (d *dataFlags) open() (*store.Store, *blob.Store, error) {
	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(filepath.Join(d.dir, "depot.db"), d.tenant, d.depot)
	if err != nil {
		return nil, nil, err
	}
	bl, err := blob.Open(filepath.Join(d.dir, "blobs"))
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, bl, nil
}

// engineFlags choose and configure the reading engine.
type engineFlags struct{ engine, model, thinking string }

func (e *engineFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&e.engine, "engine", "gemini", "reading engine: gemini | fake")
	fs.StringVar(&e.model, "model", envOr("DEPOT_GEMINI_MODEL", recognition.DefaultGeminiModel), "Gemini model id")
	fs.StringVar(&e.thinking, "thinking", envOr("DEPOT_GEMINI_THINKING", "low"), "Gemini thinking level (empty: model default)")
}

func (e *engineFlags) build() (recognition.Recognizer, error) {
	switch e.engine {
	case "fake":
		return recognition.NewDemoFake(), nil
	case "gemini":
		key := envOr("GEMINI_API_KEY", os.Getenv("GOOGLE_API_KEY"))
		if key == "" {
			return nil, errors.New("GEMINI_API_KEY is not set: set it in your own terminal " +
				"(setx GEMINI_API_KEY ...), open a new terminal, or run with -engine fake")
		}
		return recognition.NewGemini(recognition.GeminiConfig{
			APIKey: key, Model: e.model, ThinkingLevel: e.thinking, Retries: 2,
		}), nil
	default:
		return nil, fmt.Errorf("unknown engine %q (use gemini or fake)", e.engine)
	}
}

func runHub(args []string) error {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	var data dataFlags
	data.register(fs)
	var eng engineFlags
	eng.register(fs)
	addr := fs.String("addr", ":8080", "listen address")
	autoAccept := fs.Bool("auto-accept", false, "accept confident, check-digit-valid reads without a person")
	threshold := fs.Float64("threshold", 0.85, "confidence needed for auto-accept")
	accessCode := fs.String("access-code", os.Getenv("DEPOT_ACCESS_CODE"), "code people must enter to use the app (empty: none)")
	fakeCamera := fs.Bool("fake-camera", false, "use a simulated camera (try the Camera page without one)")
	fakePhotos := fs.String("fake-camera-photos", "", "folder of JPEG photos the simulated camera serves in turn (implies -fake-camera)")
	go2rtcPath := fs.String("go2rtc", defaultGo2rtc(), "go2rtc executable for live video")
	_ = fs.Parse(args)
	if *fakePhotos != "" {
		*fakeCamera = true
	}

	rec, err := eng.build()
	if err != nil {
		return err
	}
	st, bl, err := data.open()
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Take the port before anything is read: a busy port must fail fast, not
	// after photos left over from the last run were sent to the paid engine.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	broker := httpapi.NewBroker()
	svc := gate.New(st, bl, rec, gate.Config{AutoAcceptEnabled: *autoAccept, AutoAcceptThreshold: *threshold}, broker.Publish)

	watcher := gatecam.New(svc, gatecam.DefaultConfig())
	svc.SetReadHook(watcher.OnRead)
	liveVideo := live.New(live.Config{Binary: *go2rtcPath, DataDir: data.dir})
	cams := gatecam.NewManager(st, watcher, liveVideo, func(s camera.Settings) camera.Camera { return camera.NewReolink(s) })
	if *fakeCamera {
		stills, err := loadStills(*fakePhotos)
		if err != nil {
			return err
		}
		cams.UseFixed(camera.NewDemoFake(stills...))
	}
	if err := cams.Load(ctx); err != nil {
		return err
	}
	if err := svc.Start(ctx); err != nil {
		return err
	}
	var background sync.WaitGroup
	background.Add(2)
	go func() { defer background.Done(); watcher.Run(ctx) }()
	go func() { defer background.Done(); liveVideo.Run(ctx) }() // stops go2rtc on exit

	static := webui.Dist()
	if static == nil {
		log.Print("web app not built yet: run `npm --prefix web run build`, then rebuild depotd")
	}
	server := &http.Server{
		Addr: *addr,
		Handler: httpapi.New(httpapi.Options{
			Service: svc, Store: st, Blobs: bl, Static: static, AccessCode: *accessCode, Version: Version,
			Camera: cams, Live: liveVideo,
		}, broker).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Requests inherit ctx, so live-update streams end on Ctrl+C.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errc := make(chan error, 1)
	go func() { errc <- server.Serve(ln) }()
	name, model := svc.Engine()
	log.Printf("depotd %s · engine %s (%s) · data %s · auto-accept %v", Version, name, model, data.dir, *autoAccept)
	switch s := cams.Settings(); {
	case *fakeCamera:
		if *fakePhotos != "" {
			log.Printf("camera: simulated, serving the photos in %s", *fakePhotos)
		} else {
			log.Print("camera: simulated (-fake-camera)")
		}
	case s.Configured():
		log.Printf("camera: %s (automatic photos %v)", s.Host, s.Auto)
	default:
		log.Print("camera: not set up; open the Camera page")
	}
	for _, u := range lanURLs(*addr) {
		log.Printf("open: %s", u)
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	background.Wait()
	cams.Close() // log out of the camera: it allows only a few sessions
	svc.Wait()
	return nil
}

func runExport(args []string) error {
	fs := flag.NewFlagSet("export-dataset", flag.ExitOnError)
	var data dataFlags
	data.register(fs)
	out := fs.String("out", "dataset-export", "folder to write the dataset into")
	_ = fs.Parse(args)

	st, bl, err := data.open()
	if err != nil {
		return err
	}
	defer st.Close()
	sum, err := dataset.Export(context.Background(), st, bl, *out)
	if err != nil {
		return err
	}
	fmt.Printf("exported %d verified moves, %d photos (%d with number boxes, %d unread camera stills in images-unread) to %s\n",
		sum.Moves, sum.Photos, sum.Boxes, sum.Unread, *out)
	return nil
}

func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	var data dataFlags
	data.register(fs)
	var eng engineFlags
	eng.register(fs)
	limit := fs.Int("limit", 0, "only the first N verified moves (0: all)")
	threshold := fs.Float64("threshold", 0.85, "auto-accept threshold used when combining photos")
	yes := fs.Bool("yes", false, "confirm that every verified photo is sent to the paid engine")
	_ = fs.Parse(args)

	if eng.engine != "fake" && !*yes {
		return errors.New("bench sends every verified photo to the engine, which costs money: re-run with -yes")
	}
	rec, err := eng.build()
	if err != nil {
		return err
	}
	st, bl, err := data.open()
	if err != nil {
		return err
	}
	defer st.Close()

	rep, err := bench.Run(context.Background(), st, bl, rec, *threshold, *limit)
	if err != nil {
		return err
	}
	name, model := rec.Name(), rec.Model()
	fmt.Printf("engine      %s (%s)\n", name, model)
	fmt.Printf("moves       %d/%d correct (%.1f%%)\n", rep.MovesCorrect, rep.Moves, 100*rep.MoveAccuracy())
	fmt.Printf("photos      %d/%d correct (%.1f%%), %d engine errors\n", rep.PhotosCorrect, rep.Photos, 100*rep.PhotoAccuracy(), rep.Errors)
	fmt.Printf("tokens      %d in / %d out\n", rep.InputTokens, rep.OutputTokens)
	fmt.Printf("avg latency %d ms per photo\n", rep.AvgLatencyMS())
	for _, m := range rep.Misses {
		fmt.Printf("miss        %s want %s got %q (%s)\n", m.MoveID, m.Want, m.Got, m.Reason)
	}
	return nil
}

// lanURLs lists the addresses phones on the same network can open.
func lanURLs(addr string) []string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	urls := []string{"http://localhost:" + port}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return urls
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		urls = append(urls, fmt.Sprintf("http://%s:%s", ipnet.IP, port))
	}
	return urls
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultGo2rtc is go2rtc next to depotd itself (bin\go2rtc.exe).
func defaultGo2rtc() string {
	name := "go2rtc"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe, err := os.Executable()
	if err != nil {
		return name
	}
	return filepath.Join(filepath.Dir(exe), name)
}

// loadStills reads the JPEG photos in dir, in name order; an empty dir means none.
func loadStills(dir string) ([][]byte, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("-fake-camera-photos: %w", err)
	}
	var stills [][]byte
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".jpg", ".jpeg":
		default:
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("-fake-camera-photos: %w", err)
		}
		stills = append(stills, b)
	}
	if len(stills) == 0 {
		return nil, fmt.Errorf("-fake-camera-photos: no .jpg photos in %s", dir)
	}
	return stills, nil
}
