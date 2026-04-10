package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"", slog.LevelInfo},        // default
		{"unknown", slog.LevelInfo}, // default
	}

	for _, tt := range tests {
		got := ParseLogLevel(tt.input)
		if got != tt.want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestConfigLogLevelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg1, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if cfg1.LogLevel != "info" {
		t.Errorf("default log_level: got %q, want %q", cfg1.LogLevel, "info")
	}

	cfg1.LogLevel = "debug"
	if err := cfg1.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg2.LogLevel != "debug" {
		t.Errorf("log_level after reload: got %q, want %q", cfg2.LogLevel, "debug")
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `log_level = "debug"`) {
		t.Error("saved TOML should contain log_level")
	}
}

func TestHTTPRequestLogging(t *testing.T) {
	// Capture slog output
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldDefault)

	d, cfg := newTestDaemon(t)

	// Make an authenticated RPC request
	w := doRPC(t, d, cfg.APIKey, "daemon.version", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	// Check log output contains expected entries
	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	var foundHTTP, foundRPC bool
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		msg, _ := entry["msg"].(string)
		if msg == "http request" {
			foundHTTP = true
			if path, ok := entry["path"].(string); !ok || path != "/api/rpc" {
				t.Errorf("http log path: got %v", entry["path"])
			}
			if status, ok := entry["status"].(float64); !ok || status != 200 {
				t.Errorf("http log status: got %v", entry["status"])
			}
		}
		if msg == "rpc call" {
			foundRPC = true
			if method, ok := entry["method"].(string); !ok || method != "daemon.version" {
				t.Errorf("rpc log method: got %v", entry["method"])
			}
		}
	}

	if !foundHTTP {
		t.Error("expected 'http request' log entry")
	}
	if !foundRPC {
		t.Error("expected 'rpc call' log entry")
	}
}

func TestAuthFailureLogging(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldDefault)

	d, _ := newTestDaemon(t)

	// Request without auth
	req := httptest.NewRequest(http.MethodPost, "/api/rpc", strings.NewReader(`{"jsonrpc":"2.0","method":"daemon.version","id":1}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d, want 401", w.Code)
	}

	output := buf.String()
	if !strings.Contains(output, "auth failed") {
		t.Error("expected 'auth failed' log entry")
	}
}

func TestRPCErrorLogging(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldDefault)

	d, cfg := newTestDaemon(t)

	// Call nonexistent method
	w := doRPC(t, d, cfg.APIKey, "nonexistent.method", nil)
	_ = w

	output := buf.String()
	if !strings.Contains(output, "rpc error") {
		t.Error("expected 'rpc error' log entry")
	}
	if !strings.Contains(output, "nonexistent.method") {
		t.Error("expected method name in rpc error log")
	}
}
