package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigPipelineDynamicRoundTrip verifies the new pipeline_min /
// pipeline_max / pipeline_window_secs TOML fields survive Save/Load.
func TestConfigPipelineDynamicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	cfg.PipelineMin = 8
	cfg.PipelineMax = 512
	cfg.PipelineWindowSecs = 5
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pipeline_min = 8",
		"pipeline_max = 512",
		"pipeline_window_secs = 5",
	} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("missing %q in saved TOML:\n%s", want, saved)
		}
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PipelineMin != 8 {
		t.Errorf("PipelineMin = %d, want 8", reloaded.PipelineMin)
	}
	if reloaded.PipelineMax != 512 {
		t.Errorf("PipelineMax = %d, want 512", reloaded.PipelineMax)
	}
	if reloaded.PipelineWindowSecs != 5 {
		t.Errorf("PipelineWindowSecs = %d, want 5", reloaded.PipelineWindowSecs)
	}
}

// TestConfigPipelineDynamicZeroOmitted ensures unset pipeline fields
// don't leak `= 0` lines into the TOML — engine.New applies defaults.
func TestConfigPipelineDynamicZeroOmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	saved, _ := os.ReadFile(path)
	for _, key := range []string{"pipeline_min", "pipeline_max", "pipeline_window_secs"} {
		if strings.Contains(string(saved), key) {
			t.Errorf("%s should be omitted when zero, got:\n%s", key, saved)
		}
	}
}
