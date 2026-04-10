package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skmtkytr/stor/engine"
)

func newTestDaemon(t *testing.T) (*Daemon, Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		Port:        0,
		DownloadDir: filepath.Join(dir, "downloads"),
		APIKey:      "test-key-123",
		StatePath:   filepath.Join(dir, "state.json"),
		MaxActive:   5,
	}
	engCfg := engine.Config{
		DownloadDir: cfg.DownloadDir,
		StatePath:   cfg.StatePath,
		ListenPort:  6881,
		MaxActive:   cfg.MaxActive,
	}
	eng, err := engine.New(engCfg)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop() })

	d := New(eng, cfg)
	return d, cfg
}

func doRPC(t *testing.T, d *Daemon, key, method string, params any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      1,
	}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/rpc", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)
	return w
}

func TestDaemonAuthRequired(t *testing.T) {
	d, _ := newTestDaemon(t)

	req := httptest.NewRequest(http.MethodPost, "/api/rpc", strings.NewReader(`{"jsonrpc":"2.0","method":"daemon.version","id":1}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth: got %d, want 401", w.Code)
	}
}

func TestDaemonAuthInvalidKey(t *testing.T) {
	d, _ := newTestDaemon(t)
	w := doRPC(t, d, "wrong-key", "daemon.version", nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad key: got %d, want 401", w.Code)
	}
}

func TestDaemonVersion(t *testing.T) {
	d, cfg := newTestDaemon(t)
	w := doRPC(t, d, cfg.APIKey, "daemon.version", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	var resp struct {
		Result map[string]string `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result["version"] == "" {
		t.Error("version should not be empty")
	}
}

func TestDaemonStats(t *testing.T) {
	d, cfg := newTestDaemon(t)
	w := doRPC(t, d, cfg.APIKey, "daemon.stats", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	var resp struct {
		Result engine.EngineStats `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result.MaxActive != 5 {
		t.Errorf("max_active: got %d", resp.Result.MaxActive)
	}
}

func TestDaemonTorrentList(t *testing.T) {
	d, cfg := newTestDaemon(t)
	w := doRPC(t, d, cfg.APIKey, "torrent.list", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	var resp struct {
		Result []*engine.TorrentInfo `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Result) != 0 {
		t.Errorf("should be empty: got %d", len(resp.Result))
	}
}

func TestDaemonSetMaxActive(t *testing.T) {
	d, cfg := newTestDaemon(t)
	w := doRPC(t, d, cfg.APIKey, "daemon.setMaxActive", map[string]int{"max_active": 10})

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	var resp struct {
		Result map[string]int `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result["max_active"] != 10 {
		t.Errorf("result: got %d", resp.Result["max_active"])
	}
}

func TestDaemonMethodNotFound(t *testing.T) {
	d, cfg := newTestDaemon(t)
	w := doRPC(t, d, cfg.APIKey, "nonexistent.method", nil)

	var resp struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Error("expected method not found error")
	}
}

func TestDaemonCORS(t *testing.T) {
	d, _ := newTestDaemon(t)

	req := httptest.NewRequest(http.MethodOptions, "/api/rpc", http.NoBody)
	req.Header.Set("Origin", "chrome-extension://abc123")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight: got %d, want 204", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS origin header")
	}
}

func TestDaemonUIServed(t *testing.T) {
	d, _ := newTestDaemon(t)

	tests := []struct {
		path        string
		wantStatus  int
		wantType    string
		wantContain string
	}{
		{"/", 200, "text/html", "<title>stor</title>"},
		{"/index.html", 301, "", ""}, // FileServer redirects /index.html to /
		{"/style.css", 200, "text/css", ".screen"},
		{"/app.js", 200, "text/javascript", "async function rpc"},
		{"/nonexistent", 404, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, http.NoBody)
			w := httptest.NewRecorder()
			d.server.Handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status: got %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantType != "" && !strings.Contains(w.Header().Get("Content-Type"), tt.wantType) {
				t.Errorf("content-type: got %q, want to contain %q", w.Header().Get("Content-Type"), tt.wantType)
			}
			if tt.wantContain != "" && !strings.Contains(w.Body.String(), tt.wantContain) {
				t.Errorf("body should contain %q", tt.wantContain)
			}
		})
	}
}

func TestDaemonUIAssetsConsistency(t *testing.T) {
	d, _ := newTestDaemon(t)

	// index.html should reference style.css and app.js
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	for _, ref := range []string{"style.css", "app.js", "auth-screen", "app-screen", "ctx-menu", "torrent-list"} {
		if !strings.Contains(body, ref) {
			t.Errorf("index.html should reference %q", ref)
		}
	}

	// app.js should have all required RPC methods
	req = httptest.NewRequest(http.MethodGet, "/app.js", http.NoBody)
	w = httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	js := w.Body.String()
	for _, fn := range []string{
		"daemon.version", "daemon.stats", "daemon.setMaxActive",
		"torrent.list", "torrent.add", "torrent.addFile",
		"torrent.pause", "torrent.resume", "torrent.remove",
		"torrent.queueTop", "torrent.queueBottom",
	} {
		if !strings.Contains(js, fn) {
			t.Errorf("app.js should reference RPC method %q", fn)
		}
	}

	// style.css should have key class definitions
	req = httptest.NewRequest(http.MethodGet, "/style.css", http.NoBody)
	w = httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	css := w.Body.String()
	for _, cls := range []string{".screen", ".auth-box", ".progress-bar", ".state-downloading", ".ctx-menu", ".toast"} {
		if !strings.Contains(css, cls) {
			t.Errorf("style.css should define %q", cls)
		}
	}
}

func TestDaemonUINoAuthRequired(t *testing.T) {
	d, _ := newTestDaemon(t)

	// Static files should NOT require auth
	for _, path := range []string{"/", "/style.css", "/app.js"} {
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		// No Authorization header
		w := httptest.NewRecorder()
		d.server.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("%s without auth: got %d, want 200", path, w.Code)
		}
	}
}

func TestDaemonAddEndpoint(t *testing.T) {
	d, cfg := newTestDaemon(t)

	req := httptest.NewRequest(http.MethodPost, "/api/add", strings.NewReader("url=magnet:?xt=urn:btih:"+strings.Repeat("aa", 20)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	// The magnet will be added (may fail to download, but add should succeed)
	if w.Code != http.StatusOK {
		t.Errorf("add: got %d, body: %s", w.Code, w.Body.String())
	}

	// Clean up: remove the torrent so temp files are cleaned
	var resp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ID != "" {
		w2 := doRPC(t, d, cfg.APIKey, "torrent.remove", map[string]any{"id": resp.ID, "delete_files": true})
		_ = w2
	}
}

func TestConfigTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// First load creates defaults
	cfg1, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if cfg1.APIKey == "" {
		t.Error("api key should be generated")
	}
	if cfg1.MaxActive != 5 {
		t.Errorf("max_active default: got %d", cfg1.MaxActive)
	}

	// Modify and save
	cfg1.MaxActive = 10
	if err := cfg1.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reload
	cfg2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg2.MaxActive != 10 {
		t.Errorf("max_active after reload: got %d", cfg2.MaxActive)
	}
	if cfg2.APIKey != cfg1.APIKey {
		t.Error("api key should be preserved")
	}

	// Check file is TOML
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "max_active = 10") {
		t.Error("should contain TOML format")
	}
}
