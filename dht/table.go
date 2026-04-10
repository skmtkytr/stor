package dht

import (
	"net"
	"sync"
	"time"
)

const (
	// K is the maximum number of nodes per bucket.
	K = 8
	// NumBuckets is the number of buckets (160-bit ID space).
	NumBuckets = 160
)

// Node represents a DHT node in the routing table.
type Node struct {
	ID       ID
	Addr     net.UDPAddr
	LastSeen time.Time
}

// RoutingTable implements a Kademlia routing table with K-buckets.
type RoutingTable struct {
	self    ID
	buckets [NumBuckets]bucket
}

type bucket struct {
	mu    sync.RWMutex
	nodes []*Node
}

// NewRoutingTable creates a new routing table for the given node ID.
func NewRoutingTable(self ID) *RoutingTable {
	return &RoutingTable{self: self}
}

// SelfID returns the local node ID.
func (rt *RoutingTable) SelfID() ID {
	return rt.self
}

// bucketIndex returns which bucket a node ID falls into.
func (rt *RoutingTable) bucketIndex(id ID) int {
	d := Distance(rt.self, id)
	idx := d.ZeroPrefixLen()
	if idx >= NumBuckets {
		idx = NumBuckets - 1
	}
	return idx
}

// Update adds or refreshes a node in the routing table.
// If the bucket is full and the node is new, it is silently dropped (simplified).
func (rt *RoutingTable) Update(n *Node) {
	if n.ID == rt.self {
		return
	}
	idx := rt.bucketIndex(n.ID)
	b := &rt.buckets[idx]

	b.mu.Lock()
	defer b.mu.Unlock()

	// Check if already in bucket — move to tail (most recently seen)
	for i, existing := range b.nodes {
		if existing.ID == n.ID {
			existing.Addr = n.Addr
			existing.LastSeen = time.Now()
			// Move to tail
			b.nodes = append(append(b.nodes[:i], b.nodes[i+1:]...), existing)
			return
		}
	}

	// New node — add if bucket not full
	if len(b.nodes) < K {
		n.LastSeen = time.Now()
		b.nodes = append(b.nodes, n)
	}
	// Otherwise drop (full BEP 5 would ping the head and evict if dead)
}

// Closest returns the k closest nodes to the target ID.
func (rt *RoutingTable) Closest(target ID, k int) []*Node {
	var all []*Node
	for i := range NumBuckets {
		b := &rt.buckets[i]
		b.mu.RLock()
		all = append(all, b.nodes...)
		b.mu.RUnlock()
	}

	// Sort by XOR distance to target
	sortByDistance(all, target)

	if len(all) > k {
		all = all[:k]
	}
	return all
}

// Len returns the total number of nodes in the routing table.
func (rt *RoutingTable) Len() int {
	total := 0
	for i := range NumBuckets {
		b := &rt.buckets[i]
		b.mu.RLock()
		total += len(b.nodes)
		b.mu.RUnlock()
	}
	return total
}

// sortByDistance sorts nodes by XOR distance to target (insertion sort for small slices).
func sortByDistance(nodes []*Node, target ID) {
	for i := 1; i < len(nodes); i++ {
		key := nodes[i]
		keyDist := Distance(key.ID, target)
		j := i - 1
		for j >= 0 && Less(keyDist, Distance(nodes[j].ID, target)) {
			nodes[j+1] = nodes[j]
			j--
		}
		nodes[j+1] = key
	}
}
