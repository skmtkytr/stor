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
	waitCh       chan struct{}     // signaled when new work may be available
}

// NewPieceQueue creates a piece queue from a list of pieces to download.
func NewPieceQueue(pieces []PieceWork) *PieceQueue {
	pq := &PieceQueue{
		pieces:       make(map[int]PieceWork, len(pieces)),
		availability: make(map[int]int),
		pending:      make(map[int]bool),
		done:         make(map[int]bool),
		waitCh:       make(chan struct{}, 1),
	}
	for _, pw := range pieces {
		pq.pieces[pw.Index] = pw
	}
	return pq
}

// Pick selects the rarest available piece that the peer has.
// Returns false if no suitable piece is available.
func (pq *PieceQueue) Pick(hasPiece func(int) bool) (PieceWork, bool) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	bestIdx := -1
	bestAvail := int(^uint(0) >> 1) // max int

	for idx, pw := range pq.pieces {
		if pq.pending[idx] || pq.done[idx] {
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

// Complete marks a piece as done.
func (pq *PieceQueue) Complete(index int) {
	pq.mu.Lock()
	delete(pq.pending, index)
	delete(pq.pieces, index)
	pq.done[index] = true
	pq.mu.Unlock()
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
