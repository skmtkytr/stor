package engine

import (
	"testing"
)

// TestEngineDialPoolDefaults verifies that omitting MaxGlobalDials and
// DialTimeout from Config falls back to the documented defaults
// (200 dials, 8 second timeout).
func TestEngineDialPoolDefaults(t *testing.T) {
	cfg := Config{
		DownloadDir:     t.TempDir(),
		StatePath:       t.TempDir() + "/state.json",
		DisableDHT:      true,
		DisableListener: true,
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop() })

	if got := cap(eng.dialSem); got != 200 {
		t.Errorf("dialSem capacity = %d, want 200 (default)", got)
	}
	if got := eng.cfg.DialTimeout; got != 8 {
		t.Errorf("DialTimeout = %d, want 8 (default)", got)
	}
}

// TestEngineDialPoolHonoursConfig verifies user-provided values take
// precedence over the defaults — this is the knob that lets a business-
// grade router (e.g. RTX1300) push past the conservative default.
func TestEngineDialPoolHonoursConfig(t *testing.T) {
	cfg := Config{
		DownloadDir:     t.TempDir(),
		StatePath:       t.TempDir() + "/state.json",
		MaxGlobalDials:  500,
		DialTimeout:     12,
		DisableDHT:      true,
		DisableListener: true,
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop() })

	if got := cap(eng.dialSem); got != 500 {
		t.Errorf("dialSem capacity = %d, want 500", got)
	}
	if got := eng.cfg.DialTimeout; got != 12 {
		t.Errorf("DialTimeout = %d, want 12", got)
	}
}
