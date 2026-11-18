package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"depot/internal/auth"
	"depot/internal/bench"
	"depot/internal/blob"
	"depot/internal/camera"
	"depot/internal/dataset"
	"depot/internal/db"
	"depot/internal/edi"
	"depot/internal/gate"
	"depot/internal/gatecam"
	"depot/internal/httpapi"
	"depot/internal/live"
	"depot/internal/mail"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/tenancy"
	"depot/internal/webui"
	"depot/internal/yard"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "dev"

// The camera watcher drives gate-ins through the gate service.
var _ gatecam.Gate = (*gate.Service)(nil)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errUsage) || errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func run(args []string) error {
	if len(args) < 1 {
		usage()
		return errUsage
	}
	switch args[0] {
	case "version":
		fmt.Println(Version)
		return nil
	case "migrate":
		return runMigrate(args[1:])
	case "api":
		return runAPI(args[1:])
	case "hub":
		return runHub(args[1:])
	case "export-dataset":
		return runExport(args[1:])
	case "bench":
		return runBench(args[1:])
	}
	usage()
	return errUsage
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: depotd <command> [flags]

commands:
  version          print the version
  migrate          create or update the database schema and the app role
  api              run the web app + API against a PostgreSQL database
  hub              run everything on this computer: own database, camera, web app
  export-dataset   write verified photos and labels for model training
  bench            re-read verified moves with an engine and report accuracy

run "depotd <command> -h" for a command's flags`)
}

// flagOutput is where -h and flag errors are printed (a test swaps it).
var flagOutput io.Writer = os.Stderr

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(flagOutput)
	return fs
}

// envFallback fills *v from the environment after parsing. Secrets never
// are flag defaults: -h and parse errors print defaults.
func envFallback(v *string, key string) {
	if *v == "" {
		*v = os.Getenv(key)
	}
}

// engineFlags choose and configure the reading engine.
type engineFlags struct{ engine, model, thinking, fallbacks string }

func (e *engineFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&e.engine, "engine", "gemini", "reading engine: gemini | fake")
	fs.StringVar(&e.model, "model", envOr("DEPOT_GEMINI_MODEL", recognition.DefaultGeminiModel), "Gemini model id")
	fs.StringVar(&e.thinking, "thinking", envOr("DEPOT_GEMINI_THINKING", "low"), "Gemini thinking level (empty: model default)")
	fs.StringVar(&e.fallbacks, "fallback-models", "", "comma-separated Gemini models to try when the main one is overloaded (empty: default)")
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
		var fb []string
		for _, m := range strings.Split(e.fallbacks, ",") {
			if m = strings.TrimSpace(m); m != "" {
				fb = append(fb, m)
			}
		}
		return recognition.NewGemini(recognition.GeminiConfig{
			APIKey: key, Model: e.model, ThinkingLevel: e.thinking, Retries: 2, Fallbacks: fb,
		}), nil
	default:
		return nil, fmt.Errorf("unknown engine %q (use gemini or fake)", e.engine)
	}
}

// mailFlags configure EDI mail; each falls back to its environment variable.
// Secrets (the Postmark token and webhook secret) are environment only.
type mailFlags struct{ driver, dir, from, tick string }

func (m *mailFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&m.driver, "mail", "", "EDI mail driver: file | postmark (env MAIL_DRIVER, default file; postmark needs env POSTMARK_SERVER_TOKEN)")
	fs.StringVar(&m.dir, "mail-dir", "", "folder the file driver writes .eml files to (env MAIL_DIR)")
	fs.StringVar(&m.from, "mail-from", "", "address EDI mail is sent from (env MAIL_FROM, default depot@localhost)")
	fs.StringVar(&m.tick, "edi-tick", "", "how often the EDI worker runs (env EDI_TICK, default 30s)")
}

// build makes the EDI mailer and config. defaultDir is the file driver's
// folder when none is given; "" makes one required.
func (m *mailFlags) build(defaultDir string) (mail.Mailer, edi.Config, error) {
	envFallback(&m.driver, "MAIL_DRIVER")
	envFallback(&m.dir, "MAIL_DIR")
	envFallback(&m.from, "MAIL_FROM")
	envFallback(&m.tick, "EDI_TICK")
	cfg := edi.Config{From: cmp.Or(m.from, "depot@localhost"), InboundDomain: os.Getenv("EDI_INBOUND_DOMAIN")}
	tick, err := time.ParseDuration(cmp.Or(m.tick, "30s"))
	if err != nil || tick <= 0 {
		return nil, cfg, fmt.Errorf("-edi-tick (EDI_TICK) must be a positive duration such as 30s, not %q", m.tick)
	}
	cfg.Tick = tick
	switch cmp.Or(m.driver, "file") {
	case "file":
		dir := cmp.Or(m.dir, defaultDir)
		if dir == "" {
			return nil, cfg, errors.New("the file mail driver needs -mail-dir (MAIL_DIR), the folder .eml files are written to")
		}
		return mail.NewFile(dir, cfg.From), cfg, nil
	case "postmark":
		token := os.Getenv("POSTMARK_SERVER_TOKEN")
		if token == "" {
			return nil, cfg, errors.New("the postmark mail driver needs POSTMARK_SERVER_TOKEN set in the environment")
		}
		return mail.NewPostmark(token, cfg.From, ""), cfg, nil
	}
	return nil, cfg, fmt.Errorf("-mail (MAIL_DRIVER) must be file or postmark, not %q", m.driver)
}

func runMigrate(args []string) error {
	fs := newFlags("migrate")
	owner := fs.String("db-owner", "", "owner database URL (env DATABASE_OWNER_URL)")
	pw := fs.String("app-password", "", "password for the depot_app role (env DATABASE_APP_PASSWORD)")
	admin := fs.String("platform-admin", "", "email to add as platform admin (env PLATFORM_ADMIN_EMAIL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	envFallback(owner, "DATABASE_OWNER_URL")
	envFallback(pw, "DATABASE_APP_PASSWORD")
	envFallback(admin, "PLATFORM_ADMIN_EMAIL")
	if *owner == "" || *pw == "" {
		return errors.New("migrate needs -db-owner (DATABASE_OWNER_URL) and -app-password (DATABASE_APP_PASSWORD)")
	}
	if err := db.Migrate(context.Background(), *owner, db.MigrateOptions{AppPassword: *pw, PlatformAdminEmail: *admin}); err != nil {
		return err
	}
	fmt.Println("migrated")
	return nil
}

// app is what api and hub share: the database, the gate service and sessions.
type app struct {
	pool   *pgxpool.Pool
	st     *store.Store
	bl     *blob.Store
	svc    *gate.Service
	broker *httpapi.Broker
	sess   *auth.Sessions
	yard   *yard.Service
	edi    *edi.Service
}

func newApp(pool *pgxpool.Pool, blobs string, rec recognition.Recognizer, cfg gate.Config, secure bool, m mail.Mailer, ec edi.Config) (*app, error) {
	bl, err := blob.Open(blobs)
	if err != nil {
		return nil, err
	}
	st := store.New(pool)
	broker := httpapi.NewBroker()
	sess := auth.NewSessions(pool, 0)
	sess.SetSecureCookies(secure)
	y := yard.NewService(yard.NewStore(pool), st, broker.Notify)
	return &app{pool: pool, st: st, bl: bl, broker: broker, sess: sess, yard: y,
		svc: gate.New(st, bl, rec, cfg, broker.Publish),
		edi: edi.NewService(pool, bl, y, m, ec)}, nil
}

func runAPI(args []string) error {
	fs := newFlags("api")
	dbURL := fs.String("db", "", "depot_app database URL (env DATABASE_URL)")
	addr := fs.String("addr", "", "listen address (default 127.0.0.1:8080 with dev login, :8080 without)")
	env := fs.String("env", "development", "development | production")
	devLogin := fs.Bool("dev-login", true, "allow dev login (default: on in development; refused in production)")
	blobs := fs.String("blobs", filepath.Join("depot-data", "blobs"), "folder for photos")
	autoAccept := fs.Bool("auto-accept", false, "accept confident, check-digit-valid reads without a person")
	threshold := fs.Float64("threshold", 0.85, "confidence needed for auto-accept")
	public := fs.String("public-host", "", "comma-separated domain names the API answers to, e.g. app.example.com (env PUBLIC_HOST)")
	var eng engineFlags
	eng.register(fs)
	var mf mailFlags
	mf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	envFallback(dbURL, "DATABASE_URL")
	envFallback(public, "PUBLIC_HOST")
	// Anything but these two is refused: a typo must not leave dev login on.
	if *env != "development" && *env != "production" {
		return fmt.Errorf("-env must be development or production, not %q", *env)
	}
	production := *env == "production"
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["dev-login"] {
		*devLogin = !production
	}
	if err := auth.CheckDevLogin(*env, *devLogin); err != nil {
		return err
	}
	*addr = apiAddr(*addr, *devLogin)
	if *dbURL == "" {
		return errors.New("api needs -db (DATABASE_URL), the depot_app database URL")
	}
	mailer, ediCfg, err := mf.build("")
	if err != nil {
		return err
	}
	rec, err := eng.build()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	// Secure cookies are set (inside newApp) before anything is served.
	a, err := newApp(pool, *blobs, rec, gate.Config{AutoAcceptEnabled: *autoAccept, AutoAcceptThreshold: *threshold}, production, mailer, ediCfg)
	if err != nil {
		return err
	}
	if !*devLogin {
		return errors.New("no identity provider is configured yet: only dev login exists")
	}
	return a.serve(ctx, *addr, httpapi.Options{Provider: auth.NewDevProvider(), PublicHosts: strings.Split(*public, ","),
		InboundSecret: os.Getenv("POSTMARK_INBOUND_SECRET")}, nil)
}

// apiAddr is the api's listen address: loopback while dev login (which signs
// anyone in) is on, unless an address was given.
func apiAddr(addr string, devLogin bool) string {
	switch {
	case addr != "":
		return addr
	case devLogin:
		return hubDefaultAddr
	}
	return ":8080"
}

// serve starts the gate service and the HTTP server and blocks until ctx ends.
// background funcs run alongside and must return when ctx ends.
func (a *app) serve(ctx context.Context, addr string, o httpapi.Options, background []func(context.Context)) error {
	// Take the port before anything is read: a busy port must fail fast, not
	// after photos left over from the last run were sent to the paid engine.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	// A failed Serve must end the workers too, or the waits below never return.
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if err := a.svc.Start(ctx); err != nil {
		return err
	}
	background = append(background, a.edi.Run)
	var bg sync.WaitGroup
	for _, f := range background {
		bg.Add(1)
		go func() { defer bg.Done(); f(ctx) }()
	}
	static := webui.Dist()
	if static == nil {
		log.Print("web app not built yet: run `npm --prefix web run build`, then rebuild depotd")
	}
	o.Service, o.Store, o.Blobs, o.Static, o.Version, o.Sessions, o.Pool = a.svc, a.st, a.bl, static, Version, a.sess, a.pool
	o.Yard, o.EDI = a.yard, a.edi
	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.New(o, a.broker).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Requests inherit ctx, so live-update streams end on Ctrl+C.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- server.Serve(ln) }()
	name, model := a.svc.Engine()
	log.Printf("depotd %s · engine %s (%s)", Version, name, model)
	for _, u := range lanURLs(addr) {
		log.Printf("open: %s", u)
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	cancelRun()
	bg.Wait()
	a.svc.Wait()
	return serveErr
}

// The hub's one company and depot, and the password of its private database
// (which listens on 127.0.0.1 only).
const (
	hubCompany = "local"
	hubDepot   = "own-depot"
	hubPGPass  = "depot-hub"
	// Loopback: dev login signs anyone in as owner, so the hub is not on the LAN unless asked.
	hubDefaultAddr = "127.0.0.1:8080"
)

func runHub(args []string) error {
	fs := newFlags("hub")
	dir := fs.String("data", "depot-data", "folder for the database and photos")
	devEmail := fs.String("dev-email", "owner@local", "email of the owner that dev login signs in")
	var eng engineFlags
	eng.register(fs)
	var mf mailFlags
	mf.register(fs)
	addr := fs.String("addr", hubDefaultAddr, "listen address (a LAN address lets anyone on the network sign in as owner)")
	autoAccept := fs.Bool("auto-accept", false, "accept confident, check-digit-valid reads without a person")
	threshold := fs.Float64("threshold", 0.85, "confidence needed for auto-accept")
	fakeCamera := fs.Bool("fake-camera", false, "use a simulated camera (try the Camera page without one)")
	fakePhotos := fs.String("fake-camera-photos", "", "folder of JPEG photos the simulated camera serves in turn (implies -fake-camera)")
	go2rtcPath := fs.String("go2rtc", defaultGo2rtc(), "go2rtc executable for live video")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fakePhotos != "" {
		*fakeCamera = true
	}
	mailer, ediCfg, err := mf.build(filepath.Join(*dir, "mail-out"))
	if err != nil {
		return err
	}
	rec, err := eng.build()
	if err != nil {
		return err
	}
	var stills [][]byte
	if *fakeCamera {
		if stills, err = loadStills(*fakePhotos); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ownerURL, stopPG, err := startPostgres(*dir)
	if err != nil {
		return err
	}
	defer stopPG()
	appPW, err := randomHex(16)
	if err != nil {
		return err
	}
	if err := db.Migrate(ctx, ownerURL, db.MigrateOptions{AppPassword: appPW}); err != nil {
		return err
	}
	if err := seedHub(ctx, ownerURL, *devEmail); err != nil {
		return err
	}
	pool, err := db.Open(ctx, db.AppURL(ownerURL, appPW))
	if err != nil {
		return err
	}
	defer pool.Close()
	a, err := newApp(pool, filepath.Join(*dir, "blobs"), rec,
		gate.Config{AutoAcceptEnabled: *autoAccept, AutoAcceptThreshold: *threshold}, false, mailer, ediCfg)
	if err != nil {
		return err
	}

	watcher := gatecam.New(a.svc, gatecam.DefaultConfig())
	a.svc.SetReadHook(watcher.OnRead)
	liveVideo := live.New(live.Config{Binary: *go2rtcPath, DataDir: *dir})
	cams := gatecam.NewManager(a.st, watcher, liveVideo, func(s camera.Settings) camera.Camera { return camera.NewReolink(s) },
		tenancy.Scope{CompanyID: hubCompany, DepotID: hubDepot})
	if *fakeCamera {
		cams.UseFixed(camera.NewDemoFake(stills...))
	}
	if err := cams.Load(ctx); err != nil {
		return err
	}
	switch s := cams.Settings(); {
	case *fakeCamera && *fakePhotos != "":
		log.Printf("camera: simulated, serving the photos in %s", *fakePhotos)
	case *fakeCamera:
		log.Print("camera: simulated (-fake-camera)")
	case s.Configured():
		log.Printf("camera: %s (automatic photos %v)", s.Host, s.Auto)
	default:
		log.Print("camera: not set up; open the Camera page")
	}
	log.Printf("dev login signs in %s · data %s", *devEmail, *dir)
	err = a.serve(ctx, *addr, httpapi.Options{Provider: auth.NewDevProvider(), Camera: cams, Live: liveVideo, Hub: true,
		InboundSecret: os.Getenv("POSTMARK_INBOUND_SECRET")},
		[]func(context.Context){
			// The watcher takes its company and depot from ctx.
			func(c context.Context) { watcher.Run(tenancy.With(c, cams.Scope())) },
			liveVideo.Run, // stops go2rtc on exit
		})
	cams.Close() // log out of the camera: it allows only a few sessions
	return err
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// startPostgres runs the hub's own PostgreSQL with its data in <dir>/pg and
// returns the owner URL and a stop function.
func startPostgres(dir string) (string, func(), error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V17).
		Port(uint32(port)).
		Username("postgres").Password(hubPGPass).
		CachePath(filepath.Join(cache, "depot", "embedded-postgres")).
		RuntimePath(filepath.Join(dir, "pg-run")).
		DataPath(filepath.Join(dir, "pg")).
		Logger(io.Discard).
		StartTimeout(2 * time.Minute))
	if err := pg.Start(); err != nil {
		return "", nil, fmt.Errorf("start database: %w", err)
	}
	return fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/postgres", hubPGPass, port), func() { _ = pg.Stop() }, nil
}

// seedHub makes sure the hub's company, depot and owner exist. It runs as the
// database owner and is safe to repeat. The user row carries the dev login's
// subject, so the first dev login finds it and its membership.
func seedHub(ctx context.Context, ownerURL, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	conn, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO companies (id, name, modules) VALUES ($1, 'Local depot', '{ocr}') ON CONFLICT DO NOTHING", []any{hubCompany}},
			{"INSERT INTO depots (company_id, id, name) VALUES ($1, $2, 'Own depot') ON CONFLICT DO NOTHING", []any{hubCompany, hubDepot}},
			{"INSERT INTO users (id, email, idp_subject) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", []any{ulid.Make().String(), email, "dev|" + email}},
			{"INSERT INTO memberships (company_id, user_id, role) SELECT $1, id, 'owner' FROM users WHERE email = $2 ON CONFLICT DO NOTHING", []any{hubCompany, email}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return fmt.Errorf("seed: %w", err)
			}
		}
		return nil
	})
}

// scopeFlags open the database and pick the company that export and bench read.
type scopeFlags struct{ db, company, depot, blobs string }

func (f *scopeFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.db, "db", "", "depot_app database URL (env DATABASE_URL)")
	fs.StringVar(&f.company, "company", "", "company id (required)")
	fs.StringVar(&f.depot, "depot", "", "depot id (empty: all the company's depots)")
	fs.StringVar(&f.blobs, "blobs", filepath.Join("depot-data", "blobs"), "folder of photos")
}

func (f *scopeFlags) open(ctx context.Context) (context.Context, *store.Store, *blob.Store, func(), error) {
	envFallback(&f.db, "DATABASE_URL")
	if f.company == "" || f.db == "" {
		return nil, nil, nil, nil, errors.New("needs -company and -db (DATABASE_URL)")
	}
	pool, err := db.Open(ctx, f.db)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	bl, err := blob.Open(f.blobs)
	if err != nil {
		pool.Close()
		return nil, nil, nil, nil, err
	}
	ctx = tenancy.With(ctx, tenancy.Scope{CompanyID: f.company, DepotID: f.depot})
	return ctx, store.New(pool), bl, pool.Close, nil
}

func runExport(args []string) error {
	fs := newFlags("export-dataset")
	var sf scopeFlags
	sf.register(fs)
	out := fs.String("out", "dataset-export", "folder to write the dataset into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, st, bl, closeDB, err := sf.open(context.Background())
	if err != nil {
		return err
	}
	defer closeDB()
	sum, err := dataset.Export(ctx, st, bl, *out)
	if err != nil {
		return err
	}
	fmt.Printf("exported %d verified moves, %d photos (%d with number boxes, %d unread camera stills in images-unread) to %s\n",
		sum.Moves, sum.Photos, sum.Boxes, sum.Unread, *out)
	return nil
}

func runBench(args []string) error {
	fs := newFlags("bench")
	var sf scopeFlags
	sf.register(fs)
	var eng engineFlags
	eng.register(fs)
	limit := fs.Int("limit", 0, "only the first N verified moves (0: all)")
	threshold := fs.Float64("threshold", 0.85, "auto-accept threshold used when combining photos")
	yes := fs.Bool("yes", false, "confirm that every verified photo is sent to the paid engine")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if eng.engine != "fake" && !*yes {
		return errors.New("bench sends every verified photo to the engine, which costs money: re-run with -yes")
	}
	rec, err := eng.build()
	if err != nil {
		return err
	}
	ctx, st, bl, closeDB, err := sf.open(context.Background())
	if err != nil {
		return err
	}
	defer closeDB()

	rep, err := bench.Run(ctx, st, bl, rec, *threshold, *limit)
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
