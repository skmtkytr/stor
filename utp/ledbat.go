package utp

import (
	"math"
	"sync"
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

// LEDBAT tracks congestion state for a uTP connection.
type LEDBAT struct {
	mu         sync.Mutex
	cwnd       float64 // congestion window in bytes
	baseDelay  int64   // minimum observed one-way delay (microseconds)
	flightSize int     // bytes currently in flight
}

// NewLEDBAT creates a new LEDBAT controller.
func NewLEDBAT() *LEDBAT {
	return &LEDBAT{
		cwnd:      float64(mss) * 2,
		baseDelay: math.MaxInt64,
	}
}

// OnAck is called when an ACK is received.
func (l *LEDBAT) OnAck(delay int64, ackedBytes int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if delay < l.baseDelay {
		l.baseDelay = delay
	}

	queuingDelay := delay - l.baseDelay
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
