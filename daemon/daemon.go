package daemon

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/skmtkytr/stor/engine"
	"github.com/skmtkytr/stor/ui"
)

// Daemon is the HTTP server that exposes the engine via JSON-RPC and serves the Web UI.
type Daemon struct {
	engine *engine.Engine
	server *http.Server
	cfg    Config
}

// New creates a new daemon.
func New(eng *engine.Engine, cfg Config) *Daemon {
	d := &Daemon{
		engine: eng,
		cfg:    cfg,
	}

	mux := http.NewServeMux()

	rpc := NewRPCHandler(eng, &d.cfg)

	// API routes (require auth)
	mux.Handle("POST /api/rpc", d.authMiddleware(rpc))
	mux.Handle("POST /api/add", d.authMiddleware(http.HandlerFunc(d.handleAdd)))
	mux.Handle("GET /api/torrents", d.authMiddleware(http.HandlerFunc(d.handleListTorrents)))

	// Web UI (no auth — static files only, API key entered in browser)
	uiFS, _ := fs.Sub(ui.FS, "dist")
	mux.Handle("GET /", http.FileServerFS(uiFS))

	d.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           d.logMiddleware(d.corsMiddleware(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return d
}

// Start starts the HTTP server.
func (d *Daemon) Start() error {
	return d.server.ListenAndServe()
}

// Stop gracefully shuts down the server.
func (d *Daemon) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.server.Shutdown(ctx)
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
			// PNA preflight from same-origin on .local — respond even without Origin
			if origin == "" && r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
