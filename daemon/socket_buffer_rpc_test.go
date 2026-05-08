package daemon

import (
	"encoding/json"
	"testing"
)

// TestRPCSetConfigAcceptsSocketBuffers verifies that the JSON-RPC
// daemon.setConfig method accepts the new socket buffer fields and
// reflects them back via daemon.stats. The +ve path is the only
// interesting one — zero is the default and means "unchanged" at
// the engine layer (auto-tune left enabled).
func TestRPCSetConfigAcceptsSocketBuffers(t *testing.T) {
	d, cfg := newTestDaemon(t)

	w := doRPC(t, d, cfg.APIKey, "daemon.setConfig", map[string]any{
		"socket_send_buffer_bytes": 1 << 20,
		"socket_recv_buffer_bytes": 2 << 20,
	})
	var setResp struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &setResp)
	if setResp.Error != nil {
		t.Fatalf("setConfig returned error code %d", setResp.Error.Code)
	}

	w = doRPC(t, d, cfg.APIKey, "daemon.stats", nil)
	var statsResp struct {
		Result struct {
			Config struct {
				SocketSendBuffer int `json:"socket_send_buffer_bytes"`
				SocketRecvBuffer int `json:"socket_recv_buffer_bytes"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &statsResp); err != nil {
		t.Fatalf("decode stats: %v\nbody=%s", err, w.Body.String())
	}
	if statsResp.Result.Config.SocketSendBuffer != 1<<20 {
		t.Errorf("socket_send_buffer_bytes = %d, want %d",
			statsResp.Result.Config.SocketSendBuffer, 1<<20)
	}
	if statsResp.Result.Config.SocketRecvBuffer != 2<<20 {
		t.Errorf("socket_recv_buffer_bytes = %d, want %d",
			statsResp.Result.Config.SocketRecvBuffer, 2<<20)
	}
}

// TestRPCSetConfigRejectsNegativeSocketBuffers checks the input
// validation guard. We accept 0 (auto-tune) but a negative value is
// nonsensical and should be ignored to avoid silently calling
// SetReadBuffer(-1) which the kernel will reject anyway.
func TestRPCSetConfigRejectsNegativeSocketBuffers(t *testing.T) {
	d, cfg := newTestDaemon(t)

	// First, set a known-good positive value so we can detect
	// whether the negative request silently overwrote it.
	w := doRPC(t, d, cfg.APIKey, "daemon.setConfig", map[string]any{
		"socket_send_buffer_bytes": 1 << 20,
		"socket_recv_buffer_bytes": 1 << 20,
	})
	var resp struct {
		Error *struct{ Code int } `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != nil {
		t.Fatalf("setup setConfig returned error: %d", resp.Error.Code)
	}

	// Now try to push -1 — must NOT clobber the previous value.
	w = doRPC(t, d, cfg.APIKey, "daemon.setConfig", map[string]any{
		"socket_send_buffer_bytes": -1,
		"socket_recv_buffer_bytes": -1,
	})
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	// Either the RPC rejects with an error or silently ignores; we
	// only care that the value is unchanged on the next stats call.

	w = doRPC(t, d, cfg.APIKey, "daemon.stats", nil)
	var statsResp struct {
		Result struct {
			Config struct {
				SocketSendBuffer int `json:"socket_send_buffer_bytes"`
				SocketRecvBuffer int `json:"socket_recv_buffer_bytes"`
			} `json:"config"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &statsResp)
	if statsResp.Result.Config.SocketSendBuffer != 1<<20 {
		t.Errorf("send buffer was clobbered by -1 request: got %d",
			statsResp.Result.Config.SocketSendBuffer)
	}
	if statsResp.Result.Config.SocketRecvBuffer != 1<<20 {
		t.Errorf("recv buffer was clobbered by -1 request: got %d",
			statsResp.Result.Config.SocketRecvBuffer)
	}
}
