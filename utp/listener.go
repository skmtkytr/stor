package utp

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// maxConns is the maximum number of tracked uTP connections per listener.
const maxConns = 4096

// Listener implements net.Listener for uTP connections.
type Listener struct {
	udpConn  *net.UDPConn
	mu       sync.Mutex
	writeMu  sync.Mutex       // serializes all WriteToUDP on the shared socket
	conns    map[uint16]*Conn // connID → Conn
	acceptCh chan *Conn
	closed   bool
	closeCh  chan struct{}
}

// Listen creates a uTP listener on the given address.
func Listen(network, addr string) (*Listener, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("utp: resolve addr: %w", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("utp: listen: %w", err)
	}
	_ = udpConn.SetReadBuffer(2 * 1024 * 1024)

	l := &Listener{
		udpConn:  udpConn,
		conns:    make(map[uint16]*Conn),
		acceptCh: make(chan *Conn, 64),
		closeCh:  make(chan struct{}),
	}
	go l.readLoop()
	return l, nil
}

// Accept waits for and returns the next uTP connection.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c, ok := <-l.acceptCh:
		if !ok {
			return nil, fmt.Errorf("utp: listener closed")
		}
		return c, nil
	case <-l.closeCh:
		return nil, fmt.Errorf("utp: listener closed")
	}
}

// Close closes the listener.
func (l *Listener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	close(l.closeCh)
	l.mu.Unlock()
	return l.udpConn.Close()
}

// Addr returns the listener's address.
func (l *Listener) Addr() net.Addr {
	return l.udpConn.LocalAddr()
}

func (l *Listener) readLoop() {
	buf := make([]byte, 65536)
	for {
		n, addr, err := l.udpConn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-l.closeCh:
				return
			default:
				continue
			}
		}
		if n < headerSize {
			continue
		}

		pkt, err := ParsePacket(buf[:n])
		if err != nil || pkt.Header.Version != 1 {
			continue
		}

		l.handlePacket(pkt, addr)
	}
}

func (l *Listener) handlePacket(pkt *Packet, addr *net.UDPAddr) {
	h := &pkt.Header

	l.mu.Lock()

	// SYN creates a new connection.
	if h.Type == StSyn {
		// Refuse if at capacity.
		if len(l.conns) >= maxConns {
			l.mu.Unlock()
			return
		}
		// Refuse if the accept queue is full — don't SYN-ACK so the client
		// will time out cleanly instead of becoming a zombie connection.
		if len(l.acceptCh) >= cap(l.acceptCh) {
			l.mu.Unlock()
			return
		}

		connID := h.ConnID + 1 // server replies with connID+1
		c := newConn(l.udpConn, addr, connID, 1, h.SeqNr)
		c.writeMu = &l.writeMu // share listener's write serialization
		c.setFirstExpected(h.SeqNr + 1)
		l.conns[h.ConnID] = c
		l.mu.Unlock()

		c.sendAck() // SYN-ACK
		c.startRetransmitLoop()

		// Capacity was verified under the lock; this send is guaranteed
		// non-blocking because only this goroutine (readLoop) adds to acceptCh.
		l.acceptCh <- c
		return
	}

	// Route to existing connection.
	// Initiator uses connID; responder uses connID+1 — try both.
	c, ok := l.conns[h.ConnID]
	if !ok {
		c, ok = l.conns[h.ConnID-1]
	}
	l.mu.Unlock()

	if !ok {
		return // unknown connection
	}

	switch h.Type {
	case StData:
		c.updateRemoteWnd(h.WndSize)
		c.deliverInOrder(h.SeqNr, pkt.Payload)
		c.sendAckFor(h.Timestamp)

	case StState:
		c.handleAck(h)

	case StFin, StReset:
		c.mu.Lock()
		if !c.closed {
			c.closed = true
			close(c.closeCh)
		}
		c.mu.Unlock()
		l.mu.Lock()
		delete(l.conns, h.ConnID)
		delete(l.conns, h.ConnID-1)
		l.mu.Unlock()
	}
}

const (
	maxSynRetries   = 4
	synInitialDelay = time.Second
)

// Dial creates an outgoing uTP connection.
func Dial(addr string) (net.Conn, error) {
	return DialTimeout(addr, 10*time.Second)
}

// DialTimeout creates an outgoing uTP connection with a timeout.
func DialTimeout(addr string, timeout time.Duration) (net.Conn, error) {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("utp: resolve: %w", err)
	}

	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, fmt.Errorf("utp: bind: %w", err)
	}

	connID := randomConnID()
	c := newConn(udpConn, raddr, connID+1, 1, 0)

	syn := &Packet{
		Header: Header{
			Type:      StSyn,
			Version:   1,
			ConnID:    connID,
			Timestamp: utpTimestamp(),
			WndSize:   256 * mss,
			SeqNr:     1,
			AckNr:     0,
		},
	}
	synRaw := syn.Marshal()

	deadline := time.Now().Add(timeout)
	retryDelay := synInitialDelay
	connected := false
	buf := make([]byte, 65536)

	for attempt := 0; attempt < maxSynRetries; attempt++ {
		if time.Now().After(deadline) {
			break
		}
		if _, err := udpConn.WriteToUDP(synRaw, raddr); err != nil {
			_ = udpConn.Close()
			return nil, fmt.Errorf("utp: send SYN: %w", err)
		}

		// Cap per-attempt wait to remaining total time.
		wait := retryDelay
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		_ = udpConn.SetReadDeadline(time.Now().Add(wait))

		for {
			n, fromAddr, readErr := udpConn.ReadFromUDP(buf)
			if readErr != nil {
				break // deadline elapsed → retry
			}
			if n < headerSize {
				continue
			}
			pkt, parseErr := ParsePacket(buf[:n])
			if parseErr != nil || pkt.Header.Version != 1 {
				continue
			}
			if pkt.Header.Type != StState {
				continue
			}
			// Validate sender and connection ID.
			if fromAddr.String() != raddr.String() {
				continue
			}
			if pkt.Header.ConnID != connID+1 {
				continue
			}
			c.mu.Lock()
			c.ackNr = pkt.Header.SeqNr
			c.mu.Unlock()
			c.setFirstExpected(pkt.Header.SeqNr + 1)
			c.updateRemoteWnd(pkt.Header.WndSize)
			connected = true
			break
		}
		if connected {
			break
		}
		retryDelay *= 2
	}

	if !connected {
		_ = udpConn.Close()
		return nil, fmt.Errorf("utp: SYN-ACK timeout after %d attempts", maxSynRetries)
	}

	_ = udpConn.SetReadDeadline(time.Time{})
	c.ownsUDP = true
	c.startRetransmitLoop()
	go c.clientReadLoop(udpConn)

	return c, nil
}

// clientReadLoop handles incoming packets for a dialed (client) connection.
func (c *Conn) clientReadLoop(udpConn *net.UDPConn) {
	buf := make([]byte, 65536)
	for {
		n, _, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < headerSize {
			continue
		}
		pkt, err := ParsePacket(buf[:n])
		if err != nil || pkt.Header.Version != 1 {
			continue
		}

		h := &pkt.Header
		switch h.Type {
		case StData:
			c.updateRemoteWnd(h.WndSize)
			c.deliverInOrder(h.SeqNr, pkt.Payload)
			c.sendAckFor(h.Timestamp)
		case StState:
			c.handleAck(h)
		case StFin, StReset:
			c.mu.Lock()
			if !c.closed {
				c.closed = true
				close(c.closeCh)
			}
			c.mu.Unlock()
			return
		}
	}
}
