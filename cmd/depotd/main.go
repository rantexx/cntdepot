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
	"time"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/httpapi"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/webui"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "dev"

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
	_ = fs.Parse(args)

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

	broker := httpapi.NewBroker()
	svc := gate.New(st, bl, rec, gate.Config{AutoAcceptEnabled: *autoAccept, AutoAcceptThreshold: *threshold}, broker.Publish)
	if err := svc.Start(ctx); err != nil {
		return err
	}

	static := webui.Dist()
	if static == nil {
		log.Print("web app not built yet: run `npm --prefix web run build`, then rebuild depotd")
	}
	server := &http.Server{
		Addr: *addr,
		Handler: httpapi.New(httpapi.Options{
			Service: svc, Store: st, Blobs: bl, Static: static, AccessCode: *accessCode, Version: Version,
		}, broker).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Requests inherit ctx, so live-update streams end on Ctrl+C.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errc := make(chan error, 1)
	go func() { errc <- server.ListenAndServe() }()
	name, model := svc.Engine()
	log.Printf("depotd %s · engine %s (%s) · data %s · auto-accept %v", Version, name, model, data.dir, *autoAccept)
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
	svc.Wait()
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
