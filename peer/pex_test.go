package peer

import (
	"net"
	"testing"

	"github.com/skmtkytr/stor/bencode"
)

func TestPEXRoundTrip(t *testing.T) {
	msg := &PEXMessage{
		Added: []PEXPeer{
			{IP: net.IPv4(1, 2, 3, 4), Port: 6881, Seed: false},
			{IP: net.IPv4(10, 0, 0, 1), Port: 51413, Seed: true},
		},
		Dropped: []PEXPeer{
			{IP: net.IPv4(192, 168, 1, 1), Port: 8080},
		},
	}

	data, err := EncodePEX(msg)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodePEX(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Added) != 2 {
		t.Fatalf("expected 2 added, got %d", len(got.Added))
	}
	if !got.Added[0].IP.Equal(net.IPv4(1, 2, 3, 4)) || got.Added[0].Port != 6881 {
		t.Errorf("added[0] = %s:%d", got.Added[0].IP, got.Added[0].Port)
	}
	if got.Added[0].Seed {
		t.Error("added[0] should not be seed")
	}
	if !got.Added[1].IP.Equal(net.IPv4(10, 0, 0, 1)) || got.Added[1].Port != 51413 {
		t.Errorf("added[1] = %s:%d", got.Added[1].IP, got.Added[1].Port)
	}
	if !got.Added[1].Seed {
		t.Error("added[1] should be seed")
	}

	if len(got.Dropped) != 1 {
		t.Fatalf("expected 1 dropped, got %d", len(got.Dropped))
	}
	if !got.Dropped[0].IP.Equal(net.IPv4(192, 168, 1, 1)) || got.Dropped[0].Port != 8080 {
		t.Errorf("dropped[0] = %s:%d", got.Dropped[0].IP, got.Dropped[0].Port)
	}
}

func TestPEXTooManyPeers(t *testing.T) {
	// Build a PEX "added" field with 20000 peers (exceeds limit)
	n := 20000
	compact := make([]byte, n*6)
	for i := range n {
		compact[i*6] = byte(i >> 8)
		compact[i*6+1] = byte(i)
		compact[i*6+2] = 1
		compact[i*6+3] = 1
		compact[i*6+4] = 0x1A
		compact[i*6+5] = 0xE1
	}
	d := map[string]any{"added": string(compact)}
	data, _ := bencode.Encode(d)

	_, err := DecodePEX(data)
	if err == nil {
		t.Fatal("expected error for too many PEX peers")
	}
}

func TestPEXEmpty(t *testing.T) {
	msg := &PEXMessage{}
	data, err := EncodePEX(msg)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodePEX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Added) != 0 || len(got.Dropped) != 0 {
		t.Error("expected empty PEX message")
	}
}
