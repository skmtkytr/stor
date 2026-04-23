package engine

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

func minimalTF() *torrent.TorrentFile {
	return &torrent.TorrentFile{
		InfoHash: [20]byte{0x01},
		Info: torrent.Info{
			Name:        "test",
			PieceLength: 512,
			PieceHashes: [][20]byte{{}},
			Length:      512,
		},
	}
}

// TestDHTRefreshIndependentOfAnnounce verifies that the DHT lookup fires
// on its own dhtInterval, independently of the tracker re-announce interval.
func TestDHTRefreshIndependentOfAnnounce(t *testing.T) {
	var dhtCalls atomic.Int32

	peerSink := make(chan []tracker.Peer, 10)

	cfg := AnnounceConfig{
		TF:          minimalTF(),
		PeerID:      [20]byte{1},
		Port:        6881,
		NumWant:     50,
		PeerSink:    peerSink,
		Downloaded:  func() int64 { return 0 },
		Left:        func() int64 { return 512 },
		DHTInterval: 100 * time.Millisecond,
		DHTLookupFn: func(_ [20]byte) []tracker.Peer {
			dhtCalls.Add(1)
			return []tracker.Peer{{IP: net.IPv4(1, 2, 3, 4), Port: 6881}}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	a := NewAnnouncer(cfg)
	// Run in goroutine; it blocks until ctx is done.
	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()
	<-done

	calls := dhtCalls.Load()
	// Initial announce + at least 2 independent DHT refreshes within 500ms at 100ms interval.
	if calls < 3 {
		t.Errorf("expected at least 3 DHT lookup calls (1 initial + 2 refreshes), got %d", calls)
	}
}

// TestDHTRefreshSkippedForPrivate verifies DHT is not called for private torrents.
func TestDHTRefreshSkippedForPrivate(t *testing.T) {
	var dhtCalls atomic.Int32

	tf := minimalTF()
	tf.Info.Private = true

	cfg := AnnounceConfig{
		TF:          tf,
		PeerID:      [20]byte{1},
		Port:        6881,
		NumWant:     50,
		PeerSink:    make(chan []tracker.Peer, 10),
		Downloaded:  func() int64 { return 0 },
		Left:        func() int64 { return 512 },
		DHTInterval: 50 * time.Millisecond,
		DHTLookupFn: func(_ [20]byte) []tracker.Peer {
			dhtCalls.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	a := NewAnnouncer(cfg)
	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()
	<-done

	if dhtCalls.Load() != 0 {
		t.Errorf("expected 0 DHT calls for private torrent, got %d", dhtCalls.Load())
	}
}
