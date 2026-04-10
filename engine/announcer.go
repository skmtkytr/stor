package engine

import (
	"context"
	"log/slog"
	"sync"
	"time"

	dhtpkg "github.com/skmtkytr/stor/dht"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

const defaultAnnounceInterval = 1800 * time.Second // 30 minutes

// Announcer periodically re-announces to trackers and DHT,
// feeding newly discovered peers into a channel.
type Announcer struct {
	tf      *torrent.TorrentFile
	peerID  [20]byte
	port    uint16
	numWant int
	dht     *dhtpkg.DHT

	peerSink chan<- []tracker.Peer

	// Callbacks for announce params
	downloaded func() int64
	left       func() int64
}

// AnnounceConfig holds parameters for creating an Announcer.
type AnnounceConfig struct {
	TF         *torrent.TorrentFile
	PeerID     [20]byte
	Port       uint16
	NumWant    int
	DHT        *dhtpkg.DHT
	PeerSink   chan<- []tracker.Peer
	Downloaded func() int64
	Left       func() int64
}

// NewAnnouncer creates a new announcer.
func NewAnnouncer(cfg AnnounceConfig) *Announcer {
	return &Announcer{
		tf:         cfg.TF,
		peerID:     cfg.PeerID,
		port:       cfg.Port,
		numWant:    cfg.NumWant,
		dht:        cfg.DHT,
		peerSink:   cfg.PeerSink,
		downloaded: cfg.Downloaded,
		left:       cfg.Left,
	}
}

// Run starts the re-announce loop. Blocks until ctx is cancelled.
// The first announce is done immediately (EventStarted).
func (a *Announcer) Run(ctx context.Context) {
	interval := a.announce(ctx, tracker.EventStarted)
	if interval <= 0 {
		interval = defaultAnnounceInterval
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			// Best-effort stopped announce (short timeout)
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			a.announce(stopCtx, tracker.EventStopped)
			cancel()
			return
		case <-timer.C:
			interval = a.announce(ctx, tracker.EventNone)
			if interval <= 0 {
				interval = defaultAnnounceInterval
			}
			timer.Reset(interval)
		}
	}
}

// AnnounceCompleted sends a one-shot completed event to all trackers.
func (a *Announcer) AnnounceCompleted(ctx context.Context) {
	a.announce(ctx, tracker.EventCompleted)
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
			}
			resp, err := tracker.Announce(req)
			if err != nil {
				slog.Debug("announce failed", "tracker", tr, "error", err)
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
		}(tr)
	}

	// DHT lookup
	if a.dht != nil && event != tracker.EventStopped {
		wg.Add(1)
		go func() {
			defer wg.Done()
			peers := dhtLookupPeers(a.dht, a.tf.InfoHash)
			if len(peers) > 0 {
				mu.Lock()
				allPeers = append(allPeers, peers...)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Send discovered peers to the download engine
	if len(allPeers) > 0 && a.peerSink != nil {
		select {
		case a.peerSink <- allPeers:
			slog.Debug("re-announce peers sent", "count", len(allPeers), "event", event)
		case <-ctx.Done():
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
