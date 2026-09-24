package main

import (
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/catalog"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/engine/appjail"
	"github.com/daemonless/fjord/pkg/engine/podman"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/ui"
)

// version identifies this build; overridden at release time via
// `go build -ldflags "-X main.version=X.Y.Z"`. Dev builds carry the next
// version with a -dev suffix so they read as ahead of the last release.
var version = "0.2.2-dev"

// envOr returns the environment variable key, or def when it is unset/empty.
// Used to default a flag to its FJORD_* env var.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// engineDescriptors is the registry of known engines, in default-preference
// order. Adding an engine is adding its Descriptor here -- no engine-specific
// code lives in the generic daemon; each engine describes and detects itself.
var engineDescriptors = []engine.Descriptor{
	podman.Descriptor,
	appjail.Descriptor,
}

// engineNames joins the registered engine names for help text.
func engineNames() string {
	names := make([]string, 0, len(engineDescriptors))
	for _, d := range engineDescriptors {
		names = append(names, d.Name)
	}
	return strings.Join(names, "|")
}

// buildBackends registers every engine that reports itself available on this
// host. All coexist -- each stack routes to its own engine. Returns the
// registry and the default engine for new installs.
func buildBackends(fjordRoot string) (map[string]engine.Backend, string) {
	settings := loadSettings(fjordRoot)
	disabled := map[string]bool{}
	for _, name := range settings.DisabledEngines {
		disabled[name] = true
	}
	backends := map[string]engine.Backend{}
	for _, d := range engineDescriptors {
		ok, reason, warning := d.Available()
		if !ok {
			log.Printf("engine: %s unavailable (%s)", d.Name, reason)
			continue
		}
		if disabled[d.Name] {
			log.Printf("engine: %s available but disabled (Plugins)", d.Name)
			continue
		}
		backends[d.Name] = d.New()
		if warning != "" {
			log.Printf("engine: %s available (%s)", d.Name, warning)
		} else {
			log.Printf("engine: %s available", d.Name)
		}
	}

	// Default: first available engine, then the Settings choice / FJORD_ENGINE
	// if that engine is actually registered.
	def := ""
	for _, d := range engineDescriptors {
		if _, ok := backends[d.Name]; ok {
			def = d.Name
			break
		}
	}
	for _, want := range []string{settings.DefaultEngine, os.Getenv("FJORD_ENGINE")} {
		if _, ok := backends[want]; ok && want != "" {
			def = want
		}
	}
	return backends, def
}

// cacheControl makes the SPA shell always revalidate so a redeployed binary's
// new content-hashed bundle is picked up on the next load, while letting the
// hashed assets themselves cache forever. The embedded FS sends no ETag or
// Last-Modified (embed files have zero modtime), so without this a browser will
// keep serving a stale index.html that points at a bundle no longer present.
func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// compressible is the asset types worth gzipping: the bundle is over a
// megabyte of JavaScript and was going out uncompressed, which costs nothing
// on a LAN and a great deal over anything else. Images and fonts are already
// compressed and only get bigger for the trouble.
func compressible(path string) bool {
	switch {
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".css"),
		strings.HasSuffix(path, ".json"), strings.HasSuffix(path, ".svg"),
		strings.HasSuffix(path, ".html"), path == "/":
		return true
	}
	return false
}

// gzipAssets compresses the UI's own files. Deliberately not applied to the
// API: a streaming endpoint (logs, exec output, install progress) must reach
// the browser as it is written, and a compressor holding bytes back to fill a
// window would stall exactly the things that have to be live.
func gzipAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !compressible(r.URL.Path) || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		// Content-Length no longer describes the body, and the response now
		// varies by request header -- a cache that missed either would serve
		// gzip to a client that cannot read it.
		w.Header().Del("Content-Length")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next.ServeHTTP(gzipWriter{ResponseWriter: w, w: gz}, r)
	})
}

// gzipWriter sends the body through the compressor while leaving headers and
// the status line on the real ResponseWriter.
type gzipWriter struct {
	http.ResponseWriter
	w *gzip.Writer
}

func (g gzipWriter) Write(b []byte) (int, error) { return g.w.Write(b) }

func main() {
	// Config comes from FJORD_* env vars; each is also a flag that overrides
	// its env var. Flags default to the env value (or the built-in default),
	// and the resolved value is written back to the env so every consumer --
	// some deep in the engine packages -- reads one source of truth.
	var (
		fVersion      = flag.Bool("version", false, "print version and exit")
		fVersionV     = flag.Bool("v", false, "print version and exit (alias)")
		fListen       = flag.String("listen", envOr("FJORD_LISTEN", ":3567"), "listen address")
		fStacksDir    = flag.String("stacks-dir", envOr("FJORD_STACKS_DIR", "/var/db/fjord/stacks"), "stacks directory")
		fStorageBase  = flag.String("storage-base", os.Getenv("FJORD_STORAGE_BASE"), "default App data location")
		fEngine       = flag.String("engine", os.Getenv("FJORD_ENGINE"), "default engine ("+engineNames()+")")
		fPodmanSocket = flag.String("podman-socket", os.Getenv("FJORD_PODMAN_SOCKET"), "podman API socket")
		fHostAddr     = flag.String("host-addr", os.Getenv("FJORD_HOST_ADDR"), "host address advertised to the UI")
		fCatalogURL   = flag.String("catalog-url", os.Getenv("FJORD_CATALOG_URL"), "bootstrap catalog on first run")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "fjordd %s\n\nUsage: fjordd [flags]\n\n"+
			"Each flag can also be set with its FJORD_* env var; the flag wins.\n\nFlags:\n", version)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *fVersion || *fVersionV {
		fmt.Printf("fjordd %s\n", version)
		return
	}
	// Write the resolved config back to the env so all consumers (including
	// engine packages that read os.Getenv directly) honor flag overrides.
	os.Setenv("FJORD_LISTEN", *fListen)
	os.Setenv("FJORD_STACKS_DIR", *fStacksDir)
	os.Setenv("FJORD_STORAGE_BASE", *fStorageBase)
	os.Setenv("FJORD_ENGINE", *fEngine)
	os.Setenv("FJORD_PODMAN_SOCKET", *fPodmanSocket)
	os.Setenv("FJORD_HOST_ADDR", *fHostAddr)
	os.Setenv("FJORD_CATALOG_URL", *fCatalogURL)

	fmt.Printf("Starting fjordd %s - Compose Manager MVP\n", version)

	stacksDir := os.Getenv("FJORD_STACKS_DIR")
	if stacksDir == "" {
		stacksDir = "/var/db/fjord/stacks"
	}
	// The fjord root (parent of stacks) is where provisioned volumes live.
	fjordRoot := filepath.Dir(stacksDir)
	// Create the data root on startup so a fresh host works without the
	// operator pre-mkdir'ing it (the System check used to just tell them to).
	if err := os.MkdirAll(stacksDir, 0o755); err != nil {
		log.Printf("could not create data root %s: %v (create it and make it writable)", stacksDir, err)
	}

	// App-store catalogs: multiple named sources, fetched and cached locally
	// so the store updates without rebuilding the binary. Sources come from
	// the Settings page (settings.json in the fjord root); FJORD_CATALOG_URL
	// only bootstraps a first entry on a fresh install. Any source added but
	// never fetched (e.g. added while offline) is fetched in the background
	// at startup.
	settings := loadSettings(fjordRoot)
	sources := settings.Catalogs
	if len(sources) == 0 && !settings.CatalogBootstrapped {
		// Fresh install: the app store should not be empty out of the box.
		// Add the default catalog (FJORD_CATALOG_URL overrides which) as an
		// ordinary, removable source and remember that we did, so a user who
		// deletes it later doesn't get it back on every restart.
		id, url := "daemonless", defaultCatalogURL
		if env := os.Getenv("FJORD_CATALOG_URL"); env != "" {
			id, url = "default", env
		}
		sources = []catalog.Source{{ID: id, Name: id, URL: url}}
		if err := updateSettings(fjordRoot, func(st *savedSettings) { st.Catalogs = sources }); err != nil {
			log.Printf("catalog: could not persist default source: %v", err)
		}
	}
	// Once any catalog has ever been configured -- bootstrapped here or added
	// by hand -- an empty list later means "removed on purpose", never "fresh".
	if !settings.CatalogBootstrapped && len(sources) > 0 {
		if err := updateSettings(fjordRoot, func(st *savedSettings) { st.CatalogBootstrapped = true }); err != nil {
			log.Printf("catalog: could not persist bootstrap flag: %v", err)
		}
	}
	cat := catalog.New(filepath.Join(fjordRoot, "catalog"), sources)
	if unfetched := cat.Unfetched(); len(unfetched) > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			for _, src := range unfetched {
				if err := cat.RefreshOne(ctx, src.ID); err != nil {
					log.Printf("catalog %s: initial fetch from %s: %v", src.Name, src.URL, err)
				} else {
					log.Printf("catalog %s: fetched from %s", src.Name, src.URL)
				}
			}
		}()
	}

	// Listen address is resolved before server construction so /api/about can
	// report it. Overridable via FJORD_LISTEN (e.g. "127.0.0.1:3567").
	addr := os.Getenv("FJORD_LISTEN")
	if addr == "" {
		addr = ":3567"
	}

	podman.SMBCredDir = filepath.Join(fjordRoot, "smb") // Linux cifs credential files (root-only)
	backends, defEngine := buildBackends(fjordRoot)
	if len(backends) == 0 {
		// Keep serving: the System page reports what's missing and how to fix
		// it. Stack operations fail with engine.ErrUnavailable until restart.
		log.Printf("engine: NO ENGINE AVAILABLE -- stacks can't be managed until one is installed and fjordd restarts")
	}
	manager := stack.NewManager(stacksDir)
	// One-time data migration: stacks written by fjord <= 0.1.1 recorded no
	// engine because podman was the only one. Record it, so nothing at
	// runtime has to guess.
	if migrated, err := manager.MigrateEngine("podman"); err != nil {
		log.Printf("stack migration: %v", err)
	} else if len(migrated) > 0 {
		log.Printf("stack migration: recorded engine podman on %s", strings.Join(migrated, ", "))
	}
	backfillStackIcons(manager, cat)
	srv := &server{
		manager:    manager,
		backends:   backends,
		defEngine:  defEngine,
		cat:        cat,
		fjordRoot:  fjordRoot,
		stacksDir:  stacksDir,
		listenAddr: addr,
		events:     newEventHub(),
		seen:       newFirstSeen(fjordRoot),
	}
	// One server-side loop pushes stack state-changes to all SSE clients.
	go srv.runEventLoop()

	// Serve the embedded UI at /, API + catalog routes beside it.
	distFS, err := fs.Sub(ui.Files, "dist")
	if err != nil {
		log.Fatalf("Failed to sub-directory embedded dist: %v", err)
	}
	http.Handle("/", gzipAssets(cacheControl(http.FileServer(http.FS(distFS)))))
	// Top-level liveness probe (root, not under /api/), unauthenticated so
	// reverse proxies and healthchecks can reach it.
	http.HandleFunc("/healthz", srv.handleHealthz)
	api := http.NewServeMux()
	srv.routes(api)
	http.Handle("/api/", guardMutations(api)) // same-origin + JSON on every mutation
	http.Handle("/catalog/", api)

	// Startup diagnostics in the background (the socket probe can block a few
	// seconds); logs every failed readiness check with its fix.
	go logDoctor(fjordRoot, srv.engineNames())

	// Restore stacks the operator left running before this restart. Runs in
	// the background so the UI comes up immediately; each stack is brought up
	// serially to avoid hammering the runtime.
	go startOnBoot(srv)

	// Keep the app store's versions current (interval in Settings → Catalogs).
	go runCatalogScheduler(srv)

	// Default binds all interfaces so container port publishing reaches it; a
	// host daemon can narrow it to loopback via FJORD_LISTEN.
	closeShellsOnExit()
	fmt.Printf("Web UI listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// startOnBoot brings up every stack whose recorded desired_state is "running",
// one at a time, so running stacks survive a fjordd or host restart.
func startOnBoot(srv *server) {
	stacks, err := srv.manager.List()
	if err != nil {
		log.Printf("start-on-boot: list stacks: %v", err)
		return
	}
	for _, s := range stacks {
		if s.State == nil || s.State.DesiredState != "running" {
			continue
		}
		full, err := srv.manager.Get(s.Name)
		if err != nil {
			log.Printf("start-on-boot: load %s: %v", s.Name, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		// A fjordd restart is not a host boot: a stack that is already running
		// must be left alone. `podman-compose up` recreates its containers when
		// the compose file has changed (killing open shells and restarting the
		// app), so only bring up what is actually down.
		if st, err := srv.backendFor(full).Status(ctx, full); err == nil && st.State == "running" {
			cancel()
			continue
		}
		stream, err := srv.backendFor(full).Up(ctx, full)
		if err != nil {
			log.Printf("start-on-boot: up %s: %v", s.Name, err)
			cancel()
			continue
		}
		io.Copy(io.Discard, stream) // wait for the bring-up to finish before the next
		stream.Close()
		cancel()
		log.Printf("start-on-boot: brought up %s", s.Name)
	}
}
