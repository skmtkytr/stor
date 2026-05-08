package download

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readSysctlMax reads net.core.{w,r}mem_max from /proc to figure out
// the kernel's per-socket buffer ceiling. Returns 0 (and skips the
// caller via t.Skip) when the file isn't readable — non-Linux
// platforms or sandboxes — since the test below depends on knowing
// the cap to compute its expectation.
func readSysctlMax(t *testing.T, name string) int {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/net/core/" + name)
	if err != nil {
		t.Skipf("cannot read /proc/sys/net/core/%s: %v", name, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Skipf("unparseable /proc/sys/net/core/%s: %v", name, err)
	}
	return n
}

// readKeepAlive reads SO_KEEPALIVE back from the kernel via SyscallConn,
// so we can assert what the tuner actually flipped (the *net.TCPConn API
// has no public getter).
func readKeepAlive(t *testing.T, tc *net.TCPConn) bool {
	t.Helper()
	rc, err := tc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var got int
	var sockErr error
	err = rc.Control(func(fd uintptr) {
		got, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_KEEPALIVE)
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if sockErr != nil {
		t.Fatalf("GetsockoptInt: %v", sockErr)
	}
	return got != 0
}

func readSendBuf(t *testing.T, tc *net.TCPConn) int {
	t.Helper()
	rc, err := tc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var got int
	var sockErr error
	err = rc.Control(func(fd uintptr) {
		got, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if sockErr != nil {
		t.Fatalf("GetsockoptInt: %v", sockErr)
	}
	return got
}

func readRecvBuf(t *testing.T, tc *net.TCPConn) int {
	t.Helper()
	rc, err := tc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var got int
	var sockErr error
	err = rc.Control(func(fd uintptr) {
		got, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if sockErr != nil {
		t.Fatalf("GetsockoptInt: %v", sockErr)
	}
	return got
}

// dialTCPPair returns a connected client/server *net.TCPConn pair on
// 127.0.0.1, and registers Close on cleanup.
func dialTCPPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type accepted struct {
		c   net.Conn
		err error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		ch <- accepted{c, err}
	}()

	c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	srv := <-ch
	if srv.err != nil {
		t.Fatalf("Accept: %v", srv.err)
	}
	t.Cleanup(func() { _ = c.Close() })
	t.Cleanup(func() { _ = srv.c.Close() })
	return c.(*net.TCPConn), srv.c.(*net.TCPConn)
}

// TestTunePeerSocketEnablesKeepAlive verifies SO_KEEPALIVE is on after
// tunePeerSocket. Linux's default tcp_keepalive_time is 7200s — far
// longer than typical NAT session timeouts (60-300s for UDP, 600-7200s
// for TCP, with the short end winning on consumer routers). Without an
// app-layer keep-alive override (BEP-3 sends one only during active
// piece traffic), idle leech peers get reaped by the NAT box and we
// never notice until the next request times out.
func TestTunePeerSocketEnablesKeepAlive(t *testing.T) {
	c, _ := dialTCPPair(t)

	// Pre-condition: explicitly disable so the post-call read proves the
	// helper flipped it back on, not that we're reading the OS default.
	if err := c.SetKeepAlive(false); err != nil {
		t.Fatalf("pre SetKeepAlive(false): %v", err)
	}
	if readKeepAlive(t, c) {
		t.Fatal("pre-condition: SO_KEEPALIVE should be off")
	}

	tunePeerSocket(c)

	if !readKeepAlive(t, c) {
		t.Fatal("tunePeerSocket did not enable SO_KEEPALIVE")
	}
}

// TestTunePeerSocketAlsoSetsNoDelay is the belt-and-suspenders check
// that the renamed/expanded helper still does what the old one did.
func TestTunePeerSocketAlsoSetsNoDelay(t *testing.T) {
	c, _ := dialTCPPair(t)
	if err := c.SetNoDelay(false); err != nil {
		t.Fatalf("pre SetNoDelay(false): %v", err)
	}
	if readNoDelay(t, c) {
		t.Fatal("pre-condition: TCP_NODELAY should be off")
	}

	tunePeerSocket(c)

	if !readNoDelay(t, c) {
		t.Fatal("tunePeerSocket did not enable TCP_NODELAY")
	}
}

// TestTunePeerSocketIgnoresNonTCP guards the type-assertion fallback:
// uTP / mse-wrapped / net.Pipe / nil all flow through the dial / accept
// path and must not panic.
func TestTunePeerSocketIgnoresNonTCP(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()

	tunePeerSocket(a)
	tunePeerSocket(b)
	tunePeerSocket(nil)
	tunePeerSocket(&net.TCPConn{}) // zero-value TCPConn → setters error, must swallow
}

// TestTunePeerSocketDefaultLeavesBuffersAlone verifies the
// default-zero behaviour: when neither SocketSendBuffer nor
// SocketRecvBuffer is set, we MUST NOT call SetReadBuffer /
// SetWriteBuffer. Linux's TCP auto-tuning (tcp_rmem default 4K/87K/6M,
// tcp_wmem same shape) only works while the buffer is in
// auto-tune mode; the moment SetReadBuffer(n) is called the kernel
// pins the buffer at n*2 and disables further growth. So a "1 MiB
// looks safe" override actually caps throughput below the auto-tune
// ceiling on a healthy peer.
func TestTunePeerSocketDefaultLeavesBuffersAlone(t *testing.T) {
	resetPeerSocketBuffersForTest(t)

	c, _ := dialTCPPair(t)
	beforeSnd := readSendBuf(t, c)
	beforeRcv := readRecvBuf(t, c)

	tunePeerSocket(c)

	if got := readSendBuf(t, c); got != beforeSnd {
		t.Errorf("SO_SNDBUF changed despite default-zero config: before=%d after=%d", beforeSnd, got)
	}
	if got := readRecvBuf(t, c); got != beforeRcv {
		t.Errorf("SO_RCVBUF changed despite default-zero config: before=%d after=%d", beforeRcv, got)
	}
}

// TestTunePeerSocketAppliesExplicitBuffers verifies that calling
// SetPeerSocketBuffers + tunePeerSocket actually pins the kernel buffer
// to the configured size (subject to the kernel cap).
//
// What the kernel does:
//
//   - SetWriteBuffer(n) -> setsockopt(SO_SNDBUF, n)
//   - kernel clamps n to wmem_max
//   - kernel doubles the result for accounting (man socket(7))
//   - getsockopt(SO_SNDBUF) returns 2 * min(n, wmem_max)
//
// We can't just assert "grew vs. before": on a kernel where auto-tune
// already pushed the initial buffer above 2*requested (common on
// desktops with large tcp_{r,w}mem max), pinning to the requested
// value will *shrink* it. That shrink is the documented trade-off
// (auto-tune is now off), and the test would otherwise flake in a
// way that depended on the host's sysctls.
//
// Instead we read net.core.{w,r}mem_max from /proc and assert the
// exact post-pin size. Skips on non-Linux / sandboxed environments
// where the sysctl files aren't readable.
func TestTunePeerSocketAppliesExplicitBuffers(t *testing.T) {
	resetPeerSocketBuffersForTest(t)

	wmemMax := readSysctlMax(t, "wmem_max")
	rmemMax := readSysctlMax(t, "rmem_max")

	const want = 1 << 20 // 1 MiB
	SetPeerSocketBuffers(want, want)

	c, _ := dialTCPPair(t)
	tunePeerSocket(c)

	expectedSnd := 2 * want
	if want > wmemMax {
		expectedSnd = 2 * wmemMax
	}
	expectedRcv := 2 * want
	if want > rmemMax {
		expectedRcv = 2 * rmemMax
	}

	if got := readSendBuf(t, c); got != expectedSnd {
		t.Errorf("SO_SNDBUF = %d, want %d (want=%d, wmem_max=%d)", got, expectedSnd, want, wmemMax)
	}
	if got := readRecvBuf(t, c); got != expectedRcv {
		t.Errorf("SO_RCVBUF = %d, want %d (want=%d, rmem_max=%d)", got, expectedRcv, want, rmemMax)
	}
}

// TestSetPeerSocketBuffersZeroResets covers the round-trip: after
// setting then resetting to zero, a fresh tune must not touch buffers
// (auto-tune restored for any new conn, even though the old conn
// remains pinned — that's a kernel constraint, not ours).
func TestSetPeerSocketBuffersZeroResets(t *testing.T) {
	resetPeerSocketBuffersForTest(t)

	SetPeerSocketBuffers(1<<20, 1<<20)
	SetPeerSocketBuffers(0, 0)

	c, _ := dialTCPPair(t)
	beforeSnd := readSendBuf(t, c)
	beforeRcv := readRecvBuf(t, c)

	tunePeerSocket(c)

	if got := readSendBuf(t, c); got != beforeSnd {
		t.Errorf("SO_SNDBUF changed after reset to 0: before=%d after=%d", beforeSnd, got)
	}
	if got := readRecvBuf(t, c); got != beforeRcv {
		t.Errorf("SO_RCVBUF changed after reset to 0: before=%d after=%d", beforeRcv, got)
	}
}

// resetPeerSocketBuffersForTest restores package-level buffer state to
// 0 (auto-tune) and registers a Cleanup to do so again — so tests that
// mutate the globals cannot leak into subsequent tests.
func resetPeerSocketBuffersForTest(t *testing.T) {
	t.Helper()
	SetPeerSocketBuffers(0, 0)
	t.Cleanup(func() { SetPeerSocketBuffers(0, 0) })
}
