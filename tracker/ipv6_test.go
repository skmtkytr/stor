package tracker

import (
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestFilterPrivatePeersIPv4MappedV6(t *testing.T) {
	// Security: ensure a malicious tracker can't bypass private IP filtering
	// by sending IPv4-mapped IPv6 addresses like ::ffff:10.0.0.1.
	peers := []Peer{
		{IP: net.ParseIP("::ffff:10.0.0.1"), Port: 6881},    // v4-mapped private
		{IP: net.ParseIP("::ffff:127.0.0.1"), Port: 6881},   // v4-mapped loopback
		{IP: net.ParseIP("::ffff:192.168.1.1"), Port: 6881}, // v4-mapped private
		{IP: net.ParseIP("::ffff:169.254.1.1"), Port: 6881}, // v4-mapped link-local
		{IP: net.ParseIP("::ffff:8.8.8.8"), Port: 6881},     // v4-mapped public — keep
		{IP: net.ParseIP("2001:db8::1"), Port: 6881},        // public v6 — keep
	}
	filtered := FilterPrivatePeers(peers)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 public peers, got %d: %+v", len(filtered), filtered)
	}
	for _, p := range filtered {
		// Re-check: must not be any form of private
		if p.IP.IsLoopback() || p.IP.IsPrivate() || p.IP.IsLinkLocalUnicast() {
			t.Errorf("private address leaked through filter: %s", p.IP)
		}
	}
}

func TestParseAnnounceResponsePeers6Only(t *testing.T) {
	// A tracker may return only peers6 with no peers field.
	p6 := make([]byte, 18)
	copy(p6[0:16], net.ParseIP("2001:db8::1").To16())
	binary.BigEndian.PutUint16(p6[16:18], 6881)

	resp := map[string]any{
		"interval": int64(1800),
		"peers6":   string(p6),
	}
	data, _ := bencode.Encode(resp)

	r, err := parseAnnounceResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(r.Peers))
	}
	if !r.Peers[0].IP.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("peer IP: got %s, want 2001:db8::1", r.Peers[0].IP)
	}
}

func TestParseAnnounceResponseInvalidPeers6(t *testing.T) {
	// peers6 with length not multiple of 18 must cause error.
	resp := map[string]any{
		"interval": int64(1800),
		"peers6":   "shortbadbad", // 11 bytes, not multiple of 18
	}
	data, _ := bencode.Encode(resp)

	_, err := parseAnnounceResponse(data)
	if err == nil {
		t.Fatal("expected error for invalid peers6 length")
	}
}

func TestBuildAnnounceURLIPv6Escaping(t *testing.T) {
	// IPv6 addresses contain colons which must be percent-encoded.
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
	// The parsed Query().Get() decodes back to plain form
	if u.Query().Get("ipv6") != "2001:db8::1" {
		t.Errorf("ipv6 roundtrip: got %q", u.Query().Get("ipv6"))
	}
	// Raw URL must be properly percent-encoded (colons → %3A)
	raw := u.String()
	if !strings.Contains(raw, "ipv6=2001%3Adb8%3A%3A1") {
		t.Errorf("ipv6 not percent-encoded in raw URL: %s", raw)
	}
}

func TestAnnounceHTTPEndToEndWithPeers6(t *testing.T) {
	// Integration: httptest server returns bencoded response with both peers
	// and peers6; announceHTTP must read both and send ipv6= param.
	p4 := make([]byte, 6)
	copy(p4[0:4], net.IPv4(8, 8, 8, 8).To4())
	binary.BigEndian.PutUint16(p4[4:6], 6881)

	p6 := make([]byte, 18)
	copy(p6[0:16], net.ParseIP("2001:db8::1").To16())
	binary.BigEndian.PutUint16(p6[16:18], 6881)

	var gotIPv6Param string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIPv6Param = r.URL.Query().Get("ipv6")
		data, _ := bencode.Encode(map[string]any{
			"interval": int64(1800),
			"peers":    string(p4),
			"peers6":   string(p6),
		})
		_, _ = w.Write(data)
	}))
	defer server.Close()

	req := AnnounceRequest{
		AnnounceURL: server.URL + "/announce",
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        1000,
		IPv6:        "2001:db8::beef",
	}
	// Use announceHTTP with explicit client (bypasses DNS pinning for test server)
	resp, err := announceHTTP(req, &http.Client{Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("announceHTTP: %v", err)
	}
	if gotIPv6Param != "2001:db8::beef" {
		t.Errorf("tracker got ipv6=%q, want 2001:db8::beef", gotIPv6Param)
	}
	if len(resp.Peers) != 2 {
		t.Fatalf("expected 2 peers (v4+v6), got %d", len(resp.Peers))
	}
}

func TestParseDictPeersIPv6(t *testing.T) {
	// Non-compact (dict) peer list with IPv6 string. Some trackers still use
	// the legacy dict format and may embed IPv6 addresses.
	list := []any{
		map[string]any{"ip": "2001:db8::1", "port": int64(6881)},
		map[string]any{"ip": "::1", "port": int64(8080)},
	}
	peers, err := parseDictPeers(list)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	if !peers[0].IP.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("peer[0] IP: got %s", peers[0].IP)
	}
	if peers[0].Port != 6881 {
		t.Errorf("peer[0] port: got %d", peers[0].Port)
	}
}

func TestFilterUnspecifiedIPv6(t *testing.T) {
	// :: (all zeros) is unspecified and must be filtered by FilterPrivatePeers.
	peers := []Peer{
		{IP: net.ParseIP("::"), Port: 6881},
		{IP: net.ParseIP("0.0.0.0"), Port: 6881},
		{IP: net.ParseIP("2001:db8::1"), Port: 6881},
	}
	filtered := FilterPrivatePeers(peers)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 peer, got %d: %+v", len(filtered), filtered)
	}
	if !filtered[0].IP.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("unexpected filtered peer: %s", filtered[0].IP)
	}
}

func TestParseCompactPeers6UnspecifiedFiltered(t *testing.T) {
	// End-to-end: malicious tracker sends 18 zero bytes → :: peer.
	// parseCompactPeers6 accepts it (no validation), FilterPrivatePeers rejects.
	data := make([]byte, 36)
	// peer 0: all zeros (::) with port 6881
	binary.BigEndian.PutUint16(data[16:18], 6881)
	// peer 1: valid public v6
	copy(data[18:34], net.ParseIP("2001:db8::1").To16())
	binary.BigEndian.PutUint16(data[34:36], 6881)

	peers, err := parseCompactPeers6(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("parseCompactPeers6: got %d, want 2", len(peers))
	}
	filtered := FilterPrivatePeers(peers)
	if len(filtered) != 1 {
		t.Fatalf("after filter: got %d, want 1", len(filtered))
	}
}

func TestFilterULA(t *testing.T) {
	// RFC 4193 Unique Local Addresses (fc00::/7) must be filtered.
	peers := []Peer{
		{IP: net.ParseIP("fc00::1"), Port: 6881},              // ULA
		{IP: net.ParseIP("fd12::1"), Port: 6881},              // ULA
		{IP: net.ParseIP("2606:4700:4700::1111"), Port: 6881}, // public
	}
	filtered := FilterPrivatePeers(peers)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 public peer after ULA filter, got %d: %+v", len(filtered), filtered)
	}
	if !filtered[0].IP.Equal(net.ParseIP("2606:4700:4700::1111")) {
		t.Errorf("unexpected peer: %s", filtered[0].IP)
	}
}

func TestFilterIPv6Multicast(t *testing.T) {
	// Document current behavior: link-local multicast (ff02::) is filtered,
	// but global multicast (ff0e::) is NOT currently detected as "private".
	// If this behavior changes, update the test.
	peers := []Peer{
		{IP: net.ParseIP("ff02::1"), Port: 6881}, // link-local multicast — filtered
	}
	filtered := FilterPrivatePeers(peers)
	if len(filtered) != 0 {
		t.Errorf("link-local multicast should be filtered, got %d", len(filtered))
	}
}

func TestResolveAndValidateTrackerHostIPv6Public(t *testing.T) {
	// Bracketed IPv6 literal in tracker URL: hostname is extracted without brackets.
	// Public IPv6 (Cloudflare DNS) should pass.
	host := "2606:4700:4700::1111"
	resolved, err := resolveAndValidateTrackerHost(host)
	if err != nil {
		t.Fatalf("unexpected error for public IPv6: %v", err)
	}
	if resolved != host {
		t.Errorf("resolved: got %q, want %q", resolved, host)
	}
}

func TestResolveAndValidateTrackerHostIPv6ULA(t *testing.T) {
	// ULA must be rejected at host validation.
	_, err := resolveAndValidateTrackerHost("fd12::1")
	if err == nil {
		t.Fatal("expected error for ULA tracker host")
	}
}

func TestResolveAndValidateTrackerHostIPv6LinkLocal(t *testing.T) {
	_, err := resolveAndValidateTrackerHost("fe80::1")
	if err == nil {
		t.Fatal("expected error for link-local v6 tracker host")
	}
}

func TestBuildAnnounceURLIPv6LiteralInTrackerURL(t *testing.T) {
	// Tracker URL with IPv6 literal (bracketed host) must parse cleanly.
	req := AnnounceRequest{
		AnnounceURL: "http://[2606:4700:4700::1111]:6969/announce",
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        1000,
	}
	u, err := buildAnnounceURL(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Hostname() != "2606:4700:4700::1111" {
		t.Errorf("hostname: got %q, want 2606:4700:4700::1111", u.Hostname())
	}
	if u.Port() != "6969" {
		t.Errorf("port: got %q, want 6969", u.Port())
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
