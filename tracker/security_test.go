package tracker

import (
	"net"
	"testing"
)

func TestFilterPrivatePeers(t *testing.T) {
	peers := []Peer{
		{IP: net.ParseIP("8.8.8.8"), Port: 6881},     // public
		{IP: net.ParseIP("127.0.0.1"), Port: 6881},   // loopback
		{IP: net.ParseIP("192.168.1.1"), Port: 6881}, // private
		{IP: net.ParseIP("10.0.0.1"), Port: 6881},    // private
		{IP: net.ParseIP("172.16.0.1"), Port: 6881},  // private
		{IP: net.ParseIP("169.254.1.1"), Port: 6881}, // link-local
		{IP: net.ParseIP("1.2.3.4"), Port: 6881},     // public
		{IP: net.ParseIP("::1"), Port: 6881},         // loopback v6
		{IP: net.ParseIP("fe80::1"), Port: 6881},     // link-local v6
	}
	filtered := FilterPrivatePeers(peers)
	if len(filtered) != 2 {
		t.Errorf("expected 2 public peers, got %d", len(filtered))
	}
	for _, p := range filtered {
		if p.IP.IsLoopback() || p.IP.IsPrivate() || p.IP.IsLinkLocalUnicast() || p.IP.IsLinkLocalMulticast() {
			t.Errorf("private peer not filtered: %s", p.IP)
		}
	}
}

func TestAnnounceHTTPFiltersPrivatePeers(t *testing.T) {
	// parseCompactPeers should still parse all peers.
	// Filtering happens at a higher level. This test verifies
	// FilterPrivatePeers works with compact-parsed peers.
	peerData := make([]byte, 12)
	copy(peerData[0:4], net.IPv4(8, 8, 8, 8).To4())
	peerData[4], peerData[5] = 0x1A, 0xE1 // port 6881
	copy(peerData[6:10], net.IPv4(192, 168, 1, 1).To4())
	peerData[10], peerData[11] = 0x1A, 0xE1

	peers, err := parseCompactPeers(string(peerData))
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	filtered := FilterPrivatePeers(peers)
	if len(filtered) != 1 {
		t.Errorf("expected 1 public peer after filtering, got %d", len(filtered))
	}
}

func TestResolveAndValidateTrackerHost(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"public IP", "8.8.8.8", false},
		{"loopback", "127.0.0.1", true},
		{"private 10.x", "10.0.0.1", true},
		{"private 192.168.x", "192.168.1.1", true},
		{"private 172.16.x", "172.16.0.1", true},
		{"link-local", "169.254.1.1", true},
		{"loopback v6", "::1", true},
		{"unspecified", "0.0.0.0", true},
		{"localhost", "localhost", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveAndValidateTrackerHost(tt.host)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for host %q", tt.host)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for host %q: %v", tt.host, err)
			}
		})
	}
}
