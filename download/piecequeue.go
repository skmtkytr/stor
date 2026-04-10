package download

import (
	"math/rand/v2"
	"sync"

	"github.com/skmtkytr/stor/peer"
)

// PieceQueue manages piece selection with rarest-first strategy.
// Thread-safe: all methods can be called from multiple goroutines.
type PieceQueue struct {
	mu           sync.Mutex
	pieces       map[int]PieceWork // remaining pieces by index
	availability map[int]int       // piece index → peer count that has it
	pending      map[int]bool      // currently being downloaded
	done         map[int]bool      // completed pieces
	endgame      bool              // endgame mode: allow duplicate picks
	totalPieces  int               // original total for threshold calculation
	waitCh       chan struct{}     // signaled when new work may be available
	cancelCh     chan int          // piece index broadcast when completed in endgame
}

// NewPieceQueue creates a piece queue from a list of pieces to download.
func NewPieceQueue(pieces []PieceWork) *PieceQueue {
	pq := &PieceQueue{
		pieces:       make(map[int]PieceWork, len(pieces)),
		availability: make(map[int]int),
		pending:      make(map[int]bool),
		done:         make(map[int]bool),
		totalPieces:  len(pieces),
		waitCh:       make(chan struct{}, 1),
		cancelCh:     make(chan int, 64),
	}
	for _, pw := range pieces {
		pq.pieces[pw.Index] = pw
	}
	return pq
}

// Pick selects the rarest available piece that the peer has.
// In endgame mode, allows multiple peers to pick the same piece.
// Returns false if no suitable piece is available.
func (pq *PieceQueue) Pick(hasPiece func(int) bool) (PieceWork, bool) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	// Check if we should enter endgame
	if !pq.endgame {
		remaining := len(pq.pieces)
		threshold := pq.totalPieces / 100
		if threshold < 5 {
			threshold = 5
		}
		if remaining > 0 && remaining <= threshold {
			pq.endgame = true
		}
	}

	bestIdx := -1
	bestAvail := int(^uint(0) >> 1) // max int

	for idx, pw := range pq.pieces {
		if pq.done[idx] {
			continue
		}
		// In normal mode, skip pending pieces. In endgame, allow duplicates.
		if !pq.endgame && pq.pending[idx] {
			continue
		}
		if !hasPiece(pw.Index) {
			continue
		}
		avail := pq.availability[idx]
		if avail < bestAvail || (avail == bestAvail && rand.IntN(2) == 0) {
			bestAvail = avail
			bestIdx = idx
		}
	}

	if bestIdx < 0 {
		return PieceWork{}, false
	}

	pq.pending[bestIdx] = true
	return pq.pieces[bestIdx], true
}

// Return puts a piece back as available (e.g., download failed).
func (pq *PieceQueue) Return(pw PieceWork) {
	pq.mu.Lock()
	delete(pq.pending, pw.Index)
	pq.mu.Unlock()
	pq.signal()
}

// Complete marks a piece as done. In endgame mode, broadcasts to cancelCh
// so other workers downloading the same piece can abort.
func (pq *PieceQueue) Complete(index int) {
	pq.mu.Lock()
	delete(pq.pending, index)
	delete(pq.pieces, index)
	pq.done[index] = true
	eg := pq.endgame
	pq.mu.Unlock()

	if eg {
		// Non-blocking broadcast — workers check this between blocks
		select {
		case pq.cancelCh <- index:
		default:
		}
	}
}

// CancelCh returns a channel that receives piece indices completed by other workers.
// Workers should check this in endgame mode to abort duplicate downloads.
func (pq *PieceQueue) CancelCh() <-chan int {
	return pq.cancelCh
}

// IsEndgame returns whether endgame mode is active.
func (pq *PieceQueue) IsEndgame() bool {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.endgame
}

// IsDone returns whether a piece has been completed.
func (pq *PieceQueue) IsDone(index int) bool {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.done[index]
}

// AddPeerBitfield updates availability for all pieces a peer has.
func (pq *PieceQueue) AddPeerBitfield(bf peer.Bitfield) {
	pq.mu.Lock()
	for idx := range pq.pieces {
		if bf.HasPiece(idx) {
			pq.availability[idx]++
		}
	}
	pq.mu.Unlock()
	pq.signal()
}

// RemovePeerBitfield decrements availability for pieces a peer had.
func (pq *PieceQueue) RemovePeerBitfield(bf peer.Bitfield) {
	pq.mu.Lock()
	for idx := range pq.pieces {
		if bf.HasPiece(idx) {
			pq.availability[idx]--
		}
	}
	pq.mu.Unlock()
}

// PeerHave increments availability for a single piece (MsgHave received).
func (pq *PieceQueue) PeerHave(index int) {
	pq.mu.Lock()
	pq.availability[index]++
	pq.mu.Unlock()
	pq.signal()
}

// Remaining returns the number of pieces not yet completed.
func (pq *PieceQueue) Remaining() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return len(pq.pieces)
}

// Wait returns a channel that is signaled when new work may be available.
func (pq *PieceQueue) Wait() <-chan struct{} {
	return pq.waitCh
}

func (pq *PieceQueue) signal() {
	select {
	case pq.waitCh <- struct{}{}:
	default:
	}
}
