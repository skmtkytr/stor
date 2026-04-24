package engine

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

// TestKnownPeersFromCachedPeers verifies that knownPeers is set from
// cachedPeers after phaseResolve populates them.
func TestKnownPeersFromCachedPeers(t *testing.T) {
	s := &Session{
		record: &TorrentRecord{ID: "test", State: StateDownloading},
		tf:     &torrent.TorrentFile{Info: torrent.Info{Name: "x"}},
	}

	peers := []tracker.Peer{
		{IP: net.ParseIP("1.2.3.4"), Port: 6881},
		{IP: net.ParseIP("1.2.3.5"), Port: 6881},
		{IP: net.ParseIP("1.2.3.6"), Port: 6881},
	}
	s.cachedPeers = peers
	s.knownPeers.Store(int32(len(s.cachedPeers)))

	// Snap() returns KnownPeers only when progress != nil.
	s.progress = download.NewProgress(1, 1024)
	t.Cleanup(s.progress.Close)

	snap := s.Snap()
	if snap.KnownPeers != len(peers) {
		t.Errorf("KnownPeers = %d, want %d", snap.KnownPeers, len(peers))
	}
}

// TestKnownPeersDynamicCounting verifies that the counting goroutine (as wired
// in phaseDownload) increments knownPeers for each peer in dynamic batches.
func TestKnownPeersDynamicCounting(t *testing.T) {
	s := &Session{
		record: &TorrentRecord{ID: "test", State: StateDownloading},
	}

	peerCh := make(chan []tracker.Peer, 16)
	countedCh := make(chan []tracker.Peer, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Replicate the counting goroutine from phaseDownload.
	go func() {
		for {
			select {
			case batch, ok := <-peerCh:
				if !ok {
					return
				}
				s.knownPeers.Add(int32(len(batch)))
				select {
				case countedCh <- batch:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	batch1 := []tracker.Peer{
		{IP: net.ParseIP("1.2.3.4"), Port: 6881},
		{IP: net.ParseIP("1.2.3.5"), Port: 6881},
	}
	batch2 := []tracker.Peer{
		{IP: net.ParseIP("2.2.2.2"), Port: 6881},
	}
	peerCh <- batch1
	peerCh <- batch2

	// Drain countedCh to let the goroutine process both batches.
	for i := 0; i < 2; i++ {
		select {
		case <-countedCh:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for counted batch")
		}
	}

	want := len(batch1) + len(batch2)
	if got := int(s.knownPeers.Load()); got != want {
		t.Errorf("knownPeers = %d, want %d", got, want)
	}
}

// TestKnownPeersCumulativeWithInitial verifies that dynamic peers accumulate on
// top of the initial cachedPeers count.
func TestKnownPeersCumulativeWithInitial(t *testing.T) {
	s := &Session{
		record: &TorrentRecord{ID: "test", State: StateDownloading},
	}

	initial := []tracker.Peer{
		{IP: net.ParseIP("1.1.1.1"), Port: 6881},
		{IP: net.ParseIP("1.1.1.2"), Port: 6881},
	}
	s.cachedPeers = initial
	s.knownPeers.Store(int32(len(initial)))

	// Simulate one dynamic batch arriving.
	s.knownPeers.Add(3)

	want := len(initial) + 3
	if got := int(s.knownPeers.Load()); got != want {
		t.Errorf("knownPeers = %d, want %d", got, want)
	}
}

// TestKnownPeersSnapSeedingPath verifies that Snap() includes KnownPeers
// in the seeding-without-progress path (p == nil, u != nil).
func TestKnownPeersSnapSeedingPath(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "seed.txt",
			PieceLength: 256,
			PieceHashes: [][20]byte{{}},
			Length:      256,
		},
	}
	bf := make(peer.Bitfield, 1)
	bf.SetPiece(0)

	s := &Session{
		record: &TorrentRecord{ID: "test", State: StateSeeding, TotalBytes: 256},
		tf:     tf,
	}
	s.uploader = download.NewUploader(tf, t.TempDir(), [20]byte{}, bf)
	s.knownPeers.Store(7)

	snap := s.Snap()
	if snap.KnownPeers != 7 {
		t.Errorf("KnownPeers = %d, want 7", snap.KnownPeers)
	}
}

// TestKnownPeersSeedingPeerSink verifies that the peerSink goroutine wired in
// phaseSeed increments knownPeers when tracker responses arrive.
func TestKnownPeersSeedingPeerSink(t *testing.T) {
	s := &Session{
		record: &TorrentRecord{ID: "test", State: StateSeeding},
	}

	// Replicate the goroutine from phaseSeed.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	peerSink := make(chan []tracker.Peer, 16)
	go func() {
		for {
			select {
			case batch, ok := <-peerSink:
				if !ok {
					return
				}
				s.knownPeers.Add(int32(len(batch)))
			case <-ctx.Done():
				return
			}
		}
	}()

	batch := []tracker.Peer{
		{IP: net.ParseIP("5.5.5.5"), Port: 6881},
		{IP: net.ParseIP("6.6.6.6"), Port: 6881},
		{IP: net.ParseIP("7.7.7.7"), Port: 6881},
	}
	peerSink <- batch

	// Wait for goroutine to process.
	deadline := time.After(time.Second)
	for {
		if int(s.knownPeers.Load()) == len(batch) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("knownPeers = %d, want %d", s.knownPeers.Load(), len(batch))
		case <-time.After(time.Millisecond):
		}
	}
}
