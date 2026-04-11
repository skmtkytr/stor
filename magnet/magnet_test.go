package magnet

import (
	"encoding/hex"
	"testing"
)

func TestParseMagnetBasic(t *testing.T) {
	uri := "magnet:?xt=urn:btih:d69f91e6b2ae4c542468d1073a71d4ea13879a7f&dn=Test+File&tr=http://tracker.example.com/announce"

	m, err := Parse(uri)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedHash := "d69f91e6b2ae4c542468d1073a71d4ea13879a7f"
	if hex.EncodeToString(m.InfoHash[:]) != expectedHash {
		t.Errorf("info hash: got %x, want %s", m.InfoHash, expectedHash)
	}
	if m.DisplayName != "Test File" {
		t.Errorf("display name: got %q", m.DisplayName)
	}
	if len(m.Trackers) != 1 || m.Trackers[0] != "http://tracker.example.com/announce" {
		t.Errorf("trackers: got %v", m.Trackers)
	}
}

func TestParseMagnetMultipleTrackers(t *testing.T) {
	uri := "magnet:?xt=urn:btih:d69f91e6b2ae4c542468d1073a71d4ea13879a7f&tr=http://t1.example.com/announce&tr=udp://t2.example.com:6969/announce"

	m, err := Parse(uri)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(m.Trackers) != 2 {
		t.Fatalf("trackers: got %d, want 2", len(m.Trackers))
	}
	if m.Trackers[0] != "http://t1.example.com/announce" {
		t.Errorf("tracker[0]: got %q", m.Trackers[0])
	}
	if m.Trackers[1] != "udp://t2.example.com:6969/announce" {
		t.Errorf("tracker[1]: got %q", m.Trackers[1])
	}
}

func TestParseMagnetPeers(t *testing.T) {
	uri := "magnet:?xt=urn:btih:d69f91e6b2ae4c542468d1073a71d4ea13879a7f&x.pe=192.168.1.1:6881&x.pe=10.0.0.1:8080"

	m, err := Parse(uri)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(m.Peers) != 2 {
		t.Fatalf("peers: got %d, want 2", len(m.Peers))
	}
	if m.Peers[0] != "192.168.1.1:6881" {
		t.Errorf("peer[0]: got %q", m.Peers[0])
	}
}

func TestParseMagnetNoXT(t *testing.T) {
	_, err := Parse("magnet:?dn=Test")
	if err == nil {
		t.Fatal("expected error for missing xt")
	}
}

func TestParseMagnetInvalidScheme(t *testing.T) {
	_, err := Parse("http://example.com")
	if err == nil {
		t.Fatal("expected error for non-magnet scheme")
	}
}

func TestParseMagnetInvalidHashLength(t *testing.T) {
	_, err := Parse("magnet:?xt=urn:btih:abc123")
	if err == nil {
		t.Fatal("expected error for invalid hash length")
	}
}

func TestParseMagnetHybridV2(t *testing.T) {
	// Hybrid magnet with both urn:btih: and urn:btmh:
	v1Hash := "d69f91e6b2ae4c542468d1073a71d4ea13879a7f"
	v2Hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	uri := "magnet:?xt=urn:btih:" + v1Hash + "&xt=urn:btmh:1220" + v2Hash + "&dn=Hybrid"

	m, err := Parse(uri)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hex.EncodeToString(m.InfoHash[:]) != v1Hash {
		t.Errorf("v1 hash: got %x", m.InfoHash)
	}
	if !m.HasV2 {
		t.Error("expected HasV2=true")
	}
	if hex.EncodeToString(m.InfoHashV2[:]) != v2Hash {
		t.Errorf("v2 hash: got %x", m.InfoHashV2)
	}
}

func TestParseMagnetV2OnlyErrors(t *testing.T) {
	v2Hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	uri := "magnet:?xt=urn:btmh:1220" + v2Hash + "&dn=V2Only"

	_, err := Parse(uri)
	if err == nil {
		t.Fatal("expected error for v2-only magnet")
	}
}

func TestParseMagnetBase32Hash(t *testing.T) {
	// Base32-encoded info hash (32 chars)
	// "d69f91e6b2ae4c542468d1073a71d4ea13879a7f" in base32 is:
	// We'll use a known valid base32 encoding
	uri := "magnet:?xt=urn:btih:22PZDZVSVZGFIJDI2EDTU4OU5IJYPGT7"

	m, err := Parse(uri)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedHash := "d69f91e6b2ae4c542468d1073a71d4ea13879a7f"
	if hex.EncodeToString(m.InfoHash[:]) != expectedHash {
		t.Errorf("info hash: got %x, want %s", m.InfoHash, expectedHash)
	}
}
