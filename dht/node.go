package dht

import (
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"log/slog"
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
	// MaxLookupNodes is the maximum number of nodes tracked in a single lookup.
	MaxLookupNodes = 1000
	// MaxLookupPeers is the maximum number of peers collected in a single lookup.
	MaxLookupPeers = 5000
)

// DHT represents a DHT node.
type DHT struct {
	id    ID
	conn  *net.UDPConn
	table *RoutingTable
	alpha int // lookup concurrency

	// Pending transactions: txnID → pendingEntry
	pending sync.Map // string → *pendingEntry

	// Token management for announce_peer verification
	tokenSecret    [20]byte
	tokenSecretOld [20]byte
	tokenMu        sync.Mutex

	closed    chan struct{}
	closeOnce sync.Once
}

// pendingEntry tracks a pending KRPC transaction.
type pendingEntry struct {
	ch      chan *KRPCMessage
	created time.Time
}

// PendingCleanupInterval is how often stale pending transactions are cleaned.
const PendingCleanupInterval = 30 * time.Second

// PendingTTL is how long a pending transaction is kept before cleanup.
const PendingTTL = 15 * time.Second

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
	go d.pendingCleanupLoop()

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

// Close shuts down the DHT node. It is safe to call multiple times.
func (d *DHT) Close() error {
	var err error
	d.closeOnce.Do(func() {
		close(d.closed)
		err = d.conn.Close()
	})
	return err
}

// Bootstrap adds initial nodes and performs a self-lookup to populate the routing table.
func (d *DHT) Bootstrap(addrs []string) error {
	type bootstrapReq struct {
		ch   chan *KRPCMessage
		addr *net.UDPAddr
	}

	var reqs []bootstrapReq
	for _, a := range addrs {
		addr, err := net.ResolveUDPAddr("udp", a)
		if err != nil {
			continue
		}
		msg := NewFindNodeQuery(d.id, d.id)
		ch := d.sendQuery(addr, msg)
		reqs = append(reqs, bootstrapReq{ch: ch, addr: addr})
	}

	// Collect responses and populate routing table
	for _, req := range reqs {
		select {
		case resp := <-req.ch:
			if resp == nil || resp.R == nil {
				continue
			}
			// Add the bootstrap node itself
			if idStr, ok := resp.R["id"].(string); ok && len(idStr) == 20 {
				d.table.Update(&Node{ID: IDFromBytes([]byte(idStr)), Addr: *req.addr})
			}
			// Add returned nodes
			if nodesStr, ok := resp.R["nodes"].(string); ok {
				nodes, err := UnmarshalCompactNodeInfo([]byte(nodesStr))
				if err == nil {
					for _, ni := range nodes {
						d.table.Update(&Node{ID: ni.ID, Addr: ni.Addr})
					}
				}
			}
		case <-time.After(QueryTimeout):
			// skip unresponsive bootstrap node
		case <-d.closed:
			return nil
		}
	}

	slog.Debug("dht: bootstrap initial table", "size", d.table.Len())

	// Iterative self-lookup to further populate the routing table
	_, _ = d.iterativeLookup(d.id, false)
	return nil
}

// GetPeers finds peers for the given info hash using iterative DHT lookup.
// Private/loopback/link-local addresses are filtered from results.
func (d *DHT) GetPeers(infoHash [20]byte) ([]string, error) {
	target := IDFromInfoHash(infoHash)
	peers, err := d.iterativeLookup(target, true)
	if err != nil {
		return nil, err
	}
	return FilterPrivatePeers(peers), nil
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
	if val, ok := d.pending.LoadAndDelete(msg.T); ok {
		if entry, ok := val.(*pendingEntry); ok {
			select {
			case entry.ch <- msg:
			default:
			}
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

func (d *DHT) sendUDP(data []byte, addr *net.UDPAddr) {
	if _, err := d.conn.WriteToUDP(data, addr); err != nil {
		slog.Debug("dht: UDP write failed", "addr", addr, "error", err)
	}
}

func (d *DHT) respondPing(txnID string, addr *net.UDPAddr) {
	resp := NewResponse(txnID, map[string]any{"id": string(d.id[:])})
	data, _ := EncodeKRPC(resp)
	d.sendUDP(data, addr)
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
	d.sendUDP(data, addr)
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
	d.sendUDP(data, addr)
}

func (d *DHT) respondAnnouncePeer(msg *KRPCMessage, addr *net.UDPAddr) {
	// Validate token
	token, _ := msg.A["token"].(string)
	if !d.validateToken(addr, token) {
		return
	}
	resp := NewResponse(msg.T, map[string]any{"id": string(d.id[:])})
	data, _ := EncodeKRPC(resp)
	d.sendUDP(data, addr)
}

// sendQuery sends a KRPC query and returns a channel for the response.
func (d *DHT) sendQuery(addr *net.UDPAddr, msg *KRPCMessage) chan *KRPCMessage {
	ch := make(chan *KRPCMessage, 1)
	d.pending.Store(msg.T, &pendingEntry{ch: ch, created: time.Now()})

	data, err := EncodeKRPC(msg)
	if err != nil {
		d.pending.Delete(msg.T)
		return ch
	}

	if _, err := d.conn.WriteToUDP(data, addr); err != nil {
		slog.Debug("dht: UDP write failed", "addr", addr, "error", err)
	}
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
		select {
		case <-d.closed:
			return allPeers, nil
		default:
		}
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
						space := MaxLookupPeers - len(allPeers)
						if space > 0 {
							if len(p) > space {
								p = p[:space]
							}
							allPeers = append(allPeers, p...)
						}
						mu.Unlock()
					}
				}

				// Parse returned nodes and add to seen
				if nodesStr, ok := resp.R["nodes"].(string); ok {
					newNodes, err := UnmarshalCompactNodeInfo([]byte(nodesStr))
					if err == nil {
						mu.Lock()
						for _, ni := range newNodes {
							if len(seen) >= MaxLookupNodes {
								break
							}
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

// pendingCleanupLoop periodically removes stale pending transactions.
func (d *DHT) pendingCleanupLoop() {
	ticker := time.NewTicker(PendingCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.cleanStalePending(PendingTTL)
		case <-d.closed:
			return
		}
	}
}

// cleanStalePending removes pending transactions older than ttl.
func (d *DHT) cleanStalePending(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	d.pending.Range(func(key, value any) bool {
		if entry, ok := value.(*pendingEntry); ok {
			if entry.created.Before(cutoff) {
				d.pending.Delete(key)
			}
		} else {
			// Remove invalid entries
			d.pending.Delete(key)
		}
		return true
	})
}

// FilterPrivatePeers removes peers with private/loopback/link-local addresses.
func FilterPrivatePeers(peers []string) []string {
	result := make([]string, 0, len(peers))
	for _, p := range peers {
		host, _, err := net.SplitHostPort(p)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			continue
		}
		result = append(result, p)
	}
	return result
}
