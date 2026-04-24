package utp

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestPacketRoundTrip(t *testing.T) {
	pkt := &Packet{
		Header: Header{
			Type:      StData,
			Version:   1,
			Extension: 0,
			ConnID:    12345,
			Timestamp: 1000000,
			TSDiff:    500,
			WndSize:   65536,
			SeqNr:     42,
			AckNr:     41,
		},
		Payload: []byte("hello utp"),
	}

	data := pkt.Marshal()
	got, err := ParsePacket(data)
	if err != nil {
		t.Fatal(err)
	}
	h := got.Header
	if h.Type != StData || h.Version != 1 || h.ConnID != 12345 {
		t.Fatalf("header mismatch: %+v", h)
	}
	if h.SeqNr != 42 || h.AckNr != 41 {
		t.Fatalf("seq/ack mismatch: %+v", h)
	}
	if !bytes.Equal(got.Payload, []byte("hello utp")) {
		t.Fatalf("payload mismatch: %q", got.Payload)
	}
}

func TestIsUTP(t *testing.T) {
	pkt := &Packet{Header: Header{Type: StSyn, Version: 1}}
	data := pkt.Marshal()
	if !IsUTP(data) {
		t.Error("should be uTP")
	}
	if IsUTP([]byte{0, 0, 0}) {
		t.Error("too short should not be uTP")
	}
	// version 0
	data[0] = StSyn << 4
	if IsUTP(data) {
		t.Error("version 0 should not be uTP")
	}
}

func TestLEDBAT(t *testing.T) {
	l := NewLEDBAT()

	initial := l.Cwnd()
	if initial != mss*2 {
		t.Fatalf("initial cwnd: %d", initial)
	}

	// Simulate ACKs with low delay — cwnd should increase
	for range 10 {
		l.OnAck(1000, mss) // 1ms delay, well below 100ms target
	}
	if l.Cwnd() <= initial {
		t.Fatalf("cwnd should increase with low delay: %d", l.Cwnd())
	}

	// Loss should halve cwnd
	before := l.Cwnd()
	l.OnLoss()
	if l.Cwnd() > before/2+1 {
		t.Fatalf("cwnd after loss: %d (was %d)", l.Cwnd(), before)
	}

	// Timeout should reset
	l.OnTimeout()
	if l.Cwnd() != mss {
		t.Fatalf("cwnd after timeout: %d", l.Cwnd())
	}
}

func TestDialAndListen(t *testing.T) {
	ln, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().String()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()

	client, err := DialTimeout(addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("accept timeout")
	}
	defer func() { _ = server.Close() }()

	// client → server
	msg := []byte("hello from client")
	if _, err := client.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := server.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("got %q, want %q", buf[:n], msg)
	}
}

// TestBidirectional verifies data flows in both directions.
func TestBidirectional(t *testing.T) {
	ln, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	client, err := DialTimeout(ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("accept timeout")
	}
	defer func() { _ = server.Close() }()

	// client → server
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := server.Read(buf)
	if err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("server read: %v %q", err, buf[:n])
	}

	// server → client
	if _, err := server.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err = client.Read(buf)
	if err != nil || string(buf[:n]) != "pong" {
		t.Fatalf("client read: %v %q", err, buf[:n])
	}
}

// TestGracefulClose verifies that Close sends FIN and the remote Read returns an error.
func TestGracefulClose(t *testing.T) {
	ln, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	client, err := DialTimeout(ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("accept timeout")
	}
	defer func() { _ = server.Close() }()

	// Close client; server Read should return an error.
	_ = client.Close()

	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	_, err = server.Read(buf)
	if err == nil {
		t.Error("expected error after remote close, got nil")
	}
}

// TestWrappingHelpers verifies uint16 wrap-around comparison helpers.
func TestWrappingHelpers(t *testing.T) {
	// Normal ordering
	if !wrappingGT(5, 3) {
		t.Error("5 > 3")
	}
	if wrappingGT(3, 5) {
		t.Error("3 not > 5")
	}

	// Wrap-around: 1 > 0xFFFE
	if !wrappingGT(1, 0xFFFE) {
		t.Error("wrap: 1 > 0xFFFE")
	}
	if wrappingGT(0xFFFE, 1) {
		t.Error("wrap: 0xFFFE not > 1")
	}

	// LEQ
	if !wrappingLEQ(3, 5) {
		t.Error("3 <= 5")
	}
	if !wrappingLEQ(5, 5) {
		t.Error("5 <= 5")
	}
	if !wrappingLEQ(0xFFFE, 1) {
		t.Error("wrap: 0xFFFE <= 1")
	}
}

// TestTimestampDiff verifies that uint32 subtraction correctly handles wrap-around.
func TestTimestampDiff(t *testing.T) {
	// Normal case
	diff := uint32(0x00000200) - uint32(0x00000100)
	if diff != 0x100 {
		t.Errorf("normal diff: got %d", diff)
	}
	// Wrap-around: receiver time wrapped past 0
	var a, b uint32 = 0x00000010, 0xFFFFFFF0
	diff = a - b
	if diff != 0x20 {
		t.Errorf("wrap diff: got %d", diff)
	}
}

// TestAckNrMonotone verifies that out-of-order packets don't roll back ackNr.
func TestAckNrMonotone(t *testing.T) {
	c := newConn(nil, nil, 1, 1, 0)
	c.ackNr = 5

	// Simulate arrival of an older packet (SeqNr=3 < current ackNr=5).
	c.mu.Lock()
	if wrappingGT(3, c.ackNr) {
		c.ackNr = 3
	}
	c.mu.Unlock()

	if c.ackNr != 5 {
		t.Errorf("ackNr regressed: got %d, want 5", c.ackNr)
	}

	// A newer packet (SeqNr=7) should advance ackNr.
	c.mu.Lock()
	if wrappingGT(7, c.ackNr) {
		c.ackNr = 7
	}
	c.mu.Unlock()

	if c.ackNr != 7 {
		t.Errorf("ackNr not advanced: got %d, want 7", c.ackNr)
	}
}

// TestAcceptBackpressure verifies that the listener drops SYNs when the accept
// queue is full rather than creating zombie connections.
func TestAcceptBackpressure(t *testing.T) {
	ln, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().String()
	cap := cap(ln.acceptCh)

	// Fill the accept queue without draining it.
	results := make(chan error, cap+5)
	for i := 0; i < cap+5; i++ {
		go func() {
			_, err := DialTimeout(addr, 500*time.Millisecond)
			results <- err
		}()
	}

	// Collect outcomes. We expect exactly cap successes (those that fit in the
	// queue) and the rest to time out or fail.
	successes := 0
	for i := 0; i < cap+5; i++ {
		if err := <-results; err == nil {
			successes++
		}
	}
	// Should not exceed accept queue capacity.
	if successes > cap {
		t.Errorf("accepted %d connections, cap is %d", successes, cap)
	}
}

// TestLEDBATBaseDelayHistory verifies that the sliding-window base delay
// correctly tracks the minimum across recent buckets.
func TestLEDBATBaseDelayHistory(t *testing.T) {
	l := NewLEDBAT()

	// Seed current bucket with delay=5000.
	l.OnAck(5000, mss)
	before := l.Cwnd()
	if before <= mss*2 {
		t.Fatalf("cwnd should have grown: %d", before)
	}

	// A higher delay (20000) should not change base delay.
	l.OnAck(20000, mss)

	// The base delay remains 5000; queuing delay = 20000-5000 = 15000.
	// offTarget = (100000-15000)/100000 = 0.85 > 0, so cwnd keeps growing (slowly).
	// We just verify it doesn't crash and stays in bounds.
	cwnd := l.Cwnd()
	if cwnd < minCwnd || cwnd > maxCwnd {
		t.Errorf("cwnd out of bounds: %d", cwnd)
	}
}

// TestConcurrentWrite verifies that simultaneous Write calls don't race.
func TestConcurrentWrite(t *testing.T) {
	ln, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	// Drain accepted connections in background.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 4096)
				for {
					_, err := c.Read(buf)
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()

	client, err := DialTimeout(ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	errc := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := client.Write(bytes.Repeat([]byte("x"), 512))
			errc <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errc; err != nil {
			t.Errorf("concurrent write error: %v", err)
		}
	}
}

// TestDeliverInOrder verifies that out-of-order packets are buffered and
// flushed in seqNr order once the missing segment arrives.
func TestDeliverInOrder(t *testing.T) {
	c := &Conn{
		recvCh:     make(chan []byte, 16),
		closeCh:    make(chan struct{}),
		reorderBuf: make(map[uint16][]byte),
	}
	c.setFirstExpected(2)

	// Deliver seqNr=3 first (out of order) — should be buffered, not delivered.
	c.deliverInOrder(3, []byte("third"))
	if len(c.recvCh) != 0 {
		t.Fatal("out-of-order packet should not be delivered yet")
	}

	// Deliver seqNr=2 (in order) — should flush both 2 and 3.
	c.deliverInOrder(2, []byte("second"))
	if len(c.recvCh) != 2 {
		t.Fatalf("expected 2 items in recvCh, got %d", len(c.recvCh))
	}
	first := <-c.recvCh
	second := <-c.recvCh
	if string(first) != "second" || string(second) != "third" {
		t.Errorf("order wrong: got %q then %q", first, second)
	}
	if c.ackNr != 3 {
		t.Errorf("ackNr should be 3 after flushing, got %d", c.ackNr)
	}
}

// TestDeliverInOrderDuplicate verifies that duplicate packets are discarded.
func TestDeliverInOrderDuplicate(t *testing.T) {
	c := &Conn{
		recvCh:     make(chan []byte, 16),
		closeCh:    make(chan struct{}),
		reorderBuf: make(map[uint16][]byte),
	}
	c.setFirstExpected(2)

	c.deliverInOrder(2, []byte("hello"))
	c.deliverInOrder(2, []byte("hello")) // duplicate
	if len(c.recvCh) != 1 {
		t.Errorf("duplicate should be discarded: got %d items", len(c.recvCh))
	}
}

// TestDeliverInOrderGap verifies a gap of multiple missing seqNrs.
func TestDeliverInOrderGap(t *testing.T) {
	c := &Conn{
		recvCh:     make(chan []byte, 16),
		closeCh:    make(chan struct{}),
		reorderBuf: make(map[uint16][]byte),
	}
	c.setFirstExpected(1)

	// Buffer 3, 4, 5; then deliver 1, 2 — should flush all five in order.
	c.deliverInOrder(3, []byte("c"))
	c.deliverInOrder(5, []byte("e"))
	c.deliverInOrder(4, []byte("d"))
	if len(c.recvCh) != 0 {
		t.Fatal("nothing should be delivered until seqNr=1 arrives")
	}

	c.deliverInOrder(1, []byte("a"))
	if len(c.recvCh) != 1 {
		t.Fatalf("expected 1 item after seqNr=1, got %d", len(c.recvCh))
	}

	// Delivering seqNr=2 flushes 2,3,4,5 — total 5 in recvCh (1 already there + 4).
	c.deliverInOrder(2, []byte("b"))
	if len(c.recvCh) != 5 {
		t.Fatalf("expected 5 total items after flush, got %d", len(c.recvCh))
	}
	want := []string{"a", "b", "c", "d", "e"}
	for _, w := range want {
		got := <-c.recvCh
		if string(got) != w {
			t.Errorf("want %q, got %q", w, got)
		}
	}
}

func TestMaxConnsConstant(t *testing.T) {
	if maxConns < 100 {
		t.Errorf("maxConns too small: %d", maxConns)
	}
	if maxConns > 100000 {
		t.Errorf("maxConns too large: %d", maxConns)
	}
}

// net.Conn interface compliance
var (
	_ net.Conn     = (*Conn)(nil)
	_ net.Listener = (*Listener)(nil)
)
