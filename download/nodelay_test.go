package download

import (
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/skmtkytr/stor/tracker"
)

// readNoDelay reads TCP_NODELAY back from the kernel via SyscallConn, so
// we can assert what setTCPNoDelay actually flipped (the standard
// *net.TCPConn API exposes a setter but no getter).
func readNoDelay(t *testing.T, tc *net.TCPConn) bool {
	t.Helper()
	rc, err := tc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var got int
	var sockErr error
	err = rc.Control(func(fd uintptr) {
		got, sockErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY)
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if sockErr != nil {
		t.Fatalf("GetsockoptInt: %v", sockErr)
	}
	return got != 0
}

// TestSetTCPNoDelaySetsFlag verifies that setTCPNoDelay flips
// SetNoDelay(true) on a real *net.TCPConn. Nagle adds ~40 ms latency to
// the small (17-byte) request messages we pipeline; without NODELAY a
// pipeline-then-flush stalls until the kernel ACK timer fires.
func TestSetTCPNoDelaySetsFlag(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	type accepted struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		acceptCh <- accepted{c, err}
	}()

	dialed, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = dialed.Close() }()

	srv := <-acceptCh
	if srv.err != nil {
		t.Fatalf("accept: %v", srv.err)
	}
	defer func() { _ = srv.conn.Close() }()

	// Disable NODELAY first so the post-call read can prove setTCPNoDelay
	// flipped it back on rather than just observing the OS default.
	tc := dialed.(*net.TCPConn)
	if err := tc.SetNoDelay(false); err != nil {
		t.Fatalf("pre-condition SetNoDelay(false): %v", err)
	}
	if readNoDelay(t, tc) {
		t.Fatal("pre-condition: TCP_NODELAY should be off after SetNoDelay(false)")
	}

	setTCPNoDelay(dialed)

	if !readNoDelay(t, tc) {
		t.Fatal("setTCPNoDelay did not enable TCP_NODELAY")
	}
}

// TestSetTCPNoDelayIgnoresNonTCP guards the type-assertion fallback: we
// must never panic on a *utp.Conn, mse-wrapped conn, or any other
// non-*net.TCPConn that flows through the dial / accept path.
func TestSetTCPNoDelayIgnoresNonTCP(t *testing.T) {
	// net.Pipe is a net.Conn but not a *net.TCPConn — exactly the shape
	// of mse-wrapped or utp connections from setTCPNoDelay's POV.
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()

	// Must not panic. Must not error.
	setTCPNoDelay(a)
	setTCPNoDelay(b)
	setTCPNoDelay(nil) // tolerant of nil

	// Also confirm the security_test mock-conn shape (a zero-value
	// *net.TCPConn embedded in &Client{conn: &net.TCPConn{}}) doesn't
	// crash when poked. SetNoDelay on a zero TCPConn returns an error,
	// which setTCPNoDelay must swallow.
	setTCPNoDelay(&net.TCPConn{})
}

// TestNewClientEnablesTCPNoDelay verifies that NewClient (the production
// outbound path) flips NODELAY on the resulting *net.TCPConn. This is
// the regression guard for the actual perf bug — fakePeer in
// download_test.go set NODELAY only on the SERVER side; without this
// fix the client side was still Nagle-buffered.
func TestNewClientEnablesTCPNoDelay(t *testing.T) {
	infoHash := [20]byte{0xDE}
	peerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	pieces := map[int][]byte{}
	ln := fakePeer(t, infoHash, pieces)
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().(*net.TCPAddr)
	p := tracker.Peer{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr.Port)}

	c, err := NewClient(p, infoHash, peerID)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	tc, ok := c.conn.(*net.TCPConn)
	if !ok {
		t.Fatalf("expected client.conn to be *net.TCPConn, got %T", c.conn)
	}
	if !readNoDelay(t, tc) {
		t.Fatal("NewClient did not enable TCP_NODELAY on the dialed conn")
	}
}
