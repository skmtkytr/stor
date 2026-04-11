package download

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const speedWindowSize = 5 // number of 1-second buckets

// speedWindow tracks bytes downloaded in a ring buffer of 1-second buckets
// to compute a sliding-window speed instead of an all-time average.
type speedWindow struct {
	mu      sync.Mutex
	buckets [speedWindowSize]int64
	times   [speedWindowSize]int64 // unix second for each bucket
	cursor  int
}

func (sw *speedWindow) add(bytes int64) {
	now := time.Now().Unix()
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.times[sw.cursor] == now {
		sw.buckets[sw.cursor] += bytes
	} else {
		// Advance cursor
		sw.cursor = (sw.cursor + 1) % speedWindowSize
		sw.buckets[sw.cursor] = bytes
		sw.times[sw.cursor] = now
	}
}

func (sw *speedWindow) bytesPerSec() int64 {
	now := time.Now().Unix()
	sw.mu.Lock()
	defer sw.mu.Unlock()
	var total int64
	var count int64
	for i := range speedWindowSize {
		age := now - sw.times[i]
		if sw.times[i] > 0 && age < int64(speedWindowSize) {
			total += sw.buckets[i]
			count++
		}
	}
	if count == 0 {
		return 0
	}
	// Use actual window span for more accurate calculation
	return total / count
}

// Progress tracks download progress and speed.
type Progress struct {
	totalPieces int
	totalBytes  int64
	completed   atomic.Int32
	downloaded  atomic.Int64
	activePeers atomic.Int32
	startTime   time.Time
	speed       speedWindow
}

// NewProgress creates a new progress tracker.
func NewProgress(totalPieces int, totalBytes int64) *Progress {
	return &Progress{
		totalPieces: totalPieces,
		totalBytes:  totalBytes,
		startTime:   time.Now(),
	}
}

// SetInitial sets the initial completed pieces count for resume.
// pieceLength is used to estimate downloaded bytes for already-verified pieces.
// These bytes are excluded from speed calculation.
func (p *Progress) SetInitial(completedPieces int, totalBytes int64, pieceLength int64) {
	p.completed.Store(int32(completedPieces))
	estimated := int64(completedPieces) * pieceLength
	if estimated > totalBytes {
		estimated = totalBytes
	}
	p.downloaded.Store(estimated)
}

// Add records a completed piece.
func (p *Progress) Add(bytes int) {
	p.completed.Add(1)
	p.downloaded.Add(int64(bytes))
	p.speed.add(int64(bytes))
}

// PeerConnect increments the active peer count.
func (p *Progress) PeerConnect() {
	p.activePeers.Add(1)
}

// PeerDisconnect decrements the active peer count.
func (p *Progress) PeerDisconnect() {
	p.activePeers.Add(-1)
}

// String returns the current progress line.
func (p *Progress) String() string {
	completed := int(p.completed.Load())
	downloaded := p.downloaded.Load()
	peers := p.activePeers.Load()

	pct := float64(0)
	if p.totalBytes > 0 {
		pct = float64(downloaded) / float64(p.totalBytes) * 100
	}

	speed := p.speed.bytesPerSec()

	return fmt.Sprintf("\r[%5.1f%%] %d/%d pieces | %s / %s | %s/s | %d peers",
		pct,
		completed, p.totalPieces,
		formatBytes(downloaded), formatBytes(p.totalBytes),
		formatBytes(speed),
		peers,
	)
}

// ProgressSnap is a point-in-time snapshot for serialization (RPC, etc.).
type ProgressSnap struct {
	State         string  `json:"state"`
	Downloaded    int64   `json:"downloaded"`
	Total         int64   `json:"total"`
	Percent       float64 `json:"percent"`
	DownSpeed     int64   `json:"down_speed"`
	UpSpeed       int64   `json:"up_speed"`
	ActivePeers   int     `json:"active_peers"`
	TotalPieces   int     `json:"total_pieces"`
	DonePieces    int     `json:"done_pieces"`
	VerifyPercent float64 `json:"verify_percent,omitempty"`
}

// Snap returns a serializable snapshot of the current progress.
func (p *Progress) Snap() ProgressSnap {
	completed := int(p.completed.Load())
	downloaded := p.downloaded.Load()
	peers := int(p.activePeers.Load())

	pct := float64(0)
	if p.totalBytes > 0 {
		pct = float64(downloaded) / float64(p.totalBytes) * 100
	}

	var speed int64
	if completed < p.totalPieces {
		speed = p.speed.bytesPerSec()
	}

	return ProgressSnap{
		Downloaded:  downloaded,
		Total:       p.totalBytes,
		Percent:     pct,
		DownSpeed:   speed,
		ActivePeers: peers,
		TotalPieces: p.totalPieces,
		DonePieces:  completed,
	}
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
