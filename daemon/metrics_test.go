package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skmtkytr/stor/engine"
)

// newTestDaemonWithConfig builds a test daemon, applying an optional mutator
// on Config before constructing the Daemon. Mirrors newTestDaemon but allows
// callers to toggle fields such as EnableMetrics.
func newTestDaemonWithConfig(t *testing.T, mutate func(*Config)) (*Daemon, Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		Port:        0,
		DownloadDir: filepath.Join(dir, "downloads"),
		APIKey:      "test-key-123",
		StatePath:   filepath.Join(dir, "state.json"),
		MaxActive:   5,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	engCfg := engine.Config{
		DownloadDir:     cfg.DownloadDir,
		StatePath:       cfg.StatePath,
		ListenPort:      0,
		MaxActive:       cfg.MaxActive,
		DisableDHT:      true,
		DisableListener: true,
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
		_ = os.RemoveAll(dir)
	})

	d := New(eng, cfg)
	return d, cfg
}

func TestMetricsDisabledByDefault(t *testing.T) {
	d, cfg := newTestDaemon(t)

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("/metrics when disabled: got %d, want 404", w.Code)
	}
}

func TestPprofDisabledByDefault(t *testing.T) {
	d, cfg := newTestDaemon(t)

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("/debug/pprof/ when disabled: got %d, want 404", w.Code)
	}
}

func TestMetricsEnabled(t *testing.T) {
	d, cfg := newTestDaemonWithConfig(t, func(c *Config) {
		c.EnableMetrics = true
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/metrics: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Errorf("Content-Type: got %q, want text/plain; version=0.0.4; charset=utf-8", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "stor_build_info{") {
		t.Errorf("missing stor_build_info in body:\n%s", body)
	}
	// HELP and TYPE lines must be present for exposed metrics.
	if !strings.Contains(body, "# HELP stor_build_info") {
		t.Error("missing HELP for stor_build_info")
	}
	if !strings.Contains(body, "# TYPE stor_build_info gauge") {
		t.Error("missing TYPE for stor_build_info")
	}
	// A few core metrics.
	for _, name := range []string{
		"stor_torrents_total",
		"stor_peers_total",
		"stor_dht_nodes_total",
		"stor_download_bytes_total",
		"stor_upload_bytes_total",
		"stor_download_speed_bytes_per_second",
		"stor_upload_speed_bytes_per_second",
		"stor_free_space_bytes",
		"go_goroutines",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("missing metric %s in body", name)
		}
	}
}

func TestMetricsAuthRequired(t *testing.T) {
	d, _ := newTestDaemonWithConfig(t, func(c *Config) {
		c.EnableMetrics = true
	})

	// No auth header.
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/metrics without auth: got %d, want 401", w.Code)
	}

	// Bad key.
	req = httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w = httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/metrics with bad key: got %d, want 401", w.Code)
	}
}

func TestPprofEnabled(t *testing.T) {
	d, cfg := newTestDaemonWithConfig(t, func(c *Config) {
		c.EnableMetrics = true
	})

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/debug/pprof/: got %d, want 200, body=%s", w.Code, w.Body.String())
	}
}

func TestPprofAuthRequired(t *testing.T) {
	d, _ := newTestDaemonWithConfig(t, func(c *Config) {
		c.EnableMetrics = true
	})

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", http.NoBody)
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/debug/pprof/ without auth: got %d, want 401", w.Code)
	}
}

func TestMetricsConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	cfg.EnableMetrics = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.EnableMetrics {
		t.Error("enable_metrics should be preserved across save/load")
	}
}

func TestMetricsConfigDefaultFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`api_key = "sk-x"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableMetrics {
		t.Error("enable_metrics should default to false")
	}
}
