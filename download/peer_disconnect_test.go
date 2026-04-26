package download

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skmtkytr/stor/events"
)

// TestSetDisconnectReasonFirstWins verifies the first non-empty reason is
// preserved: a later "normal" from a deferred Close must NOT overwrite a
// specific earlier failure (e.g. "protocol_error: ...").
func TestSetDisconnectReasonFirstWins(t *testing.T) {
	c := &Client{Addr: "127.0.0.1:6881"}
	c.SetDisconnectReason("protocol_error: bitfield size")
	c.SetDisconnectReason("normal")
	if got := c.DisconnectReason(); got != "protocol_error: bitfield size" {
		t.Errorf("DisconnectReason = %q, want first reason preserved", got)
	}
}

// TestSetDisconnectReasonIgnoresEmpty verifies the empty string is a
// no-op (so callers can SetDisconnectReason(unconditionally) with a
// classifier output without clobbering a real reason).
func TestSetDisconnectReasonIgnoresEmpty(t *testing.T) {
	c := &Client{Addr: "127.0.0.1:6881"}
	c.SetDisconnectReason("timeout")
	c.SetDisconnectReason("")
	if got := c.DisconnectReason(); got != "timeout" {
		t.Errorf("DisconnectReason = %q, want %q", got, "timeout")
	}
}

// TestPeerDisconnectReasonPopulated drives PeerManager.Unregister through
// a freshly constructed Bus, with each test client carrying a different
// disconnect reason. The published TypePeerDisconnected event must carry
// that reason — never the empty string.
func TestPeerDisconnectReasonPopulated(t *testing.T) {
	cases := []string{
		"normal",
		"timeout",
		"protocol_error: hash mismatch",
		"choked_out",
		"peer_closed",
	}

	for _, reason := range cases {
		t.Run(reason, func(t *testing.T) {
			bus := events.New()
			t.Cleanup(bus.Close)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sub := bus.Subscribe(ctx, events.SubscribeOptions{Buffer: 8})
			rec := events.NewRecorder()
			go rec.Drain(sub)

			pm := NewPeerManager(2)
			pm.Bus = bus
			pm.TorrentID = "tid-X"

			c := &Client{Addr: "1.2.3.4:6881"}
			pm.Register(c)
			c.SetDisconnectReason(reason)
			pm.Unregister(c)

			time.Sleep(50 * time.Millisecond)
			cancel()
			rec.Wait()

			var found *events.PeerDisconnectedPayload
			for _, ev := range rec.Events() {
				if ev.Type != events.TypePeerDisconnected {
					continue
				}
				p, ok := ev.Payload.(events.PeerDisconnectedPayload)
				if !ok {
					t.Fatalf("payload type: %T", ev.Payload)
				}
				found = &p
			}
			if found == nil {
				t.Fatalf("no TypePeerDisconnected event for reason=%q", reason)
			}
			if found.Reason == "" {
				t.Errorf("Reason is empty for case %q", reason)
			}
			if found.Reason != reason {
				t.Errorf("Reason = %q, want %q", found.Reason, reason)
			}
		})
	}
}

// TestPeerDisconnectReasonDefaultsToUnknown verifies that when no caller
// set a reason, Unregister falls back to "unknown" rather than emitting
// an empty string (the bug this whole task closes).
func TestPeerDisconnectReasonDefaultsToUnknown(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx, events.SubscribeOptions{Buffer: 8})
	rec := events.NewRecorder()
	go rec.Drain(sub)

	pm := NewPeerManager(2)
	pm.Bus = bus
	pm.TorrentID = "tid-Y"

	c := &Client{Addr: "9.9.9.9:6881"}
	pm.Register(c)
	pm.Unregister(c) // no SetDisconnectReason call

	time.Sleep(50 * time.Millisecond)
	cancel()
	rec.Wait()

	for _, ev := range rec.Events() {
		if ev.Type != events.TypePeerDisconnected {
			continue
		}
		p := ev.Payload.(events.PeerDisconnectedPayload)
		if p.Reason != "unknown" {
			t.Errorf("Reason = %q, want %q", p.Reason, "unknown")
		}
		return
	}
	t.Fatalf("no TypePeerDisconnected event published")
}

// TestClassifyDownloadErrorMapping cross-checks the error→reason
// classifier so future contributors can see which error strings produce
// which short tags. Tags must be stable: UI / metrics consumers depend
// on them.
func TestClassifyDownloadErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"timeout_io", errors.New("read tcp 1.2.3.4:6881: i/o timeout"), "timeout"},
		{"timeout_deadline", errors.New("context deadline exceeded"), "timeout"},
		{"choke", errors.New("download: peer choked during piece 3"), "choked_out"},
		{"hash_mismatch_protocol", errors.New("download: piece 5 hash mismatch"), "protocol_error: download: piece 5 hash mismatch"},
		{"reject_protocol", errors.New("download: peer rejected piece 7"), "protocol_error: download: peer rejected piece 7"},
		{"oob_protocol", errors.New("download: piece 5 block out of bounds (...)"), "protocol_error: download: piece 5 block out of bounds (...)"},
		{"too_many_protocol", errors.New("download: piece 1 too many unexpected messages from peer"), "protocol_error: download: piece 1 too many unexpected messages from peer"},
		{"eof", errors.New("EOF"), "peer_closed"},
		{"reset", errors.New("read: connection reset by peer"), "peer_closed"},
		{"unknown", errors.New("something weird happened"), "unknown"},
		{"nil", nil, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyDownloadError(c.err)
			if got != c.want {
				t.Errorf("classifyDownloadError(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// TestClassifyDownloadErrorWrapped verifies the classifier handles the
// wrapped fmt.Errorf style used throughout the download package.
func TestClassifyDownloadErrorWrapped(t *testing.T) {
	wrapped := fmt.Errorf("download: outer: %w", errors.New("read tcp: i/o timeout"))
	got := classifyDownloadError(wrapped)
	if !strings.HasPrefix(got, "timeout") {
		t.Errorf("wrapped timeout: got %q, want prefix %q", got, "timeout")
	}
}
