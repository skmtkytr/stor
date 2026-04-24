package utp

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
)

var errClosed = errors.New("utp: connection closed")

const (
	maxRetransmits = 4
	reorderBufMax  = 64 // max out-of-order packets held for reassembly
)

// sentPkt tracks an unacknowledged sent packet for retransmission.
type sentPkt struct {
	raw     []byte
	seqNr   uint16
	sentAt  time.Time
	retries int
}

// Conn implements net.Conn over uTP.
type Conn struct {
	mu            sync.Mutex
	udpConn       *net.UDPConn
	remote        *net.UDPAddr
	connID        uint16
	seqNr         uint16
	ackNr         uint16
	ledbat        *LEDBAT
	recvBuf       []byte
	recvCh        chan []byte
	closed        bool
	closeCh       chan struct{}
	deadline      time.Time
	writeDeadline time.Time
	ownsUDP       bool // true for outgoing (client) connections that own the UDP socket

	// writeMu serializes WriteToUDP calls. Server-side conns share the Listener's
	// mutex; client-side conns get their own.
	writeMu *sync.Mutex

	// finAcked is signaled when the remote ACKs our FIN.
	finAcked chan struct{}
	finSeqNr uint16 // seqNr of our FIN packet

	// Retransmission buffer: seqNr → pending packet.
	sendBufMu sync.Mutex
	sendBuf   map[uint16]*sentPkt
	rto       time.Duration

	// In-order reassembly: packets that arrived out-of-order wait here until
	// all preceding seqNrs have been delivered.
	reorderBuf   map[uint16][]byte // seqNr → payload copy
	nextExpected uint16            // next seqNr to deliver; also the cumulative ACK point
	recvInited   bool              // true once nextExpected has been set
}

// newConn creates a uTP connection. The caller must call startRetransmitLoop
// after setting c.writeMu to the correct mutex.
func newConn(udp *net.UDPConn, remote *net.UDPAddr, connID, seqNr, ackNr uint16) *Conn {
	return &Conn{
		udpConn:    udp,
		remote:     remote,
		connID:     connID,
		seqNr:      seqNr,
		ackNr:      ackNr,
		ledbat:     NewLEDBAT(),
		recvCh:     make(chan []byte, 1024),
		closeCh:    make(chan struct{}),
		writeMu:    &sync.Mutex{}, // replaced by listener for server-side conns
		finAcked:   make(chan struct{}, 1),
		sendBuf:    make(map[uint16]*sentPkt),
		rto:        time.Second,
		reorderBuf: make(map[uint16][]byte),
	}
}

// startRetransmitLoop launches the retransmission goroutine. Must be called
// after writeMu is finalized (i.e., after the listener has set c.writeMu).
func (c *Conn) startRetransmitLoop() {
	go c.retransmitLoop()
}

// Read reads data from the uTP connection.
func (c *Conn) Read(b []byte) (int, error) {
	// Drain buffered data first
	if len(c.recvBuf) > 0 {
		n := copy(b, c.recvBuf)
		c.recvBuf = c.recvBuf[n:]
		return n, nil
	}

	// Wait for new data
	var timer <-chan time.Time
	c.mu.Lock()
	if !c.deadline.IsZero() {
		timer = time.After(time.Until(c.deadline))
	}
	c.mu.Unlock()

	select {
	case data, ok := <-c.recvCh:
		if !ok {
			return 0, errClosed
		}
		n := copy(b, data)
		if n < len(data) {
			c.recvBuf = data[n:]
		}
		return n, nil
	case <-c.closeCh:
		return 0, errClosed
	case <-timer:
		return 0, &net.OpError{Op: "read", Net: "utp", Err: errors.New("timeout")}
	}
}

// Write writes data to the uTP connection.
func (c *Conn) Write(b []byte) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, errClosed
	}
	c.mu.Unlock()

	total := 0
	for len(b) > 0 {
		// Wait for congestion window to allow sending
		canSend := c.ledbat.CanSend()
		for canSend <= 0 {
			c.mu.Lock()
			wd := c.writeDeadline
			closed := c.closed
			c.mu.Unlock()
			if closed {
				return total, errClosed
			}
			if !wd.IsZero() && time.Now().After(wd) {
				return total, &net.OpError{Op: "write", Net: "utp", Err: errors.New("timeout")}
			}
			time.Sleep(time.Millisecond)
			canSend = c.ledbat.CanSend()
		}

		chunkSize := mss
		if chunkSize > len(b) {
			chunkSize = len(b)
		}
		if chunkSize > canSend {
			chunkSize = canSend
		}

		c.mu.Lock()
		c.seqNr++
		seq := c.seqNr
		avail := cap(c.recvCh) - len(c.recvCh)
		if avail < 0 {
			avail = 0
		}
		pkt := &Packet{
			Header: Header{
				Type:      StData,
				Version:   1,
				ConnID:    c.connID,
				Timestamp: utpTimestamp(),
				WndSize:   uint32(avail) * mss,
				SeqNr:     seq,
				AckNr:     c.ackNr,
			},
			Payload: b[:chunkSize],
		}
		c.mu.Unlock()

		raw := pkt.Marshal()

		// Buffer before sending so retransmitLoop can re-send on loss.
		c.sendBufMu.Lock()
		c.sendBuf[seq] = &sentPkt{raw: raw, seqNr: seq, sentAt: time.Now()}
		c.sendBufMu.Unlock()

		c.writeMu.Lock()
		_, err := c.udpConn.WriteToUDP(raw, c.remote)
		c.writeMu.Unlock()
		if err != nil {
			c.sendBufMu.Lock()
			delete(c.sendBuf, seq)
			c.sendBufMu.Unlock()
			return total, err
		}

		c.ledbat.OnSend(chunkSize)
		total += chunkSize
		b = b[chunkSize:]
	}

	return total, nil
}

// Close closes the connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)

	c.seqNr++
	c.finSeqNr = c.seqNr
	pkt := &Packet{
		Header: Header{
			Type:      StFin,
			Version:   1,
			ConnID:    c.connID,
			Timestamp: utpTimestamp(),
			SeqNr:     c.seqNr,
			AckNr:     c.ackNr,
		},
	}
	raw := pkt.Marshal()
	c.mu.Unlock()

	c.writeMu.Lock()
	_, _ = c.udpConn.WriteToUDP(raw, c.remote)
	c.writeMu.Unlock()

	// Wait for FIN-ACK (best-effort, 500ms timeout).
	select {
	case <-c.finAcked:
	case <-time.After(500 * time.Millisecond):
	}

	if c.ownsUDP {
		_ = c.udpConn.Close()
	}
	return nil
}

// LocalAddr returns the local address.
func (c *Conn) LocalAddr() net.Addr {
	return c.udpConn.LocalAddr()
}

// RemoteAddr returns the remote address.
func (c *Conn) RemoteAddr() net.Addr {
	return c.remote
}

// SetDeadline sets both read and write deadlines.
func (c *Conn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

// SetReadDeadline sets the read deadline.
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

// SetWriteDeadline sets the write deadline.
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

// setFirstExpected initialises the in-order reassembly state. Must be called
// once, after the remote's initial seqNr is known (i.e. after SYN or SYN-ACK).
func (c *Conn) setFirstExpected(seqNr uint16) {
	c.mu.Lock()
	c.nextExpected = seqNr
	c.recvInited = true
	c.mu.Unlock()
}

// deliverInOrder enforces in-order delivery of incoming data packets.
// Out-of-order packets are buffered (up to reorderBufMax); duplicates are
// discarded. ackNr is advanced to the highest consecutive seqNr delivered.
func (c *Conn) deliverInOrder(seqNr uint16, payload []byte) {
	var buf []byte
	if len(payload) > 0 {
		buf = make([]byte, len(payload))
		copy(buf, payload)
	}

	c.mu.Lock()
	if !c.recvInited {
		// Fallback: initialise from the first packet seen.
		c.nextExpected = seqNr
		c.recvInited = true
	}

	var toDeliver [][]byte
	switch {
	case seqNr == c.nextExpected:
		c.nextExpected++
		if buf != nil {
			toDeliver = append(toDeliver, buf)
		}
		// Flush any consecutive buffered packets.
		for {
			p, ok := c.reorderBuf[c.nextExpected]
			if !ok {
				break
			}
			delete(c.reorderBuf, c.nextExpected)
			c.nextExpected++
			if len(p) > 0 {
				toDeliver = append(toDeliver, p)
			}
		}
		c.ackNr = c.nextExpected - 1

	case wrappingGT(seqNr, c.nextExpected):
		// Out-of-order: buffer up to reorderBufMax; drop beyond that.
		if len(c.reorderBuf) < reorderBufMax {
			c.reorderBuf[seqNr] = buf
		}
		// ackNr stays at the last consecutive seqNr (no cumulative advance).
		// else: duplicate or old seqNr — discard silently.
	}
	c.mu.Unlock()

	for _, data := range toDeliver {
		select {
		case c.recvCh <- data:
		case <-c.closeCh:
			return
		}
	}
}

// handleAck processes an incoming STATE (ACK) packet.
func (c *Conn) handleAck(h *Header) {
	// TSDiff is receiver_time − sender_timestamp, giving the one-way delay.
	// Only update LEDBAT when the sender populated TSDiff (non-zero).
	if h.TSDiff != 0 {
		c.ledbat.OnAck(int64(h.TSDiff), mss)
	}

	// Remove cumulative-ACKed packets from retransmit buffer.
	c.sendBufMu.Lock()
	for seq := range c.sendBuf {
		if wrappingLEQ(seq, h.AckNr) {
			delete(c.sendBuf, seq)
		}
	}
	c.sendBufMu.Unlock()

	// Signal if remote ACKed our FIN.
	c.mu.Lock()
	closed := c.closed
	finSeq := c.finSeqNr
	c.mu.Unlock()
	if closed && finSeq != 0 && wrappingLEQ(finSeq, h.AckNr) {
		select {
		case c.finAcked <- struct{}{}:
		default:
		}
	}
}

// sendAckFor sends a STATE (ACK) packet. dataTimestamp is the Timestamp field
// from the incoming DATA packet; the resulting TSDiff lets the remote measure
// one-way delay. Pass 0 for SYN-ACK and other non-data ACKs.
func (c *Conn) sendAckFor(dataTimestamp uint32) {
	now := utpTimestamp()
	var tsDiff uint32
	if dataTimestamp != 0 {
		tsDiff = now - dataTimestamp // uint32 subtraction handles wrap-around
	}
	c.mu.Lock()
	avail := cap(c.recvCh) - len(c.recvCh)
	if avail < 0 {
		avail = 0
	}
	pkt := &Packet{
		Header: Header{
			Type:    StState,
			Version: 1,
			ConnID:  c.connID,
			// Timestamp lets the remote compute TSDiff for its LEDBAT.
			Timestamp: now,
			TSDiff:    tsDiff,
			WndSize:   uint32(avail) * mss,
			SeqNr:     c.seqNr,
			AckNr:     c.ackNr,
		},
	}
	c.mu.Unlock()
	c.writeMu.Lock()
	_, _ = c.udpConn.WriteToUDP(pkt.Marshal(), c.remote)
	c.writeMu.Unlock()
}

// sendAck sends a STATE packet without OWD information (for SYN-ACK etc.).
func (c *Conn) sendAck() {
	c.sendAckFor(0)
}

// retransmitLoop periodically retransmits unacknowledged packets.
// It exits when closeCh is closed.
func (c *Conn) retransmitLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.closeCh:
			return
		case <-ticker.C:
			now := time.Now()
			c.sendBufMu.Lock()
			for seq, sp := range c.sendBuf {
				if now.Sub(sp.sentAt) < c.rto {
					continue
				}
				if sp.retries >= maxRetransmits {
					delete(c.sendBuf, seq)
					c.ledbat.OnLoss()
					continue
				}
				c.writeMu.Lock()
				_, _ = c.udpConn.WriteToUDP(sp.raw, c.remote)
				c.writeMu.Unlock()
				sp.sentAt = now
				sp.retries++
				c.ledbat.OnTimeout()
			}
			c.sendBufMu.Unlock()
		}
	}
}

// --- helpers ---

func utpTimestamp() uint32 {
	return uint32(time.Now().UnixMicro() & 0xFFFFFFFF)
}

// wrappingGT reports whether a > b in uint16 wrap-around arithmetic (RFC 793).
func wrappingGT(a, b uint16) bool {
	return int16(a-b) > 0
}

// wrappingLEQ reports whether a <= b in uint16 wrap-around arithmetic.
func wrappingLEQ(a, b uint16) bool {
	return !wrappingGT(a, b)
}

func randomConnID() uint16 {
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	return binary.BigEndian.Uint16(buf[:])
}
