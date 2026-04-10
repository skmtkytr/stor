package tracker

import (
	"encoding/binary"
	"fmt"
	"net"
	"testing"
)

// fakeUDPTracker creates a UDP server that responds to connect + announce.
func fakeUDPTracker(t *testing.T, peers []Peer) *net.UDPConn {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	go func() {
		buf := make([]byte, 4096)
		var connID uint64 = 0xDEADBEEF12345678

		for {
			n, remoteAddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}

			if n < 16 {
				continue
			}

			action := binary.BigEndian.Uint32(buf[8:12])
			txnID := binary.BigEndian.Uint32(buf[12:16])

			switch action {
			case actionConnect:
				// Verify magic
				magic := binary.BigEndian.Uint64(buf[0:8])
				if magic != udpConnectMagic {
					continue
				}

				resp := make([]byte, 16)
				binary.BigEndian.PutUint32(resp[0:4], actionConnect)
				binary.BigEndian.PutUint32(resp[4:8], txnID)
				binary.BigEndian.PutUint64(resp[8:16], connID)
				_, _ = conn.WriteToUDP(resp, remoteAddr)

			case actionAnnounce:
				if n < 98 {
					continue
				}
				// Verify connection ID
				reqConnID := binary.BigEndian.Uint64(buf[0:8])
				if reqConnID != connID {
					continue
				}

				// Build response
				resp := make([]byte, 20+6*len(peers))
				binary.BigEndian.PutUint32(resp[0:4], actionAnnounce)
				binary.BigEndian.PutUint32(resp[4:8], txnID)
				binary.BigEndian.PutUint32(resp[8:12], 1800)                // interval
				binary.BigEndian.PutUint32(resp[12:16], 0)                  // leechers
				binary.BigEndian.PutUint32(resp[16:20], uint32(len(peers))) // seeders

				for i, p := range peers {
					offset := 20 + i*6
					copy(resp[offset:offset+4], p.IP.To4())
					binary.BigEndian.PutUint16(resp[offset+4:offset+6], p.Port)
				}
				_, _ = conn.WriteToUDP(resp, remoteAddr)
			}
		}
	}()

	return conn
}

func TestUDPAnnounce(t *testing.T) {
	expectedPeers := []Peer{
		{IP: net.IPv4(192, 168, 1, 1), Port: 6881},
		{IP: net.IPv4(10, 0, 0, 1), Port: 8080},
	}

	server := fakeUDPTracker(t, expectedPeers)
	defer func() { _ = server.Close() }()

	addr := server.LocalAddr().(*net.UDPAddr)
	announceURL := fmt.Sprintf("udp://127.0.0.1:%d/announce", addr.Port)

	req := AnnounceRequest{
		AnnounceURL: announceURL,
		InfoHash:    [20]byte{0x01},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        1000,
		Event:       EventStarted,
	}

	resp, err := AnnounceUDP(req)
	if err != nil {
		t.Fatalf("AnnounceUDP failed: %v", err)
	}

	if resp.Interval != 1800 {
		t.Errorf("interval: got %d, want 1800", resp.Interval)
	}
	if len(resp.Peers) != 2 {
		t.Fatalf("peers: got %d, want 2", len(resp.Peers))
	}
	if resp.Peers[0].IP.String() != "192.168.1.1" {
		t.Errorf("peer[0] IP: got %s", resp.Peers[0].IP)
	}
	if resp.Peers[0].Port != 6881 {
		t.Errorf("peer[0] port: got %d", resp.Peers[0].Port)
	}
	if resp.Peers[1].IP.String() != "10.0.0.1" {
		t.Errorf("peer[1] IP: got %s", resp.Peers[1].IP)
	}
}

func TestAnnounceAutoDetectUDP(t *testing.T) {
	expectedPeers := []Peer{
		{IP: net.IPv4(127, 0, 0, 1), Port: 9999},
	}

	server := fakeUDPTracker(t, expectedPeers)
	defer func() { _ = server.Close() }()

	addr := server.LocalAddr().(*net.UDPAddr)
	announceURL := fmt.Sprintf("udp://127.0.0.1:%d/announce", addr.Port)

	req := AnnounceRequest{
		AnnounceURL: announceURL,
		InfoHash:    [20]byte{0x02},
		PeerID:      [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Port:        6881,
		Left:        500,
	}

	// Use Announce (auto-detect) instead of AnnounceUDP directly
	resp, err := Announce(req)
	if err != nil {
		t.Fatalf("Announce (auto-detect UDP) failed: %v", err)
	}

	if len(resp.Peers) != 1 {
		t.Fatalf("peers: got %d, want 1", len(resp.Peers))
	}
	if resp.Peers[0].Port != 9999 {
		t.Errorf("peer port: got %d", resp.Peers[0].Port)
	}
}
