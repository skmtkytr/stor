package dht

import (
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	// DefaultAlpha is the default concurrency parameter for iterative lookups.
	DefaultAlpha = 8
	// LookupTimeout is the maximum time for a single lookup.
	LookupTimeout = 15 * time.Second
	// QueryTimeout is the timeout for a single KRPC query.
	QueryTimeout = 5 * time.Second
)

// DHT represents a DHT node.
type DHT struct {
	id    ID
	conn  *net.UDPConn
	table *RoutingTable
	alpha int // lookup concurrency

	// Pending transactions: txnID → response channel
	pending sync.Map // string → chan *KRPCMessage

	// Token management for announce_peer verification
	tokenSecret    [20]byte
	tokenSecretOld [20]byte
	tokenMu        sync.Mutex

	closed chan struct{}
}

// Option configures a DHT node.
type Option func(*DHT)

// WithAlpha sets the lookup concurrency parameter.
func WithAlpha(alpha int) Option {
	return func(d *DHT) {
		if alpha > 0 {
			d.alpha = alpha
		}
	}
}

// Alpha returns the current lookup concurrency.
func (d *DHT) Alpha() int {
	return d.alpha
}

// New creates and starts a new DHT node.
func New(listenAddr string, opts ...Option) (*DHT, error) {
	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("dht: resolve addr: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("dht: listen: %w", err)
	}

	// Set read buffer to 2MB for high throughput
	_ = conn.SetReadBuffer(2 * 1024 * 1024)

	id := GenerateID()
	d := &DHT{
		id:     id,
		conn:   conn,
		table:  NewRoutingTable(id),
		alpha:  DefaultAlpha,
		closed: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(d)
	}
	d.rotateToken()

	go d.readLoop()
	go d.tokenRotateLoop()

	return d, nil
}

// ID returns the node's ID.
func (d *DHT) ID() ID {
	return d.id
}

// Addr returns the listening address.
func (d *DHT) Addr() net.Addr {
	return d.conn.LocalAddr()
}

// TableSize returns the number of nodes in the routing table.
func (d *DHT) TableSize() int {
	return d.table.Len()
}

// Close shuts down the DHT node.
func (d *DHT) Close() error {
	close(d.closed)
	return d.conn.Close()
}

// Bootstrap adds initial nodes and performs a self-lookup to populate the routing table.
func (d *DHT) Bootstrap(addrs []string) error {
	for _, a := range addrs {
		addr, err := net.ResolveUDPAddr("udp", a)
		if err != nil {
			continue
		}
		// Send find_node for our own ID
		msg := NewFindNodeQuery(d.id, d.id)
		_ = d.sendQuery(addr, msg)
	}

	// Wait a bit for responses, then do iterative lookup for our own ID
	time.Sleep(500 * time.Millisecond)
	_, _ = d.iterativeLookup(d.id, false)
	return nil
}

// GetPeers finds peers for the given info hash using iterative DHT lookup.
func (d *DHT) GetPeers(infoHash [20]byte) ([]string, error) {
	target := IDFromInfoHash(infoHash)
	peers, err := d.iterativeLookup(target, true)
	if err != nil {
		return nil, err
	}
	return peers, nil
}

// readLoop reads incoming UDP packets and dispatches them.
func (d *DHT) readLoop() {
	buf := make([]byte, 65536)
	for {
		select {
		case <-d.closed:
			return
		default:
		}

		_ = d.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		msg, err := DecodeKRPC(buf[:n])
		if err != nil {
			continue
		}

		switch msg.Y {
		case MsgResponse:
			d.handleResponse(msg)
		case MsgQuery:
			d.handleQuery(msg, addr)
		case MsgError:
			d.handleResponse(msg) // treat errors as responses to unblock waiters
		}
	}
}

// handleResponse routes a response to the waiting goroutine.
func (d *DHT) handleResponse(msg *KRPCMessage) {
	if ch, ok := d.pending.LoadAndDelete(msg.T); ok {
		select {
		case ch.(chan *KRPCMessage) <- msg:
		default:
		}
	}
}

// handleQuery responds to incoming queries.
func (d *DHT) handleQuery(msg *KRPCMessage, addr *net.UDPAddr) {
	// Update routing table with the querying node
	if idStr, ok := msg.A["id"].(string); ok && len(idStr) == 20 {
		d.table.Update(&Node{ID: IDFromBytes([]byte(idStr)), Addr: *addr})
	}

	switch msg.Q {
	case "ping":
		d.respondPing(msg.T, addr)
	case "find_node":
		d.respondFindNode(msg, addr)
	case "get_peers":
		d.respondGetPeers(msg, addr)
	case "announce_peer":
		d.respondAnnouncePeer(msg, addr)
	}
}

func (d *DHT) respondPing(txnID string, addr *net.UDPAddr) {
	resp := NewResponse(txnID, map[string]any{"id": string(d.id[:])})
	data, _ := EncodeKRPC(resp)
	_, _ = d.conn.WriteToUDP(data, addr)
}

func (d *DHT) respondFindNode(msg *KRPCMessage, addr *net.UDPAddr) {
	targetStr, _ := msg.A["target"].(string)
	if len(targetStr) != 20 {
		return
	}
	target := IDFromBytes([]byte(targetStr))
	closest := d.table.Closest(target, K)

	nodes := make([]CompactNodeInfo, len(closest))
	for i, n := range closest {
		nodes[i] = CompactNodeInfo{ID: n.ID, Addr: n.Addr}
	}

	resp := NewResponse(msg.T, map[string]any{
		"id":    string(d.id[:]),
		"nodes": string(MarshalCompactNodeInfo(nodes)),
	})
	data, _ := EncodeKRPC(resp)
	_, _ = d.conn.WriteToUDP(data, addr)
}

func (d *DHT) respondGetPeers(msg *KRPCMessage, addr *net.UDPAddr) {
	// We don't store peers in this implementation, so always return closest nodes
	infoHashStr, _ := msg.A["info_hash"].(string)
	if len(infoHashStr) != 20 {
		return
	}
	target := IDFromBytes([]byte(infoHashStr))
	closest := d.table.Closest(target, K)

	nodes := make([]CompactNodeInfo, len(closest))
	for i, n := range closest {
		nodes[i] = CompactNodeInfo{ID: n.ID, Addr: n.Addr}
	}

	token := d.makeToken(addr)
	resp := NewResponse(msg.T, map[string]any{
		"id":    string(d.id[:]),
		"token": token,
		"nodes": string(MarshalCompactNodeInfo(nodes)),
	})
	data, _ := EncodeKRPC(resp)
	_, _ = d.conn.WriteToUDP(data, addr)
}

func (d *DHT) respondAnnouncePeer(msg *KRPCMessage, addr *net.UDPAddr) {
	// Validate token
	token, _ := msg.A["token"].(string)
	if !d.validateToken(addr, token) {
		return
	}
	resp := NewResponse(msg.T, map[string]any{"id": string(d.id[:])})
	data, _ := EncodeKRPC(resp)
	_, _ = d.conn.WriteToUDP(data, addr)
}

// sendQuery sends a KRPC query and returns a channel for the response.
func (d *DHT) sendQuery(addr *net.UDPAddr, msg *KRPCMessage) chan *KRPCMessage {
	ch := make(chan *KRPCMessage, 1)
	d.pending.Store(msg.T, ch)

	data, err := EncodeKRPC(msg)
	if err != nil {
		d.pending.Delete(msg.T)
		return ch
	}

	_, _ = d.conn.WriteToUDP(data, addr)
	return ch
}

// query sends a KRPC query and waits for a response with timeout.
func (d *DHT) query(addr *net.UDPAddr, msg *KRPCMessage) (*KRPCMessage, error) {
	ch := d.sendQuery(addr, msg)

	select {
	case resp := <-ch:
		if resp == nil {
			return nil, fmt.Errorf("dht: nil response")
		}
		if resp.Y == MsgError {
			return nil, fmt.Errorf("dht: error response: %v", resp.E)
		}
		return resp, nil
	case <-time.After(QueryTimeout):
		d.pending.Delete(msg.T)
		return nil, fmt.Errorf("dht: query timeout")
	case <-d.closed:
		return nil, fmt.Errorf("dht: closed")
	}
}

type nodeEntry struct {
	node    *Node
	queried bool
}

// iterativeLookup performs a Kademlia iterative lookup.
// If getPeers is true, sends get_peers; otherwise find_node.
// Returns discovered peers (for get_peers) or nil.
func (d *DHT) iterativeLookup(target ID, getPeers bool) ([]string, error) {
	// Seed with closest known nodes
	closest := d.table.Closest(target, K)
	if len(closest) == 0 {
		return nil, fmt.Errorf("dht: no nodes in routing table")
	}

	// Contacted set: dedup by ID
	seen := make(map[ID]*nodeEntry)
	var mu sync.Mutex
	var allPeers []string

	for _, n := range closest {
		seen[n.ID] = &nodeEntry{node: n}
	}

	deadline := time.Now().Add(LookupTimeout)

	for round := 0; round < 10; round++ {
		if time.Now().After(deadline) {
			break
		}

		// Pick up to Alpha unqueried closest nodes
		mu.Lock()
		var toQuery []*Node
		entries := make([]*nodeEntry, 0, len(seen))
		for _, e := range seen {
			entries = append(entries, e)
		}
		// Sort by distance
		sortEntries(entries, target)
		for _, e := range entries {
			if !e.queried && len(toQuery) < d.alpha {
				toQuery = append(toQuery, e.node)
				e.queried = true
			}
		}
		mu.Unlock()

		if len(toQuery) == 0 {
			break
		}

		// Query in parallel
		var wg sync.WaitGroup
		for _, n := range toQuery {
			wg.Add(1)
			go func(n *Node) {
				defer wg.Done()

				var msg *KRPCMessage
				if getPeers {
					msg = NewGetPeersQuery(d.id, target)
				} else {
					msg = NewFindNodeQuery(d.id, target)
				}

				resp, err := d.query(&n.Addr, msg)
				if err != nil {
					return
				}
				if resp.R == nil {
					return
				}

				// Update routing table
				if idStr, ok := resp.R["id"].(string); ok && len(idStr) == 20 {
					d.table.Update(&Node{ID: IDFromBytes([]byte(idStr)), Addr: n.Addr})
				}

				// Collect peers (get_peers only)
				if getPeers {
					if values, ok := resp.R["values"].([]any); ok {
						p := ParseCompactPeers(values)
						mu.Lock()
						allPeers = append(allPeers, p...)
						mu.Unlock()
					}
				}

				// Parse returned nodes and add to seen
				if nodesStr, ok := resp.R["nodes"].(string); ok {
					newNodes, err := UnmarshalCompactNodeInfo([]byte(nodesStr))
					if err == nil {
						mu.Lock()
						for _, ni := range newNodes {
							if _, exists := seen[ni.ID]; !exists {
								node := &Node{ID: ni.ID, Addr: ni.Addr}
								seen[ni.ID] = &nodeEntry{node: node}
								d.table.Update(node)
							}
						}
						mu.Unlock()
					}
				}
			}(n)
		}
		wg.Wait()

		// If we found peers, we can return early
		mu.Lock()
		peerCount := len(allPeers)
		mu.Unlock()
		if getPeers && peerCount > 0 {
			break
		}
	}

	return allPeers, nil
}

func sortEntries(entries []*nodeEntry, target ID) {
	for i := 1; i < len(entries); i++ {
		key := entries[i]
		keyDist := Distance(key.node.ID, target)
		j := i - 1
		for j >= 0 && Less(keyDist, Distance(entries[j].node.ID, target)) {
			entries[j+1] = entries[j]
			j--
		}
		entries[j+1] = key
	}
}

// Token management: SHA1(IP + secret)
func (d *DHT) makeToken(addr *net.UDPAddr) string {
	d.tokenMu.Lock()
	secret := d.tokenSecret
	d.tokenMu.Unlock()

	h := sha1.New()
	h.Write(addr.IP)
	h.Write(secret[:])
	return string(h.Sum(nil)[:8])
}

func (d *DHT) validateToken(addr *net.UDPAddr, token string) bool {
	d.tokenMu.Lock()
	secrets := [2][20]byte{d.tokenSecret, d.tokenSecretOld}
	d.tokenMu.Unlock()

	for _, secret := range secrets {
		h := sha1.New()
		h.Write(addr.IP)
		h.Write(secret[:])
		if string(h.Sum(nil)[:8]) == token {
			return true
		}
	}
	return false
}

// tokenRotateLoop rotates the token secret every 5 minutes (BEP 5).
func (d *DHT) tokenRotateLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.rotateToken()
		case <-d.closed:
			return
		}
	}
}

func (d *DHT) rotateToken() {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	d.tokenSecretOld = d.tokenSecret
	var secret [20]byte
	_, _ = rand.Read(secret[:])
	d.tokenSecret = secret
}
