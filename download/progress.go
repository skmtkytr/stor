package download

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// emaSpeed implements libtorrent-style speed measurement using an
// Exponential Moving Average (EMA). Bytes are accumulated in a counter
// and sampled once per second: avg = avg * 0.8 + sample * 0.2.
// This produces smooth, responsive speed readings.
type emaSpeed struct {
	mu      sync.Mutex
	counter int64   // bytes accumulated since last tick
	average float64 // EMA of bytes/sec
	stop    chan struct{}
}

func newEMASpeed() *emaSpeed {
	e := &emaSpeed{stop: make(chan struct{})}
	go e.run()
	return e
}

func (e *emaSpeed) run() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case <-e.stop:
			return
		case now := <-ticker.C:
			elapsed := now.Sub(last).Milliseconds()
			if elapsed <= 0 {
				elapsed = 1000
			}
			last = now

			e.mu.Lock()
			sample := float64(e.counter) * 1000.0 / float64(elapsed)
			e.average = e.average*0.8 + sample*0.2
			e.counter = 0
			e.mu.Unlock()
		}
	}
}

func (e *emaSpeed) add(bytes int64) {
	e.mu.Lock()
	e.counter += bytes
	e.mu.Unlock()
}

func (e *emaSpeed) rate() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return int64(e.average)
}

func (e *emaSpeed) close() {
	select {
	case <-e.stop:
	default:
		close(e.stop)
	}
}

// Progress tracks download progress and speed.
type Progress struct {
	totalPieces int
	totalBytes  int64
	completed   atomic.Int32
	downloaded  atomic.Int64
	activePeers atomic.Int32
	startTime   time.Time
	speed       *emaSpeed
	upSpeed     *emaSpeed // upload speed from outgoing peer connections
}

// NewProgress creates a new progress tracker.
func NewProgress(totalPieces int, totalBytes int64) *Progress {
	return &Progress{
		totalPieces: totalPieces,
		totalBytes:  totalBytes,
		startTime:   time.Now(),
		speed:       newEMASpeed(),
		upSpeed:     newEMASpeed(),
	}
}

// Close stops the speed ticker goroutines.
func (p *Progress) Close() {
	if p.speed != nil {
		p.speed.close()
	}
	if p.upSpeed != nil {
		p.upSpeed.close()
	}
}

// AddUploadBytes records uploaded bytes for speed tracking.
func (p *Progress) AddUploadBytes(n int64) {
	p.upSpeed.add(n)
}

// UploadRate returns the current upload speed from outgoing peer connections.
func (p *Progress) UploadRate() int64 {
	return p.upSpeed.rate()
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
	p.speed.add(n)
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

	speed := p.speed.rate()

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
		speed = p.speed.rate()
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
