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
	// Start listener
	ln, err := Listen("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().String()

	// Accept in background
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()

	// Dial
	client, err := DialTimeout(addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	// Wait for accept
	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("accept timeout")
	}
	defer func() { _ = server.Close() }()

	// Send data client → server
	msg := []byte("hello from client")
	if _, err := client.Write(msg); err != nil {
		t.Fatal(err)
	}

	// Read on server
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

// net.Conn interface compliance
var (
	_ net.Conn     = (*Conn)(nil)
	_ net.Listener = (*Listener)(nil)
)
