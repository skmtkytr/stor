package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/skmtkytr/stor/torrent"
)

// TestFindPeersExitsOnCancel verifies that findPeers returns context.Canceled
// when the context is cancelled while waiting for the retry backoff. With no
// trackers and a private torrent (DHT disabled), each attempt returns
// immediately and the function spends most of its time in the retry sleep.
func TestFindPeersExitsOnCancel(t *testing.T) {
	s := &Session{
		record: &TorrentRecord{ID: "test"},
		tf: &torrent.TorrentFile{
			Info: torrent.Info{
				Name:    "x",
				Private: true, // skip DHT
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := s.findPeers(ctx)
		done <- err
	}()

	// Let first attempt complete and enter the retry sleep.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("findPeers err: got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("findPeers did not return after cancel")
	}
}

// TestFindPeersAttemptEmpty verifies a single attempt returns an empty
// slice (not nil-panic) when there are no peer sources available.
func TestFindPeersAttemptEmpty(t *testing.T) {
	s := &Session{
		record: &TorrentRecord{ID: "test"},
		tf: &torrent.TorrentFile{
			Info: torrent.Info{Name: "x", Private: true},
		},
	}
	peers := s.findPeersAttempt(context.Background())
	if len(peers) != 0 {
		t.Errorf("expected 0 peers, got %d", len(peers))
	}
}
