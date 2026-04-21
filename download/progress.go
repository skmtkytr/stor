package download

import (
	"fmt"
	"sync/atomic"
	"time"
)

// Progress tracks download progress and speed. Speed fields are written
// by the shared rateRegistry ticker (see rate.go) — there is no per-
// Progress goroutine.
type Progress struct {
	totalPieces int
	totalBytes  int64
	completed   atomic.Int32
	downloaded  atomic.Int64
	activePeers atomic.Int32
	startTime   time.Time

	// Speed tracking: add sites bump *Counter fields; the rate sampler
	// drains them every second and writes the EMA result into *Rate.
	downCounter atomic.Int64
	downRate    atomic.Int64
	upCounter   atomic.Int64
	upRate      atomic.Int64
	unregRate   func() // deregister from rateRegistry on Close

	uploaded atomic.Int64
}

// NewProgress creates a new progress tracker.
func NewProgress(totalPieces int, totalBytes int64) *Progress {
	p := &Progress{
		totalPieces: totalPieces,
		totalBytes:  totalBytes,
		startTime:   time.Now(),
	}
	reg := globalRateRegistry()
	unregDown := reg.register(&rateTarget{counter: &p.downCounter, rate: &p.downRate})
	unregUp := reg.register(&rateTarget{counter: &p.upCounter, rate: &p.upRate})
	p.unregRate = func() {
		unregDown()
		unregUp()
	}
	return p
}

// Close deregisters this Progress from the shared rate sampler. Safe to
// call multiple times.
func (p *Progress) Close() {
	if p.unregRate != nil {
		p.unregRate()
		p.unregRate = nil
	}
}

// AddUploadBytes records uploaded bytes for speed + total tracking.
func (p *Progress) AddUploadBytes(n int64) {
	p.upCounter.Add(n)
	p.uploaded.Add(n)
}

// UploadRate returns the current upload speed from outgoing peer connections.
func (p *Progress) UploadRate() int64 {
	return p.upRate.Load()
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

// Add records a completed piece (piece count + total bytes).
func (p *Progress) Add(bytes int) {
	p.completed.Add(1)
	p.downloaded.Add(int64(bytes))
}

// AddBytes records downloaded bytes for speed tracking (called per block).
func (p *Progress) AddBytes(n int64) {
	p.downCounter.Add(n)
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

	speed := p.downRate.Load()

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
	Uploaded      int64   `json:"uploaded"`
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
		speed = p.downRate.Load()
	}

	upSpeed := p.upRate.Load()

	return ProgressSnap{
		Downloaded:  downloaded,
		Uploaded:    p.uploaded.Load(),
		Total:       p.totalBytes,
		Percent:     pct,
		DownSpeed:   speed,
		UpSpeed:     upSpeed,
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
