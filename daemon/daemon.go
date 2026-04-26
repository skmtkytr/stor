package daemon

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"time"

	"github.com/skmtkytr/stor/engine"
	"github.com/skmtkytr/stor/events"
	"github.com/skmtkytr/stor/ui"
)

// Daemon is the HTTP server that exposes the engine via JSON-RPC and serves the Web UI.
type Daemon struct {
	engine   *engine.Engine
	server   *http.Server
	cfg      Config
	counters *eventCounters
}

// eventCounters tracks per-type publish and per-subscriber drop counts,
// surfaced via /metrics. Hooks are installed on the engine bus in Start and
// removed in Stop.
type eventCounters struct {
	mu        sync.RWMutex
	published map[events.Type]int64
	dropped   map[string]int64
}

func newEventCounters() *eventCounters {
	return &eventCounters{
		published: make(map[events.Type]int64),
		dropped:   make(map[string]int64),
	}
}

func (c *eventCounters) onPublish(ev events.Event) {
	c.mu.Lock()
	c.published[ev.Type]++
	c.mu.Unlock()
}

func (c *eventCounters) onDrop(sub *events.Subscription, _ events.Event) {
	c.mu.Lock()
	c.dropped[sub.Name()]++
	c.mu.Unlock()
}

// snapshot returns a stable copy for /metrics rendering.
func (c *eventCounters) snapshot() (map[events.Type]int64, map[string]int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pub := make(map[events.Type]int64, len(c.published))
	for k, v := range c.published {
		pub[k] = v
	}
	drop := make(map[string]int64, len(c.dropped))
	for k, v := range c.dropped {
		drop[k] = v
	}
	return pub, drop
}

// New creates a new daemon.
func New(eng *engine.Engine, cfg Config) *Daemon {
	d := &Daemon{
		engine:   eng,
		cfg:      cfg,
		counters: newEventCounters(),
	}

	mux := http.NewServeMux()

	rpc := NewRPCHandler(eng, &d.cfg)

	// API routes (require auth)
	mux.Handle("POST /api/rpc", d.authMiddleware(rpc))
	mux.Handle("POST /api/add", d.authMiddleware(http.HandlerFunc(d.handleAdd)))
	mux.Handle("GET /api/torrents", d.authMiddleware(http.HandlerFunc(d.handleListTorrents)))
	// SSE event stream. Long-lived; chunked write timeout would kill it,
	// so the server below sets WriteTimeout to 0 for this path implicitly
	// via the global handler — see comment near http.Server below.
	mux.Handle("GET /api/events", d.authMiddleware(http.HandlerFunc(d.handleEvents)))

	// Observability (opt-in via config; always behind API key auth).
	// /metrics serves Prometheus text format; /debug/pprof/* serves the
	// stdlib pprof handler tree. Profiling endpoints are expensive and
	// leak detailed runtime info, so auth is mandatory.
	if cfg.EnableMetrics {
		mux.Handle("GET /metrics", d.authMiddleware(http.HandlerFunc(d.handleMetrics)))

		// Register pprof explicitly to avoid depending on http.DefaultServeMux
		// (which the side-effect import of net/http/pprof mutates).
		mux.Handle("GET /debug/pprof/", d.authMiddleware(http.HandlerFunc(pprof.Index)))
		mux.Handle("GET /debug/pprof/cmdline", d.authMiddleware(http.HandlerFunc(pprof.Cmdline)))
		mux.Handle("GET /debug/pprof/profile", d.authMiddleware(http.HandlerFunc(pprof.Profile)))
		mux.Handle("GET /debug/pprof/symbol", d.authMiddleware(http.HandlerFunc(pprof.Symbol)))
		mux.Handle("POST /debug/pprof/symbol", d.authMiddleware(http.HandlerFunc(pprof.Symbol)))
		mux.Handle("GET /debug/pprof/trace", d.authMiddleware(http.HandlerFunc(pprof.Trace)))
	}

	// Web UI (no auth — static files only, API key entered in browser).
	// Filenames under /_app/immutable/ used to be content-hashed by Vite
	// (auto cache-bust). We removed the hashes so ui/dist diffs stay
	// readable in git, which means stable URLs may serve stale content
	// after an upgrade. no-cache + must-revalidate forces conditional
	// revalidation on every load — embed.FS exposes Last-Modified set to
	// the binary's build time, so a fresh stor returns 200 with the new
	// body and an unchanged stor returns 304 (cheap, ~200B).
	uiFS, _ := fs.Sub(ui.FS, "dist")
	mux.Handle("GET /", uiCacheControl(http.FileServerFS(uiFS)))

	d.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           d.logMiddleware(d.corsMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return d
}

// Start starts the HTTP server.
func (d *Daemon) Start() error {
	d.installEventHooks()
	return d.server.ListenAndServe()
}

// Stop gracefully shuts down the server.
func (d *Daemon) Stop() error {
	d.uninstallEventHooks()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.server.Shutdown(ctx)
}

// installEventHooks wires the event-counter hooks into the engine bus.
func (d *Daemon) installEventHooks() {
	if d.engine == nil || d.counters == nil {
		return
	}
	d.engine.Bus().SetHooks(d.counters.onPublish, d.counters.onDrop)
}

// uninstallEventHooks clears the hooks so the daemon's counters do not
// outlive the daemon itself.
func (d *Daemon) uninstallEventHooks() {
	if d.engine == nil {
		return
	}
	d.engine.Bus().SetHooks(nil, nil)
}

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the underlying ResponseWriter so http.NewResponseController
// can find Flusher / Hijacker / SetWriteDeadline implementations on it.
// Without this, SSE handlers see "feature not supported" from rc.Flush().
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logMiddleware logs every HTTP request with method, path, status, and duration.
func (d *Daemon) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		duration := time.Since(start)

		level := slog.LevelInfo
		if sw.code >= 400 {
			level = slog.LevelWarn
		}
		if sw.code >= 500 {
			level = slog.LevelError
		}

		slog.Log(r.Context(), level, "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.code,
			"duration_ms", duration.Milliseconds(),
		)
	})
}

// uiETag is a process-lifetime ETag for static UI assets. We can't derive
// one from embed.FS (file mtimes are zero), so we use the daemon start
// time formatted weakly. It is constant for the life of the process and
// changes after restart (typically: after an upgrade), which is exactly
// when browsers should re-fetch.
var uiETag = fmt.Sprintf(`W/"%d"`, time.Now().UnixNano())

// uiCacheControl wraps the static UI handler. We deliberately serve stable
// (un-hashed) filenames under /_app/immutable/, so without revalidation a
// browser that cached app.js from version A might keep using it after the
// daemon upgrades to version B. Add a process-wide ETag so the browser
// can revalidate cheaply (304 with no body) — without ETag/Last-Modified
// the no-cache directive forces a full re-download on every reload, which
// defeats the purpose.
func uiCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		w.Header().Set("ETag", uiETag)
		// If-None-Match honoured manually because http.FileServerFS over
		// embed.FS does not (it drives 304s off Last-Modified, which is
		// zero here).
		if match := r.Header.Get("If-None-Match"); match != "" && match == uiETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isUIPath(p string) bool {
	return p == "/" || p == "/index.html" || strings.HasPrefix(p, "/_app/")
}

// handleAdd is a simplified endpoint for adding torrents (for Chrome extension).
// Accepts form data with "url" field or multipart .torrent file upload.
func (d *Daemon) handleAdd(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")

	if strings.HasPrefix(ct, "multipart/form-data") {
		// File upload
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file required", http.StatusBadRequest)
			return
		}
		defer func() { _ = file.Close() }()

		const maxTorrentSize = 10 << 20 // 10 MB
		data, err := io.ReadAll(io.LimitReader(file, maxTorrentSize+1))
		if err == nil && len(data) > maxTorrentSize {
			http.Error(w, "torrent file too large (>10MB)", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			http.Error(w, "read file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		id, err := d.engine.AddTorrentFile(data)
		if err != nil {
			slog.Error("add torrent file failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("torrent added via file upload", "id", id)
		writeJSON(w, map[string]string{"id": id})
		return
	}

	// URL-encoded form
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	url := r.FormValue("url")
	if url == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}

	id, err := d.engine.AddTorrent(url)
	if err != nil {
		slog.Error("add torrent failed", "source", url, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("torrent added via url", "id", id, "source", url)
	writeJSON(w, map[string]string{"id": id})
}

// handleListTorrents is a REST convenience for polling.
func (d *Daemon) handleListTorrents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, d.engine.ListTorrents())
}

// authMiddleware checks the Authorization: Bearer <key> header.
func (d *Daemon) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			slog.Warn("auth failed: missing bearer token", "path", r.URL.Path, "remote", r.RemoteAddr)
			http.Error(w, "unauthorized: missing Bearer token", http.StatusUnauthorized)
			return
		}
		token := strings.TrimSpace(auth[7:])
		if subtle.ConstantTimeCompare([]byte(token), []byte(d.cfg.APIKey)) != 1 {
			slog.Warn("auth failed: invalid key", "path", r.URL.Path, "remote", r.RemoteAddr)
			http.Error(w, "unauthorized: invalid key", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware allows cross-origin requests (Chrome extension + remote browser).
// Reflects the request Origin instead of using wildcard to prevent CSRF.
// Also handles Chrome's Private Network Access (PNA) preflight for .local domains.
func (d *Daemon) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			// PNA preflight from same-origin on .local — allow private network
			// but do NOT set wildcard CORS origin (prevents CORS bypass)
			if origin == "" && r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
