package download

import (
	"fmt"
	"sync/atomic"
	"time"
)

// Progress tracks download progress and speed.
type Progress struct {
	totalPieces int
	totalBytes  int64
	completed   atomic.Int32
	downloaded  atomic.Int64
	activePeers atomic.Int32
	startTime   time.Time
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
func (p *Progress) SetInitial(completedPieces int, totalBytes int64, pieceLength int64) {
	p.completed.Store(int32(completedPieces))
	// Estimate bytes from completed pieces (last piece may be shorter)
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
	elapsed := time.Since(p.startTime).Seconds()

	pct := float64(0)
	if p.totalBytes > 0 {
		pct = float64(downloaded) / float64(p.totalBytes) * 100
	}

	speed := float64(0)
	if elapsed > 0 {
		speed = float64(downloaded) / elapsed
	}

	return fmt.Sprintf("\r[%5.1f%%] %d/%d pieces | %s / %s | %s/s | %d peers",
		pct,
		completed, p.totalPieces,
		formatBytes(downloaded), formatBytes(p.totalBytes),
		formatBytes(int64(speed)),
		peers,
	)
}

// ProgressSnap is a point-in-time snapshot for serialization (RPC, etc.).
type ProgressSnap struct {
	State       string  `json:"state"`
	Downloaded  int64   `json:"downloaded"`
	Total       int64   `json:"total"`
	Percent     float64 `json:"percent"`
	DownSpeed   int64   `json:"down_speed"`
	ActivePeers int     `json:"active_peers"`
	TotalPieces int     `json:"total_pieces"`
	DonePieces  int     `json:"done_pieces"`
}

// Snap returns a serializable snapshot of the current progress.
func (p *Progress) Snap() ProgressSnap {
	completed := int(p.completed.Load())
	downloaded := p.downloaded.Load()
	peers := int(p.activePeers.Load())
	elapsed := time.Since(p.startTime).Seconds()

	pct := float64(0)
	if p.totalBytes > 0 {
		pct = float64(downloaded) / float64(p.totalBytes) * 100
	}

	var speed int64
	if elapsed > 0 {
		speed = int64(float64(downloaded) / elapsed)
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
