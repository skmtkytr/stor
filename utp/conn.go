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
}

// newConn creates a uTP connection.
func newConn(udp *net.UDPConn, remote *net.UDPAddr, connID, seqNr, ackNr uint16) *Conn {
	return &Conn{
		udpConn: udp,
		remote:  remote,
		connID:  connID,
		seqNr:   seqNr,
		ackNr:   ackNr,
		ledbat:  NewLEDBAT(),
		recvCh:  make(chan []byte, 256),
		closeCh: make(chan struct{}),
	}
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
			c.mu.Unlock()
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
		pkt := &Packet{
			Header: Header{
				Type:      StData,
				Version:   1,
				ConnID:    c.connID,
				Timestamp: utpTimestamp(),
				WndSize:   uint32(len(c.recvCh)) * mss,
				SeqNr:     c.seqNr,
				AckNr:     c.ackNr,
			},
			Payload: b[:chunkSize],
		}
		c.mu.Unlock()

		_, err := c.udpConn.WriteToUDP(pkt.Marshal(), c.remote)
		if err != nil {
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

	// Send FIN
	c.seqNr++
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
	c.mu.Unlock()
	_, _ = c.udpConn.WriteToUDP(pkt.Marshal(), c.remote)
	// Close the underlying UDP socket for outgoing connections (we own it).
	// Server-side connections share the listener's socket and must not close it.
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

// SetDeadline sets read and write deadlines.
func (c *Conn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

// SetReadDeadline sets the read deadline.
func (c *Conn) SetReadDeadline(t time.Time) error {
	return c.SetDeadline(t)
}

// SetWriteDeadline sets the write deadline.
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

// deliverData is called by the listener/muxer when a data packet arrives.
func (c *Conn) deliverData(payload []byte) {
	select {
	case c.recvCh <- payload:
	default:
		// drop if buffer full
	}
}

// handleAck processes an incoming ACK.
func (c *Conn) handleAck(h *Header) {
	delay := int64(h.TSDiff)
	c.ledbat.OnAck(delay, mss) // approximate acked bytes
	c.mu.Lock()
	c.ackNr = h.SeqNr
	c.mu.Unlock()
}

// sendAck sends a state (ACK) packet.
func (c *Conn) sendAck() {
	c.mu.Lock()
	pkt := &Packet{
		Header: Header{
			Type:      StState,
			Version:   1,
			ConnID:    c.connID,
			Timestamp: utpTimestamp(),
			WndSize:   256 * mss,
			SeqNr:     c.seqNr,
			AckNr:     c.ackNr,
		},
	}
	c.mu.Unlock()
	_, _ = c.udpConn.WriteToUDP(pkt.Marshal(), c.remote)
}

// --- helpers ---

func utpTimestamp() uint32 {
	return uint32(time.Now().UnixMicro() & 0xFFFFFFFF)
}

func randomConnID() uint16 {
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	return binary.BigEndian.Uint16(buf[:])
}
