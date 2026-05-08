package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigSocketBufferRoundTrip verifies the new
// socket_send_buffer_bytes / socket_recv_buffer_bytes TOML fields
// survive Save/Load. Default 0 (auto-tune) is the safe choice and we
// rely on the UI / docs to steer users away from setting these unless
// they have measured a need.
func TestConfigSocketBufferRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	cfg.SocketSendBuffer = 1 << 20
	cfg.SocketRecvBuffer = 2 << 20
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"socket_send_buffer_bytes = 1048576",
		"socket_recv_buffer_bytes = 2097152",
	} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("missing %q in saved TOML:\n%s", want, saved)
		}
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SocketSendBuffer != 1<<20 {
		t.Errorf("SocketSendBuffer = %d, want %d", reloaded.SocketSendBuffer, 1<<20)
	}
	if reloaded.SocketRecvBuffer != 2<<20 {
		t.Errorf("SocketRecvBuffer = %d, want %d", reloaded.SocketRecvBuffer, 2<<20)
	}
}

// TestConfigSocketBufferZeroOmitted ensures unset (=0, "auto-tune")
// fields don't leak `= 0` lines into the TOML — that would suggest
// to users reading the file that we're explicitly disabling
// auto-tuning, which is exactly the trap we want to avoid.
func TestConfigSocketBufferZeroOmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.APIKey = "sk-test"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	saved, _ := os.ReadFile(path)
	for _, key := range []string{"socket_send_buffer_bytes", "socket_recv_buffer_bytes"} {
		if strings.Contains(string(saved), key) {
			t.Errorf("%s should be omitted when zero, got:\n%s", key, saved)
		}
	}
}
