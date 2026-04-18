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
