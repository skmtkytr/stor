package dht

import (
	"net"
	"testing"
	"time"
)

func TestPendingTransactionCleanup(t *testing.T) {
	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	// Create stale pending transactions
	for i := range 100 {
		txn := string(rune(i))
		ch := make(chan *KRPCMessage, 1)
		d.pending.Store(txn, ch)
	}

	// Run cleanup with a very short TTL
	d.cleanStalePending(1 * time.Nanosecond)

	// All entries should be cleaned
	count := 0
	d.pending.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count > 0 {
		t.Errorf("expected 0 pending after cleanup, got %d", count)
	}
}

func TestFilterPrivatePeers(t *testing.T) {
	peers := []string{
		"8.8.8.8:6881",     // public - keep
		"127.0.0.1:6881",   // loopback - remove
		"192.168.1.1:6881", // private - remove
		"10.0.0.1:6881",    // private - remove
		"172.16.0.1:6881",  // private - remove
		"169.254.1.1:6881", // link-local - remove
		"1.2.3.4:6881",     // public - keep
		"[::1]:6881",       // loopback v6 - remove
		"[fe80::1]:6881",   // link-local v6 - remove
	}
	filtered := FilterPrivatePeers(peers)
	if len(filtered) != 2 {
		t.Errorf("expected 2 public peers, got %d: %v", len(filtered), filtered)
	}
	for _, p := range filtered {
		host, _, _ := net.SplitHostPort(p)
		ip := net.ParseIP(host)
		if ip == nil {
			t.Errorf("invalid IP: %s", host)
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			t.Errorf("private IP not filtered: %s", p)
		}
	}
}
