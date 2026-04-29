package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigMaxGlobalDialsRoundTrip verifies that the new max_global_dials
// TOML field is loaded, saved, and re-loaded without loss.
func TestConfigMaxGlobalDialsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	cfg.MaxGlobalDials = 500
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "max_global_dials = 500") {
		t.Errorf("expected max_global_dials in saved TOML, got:\n%s", saved)
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.MaxGlobalDials != 500 {
		t.Errorf("MaxGlobalDials = %d, want 500", reloaded.MaxGlobalDials)
	}
}

// TestConfigMaxGlobalDialsZeroOmitted ensures that when the user has not set
// max_global_dials, Save() does not write a useless `= 0` line — engine.New
// will apply its default in that case.
func TestConfigMaxGlobalDialsZeroOmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	// MaxGlobalDials left at 0 (use default).
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), "max_global_dials") {
		t.Errorf("max_global_dials should be omitted when zero, got:\n%s", saved)
	}
}
