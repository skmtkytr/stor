package tracker

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skmtkytr/stor/bencode"
)

func TestBuildAnnounceURL(t *testing.T) {
	req := AnnounceRequest{
		AnnounceURL: "http://tracker.example.com/announce",
		InfoHash:    [20]byte{0x01, 0x02, 0x03},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Uploaded:    0,
		Downloaded:  0,
		Left:        1000,
		Event:       EventStarted,
	}

	u, err := buildAnnounceURL(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check required params exist
	q := u.Query()
	for _, key := range []string{"info_hash", "peer_id", "port", "uploaded", "downloaded", "left", "event", "compact"} {
		if q.Get(key) == "" {
			t.Errorf("missing query param: %s", key)
		}
	}
	if q.Get("compact") != "1" {
		t.Errorf("compact should be 1, got %q", q.Get("compact"))
	}
	if q.Get("event") != "started" {
		t.Errorf("event should be 'started', got %q", q.Get("event"))
	}
	if q.Get("port") != "6881" {
		t.Errorf("port should be '6881', got %q", q.Get("port"))
	}
}

func TestParseCompactPeers(t *testing.T) {
	// 2 peers: 192.168.1.1:6881 and 10.0.0.1:8080
	data := make([]byte, 12)
	copy(data[0:4], net.IPv4(192, 168, 1, 1).To4())
	binary.BigEndian.PutUint16(data[4:6], 6881)
	copy(data[6:10], net.IPv4(10, 0, 0, 1).To4())
	binary.BigEndian.PutUint16(data[10:12], 8080)

	peers, err := parseCompactPeers(string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(peers))
	}
	if peers[0].IP.String() != "192.168.1.1" {
		t.Errorf("peer[0] IP: got %s", peers[0].IP)
	}
	if peers[0].Port != 6881 {
		t.Errorf("peer[0] port: got %d", peers[0].Port)
	}
	if peers[1].IP.String() != "10.0.0.1" {
		t.Errorf("peer[1] IP: got %s", peers[1].IP)
	}
	if peers[1].Port != 8080 {
		t.Errorf("peer[1] port: got %d", peers[1].Port)
	}
}

func TestParseCompactPeersInvalidLength(t *testing.T) {
	_, err := parseCompactPeers("12345") // not multiple of 6
	if err == nil {
		t.Fatal("expected error for invalid length")
	}
}

func TestAnnounceHTTP(t *testing.T) {
	// Build compact peer list: 127.0.0.1:9999
	peerData := make([]byte, 6)
	copy(peerData[0:4], net.IPv4(127, 0, 0, 1).To4())
	binary.BigEndian.PutUint16(peerData[4:6], 9999)

	resp := map[string]any{
		"interval": int64(1800),
		"peers":    string(peerData),
	}
	respData, _ := bencode.Encode(resp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("info_hash") == "" {
			http.Error(w, "missing info_hash", 400)
			return
		}
		_, _ = w.Write(respData)
	}))
	defer server.Close()

	req := AnnounceRequest{
		AnnounceURL: server.URL + "/announce",
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        1000,
		Event:       EventStarted,
	}

	result, err := Announce(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Interval != 1800 {
		t.Errorf("interval: got %d, want 1800", result.Interval)
	}
	if len(result.Peers) != 1 {
		t.Fatalf("peers: got %d, want 1", len(result.Peers))
	}
	if result.Peers[0].IP.String() != "127.0.0.1" {
		t.Errorf("peer IP: got %s", result.Peers[0].IP)
	}
	if result.Peers[0].Port != 9999 {
		t.Errorf("peer port: got %d", result.Peers[0].Port)
	}
}

func TestAnnounceHTTPTrackerError(t *testing.T) {
	resp := map[string]any{
		"failure reason": "info_hash not found",
	}
	respData, _ := bencode.Encode(resp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(respData)
	}))
	defer server.Close()

	req := AnnounceRequest{
		AnnounceURL: server.URL + "/announce",
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        1000,
	}

	_, err := Announce(req)
	if err == nil {
		t.Fatal("expected error for tracker failure response")
	}
}

func TestAnnounceHTTPLargeResponse(t *testing.T) {
	// Build a valid bencode response with a large peers string (>10MB).
	// Without a response size limit, this would be read fully and parsed.
	// With the limit, ReadAll only reads MaxTrackerResponseSize bytes,
	// truncating the bencode and causing a decode error.
	peerCount := (11 * 1024 * 1024) / 6
	bigPeers := make([]byte, peerCount*6) // ~11MB of "compact peers" (multiple of 6)
	for i := range bigPeers {
		bigPeers[i] = byte(i % 256)
	}
	resp := map[string]any{
		"interval": int64(1800),
		"peers":    string(bigPeers),
	}
	respData, _ := bencode.Encode(resp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(respData)
	}))
	defer server.Close()

	req := AnnounceRequest{
		AnnounceURL: server.URL + "/announce",
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        1000,
	}

	_, err := Announce(req)
	// With MaxTrackerResponseSize limit, the response should be truncated
	// and fail to parse, preventing DoS from oversized responses.
	if err == nil {
		t.Fatal("expected error for oversized tracker response (should be limited to MaxTrackerResponseSize)")
	}
}

func TestAnnounceHTTPDictPeers(t *testing.T) {
	// Non-compact (legacy) peer list format
	resp := map[string]any{
		"interval": int64(900),
		"peers": []any{
			map[string]any{
				"ip":      "192.168.1.100",
				"peer id": fmt.Sprintf("%-20s", "-XX0001-"),
				"port":    int64(51413),
			},
		},
	}
	respData, _ := bencode.Encode(resp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(respData)
	}))
	defer server.Close()

	req := AnnounceRequest{
		AnnounceURL: server.URL + "/announce",
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        500,
	}

	result, err := Announce(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Peers) != 1 {
		t.Fatalf("peers: got %d, want 1", len(result.Peers))
	}
	if result.Peers[0].IP.String() != "192.168.1.100" {
		t.Errorf("peer IP: got %s", result.Peers[0].IP)
	}
	if result.Peers[0].Port != 51413 {
		t.Errorf("peer port: got %d", result.Peers[0].Port)
	}
}
