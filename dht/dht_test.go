package dht

import (
	"net"
	"testing"
	"time"
)

func TestIDDistance(t *testing.T) {
	a := ID{0xFF}
	b := ID{0x0F}
	d := Distance(a, b)
	if d[0] != 0xF0 {
		t.Errorf("distance[0]: got %x, want F0", d[0])
	}
}

func TestIDZeroPrefixLen(t *testing.T) {
	tests := []struct {
		id   ID
		want int
	}{
		{ID{0xFF}, 0},
		{ID{0x0F}, 4},
		{ID{0x01}, 7},
		{ID{0x00, 0x80}, 8},
		{ID{0x00, 0x01}, 15},
		{ID{}, 160},
	}
	for _, tt := range tests {
		got := tt.id.ZeroPrefixLen()
		if got != tt.want {
			t.Errorf("ZeroPrefixLen(%x): got %d, want %d", tt.id[:3], got, tt.want)
		}
	}
}

func TestIDLess(t *testing.T) {
	a := ID{0x01}
	b := ID{0x02}
	if !Less(a, b) {
		t.Error("expected a < b")
	}
	if Less(b, a) {
		t.Error("expected b >= a")
	}
	if Less(a, a) {
		t.Error("expected a not < a")
	}
}

func TestCompactNodeInfoRoundTrip(t *testing.T) {
	nodes := []CompactNodeInfo{
		{ID: ID{0x01}, Addr: net.UDPAddr{IP: net.IPv4(192, 168, 1, 1), Port: 6881}},
		{ID: ID{0x02}, Addr: net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 8080}},
	}

	data := MarshalCompactNodeInfo(nodes)
	if len(data) != 52 {
		t.Fatalf("marshalled length: got %d, want 52", len(data))
	}

	got, err := UnmarshalCompactNodeInfo(data)
	if err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2", len(got))
	}
	if got[0].Addr.Port != 6881 {
		t.Errorf("node[0] port: got %d", got[0].Addr.Port)
	}
	if got[1].Addr.IP.String() != "10.0.0.1" {
		t.Errorf("node[1] IP: got %s", got[1].Addr.IP)
	}
}

func TestKRPCRoundTrip(t *testing.T) {
	id := GenerateID()
	msg := NewPingQuery(id)

	data, err := EncodeKRPC(msg)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}

	got, err := DecodeKRPC(data)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if got.Y != MsgQuery {
		t.Errorf("Y: got %q, want %q", got.Y, MsgQuery)
	}
	if got.Q != "ping" {
		t.Errorf("Q: got %q, want %q", got.Q, "ping")
	}
	if got.T != msg.T {
		t.Errorf("T mismatch")
	}
}

func TestRoutingTableUpdateAndClosest(t *testing.T) {
	self := ID{0x00}
	rt := NewRoutingTable(self)

	// Add some nodes
	for i := range 20 {
		id := ID{byte(i + 1)}
		rt.Update(&Node{ID: id, Addr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 6000 + i}})
	}

	if rt.Len() != 20 {
		t.Errorf("table size: got %d, want 20", rt.Len())
	}

	// Closest to ID{0x01} should be ID{0x01}
	target := ID{0x01}
	closest := rt.Closest(target, 3)
	if len(closest) != 3 {
		t.Fatalf("closest: got %d, want 3", len(closest))
	}
	if closest[0].ID != target {
		t.Errorf("closest[0]: got %x, want %x", closest[0].ID[:1], target[:1])
	}
}

func TestRoutingTableSelfNotAdded(t *testing.T) {
	self := ID{0x42}
	rt := NewRoutingTable(self)
	rt.Update(&Node{ID: self, Addr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 6881}})
	if rt.Len() != 0 {
		t.Error("self should not be added to routing table")
	}
}

func TestDHTPingPong(t *testing.T) {
	// Start two DHT nodes
	d1, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatalf("d1 create: %v", err)
	}
	defer func() { _ = d1.Close() }()

	d2, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatalf("d2 create: %v", err)
	}
	defer func() { _ = d2.Close() }()

	// d1 pings d2
	d2Addr := d2.Addr().(*net.UDPAddr)
	msg := NewPingQuery(d1.id)
	resp, err := d1.query(d2Addr, msg)
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}

	respID, ok := resp.R["id"].(string)
	if !ok || len(respID) != 20 {
		t.Fatal("missing id in ping response")
	}
	if IDFromBytes([]byte(respID)) != d2.id {
		t.Error("response ID doesn't match d2")
	}
}

func TestDHTFindNode(t *testing.T) {
	d1, _ := New("127.0.0.1:0")
	defer func() { _ = d1.Close() }()
	d2, _ := New("127.0.0.1:0")
	defer func() { _ = d2.Close() }()
	d3, _ := New("127.0.0.1:0")
	defer func() { _ = d3.Close() }()

	// d2 knows about d3
	d3Addr := d3.Addr().(*net.UDPAddr)
	d2.table.Update(&Node{ID: d3.id, Addr: *d3Addr})

	// d1 asks d2 for find_node(d3.id)
	d2Addr := d2.Addr().(*net.UDPAddr)
	msg := NewFindNodeQuery(d1.id, d3.id)
	resp, err := d1.query(d2Addr, msg)
	if err != nil {
		t.Fatalf("find_node failed: %v", err)
	}

	nodesStr, ok := resp.R["nodes"].(string)
	if !ok {
		t.Fatal("missing nodes in response")
	}
	nodes, err := UnmarshalCompactNodeInfo([]byte(nodesStr))
	if err != nil {
		t.Fatalf("unmarshal nodes: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected at least 1 node in response")
	}

	found := false
	for _, n := range nodes {
		if n.ID == d3.id {
			found = true
			break
		}
	}
	if !found {
		t.Error("d3 should be in find_node response from d2")
	}
}

func TestDHTBootstrapAndLookup(t *testing.T) {
	// Create a small network of 5 nodes
	nodes := make([]*DHT, 5)
	for i := range nodes {
		d, err := New("127.0.0.1:0")
		if err != nil {
			t.Fatalf("create node %d: %v", i, err)
		}
		nodes[i] = d
	}
	t.Cleanup(func() {
		for _, d := range nodes {
			_ = d.Close()
		}
	})

	// Chain: each node knows the next
	for i := 0; i < len(nodes)-1; i++ {
		nextAddr := nodes[i+1].Addr().(*net.UDPAddr)
		nodes[i].table.Update(&Node{ID: nodes[i+1].id, Addr: *nextAddr})
	}
	// Last knows first (loop)
	firstAddr := nodes[0].Addr().(*net.UDPAddr)
	nodes[len(nodes)-1].table.Update(&Node{ID: nodes[0].id, Addr: *firstAddr})

	// Node 0 does a lookup for node 4's ID
	// Should traverse the chain and find node 4
	time.Sleep(100 * time.Millisecond)
	_, err := nodes[0].iterativeLookup(nodes[4].id, false)
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}

	// After lookup, node 0 should know more nodes
	if nodes[0].table.Len() < 3 {
		t.Errorf("node 0 should know at least 3 nodes after lookup, got %d", nodes[0].table.Len())
	}
}

func TestDHTBootstrapPopulatesTable(t *testing.T) {
	// Create 3 "bootstrap" nodes that know each other
	b1, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b1.Close() }()

	b2, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b2.Close() }()

	b3, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b3.Close() }()

	// Bootstrap nodes know each other
	b1Addr := b1.Addr().(*net.UDPAddr)
	b2Addr := b2.Addr().(*net.UDPAddr)
	b3Addr := b3.Addr().(*net.UDPAddr)
	b1.table.Update(&Node{ID: b2.id, Addr: *b2Addr})
	b1.table.Update(&Node{ID: b3.id, Addr: *b3Addr})
	b2.table.Update(&Node{ID: b1.id, Addr: *b1Addr})
	b2.table.Update(&Node{ID: b3.id, Addr: *b3Addr})
	b3.table.Update(&Node{ID: b1.id, Addr: *b1Addr})
	b3.table.Update(&Node{ID: b2.id, Addr: *b2Addr})

	// Create a new node that bootstraps from b1 and b2
	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	if d.TableSize() != 0 {
		t.Fatalf("new node should start with empty table, got %d", d.TableSize())
	}

	err = d.Bootstrap([]string{b1Addr.String(), b2Addr.String()})
	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	// After bootstrap, the node should have discovered nodes
	if d.TableSize() == 0 {
		t.Error("routing table should not be empty after bootstrap")
	}
	if d.TableSize() < 2 {
		t.Errorf("expected at least 2 nodes in table after bootstrap, got %d", d.TableSize())
	}
}

func TestHandleResponseInvalidPendingType(t *testing.T) {
	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	// Store a non-channel value in pending — should not panic
	d.pending.Store("bad-txn", "not-a-channel")
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleResponse panicked on invalid pending type: %v", r)
		}
	}()
	d.handleResponse(&KRPCMessage{T: "bad-txn", Y: MsgResponse})
}

func TestTokenRotation(t *testing.T) {
	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = d.Close() }()

	addr := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 1), Port: 6881}
	token1 := d.makeToken(addr)

	// Force rotation
	d.rotateToken()
	token2 := d.makeToken(addr)

	if token1 == token2 {
		t.Error("token should change after rotation")
	}

	// Old token should still be valid (tokenSecretOld)
	if !d.validateToken(addr, token1) {
		t.Error("old token should still be valid after one rotation")
	}

	// Rotate again — token1 should now be invalid
	d.rotateToken()
	if d.validateToken(addr, token1) {
		t.Error("token should be invalid after two rotations")
	}
}

func TestTokenValidation(t *testing.T) {
	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = d.Close() }()

	addr := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 1), Port: 6881}
	token := d.makeToken(addr)

	if !d.validateToken(addr, token) {
		t.Error("token should be valid")
	}

	otherAddr := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 6881}
	if d.validateToken(otherAddr, token) {
		t.Error("token should not be valid for different IP")
	}
}

func TestMaxLookupNodesConstant(t *testing.T) {
	// Verify MaxLookupNodes and MaxLookupPeers are defined and reasonable.
	if MaxLookupNodes < 100 {
		t.Errorf("MaxLookupNodes too small: %d", MaxLookupNodes)
	}
	if MaxLookupNodes > 100000 {
		t.Errorf("MaxLookupNodes too large: %d", MaxLookupNodes)
	}
	if MaxLookupPeers < 100 {
		t.Errorf("MaxLookupPeers too small: %d", MaxLookupPeers)
	}
	if MaxLookupPeers > 100000 {
		t.Errorf("MaxLookupPeers too large: %d", MaxLookupPeers)
	}
}
