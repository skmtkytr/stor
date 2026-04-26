package engine

import (
	"context"
	"log/slog"
	"sync"
	"time"

	dhtpkg "github.com/skmtkytr/stor/dht"
	"github.com/skmtkytr/stor/events"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

const (
	defaultAnnounceInterval = 1800 * time.Second // 30 minutes
	defaultDHTInterval      = 5 * time.Minute    // DHT refresh independent of tracker interval
)

// Announcer periodically re-announces to trackers and DHT,
// feeding newly discovered peers into a channel.
type Announcer struct {
	tf      *torrent.TorrentFile
	peerID  [20]byte
	port    uint16
	numWant int
	dht     *dhtpkg.DHT

	dhtInterval time.Duration
	dhtLookupFn func([20]byte) []tracker.Peer // injectable for tests; nil uses real DHT

	peerSink chan<- []tracker.Peer

	// Callbacks for announce params
	downloaded func() int64
	left       func() int64

	// Observation. May be nil (e.g. in tests that build an Announcer directly).
	bus       *events.Bus
	torrentID string
}

// AnnounceConfig holds parameters for creating an Announcer.
type AnnounceConfig struct {
	TF          *torrent.TorrentFile
	PeerID      [20]byte
	Port        uint16
	NumWant     int
	DHT         *dhtpkg.DHT
	DHTInterval time.Duration                 // 0 = defaultDHTInterval
	DHTLookupFn func([20]byte) []tracker.Peer // optional override for testing
	PeerSink    chan<- []tracker.Peer
	Downloaded  func() int64
	Left        func() int64

	// Observation hooks. Bus may be nil; TorrentID is included on every emitted event.
	Bus       *events.Bus
	TorrentID string
}

// NewAnnouncer creates a new announcer.
func NewAnnouncer(cfg AnnounceConfig) *Announcer {
	dhtInterval := cfg.DHTInterval
	if dhtInterval <= 0 {
		dhtInterval = defaultDHTInterval
	}
	return &Announcer{
		tf:          cfg.TF,
		peerID:      cfg.PeerID,
		port:        cfg.Port,
		numWant:     cfg.NumWant,
		dht:         cfg.DHT,
		dhtInterval: dhtInterval,
		dhtLookupFn: cfg.DHTLookupFn,
		peerSink:    cfg.PeerSink,
		downloaded:  cfg.Downloaded,
		left:        cfg.Left,
		bus:         cfg.Bus,
		torrentID:   cfg.TorrentID,
	}
}

// Run starts the re-announce loop. Blocks until ctx is cancelled.
// The first announce is done immediately (EventStarted).
// A separate DHT refresh timer fires every dhtInterval independent of tracker interval.
func (a *Announcer) Run(ctx context.Context) {
	interval := a.announce(ctx, tracker.EventStarted)
	if interval <= 0 {
		interval = defaultAnnounceInterval
	}

	announceTimer := time.NewTimer(interval)
	defer announceTimer.Stop()

	dhtTimer := time.NewTimer(a.dhtInterval)
	defer dhtTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			// Best-effort stopped announce (short timeout)
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			a.announce(stopCtx, tracker.EventStopped)
			cancel()
			return
		case <-announceTimer.C:
			interval = a.announce(ctx, tracker.EventNone)
			if interval <= 0 {
				interval = defaultAnnounceInterval
			}
			announceTimer.Reset(interval)
			// Reset DHT timer so we don't double-lookup right after a full announce
			if !dhtTimer.Stop() {
				select {
				case <-dhtTimer.C:
				default:
				}
			}
			dhtTimer.Reset(a.dhtInterval)
		case <-dhtTimer.C:
			a.runDHTLookup(ctx)
			dhtTimer.Reset(a.dhtInterval)
		}
	}
}

// AnnounceCompleted sends a one-shot completed event to all trackers.
func (a *Announcer) AnnounceCompleted(ctx context.Context) {
	a.announce(ctx, tracker.EventCompleted)
}

// runDHTLookup performs a standalone DHT lookup and sends peers to peerSink.
func (a *Announcer) runDHTLookup(ctx context.Context) {
	if a.tf.Info.Private {
		return
	}
	peers := a.lookupDHT(a.tf.InfoHash)
	if len(peers) == 0 {
		return
	}
	if a.bus != nil {
		a.bus.Publish(events.Event{
			Type:      events.TypeDHTReply,
			TorrentID: a.torrentID,
			Payload:   events.DHTReplyPayload{NumPeers: len(peers)},
		})
	}
	if a.peerSink == nil {
		return
	}
	select {
	case <-ctx.Done():
	case a.peerSink <- peers:
		slog.Debug("dht refresh peers sent", "count", len(peers))
	}
}

// lookupDHT calls the injectable function if set, otherwise the real DHT.
func (a *Announcer) lookupDHT(infoHash [20]byte) []tracker.Peer {
	if a.dhtLookupFn != nil {
		return a.dhtLookupFn(infoHash)
	}
	if a.dht == nil {
		return nil
	}
	return dhtLookupPeers(a.dht, infoHash)
}

// announce sends announces to all trackers and DHT in parallel.
// Returns the minimum interval from tracker responses.
func (a *Announcer) announce(ctx context.Context, event tracker.Event) time.Duration {
	trackers := a.tf.TrackerURLs()
	var mu sync.Mutex
	var allPeers []tracker.Peer
	minInterval := defaultAnnounceInterval
	var wg sync.WaitGroup

	// Tracker announces
	for _, tr := range trackers {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			req := tracker.AnnounceRequest{
				AnnounceURL: tr,
				InfoHash:    a.tf.InfoHash,
				PeerID:      a.peerID,
				Port:        a.port,
				Downloaded:  a.downloaded(),
				Left:        a.left(),
				Event:       event,
				NumWant:     a.numWant,
				IPv6:        tracker.LocalIPv6(),
			}
			resp, err := tracker.Announce(req)
			if err != nil {
				slog.Debug("announce failed", "tracker", tr, "error", err)
				if a.bus != nil {
					a.bus.Publish(events.Event{
						Type:      events.TypeAnnounceError,
						TorrentID: a.torrentID,
						Payload: events.AnnounceErrorPayload{
							Tracker: tr,
							Error:   err.Error(),
						},
					})
				}
				return
			}
			mu.Lock()
			allPeers = append(allPeers, resp.Peers...)
			if resp.Interval > 0 {
				d := time.Duration(resp.Interval) * time.Second
				if d < minInterval {
					minInterval = d
				}
			}
			mu.Unlock()
			// Emit reply outside the mu critical section.
			if a.bus != nil && len(resp.Peers) > 0 {
				a.bus.Publish(events.Event{
					Type:      events.TypeAnnounceReply,
					TorrentID: a.torrentID,
					Payload: events.AnnounceReplyPayload{
						Tracker:         tr,
						NumPeers:        len(resp.Peers),
						IntervalSeconds: int(resp.Interval),
					},
				})
			}
		}(tr)
	}

	// DHT lookup — skip for private torrents (BEP 27)
	if event != tracker.EventStopped && !a.tf.Info.Private {
		peers := a.lookupDHT(a.tf.InfoHash)
		if len(peers) > 0 {
			mu.Lock()
			allPeers = append(allPeers, peers...)
			mu.Unlock()
			if a.bus != nil {
				a.bus.Publish(events.Event{
					Type:      events.TypeDHTReply,
					TorrentID: a.torrentID,
					Payload:   events.DHTReplyPayload{NumPeers: len(peers)},
				})
			}
		}
	}

	wg.Wait()

	// Send discovered peers to the download engine.
	// Skip if context is already done (peerSink may be closed during shutdown).
	if len(allPeers) > 0 && a.peerSink != nil {
		select {
		case <-ctx.Done():
			// Context cancelled, peerSink may already be closed
		case a.peerSink <- allPeers:
			slog.Debug("re-announce peers sent", "count", len(allPeers), "event", event)
		}
	}

	return minInterval
}

// dhtLookupPeers performs a DHT GetPeers and converts to tracker.Peer slice.
func dhtLookupPeers(d *dhtpkg.DHT, infoHash [20]byte) []tracker.Peer {
	peerAddrs, err := d.GetPeers(infoHash)
	if err != nil {
		return nil
	}
	return parsePeerAddrs(peerAddrs)
}
