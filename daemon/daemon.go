package daemon

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/skmtkytr/stor/engine"
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

	rpc := NewRPCHandler(eng)

	// API routes (require auth)
	mux.Handle("POST /api/rpc", d.authMiddleware(rpc))
	mux.Handle("POST /api/add", d.authMiddleware(http.HandlerFunc(d.handleAdd)))
	mux.Handle("GET /api/torrents", d.authMiddleware(http.HandlerFunc(d.handleListTorrents)))

	// Web UI (no auth — static files only, API key entered in browser)
	mux.Handle("GET /", http.HandlerFunc(d.handleUI))

	d.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           d.corsMiddleware(mux),
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

		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "read file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		id, err := d.engine.AddTorrentFile(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

// handleListTorrents is a REST convenience for polling.
func (d *Daemon) handleListTorrents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, d.engine.ListTorrents())
}

// handleUI serves the embedded Web UI.
func (d *Daemon) handleUI(w http.ResponseWriter, r *http.Request) {
	// For now, serve a minimal placeholder. Will be replaced by embed.FS in Step 8.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>stor</title></head>
<body><h1>stor</h1><p>Web UI placeholder. See Step 8.</p></body>
</html>`)
}

// authMiddleware checks the Authorization: Bearer <key> header.
func (d *Daemon) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := auth[7:]
		if subtle.ConstantTimeCompare([]byte(token), []byte(d.cfg.APIKey)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware allows cross-origin requests (Chrome extension + remote browser).
func (d *Daemon) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
