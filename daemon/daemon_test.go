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
		DownloadDir:     cfg.DownloadDir,
		StatePath:       cfg.StatePath,
		ListenPort:      0, // random port to avoid conflicts
		MaxActive:       cfg.MaxActive,
		DisableDHT:      true, // skip DHT in tests to avoid UDP socket exhaustion
		DisableListener: true, // skip peer listener in tests
	}
	eng, err := engine.New(engCfg)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = eng.Stop()
		// Remove entire temp dir to avoid "directory not empty" from async goroutines
		_ = os.RemoveAll(dir)
	})

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
	origin := w.Header().Get("Access-Control-Allow-Origin")
	if origin == "" {
		t.Error("missing CORS origin header")
	}
	// Should reflect the request origin, not wildcard "*"
	if origin == "*" {
		t.Error("CORS should not use wildcard '*', should reflect request origin")
	}
	if origin != "chrome-extension://abc123" {
		t.Errorf("CORS origin: got %q, want %q", origin, "chrome-extension://abc123")
	}
}

func TestDaemonCORSNoOrigin(t *testing.T) {
	d, _ := newTestDaemon(t)

	// GET without Origin header should not get CORS headers
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("should not set CORS origin when no Origin header in GET request")
	}
}

func TestDaemonCORSPrivateNetworkAccess(t *testing.T) {
	d, _ := newTestDaemon(t)

	// Chrome PNA preflight for .local domains
	req := httptest.NewRequest(http.MethodOptions, "/_app/immutable/chunks/test.js", http.NoBody)
	req.Header.Set("Origin", "http://ugnas01.local:9090")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("PNA preflight: got %d, want 204", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Private-Network") != "true" {
		t.Error("missing Access-Control-Allow-Private-Network header")
	}
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("missing Access-Control-Allow-Origin header")
	}
}

func TestDaemonCORSPNANoOriginNoWildcard(t *testing.T) {
	d, _ := newTestDaemon(t)

	// PNA preflight WITHOUT Origin header should NOT get wildcard CORS
	req := httptest.NewRequest(http.MethodOptions, "/api/rpc", http.NoBody)
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	origin := w.Header().Get("Access-Control-Allow-Origin")
	if origin == "*" {
		t.Error("PNA preflight without Origin should NOT use wildcard '*'")
	}
}

func TestDaemonAddFileSizeLimit(t *testing.T) {
	d, cfg := newTestDaemon(t)

	// Create a multipart form with a file > 10MB
	var body strings.Builder
	boundary := "----TestBoundary"
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"big.torrent\"\r\n")
	body.WriteString("Content-Type: application/x-bittorrent\r\n\r\n")
	body.WriteString(strings.Repeat("X", 11*1024*1024)) // 11MB
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req := httptest.NewRequest(http.MethodPost, "/api/add", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("expected rejection for oversized file upload")
	}
}

func TestDaemonUIServed(t *testing.T) {
	d, _ := newTestDaemon(t)

	// Root should always return 200 (placeholder or Svelte build)
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("/ status: got %d, want 200", w.Code)
	}
}

func TestDaemonUINoAuthRequired(t *testing.T) {
	d, _ := newTestDaemon(t)

	// Static UI should not require auth
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("/ without auth: got %d, want 200", w.Code)
	}
}

func TestDaemonAddEndpoint(t *testing.T) {
	d, cfg := newTestDaemon(t)

	req := httptest.NewRequest(http.MethodPost, "/api/add", strings.NewReader("url=magnet:?xt=urn:btih:"+strings.Repeat("aa", 20)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("add: got %d, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ID != "" {
		w2 := doRPC(t, d, cfg.APIKey, "torrent.remove", map[string]any{"id": resp.ID, "delete_files": true})
		_ = w2
	}
}

func TestRPCTorrentAddMagnet(t *testing.T) {
	d, cfg := newTestDaemon(t)
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("bb", 20)
	w := doRPC(t, d, cfg.APIKey, "torrent.add", map[string]string{"source": magnet})

	var resp struct {
		Result map[string]string         `json:"result"`
		Error  *struct{ Message string } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result == nil || resp.Result["id"] == "" {
		errMsg := ""
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		t.Fatalf("expected id in result, error: %s", errMsg)
	}
}

func TestRPCTorrentAddInvalidSource(t *testing.T) {
	d, cfg := newTestDaemon(t)
	w := doRPC(t, d, cfg.APIKey, "torrent.add", map[string]string{"source": ""})

	var resp struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil {
		t.Error("expected error for empty source")
	}
}

func TestRPCTorrentAddFileInvalidBase64(t *testing.T) {
	d, cfg := newTestDaemon(t)
	w := doRPC(t, d, cfg.APIKey, "torrent.addFile", map[string]string{"data": "not-valid-base64!!!"})

	var resp struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestRPCTorrentPauseResumeRemove(t *testing.T) {
	d, cfg := newTestDaemon(t)
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("cc", 20)
	w := doRPC(t, d, cfg.APIKey, "torrent.add", map[string]string{"source": magnet})

	var addResp struct {
		Result map[string]string `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &addResp)
	id := addResp.Result["id"]
	if id == "" {
		t.Fatal("no id returned")
	}

	// Pause
	w = doRPC(t, d, cfg.APIKey, "torrent.pause", map[string]string{"id": id})
	if w.Code != http.StatusOK {
		t.Errorf("pause: %d", w.Code)
	}

	// Resume
	w = doRPC(t, d, cfg.APIKey, "torrent.resume", map[string]string{"id": id})
	if w.Code != http.StatusOK {
		t.Errorf("resume: %d", w.Code)
	}

	// Get
	w = doRPC(t, d, cfg.APIKey, "torrent.get", map[string]string{"id": id})
	if w.Code != http.StatusOK {
		t.Errorf("get: %d", w.Code)
	}

	// Remove
	w = doRPC(t, d, cfg.APIKey, "torrent.remove", map[string]any{"id": id, "delete_files": false})
	if w.Code != http.StatusOK {
		t.Errorf("remove: %d", w.Code)
	}

	// Verify removed
	w = doRPC(t, d, cfg.APIKey, "torrent.list", nil)
	var listResp struct {
		Result []*engine.TorrentInfo `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Result) != 0 {
		t.Errorf("should be empty after remove: got %d", len(listResp.Result))
	}
}

func TestRPCTorrentQueueOperations(t *testing.T) {
	d, cfg := newTestDaemon(t)

	// Add 3 torrents with pause to avoid race from session goroutines
	var ids []string
	for i := range 3 {
		hash := strings.Repeat(string(rune('a'+i))+string(rune('0'+i)), 20)
		magnet := "magnet:?xt=urn:btih:" + hash[:40]
		w := doRPC(t, d, cfg.APIKey, "torrent.add", map[string]string{"source": magnet})
		var resp struct {
			Result map[string]string `json:"result"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Result["id"] != "" {
			ids = append(ids, resp.Result["id"])
			// Pause immediately to prevent session goroutines from racing
			doRPC(t, d, cfg.APIKey, "torrent.pause", map[string]string{"id": resp.Result["id"]})
		}
	}

	if len(ids) < 2 {
		t.Skip("not enough torrents added")
	}

	// Queue operations should not error
	for _, op := range []string{"torrent.queueTop", "torrent.queueBottom", "torrent.queueUp", "torrent.queueDown"} {
		w := doRPC(t, d, cfg.APIKey, op, map[string]string{"id": ids[0]})
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d", op, w.Code)
		}
	}
}

func TestRPCTorrentAddURLRejectsPrivateIP(t *testing.T) {
	d, cfg := newTestDaemon(t)
	for _, u := range []string{
		"http://127.0.0.1/evil.torrent",
		"http://192.168.1.1/evil.torrent",
		"http://10.0.0.1/evil.torrent",
		"http://localhost/evil.torrent",
	} {
		w := doRPC(t, d, cfg.APIKey, "torrent.addURL", map[string]string{"url": u})
		var resp struct {
			Error *struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Error == nil {
			t.Errorf("expected SSRF rejection for %s", u)
		}
	}
}

func TestRPCTorrentAddURL(t *testing.T) {
	// Set up a fake .torrent server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a real torrent"))
	}))
	defer ts.Close()

	d, cfg := newTestDaemon(t)
	w := doRPC(t, d, cfg.APIKey, "torrent.addURL", map[string]string{"url": ts.URL + "/test.torrent"})

	var resp struct {
		Error *struct{ Message string } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	// Should fail because it's not a valid torrent, but shouldn't crash
	if resp.Error == nil {
		t.Log("surprisingly succeeded with invalid torrent data")
	}
}

func TestRPCSetConfigRejectsTraversalPaths(t *testing.T) {
	d, cfg := newTestDaemon(t)

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"download_dir with traversal", map[string]any{"download_dir": "/tmp/../../../etc"}},
		{"tmp_dir with traversal", map[string]any{"tmp_dir": "../../root"}},
		{"download_dir relative", map[string]any{"download_dir": "relative/path"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRPC(t, d, cfg.APIKey, "daemon.setConfig", tt.params)
			var resp struct {
				Error *struct {
					Code    int
					Message string
				} `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			if resp.Error == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}

func TestRPCSetConfigRejectsSymlinkTraversal(t *testing.T) {
	d, cfg := newTestDaemon(t)

	// Create a symlink pointing to /
	dir := t.TempDir()
	link := filepath.Join(dir, "evil_link")
	if err := os.Symlink("/", link); err != nil {
		t.Skip("cannot create symlinks:", err)
	}

	w := doRPC(t, d, cfg.APIKey, "daemon.setConfig", map[string]any{
		"download_dir": link,
	})
	var resp struct {
		Error *struct {
			Code    int
			Message string
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil {
		t.Error("expected error for symlink pointing to /")
	}
}

func TestRPCSetConfigAcceptsValidPaths(t *testing.T) {
	d, cfg := newTestDaemon(t)

	dir := t.TempDir()
	w := doRPC(t, d, cfg.APIKey, "daemon.setConfig", map[string]any{
		"download_dir": dir,
	})
	var resp struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != nil {
		t.Errorf("expected success for valid absolute path, got error code %d", resp.Error.Code)
	}
}

func TestRPCRejectsOversizedBody(t *testing.T) {
	d, cfg := newTestDaemon(t)

	// Send a JSON body larger than 10 MB
	bigBody := `{"jsonrpc":"2.0","method":"daemon.version","id":1,"extra":"` + strings.Repeat("x", 11*1024*1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/rpc", strings.NewReader(bigBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	// Should fail with parse error (body truncated by LimitReader)
	var resp struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil {
		t.Error("expected error for oversized body")
	}
}

func TestConfigTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

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

	cfg1.MaxActive = 10
	if err := cfg1.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

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

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "max_active = 10") {
		t.Error("should contain TOML format")
	}
}
