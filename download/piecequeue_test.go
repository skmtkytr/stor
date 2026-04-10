package download

import (
	"testing"

	"github.com/skmtkytr/stor/peer"
)

func TestPieceQueueRarestFirst(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
		{Index: 2, Length: 100},
		{Index: 3, Length: 100},
	}
	pq := NewPieceQueue(pieces)

	// Set availability: piece 1 is rarest (1), piece 2 most common (10)
	pq.mu.Lock()
	pq.availability[0] = 5
	pq.availability[1] = 1
	pq.availability[2] = 10
	pq.availability[3] = 3
	pq.mu.Unlock()

	// Peer has all pieces
	hasAll := func(int) bool { return true }

	pw, ok := pq.Pick(hasAll)
	if !ok {
		t.Fatal("expected a piece")
	}
	if pw.Index != 1 {
		t.Fatalf("expected rarest piece 1, got %d", pw.Index)
	}
}

func TestPieceQueuePeerFilter(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
		{Index: 2, Length: 100},
	}
	pq := NewPieceQueue(pieces)

	pq.mu.Lock()
	pq.availability[0] = 10
	pq.availability[1] = 1 // rarest, but peer doesn't have it
	pq.availability[2] = 3
	pq.mu.Unlock()

	// Peer only has pieces 0 and 2
	hasPartial := func(idx int) bool { return idx == 0 || idx == 2 }

	pw, ok := pq.Pick(hasPartial)
	if !ok {
		t.Fatal("expected a piece")
	}
	if pw.Index != 2 {
		t.Fatalf("expected piece 2 (rarest peer has), got %d", pw.Index)
	}
}

func TestPieceQueueReturnAndComplete(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
	}
	pq := NewPieceQueue(pieces)

	hasAll := func(int) bool { return true }

	// Pick piece 0 (it becomes pending)
	pw, _ := pq.Pick(hasAll)

	// Complete it
	pq.Complete(pw.Index)

	// Pick again — should get piece 1
	pw2, ok := pq.Pick(hasAll)
	if !ok {
		t.Fatal("expected piece 1")
	}
	if pw2.Index == pw.Index {
		t.Fatal("should not re-pick completed piece")
	}

	// Return piece 1 (simulate failure)
	pq.Return(pw2)

	// Pick again — should get piece 1 back
	pw3, ok := pq.Pick(hasAll)
	if !ok {
		t.Fatal("expected piece after return")
	}
	if pw3.Index != pw2.Index {
		t.Fatalf("expected returned piece %d, got %d", pw2.Index, pw3.Index)
	}
}

func TestPieceQueueNoPieceAvailable(t *testing.T) {
	pieces := []PieceWork{{Index: 0, Length: 100}}
	pq := NewPieceQueue(pieces)

	// Peer has nothing
	hasNone := func(int) bool { return false }

	_, ok := pq.Pick(hasNone)
	if ok {
		t.Fatal("should not pick any piece")
	}
}

func TestPieceQueueAddRemoveBitfield(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
		{Index: 2, Length: 100},
	}
	pq := NewPieceQueue(pieces)

	bf := make(peer.Bitfield, 1)
	bf.SetPiece(0)
	bf.SetPiece(2)

	pq.AddPeerBitfield(bf)

	pq.mu.Lock()
	if pq.availability[0] != 1 {
		t.Errorf("piece 0 availability: want 1, got %d", pq.availability[0])
	}
	if pq.availability[1] != 0 {
		t.Errorf("piece 1 availability: want 0, got %d", pq.availability[1])
	}
	if pq.availability[2] != 1 {
		t.Errorf("piece 2 availability: want 1, got %d", pq.availability[2])
	}
	pq.mu.Unlock()

	pq.RemovePeerBitfield(bf)

	pq.mu.Lock()
	if pq.availability[0] != 0 {
		t.Errorf("after remove, piece 0 availability: want 0, got %d", pq.availability[0])
	}
	pq.mu.Unlock()
}

func TestPieceQueueEndgame(t *testing.T) {
	// 3 pieces, threshold = max(5, 3/100) = 5, so endgame triggers immediately
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
		{Index: 2, Length: 100},
	}
	pq := NewPieceQueue(pieces)
	hasAll := func(int) bool { return true }

	// Pick first piece — should trigger endgame (3 <= 5)
	pw1, ok := pq.Pick(hasAll)
	if !ok {
		t.Fatal("expected a piece")
	}

	if !pq.IsEndgame() {
		t.Fatal("expected endgame mode")
	}

	// In endgame, should be able to pick the same pending piece again
	pw2, ok := pq.Pick(hasAll)
	if !ok {
		t.Fatal("expected a piece in endgame (duplicate allowed)")
	}
	// pw2 might be the same as pw1 or different — both are valid in endgame

	// Complete pw1 — should broadcast to cancelCh
	pq.Complete(pw1.Index)

	// Check IsDone
	if !pq.IsDone(pw1.Index) {
		t.Fatal("piece should be done")
	}

	_ = pw2 // used
}

func TestPieceQueueRemaining(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
	}
	pq := NewPieceQueue(pieces)

	if pq.Remaining() != 2 {
		t.Fatalf("expected 2, got %d", pq.Remaining())
	}

	pq.Complete(0)
	if pq.Remaining() != 1 {
		t.Fatalf("expected 1, got %d", pq.Remaining())
	}
}
