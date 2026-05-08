package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPeerPortLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	cfg.PeerPort = 16881
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	// Round-trip: reload and verify peer_port preserved.
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PeerPort != 16881 {
		t.Errorf("peer_port: got %d, want 16881", reloaded.PeerPort)
	}
}

func TestConfigPeerPortDefaultsToZero(t *testing.T) {
	// When peer_port is not present in the TOML, it should default to 0
	// so the engine falls back to its own default (6881).
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("port = 9090\napi_key = \"sk-x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PeerPort != 0 {
		t.Errorf("peer_port should default to 0, got %d", cfg.PeerPort)
	}
}

func TestConfigPeerPortParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("peer_port = 51413\napi_key = \"sk-x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PeerPort != 51413 {
		t.Errorf("peer_port: got %d, want 51413", cfg.PeerPort)
	}
}

func TestConfigParsesInlineComments(t *testing.T) {
	// Users copy the README example which has inline `#` comments.
	// The parser must strip them before converting numeric values.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
peer_port = 16881    # avoid collision with Deluge
port = 9090          # Web UI port
max_active = 20      # concurrent downloads
api_key = "sk-x"     # pre-generated
enable_utp = true    # try uTP first
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PeerPort != 16881 {
		t.Errorf("peer_port: got %d, want 16881", cfg.PeerPort)
	}
	if cfg.Port != 9090 {
		t.Errorf("port: got %d, want 9090", cfg.Port)
	}
	if cfg.MaxActive != 20 {
		t.Errorf("max_active: got %d, want 20", cfg.MaxActive)
	}
	if cfg.APIKey != "sk-x" {
		t.Errorf("api_key: got %q, want %q", cfg.APIKey, "sk-x")
	}
	if !cfg.EnableUTP {
		t.Error("enable_utp: should be true")
	}
}

// TestConfigPrioritizeFirstLastRoundTrip ensures that the
// `prioritize_first_last_pieces` flag survives Save → LoadConfig and that
// it defaults to false when absent — matching the deluge default.
func TestConfigPrioritizeFirstLastRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	cfg.PrioritizeFirstLast = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.PrioritizeFirstLast {
		t.Error("prioritize_first_last_pieces: should be true after round-trip")
	}
}

func TestConfigPrioritizeFirstLastDefaultsToFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("port = 9090\napi_key = \"sk-x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PrioritizeFirstLast {
		t.Error("prioritize_first_last_pieces: should default to false when absent")
	}
}

func TestConfigPreservesHashInQuotedString(t *testing.T) {
	// A `#` inside a quoted string must NOT be stripped as a comment.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `api_key = "sk-abc#def"
download_dir = "/data/has#hash"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "sk-abc#def" {
		t.Errorf("api_key: got %q, want %q", cfg.APIKey, "sk-abc#def")
	}
	if cfg.DownloadDir != "/data/has#hash" {
		t.Errorf("download_dir: got %q, want %q", cfg.DownloadDir, "/data/has#hash")
	}
}
