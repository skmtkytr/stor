package tracker

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/skmtkytr/stor/bencode"
)

func TestParseCompactPeers6(t *testing.T) {
	// BEP 7: compact IPv6 peers are 18 bytes each (16-byte IPv6 + 2-byte port).
	// Build 2 peers: [2001:db8::1]:6881 and [::1]:8080
	p1 := net.ParseIP("2001:db8::1").To16()
	p2 := net.ParseIP("::1").To16()

	data := make([]byte, 36)
	copy(data[0:16], p1)
	binary.BigEndian.PutUint16(data[16:18], 6881)
	copy(data[18:34], p2)
	binary.BigEndian.PutUint16(data[34:36], 8080)

	peers, err := parseCompactPeers6(string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(peers))
	}
	if !peers[0].IP.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("peer[0] IP: got %s, want 2001:db8::1", peers[0].IP)
	}
	if peers[0].Port != 6881 {
		t.Errorf("peer[0] port: got %d, want 6881", peers[0].Port)
	}
	if !peers[1].IP.Equal(net.ParseIP("::1")) {
		t.Errorf("peer[1] IP: got %s, want ::1", peers[1].IP)
	}
	if peers[1].Port != 8080 {
		t.Errorf("peer[1] port: got %d, want 8080", peers[1].Port)
	}
}

func TestParseCompactPeers6InvalidLength(t *testing.T) {
	// Not a multiple of 18
	_, err := parseCompactPeers6("short")
	if err == nil {
		t.Fatal("expected error for invalid peers6 length")
	}
}

func TestParseAnnounceResponseWithPeers6(t *testing.T) {
	// BEP 7: response can contain both peers (v4) and peers6 (v6).
	p4 := make([]byte, 6)
	copy(p4[0:4], net.IPv4(8, 8, 8, 8).To4())
	binary.BigEndian.PutUint16(p4[4:6], 6881)

	p6 := make([]byte, 18)
	copy(p6[0:16], net.ParseIP("2001:db8::1").To16())
	binary.BigEndian.PutUint16(p6[16:18], 6881)

	resp := map[string]any{
		"interval": int64(1800),
		"peers":    string(p4),
		"peers6":   string(p6),
	}
	data, _ := bencode.Encode(resp)

	r, err := parseAnnounceResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Peers) != 2 {
		t.Fatalf("expected 2 peers (v4+v6), got %d", len(r.Peers))
	}
	// Verify one v4 and one v6 are present
	var gotV4, gotV6 bool
	for _, p := range r.Peers {
		if p.IP.To4() != nil {
			gotV4 = true
		} else if p.IP.To16() != nil {
			gotV6 = true
		}
	}
	if !gotV4 {
		t.Error("expected at least one IPv4 peer")
	}
	if !gotV6 {
		t.Error("expected at least one IPv6 peer")
	}
}

func TestBuildAnnounceURLWithIPv6(t *testing.T) {
	req := AnnounceRequest{
		AnnounceURL: "http://tracker.example.com/announce",
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        1000,
		IPv6:        "2001:db8::1",
	}
	u, err := buildAnnounceURL(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := u.Query().Get("ipv6")
	if got != "2001:db8::1" {
		t.Errorf("ipv6 param: got %q, want %q", got, "2001:db8::1")
	}
}

func TestBuildAnnounceURLWithoutIPv6(t *testing.T) {
	// If IPv6 is empty, the ipv6 param must not be set.
	req := AnnounceRequest{
		AnnounceURL: "http://tracker.example.com/announce",
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        1000,
	}
	u, err := buildAnnounceURL(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := u.Query()["ipv6"]; ok {
		t.Error("ipv6 param should not be present when IPv6 is empty")
	}
}

func TestLocalIPv6(t *testing.T) {
	// LocalIPv6 returns either a global unicast v6 address string or "".
	// We can't assert a specific value (depends on the host), but we
	// can verify it returns either empty or a parseable global IPv6.
	got := LocalIPv6()
	if got == "" {
		t.Skip("no IPv6 address on this host")
	}
	ip := net.ParseIP(got)
	if ip == nil {
		t.Errorf("LocalIPv6 returned unparseable address: %q", got)
	}
	if ip.To4() != nil {
		t.Errorf("LocalIPv6 returned IPv4: %q", got)
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		t.Errorf("LocalIPv6 returned non-global address: %q", got)
	}
}

func TestParseCompactPeers6Limit(t *testing.T) {
	numPeers := MaxPeersPerResponse + 50
	data := make([]byte, numPeers*18)
	for i := range numPeers {
		data[i*18+15] = byte(i + 1)
		binary.BigEndian.PutUint16(data[i*18+16:], 6881)
	}
	peers, err := parseCompactPeers6(string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(peers) > MaxPeersPerResponse {
		t.Errorf("peers: got %d, want at most %d", len(peers), MaxPeersPerResponse)
	}
}
