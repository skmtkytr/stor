package download

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/skmtkytr/stor/events"
)

const (
	// DefaultUnchokeSlots is the number of fastest peers to unchoke.
	DefaultUnchokeSlots = 8
	// RechokeInterval is how often to re-evaluate peer speeds.
	RechokeInterval = 10 * time.Second
	// OptimisticInterval is how often to rotate the optimistic unchoke slot.
	OptimisticInterval = 30 * time.Second
)

// PeerManager implements the BEP 3 §5 choking algorithm.
// It tracks all connected peers, periodically ranks them by download speed,
// unchokes the fastest N, and rotates one optimistic unchoke slot.
type PeerManager struct {
	mu             sync.Mutex
	peers          []*Client
	unchokeSlots   int
	optimisticPeer *Client

	// Observation hooks. Both fields are public and may be set after
	// construction (the engine layer injects them via OnPeerMgr to keep
	// this package free of an engine-side dependency). Bus may stay nil.
	Bus       *events.Bus
	TorrentID string
}

// NewPeerManager creates a new peer manager.
func NewPeerManager(unchokeSlots int) *PeerManager {
	if unchokeSlots <= 0 {
		unchokeSlots = DefaultUnchokeSlots
	}
	return &PeerManager{
		unchokeSlots: unchokeSlots,
	}
}

// Register adds a peer to management. Called when a peer connects.
func (pm *PeerManager) Register(c *Client) {
	pm.mu.Lock()
	pm.peers = append(pm.peers, c)
	bus := pm.Bus
	id := pm.TorrentID
	pm.mu.Unlock()

	if bus != nil {
		bus.Publish(events.Event{
			Type:      events.TypePeerConnected,
			TorrentID: id,
			Payload: events.PeerConnectedPayload{
				Addr:      c.Addr,
				Direction: peerDirection(c),
				Transport: peerTransport(c),
			},
		})
	}
}

// Unregister removes a peer from management. Called when a peer disconnects.
func (pm *PeerManager) Unregister(c *Client) {
	pm.mu.Lock()
	removed := false
	for i, p := range pm.peers {
		if p == c {
			pm.peers = append(pm.peers[:i], pm.peers[i+1:]...)
			if pm.optimisticPeer == c {
				pm.optimisticPeer = nil
			}
			removed = true
			break
		}
	}
	bus := pm.Bus
	id := pm.TorrentID
	pm.mu.Unlock()

	if removed && bus != nil {
		bus.Publish(events.Event{
			Type:      events.TypePeerDisconnected,
			TorrentID: id,
			Payload: events.PeerDisconnectedPayload{
				Addr: c.Addr,
			},
		})
	}
}

// peerDirection returns "in" / "out" / "" for the given client.
func peerDirection(c *Client) string {
	if c == nil {
		return ""
	}
	if c.Incoming {
		return "in"
	}
	return "out"
}

// peerTransport returns "utp" / "tcp" / "" for the given client.
func peerTransport(c *Client) string {
	if c == nil {
		return ""
	}
	if c.UsingUTP {
		return "utp"
	}
	return "tcp"
}

// Run starts the choking algorithm loop. Blocks until ctx is cancelled.
func (pm *PeerManager) Run(ctx context.Context) {
	rechokeTicker := time.NewTicker(RechokeInterval)
	optimisticTicker := time.NewTicker(OptimisticInterval)
	defer rechokeTicker.Stop()
	defer optimisticTicker.Stop()

	optimisticIdx := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-rechokeTicker.C:
			pm.rechoke()
		case <-optimisticTicker.C:
			pm.mu.Lock()
			optimisticIdx = pm.rotateOptimistic(optimisticIdx)
			pm.mu.Unlock()
		}
	}
}

// rechoke re-evaluates all peers and unchokes the fastest ones.
func (pm *PeerManager) rechoke() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if len(pm.peers) == 0 {
		return
	}

	// Snapshot speeds and reset measurement windows
	type peerSpeed struct {
		client *Client
		speed  float64
	}
	ranked := make([]peerSpeed, 0, len(pm.peers))
	for _, c := range pm.peers {
		ranked = append(ranked, peerSpeed{client: c, speed: c.Speed()})
		c.ResetSpeed()
	}

	// Sort by speed descending
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].speed > ranked[j].speed
	})

	// Unchoke top N, choke the rest (but preserve optimistic unchoke)
	for i, ps := range ranked {
		if i < pm.unchokeSlots || ps.client == pm.optimisticPeer {
			_ = ps.client.SendUnchoke()
		} else {
			_ = ps.client.SendChoke()
		}
	}
}

// rotateOptimistic picks the next choked peer for optimistic unchoke.
// Returns the updated index for round-robin.
func (pm *PeerManager) rotateOptimistic(startIdx int) int {
	if len(pm.peers) == 0 {
		return 0
	}

	// Find next choked peer (round-robin)
	for i := 0; i < len(pm.peers); i++ {
		idx := (startIdx + i) % len(pm.peers)
		c := pm.peers[idx]
		if c.IsChoking() {
			// Unchoke the old optimistic peer (will be handled by rechoke)
			pm.optimisticPeer = c
			_ = c.SendUnchoke()
			return (idx + 1) % len(pm.peers)
		}
	}

	return startIdx
}

// PeerCount returns the number of registered peers.
func (pm *PeerManager) PeerCount() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return len(pm.peers)
}

// BroadcastHave sends MsgHave to all connected peers for a completed piece.
func (pm *PeerManager) BroadcastHave(index int) {
	pm.mu.Lock()
	peers := make([]*Client, len(pm.peers))
	copy(peers, pm.peers)
	pm.mu.Unlock()

	for _, c := range peers {
		_ = c.SendHave(index)
	}
}
