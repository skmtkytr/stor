package engine

import (
	"context"
	"testing"
	"time"

	"github.com/skmtkytr/stor/events"
	"github.com/skmtkytr/stor/magnet"
	"github.com/skmtkytr/stor/torrent"
)

// TestEventPeerSearchFailed verifies that the peer-search retry path on
// findPeers emits TypePeerSearchFailed with attempt count and backoff.
//
// Setup: a Session with no trackers and a private torrent (so DHT is
// skipped). The first attempt completes immediately with 0 peers and the
// retry loop fires the event before sleeping for the next backoff. We give
// the call a small ctx timeout so it returns instead of looping forever.
func TestEventPeerSearchFailed(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	_, finish := startRecorder(t, bus, "peer-search-failed")

	s := &Session{
		record: &TorrentRecord{ID: "tid-peer-fail"},
		tf: &torrent.TorrentFile{
			Info: torrent.Info{Name: "x", Private: true},
		},
		bus: bus,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = s.findPeers(ctx)
		close(done)
	}()

	// Wait long enough for the first attempt + emit, but cancel before the
	// 30s peerSearchInitial backoff actually elapses. The emit happens
	// before the time.After in findPeers so we are guaranteed to see it.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	evs := finish()
	if got := countByType(evs, events.TypePeerSearchFailed); got < 1 {
		t.Fatalf("TypePeerSearchFailed not emitted; events=%v", eventTypes(evs))
	}

	for _, ev := range evs {
		if ev.Type != events.TypePeerSearchFailed {
			continue
		}
		if ev.TorrentID != "tid-peer-fail" {
			t.Errorf("TorrentID = %q, want tid-peer-fail", ev.TorrentID)
		}
		p, ok := ev.Payload.(events.PeerSearchFailedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want PeerSearchFailedPayload", ev.Payload)
		}
		if p.AttemptCount < 1 {
			t.Errorf("AttemptCount = %d, want >= 1", p.AttemptCount)
		}
		if p.NextRetryIn <= 0 {
			t.Errorf("NextRetryIn = %d, want > 0", p.NextRetryIn)
		}
	}
}

// TestEventMetadataAttemptFailed verifies the metadata-fetch retry path.
//
// Setup: a Session resolving a magnet URI with no trackers, no DHT, no
// x.pe peers. fetchMetadataAttempt's metaCtx times out after
// metadataAttemptTTL (60s default), which is too long for tests. We bypass
// that by calling the outer resolveMetadata loop and cancelling early —
// the emit at the resolveMetadata level fires before the backoff sleep.
//
// To keep this test fast we override metadataAttemptTTL via a magnet URI
// path that produces zero peers immediately: with no trackers, no DHT,
// and no x.pe addresses, fetchMetadataAttempt waits up to its TTL for any
// peer to appear. We make ctx cancellation cut that short.
func TestEventMetadataAttemptFailed(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	_, finish := startRecorder(t, bus, "meta-fail")

	// Construct a magnet with zero discoverable peers.
	const magURI = "magnet:?xt=urn:btih:0000000000000000000000000000000000000001"
	if _, err := magnet.Parse(magURI); err != nil {
		t.Fatalf("magnet parse: %v", err)
	}

	s := &Session{
		record: &TorrentRecord{ID: "tid-meta-fail", Source: magURI},
		bus:    bus,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = s.resolveMetadata(ctx)
		close(done)
	}()

	// fetchMetadataAttempt blocks until its inner metaCtx times out
	// (metadataAttemptTTL == 60s). Cancel the outer ctx after a short
	// pause so the metaCtx parent is also cancelled, unblocking the
	// inner select and triggering the emit on resolveMetadata's slow-path
	// retry log line. We wait a generous slice for the goroutine teardown.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("resolveMetadata did not return after ctx cancel")
	}

	evs := finish()
	// Either of the two emit points fires depending on timing: the inner
	// fetchMetadataAttempt path (when metaCtx ends with no result) and the
	// outer resolveMetadata retry path (when fetchMetadataAttempt returns
	// nil and we are about to back off). At minimum one should fire.
	if got := countByType(evs, events.TypeMetadataAttemptFailed); got < 1 {
		t.Fatalf("TypeMetadataAttemptFailed not emitted; events=%v", eventTypes(evs))
	}

	for _, ev := range evs {
		if ev.Type != events.TypeMetadataAttemptFailed {
			continue
		}
		if ev.TorrentID != "tid-meta-fail" {
			t.Errorf("TorrentID = %q, want tid-meta-fail", ev.TorrentID)
		}
		if _, ok := ev.Payload.(events.MetadataAttemptFailedPayload); !ok {
			t.Fatalf("payload type = %T, want MetadataAttemptFailedPayload", ev.Payload)
		}
	}
}

// TestEventDHTLookupFailed exercises the actual dhtLookup error path by
// injecting a GetPeers stub that returns an error. We cannot use a real
// DHT in unit tests (DNS bootstrap, goroutines), so dhtGetPeersFn provides
// a seam.
func TestEventDHTLookupFailed(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	_, finish := startRecorder(t, bus, "dht-fail")

	s := &Session{
		record: &TorrentRecord{ID: "tid-dht-fail"},
		bus:    bus,
		dhtGetPeersFn: func(_ [20]byte) ([]string, error) {
			return nil, &dhtTestError{msg: "no responses within timeout"}
		},
	}

	got := s.dhtLookup([20]byte{0x01})
	if got != nil {
		t.Errorf("dhtLookup result = %v, want nil on error", got)
	}

	evs := finish()
	if c := countByType(evs, events.TypeDHTLookupFailed); c != 1 {
		t.Fatalf("TypeDHTLookupFailed count = %d, want 1; events=%v", c, eventTypes(evs))
	}
	for _, ev := range evs {
		if ev.Type != events.TypeDHTLookupFailed {
			continue
		}
		if ev.TorrentID != "tid-dht-fail" {
			t.Errorf("TorrentID = %q, want tid-dht-fail", ev.TorrentID)
		}
		p, ok := ev.Payload.(events.DHTLookupFailedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want DHTLookupFailedPayload", ev.Payload)
		}
		if p.Error != "no responses within timeout" {
			t.Errorf("Error = %q, want %q", p.Error, "no responses within timeout")
		}
	}
}

// dhtTestError implements error with a stable message for the failure test.
type dhtTestError struct{ msg string }

func (e *dhtTestError) Error() string { return e.msg }
