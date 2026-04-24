package utp

import (
	"math"
	"sync"
	"time"
)

// LEDBAT implements the Low Extra Delay Background Transport congestion control.
// Target delay is 100ms. The algorithm increases cwnd when delay is below target
// and decreases when above, ensuring uTP yields to TCP traffic.
const (
	targetDelay = 100_000 // 100ms in microseconds
	maxCwnd     = 1048576 // 1MB max window
	minCwnd     = 150     // minimum ~1 packet
	mss         = 1400    // max segment size
)

// baseDelay history: 8 buckets × 30 s = 4 minutes of rolling minimum.
const (
	baseDelayBuckets = 8
	bucketInterval   = 30 * time.Second
)

type delayBucket struct {
	minDelay int64
}

// LEDBAT tracks congestion state for a uTP connection.
type LEDBAT struct {
	mu         sync.Mutex
	cwnd       float64
	flightSize int

	// RFC 6817 sliding-window base delay.
	buckets    [baseDelayBuckets]delayBucket
	currentBkt int
	lastRotate time.Time
}

// NewLEDBAT creates a new LEDBAT controller.
func NewLEDBAT() *LEDBAT {
	l := &LEDBAT{
		cwnd:       float64(mss) * 2,
		lastRotate: time.Now(),
	}
	for i := range l.buckets {
		l.buckets[i].minDelay = math.MaxInt64
	}
	return l
}

// rotateBuckets advances the current bucket when its interval has elapsed.
// Must be called with l.mu held.
func (l *LEDBAT) rotateBuckets() {
	if time.Since(l.lastRotate) < bucketInterval {
		return
	}
	l.currentBkt = (l.currentBkt + 1) % baseDelayBuckets
	l.buckets[l.currentBkt].minDelay = math.MaxInt64
	l.lastRotate = time.Now()
}

// getBaseDelay returns the minimum observed delay across all buckets.
// Must be called with l.mu held.
func (l *LEDBAT) getBaseDelay() int64 {
	min := int64(math.MaxInt64)
	for i := range l.buckets {
		if l.buckets[i].minDelay < min {
			min = l.buckets[i].minDelay
		}
	}
	return min
}

// OnAck is called when an ACK is received.
func (l *LEDBAT) OnAck(delay int64, ackedBytes int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.rotateBuckets()

	if delay < l.buckets[l.currentBkt].minDelay {
		l.buckets[l.currentBkt].minDelay = delay
	}

	base := l.getBaseDelay()
	var queuingDelay int64
	if base == math.MaxInt64 {
		// No base delay established yet; treat as zero queuing delay.
		queuingDelay = 0
	} else {
		queuingDelay = delay - base
		if queuingDelay < 0 {
			queuingDelay = 0
		}
	}

	offTarget := float64(targetDelay-queuingDelay) / float64(targetDelay)
	gain := float64(ackedBytes) * offTarget / l.cwnd
	l.cwnd += gain * float64(mss)

	if l.cwnd < minCwnd {
		l.cwnd = minCwnd
	}
	if l.cwnd > maxCwnd {
		l.cwnd = maxCwnd
	}

	l.flightSize -= ackedBytes
	if l.flightSize < 0 {
		l.flightSize = 0
	}
}

// OnLoss is called when packet loss is detected.
func (l *LEDBAT) OnLoss() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cwnd /= 2
	if l.cwnd < minCwnd {
		l.cwnd = minCwnd
	}
}

// OnTimeout is called when a retransmission timeout fires.
func (l *LEDBAT) OnTimeout() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cwnd = float64(mss)
	l.flightSize = 0
}

// OnSend is called when data is sent.
func (l *LEDBAT) OnSend(bytes int) {
	l.mu.Lock()
	l.flightSize += bytes
	l.mu.Unlock()
}

// CanSend returns how many bytes can be sent right now.
func (l *LEDBAT) CanSend() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	avail := int(l.cwnd) - l.flightSize
	if avail < 0 {
		return 0
	}
	return avail
}

// Cwnd returns the current congestion window size.
func (l *LEDBAT) Cwnd() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int(l.cwnd)
}
