package engine

import (
	"testing"

	"github.com/skmtkytr/stor/download"
)

// TestSetFilePriorityHotUpdatesLivePieceQueue verifies that calling
// SetFilePriority while a download is in progress pushes the new skip
// mask to the live PieceQueue immediately — no pause/resume required.
//
// We don't spin up a real download goroutine (that would need peers,
// storage, etc.), so we drive the session's pq field directly: attach a
// PieceQueue as if phaseDownload had just installed it, call
// SetFilePriority, and check Pick no longer returns the skipped piece.
func TestSetFilePriorityHotUpdatesLivePieceQueue(t *testing.T) {
	eng := newTestEngine(t)

	// Three files of one piece each (piece length 256, file size 256 each),
	// so the file → piece mapping is 1:1 and hot-skipping file N makes
	// piece N unpickable.
	data, _ := buildMultiFileTorrentData(t, "hot", 256, []struct {
		path []string
		size int
	}{
		{path: []string{"a.bin"}, size: 256},
		{path: []string{"b.bin"}, size: 256},
		{path: []string{"c.bin"}, size: 256},
	})
	id, err := eng.AddTorrentFile(data)
	if err != nil {
		t.Fatalf("AddTorrentFile: %v", err)
	}

	// Reach into the engine to simulate phaseDownload having started.
	eng.mu.RLock()
	s := eng.sessions[id]
	eng.mu.RUnlock()
	if s == nil {
		t.Fatalf("session not found")
	}

	pieces := []download.PieceWork{
		{Index: 0, Length: 256},
		{Index: 1, Length: 256},
		{Index: 2, Length: 256},
	}
	pq := download.NewPieceQueue(pieces)
	s.mu.Lock()
	s.pq = pq
	s.mu.Unlock()

	// All three pieces pickable before we change priorities.
	if got := pq.Remaining(); got != 3 {
		t.Fatalf("Remaining before: got %d, want 3", got)
	}

	// Skip file 1 (piece 1).
	if err := eng.SetFilePriority(id, 1, PrioritySkip); err != nil {
		t.Fatalf("SetFilePriority: %v", err)
	}

	if got := pq.Remaining(); got != 2 {
		t.Fatalf("Remaining after hot skip: got %d, want 2", got)
	}

	// Pick should never return piece 1.
	hasAll := func(int) bool { return true }
	picked := make(map[int]bool)
	for range 10 {
		pw, ok := pq.Pick(hasAll)
		if !ok {
			break
		}
		picked[pw.Index] = true
		pq.Complete(pw.Index)
	}
	if picked[1] {
		t.Errorf("hot skip did not reach the live queue: piece 1 was picked")
	}
	if !picked[0] || !picked[2] {
		t.Errorf("wanted pieces missed: %v", picked)
	}
}

// TestSetFilePriorityHotUnskipRestoresPiece verifies that clearing a
// skip priority while a download is in progress un-filters the piece
// in the live queue — again without pause/resume.
func TestSetFilePriorityHotUnskipRestoresPiece(t *testing.T) {
	eng := newTestEngine(t)

	data, _ := buildMultiFileTorrentData(t, "hotunskip", 256, []struct {
		path []string
		size int
	}{
		{path: []string{"a.bin"}, size: 256},
		{path: []string{"b.bin"}, size: 256},
	})
	id, err := eng.AddTorrentFile(data)
	if err != nil {
		t.Fatalf("AddTorrentFile: %v", err)
	}

	eng.mu.RLock()
	s := eng.sessions[id]
	eng.mu.RUnlock()

	pieces := []download.PieceWork{
		{Index: 0, Length: 256},
		{Index: 1, Length: 256},
	}
	pq := download.NewPieceQueue(pieces) // ctor had no skip — both pieces in queue
	s.mu.Lock()
	s.pq = pq
	s.mu.Unlock()

	// Skip file 0.
	if err := eng.SetFilePriority(id, 0, PrioritySkip); err != nil {
		t.Fatalf("skip: %v", err)
	}
	if pq.Remaining() != 1 {
		t.Fatalf("after skip: Remaining got %d, want 1", pq.Remaining())
	}

	// Clear skip.
	if err := eng.SetFilePriority(id, 0, PriorityNormal); err != nil {
		t.Fatalf("unskip: %v", err)
	}
	if pq.Remaining() != 2 {
		t.Fatalf("after unskip: Remaining got %d, want 2", pq.Remaining())
	}
}
