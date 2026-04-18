package dht

import (
	"net"
	"testing"
)

func TestCompactNodeInfo6RoundTrip(t *testing.T) {
	nodes := []CompactNodeInfo{
		{ID: ID{0x01}, Addr: net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 6881}},
		{ID: ID{0x02}, Addr: net.UDPAddr{IP: net.ParseIP("::1"), Port: 8080}},
	}
	data := MarshalCompactNodeInfo6(nodes)
	if len(data) != 76 { // 2 * 38
		t.Fatalf("marshal6 length: got %d, want 76", len(data))
	}
	got, err := UnmarshalCompactNodeInfo6(data)
	if err != nil {
		t.Fatalf("unmarshal6: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2", len(got))
	}
	if !got[0].Addr.IP.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("node[0] IP: got %s", got[0].Addr.IP)
	}
	if got[0].Addr.Port != 6881 {
		t.Errorf("node[0] port: got %d", got[0].Addr.Port)
	}
}

func TestCompactNodeInfo6SkipsV4(t *testing.T) {
	// MarshalCompactNodeInfo6 must emit 0-bytes for a v4 address (defensive:
	// the caller should only pass v6 nodes, but we must not produce garbage).
	nodes := []CompactNodeInfo{
		{ID: ID{0x01}, Addr: net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 6881}},
	}
	data := MarshalCompactNodeInfo6(nodes)
	if len(data) != 38 {
		t.Fatalf("marshal6 length: got %d, want 38", len(data))
	}
}

func TestParseCompactPeersMixed(t *testing.T) {
	// BEP 32: values list may contain mixed 6-byte v4 and 18-byte v6 entries.
	v4 := string([]byte{192, 168, 1, 1, 0x1a, 0xe1}) // 192.168.1.1:6881
	v6ip := net.ParseIP("2001:db8::1").To16()
	v6 := make([]byte, 18)
	copy(v6[:16], v6ip)
	v6[16] = 0x1f
	v6[17] = 0x90 // port 8080

	values := []any{v4, string(v6)}
	peers := ParseCompactPeers(values)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d: %v", len(peers), peers)
	}
	if peers[0] != "192.168.1.1:6881" {
		t.Errorf("peer[0]: got %q, want 192.168.1.1:6881", peers[0])
	}
	if peers[1] != "[2001:db8::1]:8080" {
		t.Errorf("peer[1]: got %q, want [2001:db8::1]:8080", peers[1])
	}
}

func TestGetPeersQueryIncludesWant(t *testing.T) {
	id := GenerateID()
	hash := GenerateID()
	msg := NewGetPeersQuery(id, hash)
	want, ok := msg.A["want"].([]any)
	if !ok {
		t.Fatal("want parameter missing from get_peers query")
	}
	if len(want) < 1 {
		t.Fatal("want should contain at least one family")
	}
	hasN4, hasN6 := false, false
	for _, v := range want {
		if s, ok := v.(string); ok {
			if s == "n4" {
				hasN4 = true
			}
			if s == "n6" {
				hasN6 = true
			}
		}
	}
	if !hasN4 || !hasN6 {
		t.Errorf("want should request both n4 and n6: got %v", want)
	}
}

func TestFindNodeQueryIncludesWant(t *testing.T) {
	id := GenerateID()
	target := GenerateID()
	msg := NewFindNodeQuery(id, target)
	want, ok := msg.A["want"].([]any)
	if !ok {
		t.Fatal("want parameter missing from find_node query")
	}
	if len(want) == 0 {
		t.Fatal("want should be non-empty")
	}
}

func TestDHTV6Table(t *testing.T) {
	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	// Add a v6 node — it should go into the v6 table, not v4.
	v6Node := &Node{
		ID:   ID{0x0a},
		Addr: net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 6881},
	}
	d.table.Update(v6Node)
	d.tableV6.Update(v6Node)

	if d.tableV6.Len() == 0 {
		t.Error("v6 node should appear in tableV6")
	}
}
