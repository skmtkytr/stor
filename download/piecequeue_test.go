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
	// Rebuild buckets to match
	pq.buckets = map[int]map[int]bool{
		5:  {0: true},
		1:  {1: true},
		10: {2: true},
		3:  {3: true},
	}
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
	pq.buckets = map[int]map[int]bool{
		10: {0: true},
		1:  {1: true},
		3:  {2: true},
	}
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

func TestPieceQueueRespectsSkipMask(t *testing.T) {
	// 4 pieces; skip mask marks pieces 1 and 3 as skipped → they must never
	// be handed out by Pick, and Remaining() counts only the 2 wanted.
	// Use many pieces to stay out of endgame (threshold is max(5, n/100)).
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
		{Index: 2, Length: 100},
		{Index: 3, Length: 100},
	}
	skip := make(peer.Bitfield, 1) // 1 byte covers 4 bits
	skip.SetPiece(1)
	skip.SetPiece(3)

	pq := NewPieceQueueWithSkip(pieces, skip)

	if got := pq.Remaining(); got != 2 {
		t.Fatalf("Remaining: got %d, want 2 (pieces 1,3 skipped)", got)
	}

	// Loop until no more work is available (complete each pick to ensure
	// pending bookkeeping can't re-hand the same index in endgame mode).
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
	if picked[1] || picked[3] {
		t.Fatalf("skipped pieces should never be picked, got %v", picked)
	}
	if !picked[0] || !picked[2] {
		t.Fatalf("wanted pieces 0,2 should be picked, got %v", picked)
	}
}

func TestPieceQueueNilSkipMaskEquivalentToNoMask(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
	}
	pq := NewPieceQueueWithSkip(pieces, nil)
	if pq.Remaining() != 2 {
		t.Fatalf("Remaining: got %d, want 2", pq.Remaining())
	}
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

// TestUpdateSkipMaskFiltersPick verifies that after a session has started
// with no skipped pieces, a runtime UpdateSkipMask prevents the masked
// pieces from being returned by Pick — the lightweight hot-requeue path
// that lets a user's mid-session Skip click take effect without
// pause/resume.
func TestUpdateSkipMaskFiltersPick(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
		{Index: 2, Length: 100},
		{Index: 3, Length: 100},
	}
	pq := NewPieceQueue(pieces)

	// At first all four pieces should be pickable.
	if got := pq.Remaining(); got != 4 {
		t.Fatalf("initial Remaining: got %d, want 4", got)
	}

	// Simulate user clicking Skip on files whose ranges map to pieces 1 and 3.
	skip := make(peer.Bitfield, 1)
	skip.SetPiece(1)
	skip.SetPiece(3)
	pq.UpdateSkipMask(skip)

	if got := pq.Remaining(); got != 2 {
		t.Fatalf("Remaining after UpdateSkipMask: got %d, want 2", got)
	}

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
	if picked[1] || picked[3] {
		t.Fatalf("Pick returned a skipped piece: %v", picked)
	}
	if !picked[0] || !picked[2] {
		t.Fatalf("Pick missed wanted pieces: %v", picked)
	}
}

// TestUpdateSkipMaskUnskipRestoresInitiallyWantedPieces checks that a
// piece that was wanted at construction, then skipped via UpdateSkipMask,
// becomes pickable again when the mask is cleared. This is the "user
// changed their mind" case for files that were in the queue all along.
func TestUpdateSkipMaskUnskipRestoresInitiallyWantedPieces(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
	}
	pq := NewPieceQueue(pieces)

	skip := make(peer.Bitfield, 1)
	skip.SetPiece(1)
	pq.UpdateSkipMask(skip)

	if pq.Remaining() != 1 {
		t.Fatalf("after skip: Remaining got %d, want 1", pq.Remaining())
	}

	// Clear mask.
	pq.UpdateSkipMask(nil)
	if pq.Remaining() != 2 {
		t.Fatalf("after clear: Remaining got %d, want 2", pq.Remaining())
	}

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
	if !picked[0] || !picked[1] {
		t.Fatalf("both pieces should be pickable after mask clear, got %v", picked)
	}
}

// TestUpdateSkipMaskDoesNotResurrectInitiallyFilteredPieces documents the
// known limitation of the lightweight hot-requeue: pieces that were
// filtered out at construction (NewPieceQueueWithSkip) stay out, even
// if UpdateSkipMask later clears the bit. Recovering them requires
// pause/resume to rebuild the queue.
func TestUpdateSkipMaskDoesNotResurrectInitiallyFilteredPieces(t *testing.T) {
	pieces := []PieceWork{
		{Index: 0, Length: 100},
		{Index: 1, Length: 100},
	}
	initial := make(peer.Bitfield, 1)
	initial.SetPiece(1)
	pq := NewPieceQueueWithSkip(pieces, initial)

	// Piece 1 was filtered at construction.
	if pq.Remaining() != 1 {
		t.Fatalf("initial Remaining: got %d, want 1", pq.Remaining())
	}

	// Try to unskip at runtime.
	pq.UpdateSkipMask(nil)
	if pq.Remaining() != 1 {
		t.Fatalf("Remaining should stay 1 (piece 1 was filtered at ctor): got %d", pq.Remaining())
	}

	hasAll := func(int) bool { return true }
	pw, ok := pq.Pick(hasAll)
	if !ok {
		t.Fatal("Pick should return piece 0")
	}
	if pw.Index == 1 {
		t.Error("piece 1 was filtered at construction; should not be resurrected")
	}
}
