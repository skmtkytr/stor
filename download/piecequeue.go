package download

import (
	"math/rand/v2"
	"slices"
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

	// Availability buckets: avail level → set of piece indices.
	// Enables O(k) Pick where k = number of distinct availability levels.
	buckets map[int]map[int]bool // availability → {piece indices}

	// skipMask supports the lightweight hot-requeue path: Session can
	// call UpdateSkipMask mid-download to tell Pick to stop handing out
	// pieces whose bit is set. Unlike the constructor-time filter in
	// NewPieceQueueWithSkip — which removes pieces from the map entirely
	// — this is a dynamic filter. Pieces set in skipMask are still in
	// pq.pieces and become pickable again when the bit is cleared.
	skipMask peer.Bitfield

	// priority is the set of piece indices that should be handed out
	// before any availability bucket is consulted. Used for the
	// `prioritize_first_last_pieces` option (deluge parity): when
	// enabled at construction, the first and last piece are placed
	// here so streaming clients can decode container metadata from
	// both ends as soon as possible. Pieces stay in the regular
	// availability buckets too — Complete() removes them from both
	// places, so once a priority piece is done it never comes back.
	priority map[int]bool
}

// NewPieceQueue creates a piece queue from a list of pieces to download.
func NewPieceQueue(pieces []PieceWork) *PieceQueue {
	return NewPieceQueueWithSkip(pieces, nil)
}

// NewPieceQueueWithSkip creates a piece queue from a list of pieces, omitting
// any piece whose bit is set in skipMask. A nil skipMask is equivalent to
// calling NewPieceQueue directly. Skipped pieces are never handed out by
// Pick and do not count toward Remaining().
func NewPieceQueueWithSkip(pieces []PieceWork, skipMask peer.Bitfield) *PieceQueue {
	return NewPieceQueueWithOptions(pieces, skipMask, 0, false)
}

// NewPieceQueueWithOptions is the full constructor. In addition to the
// skipMask handling of NewPieceQueueWithSkip, it accepts:
//
//   - totalPieces: the original piece count of the torrent (NOT the post-skip
//     length). Used to identify the "last" piece when
//     prioritizeFirstLast is true. A non-positive value disables the
//     priority logic regardless of the flag.
//   - prioritizeFirstLast: when true, piece index 0 and piece index
//     totalPieces-1 (if not already filtered by skipMask) are placed in
//     a separate priority bucket that Pick consults before any
//     availability bucket. This implements the deluge
//     `prioritize_first_last_pieces` option, useful for streaming media
//     where the leading/trailing pieces hold container metadata.
//
// numPieces==1 is a degenerate but valid case: the only piece is added
// to the priority bucket exactly once (no double-registration).
func NewPieceQueueWithOptions(pieces []PieceWork, skipMask peer.Bitfield, totalPieces int, prioritizeFirstLast bool) *PieceQueue {
	filtered := pieces
	if skipMask != nil {
		filtered = make([]PieceWork, 0, len(pieces))
		for _, pw := range pieces {
			if skipMask.HasPiece(pw.Index) {
				continue
			}
			filtered = append(filtered, pw)
		}
	}

	pq := &PieceQueue{
		pieces:       make(map[int]PieceWork, len(filtered)),
		availability: make(map[int]int),
		pending:      make(map[int]bool),
		done:         make(map[int]bool),
		totalPieces:  len(filtered),
		waitCh:       make(chan struct{}, 1),
		cancelCh:     make(chan int, 64),
		buckets:      make(map[int]map[int]bool),
	}
	// All pieces start with availability 0
	bucket0 := make(map[int]bool, len(filtered))
	for _, pw := range filtered {
		pq.pieces[pw.Index] = pw
		bucket0[pw.Index] = true
	}
	if len(bucket0) > 0 {
		pq.buckets[0] = bucket0
	}

	if prioritizeFirstLast && totalPieces > 0 {
		pq.priority = make(map[int]bool, 2)
		// Only add an index to the priority set if it actually exists in
		// the queue — skipMask may have removed it.
		if _, ok := pq.pieces[0]; ok {
			pq.priority[0] = true
		}
		last := totalPieces - 1
		if last != 0 { // numPieces==1 → first and last are the same
			if _, ok := pq.pieces[last]; ok {
				pq.priority[last] = true
			}
		}
	}

	return pq
}

// moveBucket moves a piece from one availability bucket to another.
// Caller must hold pq.mu.
func (pq *PieceQueue) moveBucket(idx, oldAvail, newAvail int) {
	if b, ok := pq.buckets[oldAvail]; ok {
		delete(b, idx)
		if len(b) == 0 {
			delete(pq.buckets, oldAvail)
		}
	}
	b, ok := pq.buckets[newAvail]
	if !ok {
		b = make(map[int]bool)
		pq.buckets[newAvail] = b
	}
	b[idx] = true
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

	// Priority bucket first (when configured): if either end-piece is
	// pickable for this peer, hand it out before consulting the
	// availability buckets. Reservoir-sample so we don't bias toward
	// piece 0 over piece numPieces-1 when both are eligible.
	if len(pq.priority) > 0 {
		var chosen int
		count := 0
		for idx := range pq.priority {
			if pq.done[idx] {
				continue
			}
			if !pq.endgame && pq.pending[idx] {
				continue
			}
			if pq.skipMask != nil && pq.skipMask.HasPiece(idx) {
				continue
			}
			if !hasPiece(idx) {
				continue
			}
			count++
			if rand.IntN(count) == 0 {
				chosen = idx
			}
		}
		if count > 0 {
			pq.pending[chosen] = true
			return pq.pieces[chosen], true
		}
	}

	// Collect non-empty bucket levels and sort ascending.
	avails := make([]int, 0, len(pq.buckets))
	for a, b := range pq.buckets {
		if len(b) > 0 {
			avails = append(avails, a)
		}
	}
	slices.Sort(avails)

	for _, avail := range avails {
		bucket := pq.buckets[avail]
		// Count eligible pieces and pick one randomly using reservoir sampling
		var chosen int
		count := 0
		for idx := range bucket {
			if pq.done[idx] {
				continue
			}
			if !pq.endgame && pq.pending[idx] {
				continue
			}
			if pq.skipMask != nil && pq.skipMask.HasPiece(idx) {
				continue
			}
			if !hasPiece(idx) {
				continue
			}
			count++
			if rand.IntN(count) == 0 {
				chosen = idx
			}
		}
		if count > 0 {
			pq.pending[chosen] = true
			return pq.pieces[chosen], true
		}
	}

	return PieceWork{}, false
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
	// Remove from bucket
	avail := pq.availability[index]
	if b, ok := pq.buckets[avail]; ok {
		delete(b, index)
		if len(b) == 0 {
			delete(pq.buckets, avail)
		}
	}
	// Drop the index from the priority set too — Pick already filters
	// done pieces, but trimming here keeps the iteration cheap once
	// the prioritized ends are out of the way.
	if pq.priority != nil {
		delete(pq.priority, index)
	}
	delete(pq.pieces, index)
	pq.done[index] = true
	eg := pq.endgame
	pq.mu.Unlock()

	if eg {
		select {
		case pq.cancelCh <- index:
		default:
		}
	}
	// Wake blocked workers so they can exit if no work remains
	pq.signal()
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
// A nil bitfield means "have all" (BEP 6 have-all).
func (pq *PieceQueue) AddPeerBitfield(bf peer.Bitfield) {
	pq.mu.Lock()
	for idx := range pq.pieces {
		if bf == nil || bf.HasPiece(idx) {
			old := pq.availability[idx]
			pq.availability[idx]++
			pq.moveBucket(idx, old, old+1)
		}
	}
	pq.mu.Unlock()
	pq.signal()
}

// RemovePeerBitfield decrements availability for pieces a peer had.
// A nil bitfield means "have all" (BEP 6 have-all).
func (pq *PieceQueue) RemovePeerBitfield(bf peer.Bitfield) {
	pq.mu.Lock()
	for idx := range pq.pieces {
		if bf == nil || bf.HasPiece(idx) {
			old := pq.availability[idx]
			pq.availability[idx]--
			pq.moveBucket(idx, old, old-1)
		}
	}
	pq.mu.Unlock()
}

// PeerHave increments availability for a single piece (MsgHave received).
func (pq *PieceQueue) PeerHave(index int) {
	pq.mu.Lock()
	old := pq.availability[index]
	pq.availability[index]++
	if _, ok := pq.pieces[index]; ok {
		pq.moveBucket(index, old, old+1)
	}
	pq.mu.Unlock()
	pq.signal()
}

// maxPieceLen returns the maximum piece length in the queue.
func (pq *PieceQueue) maxPieceLen() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	max := 0
	for _, pw := range pq.pieces {
		if pw.Length > max {
			max = pw.Length
		}
	}
	return max
}

// Remaining returns the number of pieces not yet completed, excluding any
// currently marked as skipped by UpdateSkipMask.
func (pq *PieceQueue) Remaining() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.skipMask == nil {
		return len(pq.pieces)
	}
	n := 0
	for idx := range pq.pieces {
		if pq.skipMask.HasPiece(idx) {
			continue
		}
		n++
	}
	return n
}

// UpdateSkipMask replaces the runtime skip filter. Pieces whose bit is set
// are excluded from Pick and Remaining until the bit is cleared. Passing
// nil clears the filter. A defensive copy is taken so the caller may
// mutate its own slice afterwards.
//
// Note: pieces that were removed from the queue by the constructor-time
// filter in NewPieceQueueWithSkip are not restored by this call — that
// path requires rebuilding the queue (pause/resume).
func (pq *PieceQueue) UpdateSkipMask(mask peer.Bitfield) {
	pq.mu.Lock()
	if mask == nil {
		pq.skipMask = nil
	} else {
		cp := make(peer.Bitfield, len(mask))
		copy(cp, mask)
		pq.skipMask = cp
	}
	pq.mu.Unlock()
	// Wake workers that may have been blocked waiting for work — unskipping
	// can create new pickable pieces.
	pq.signal()
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
