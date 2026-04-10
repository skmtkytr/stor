package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/skmtkytr/stor/bencode"
	dhtpkg "github.com/skmtkytr/stor/dht"
	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/magnet"
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

// Well-known DHT bootstrap nodes.
var dhtBootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"router.utorrent.com:6881",
	"dht.libtorrent.org:25401",
}

// Session manages a single torrent's lifecycle.
type Session struct {
	mu       sync.RWMutex
	record   *TorrentRecord
	tf       *torrent.TorrentFile
	progress *download.Progress
	cancel   context.CancelFunc
	err      error

	// Config from engine
	peerID      [20]byte
	downloadDir string
	port        uint16
	dlCfg       download.DownloadConfig
	numWant     int
	dht         *dhtpkg.DHT         // shared DHT instance from engine (may be nil)
	cachedPeers []tracker.Peer      // peers collected during metadata phase (avoids double query)
	peerCh      chan []tracker.Peer // dynamic peer injection channel (active during download)
}

// NewSession creates a session from a persisted record.
func NewSession(record *TorrentRecord, peerID [20]byte, downloadDir string, port uint16, dlCfg download.DownloadConfig, numWant int, dht *dhtpkg.DHT) *Session {
	return &Session{
		record:      record,
		peerID:      peerID,
		downloadDir: downloadDir,
		port:        port,
		dlCfg:       dlCfg,
		numWant:     numWant,
		dht:         dht,
	}
}

// Record returns the current record (thread-safe copy of state fields).
func (s *Session) Record() *TorrentRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a shallow copy
	r := *s.record
	return &r
}

// Snap returns a progress snapshot. Safe to call from any goroutine.
func (s *Session) Snap() download.ProgressSnap {
	s.mu.RLock()
	p := s.progress
	state := s.record.State
	s.mu.RUnlock()

	if p != nil {
		snap := p.Snap()
		snap.State = string(state)
		return snap
	}
	return download.ProgressSnap{
		State: string(state),
		Total: s.record.TotalBytes,
	}
}

// Err returns the last error.
func (s *Session) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// Start begins the torrent session in a goroutine.
// onDone is called when the session reaches a terminal state (complete/error).
func (s *Session) Start(ctx context.Context, onDone func(id string)) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	go func() {
		defer cancel()
		slog.Info("session started", "id", s.record.ID, "source", s.record.Source)
		err := s.run(ctx)
		s.mu.Lock()
		if err != nil && !errors.Is(err, context.Canceled) {
			s.err = err
			s.record.State = StateError
			s.record.Error = err.Error()
			slog.Error("session failed", "id", s.record.ID, "name", s.record.Name, "error", err)
		}
		// If context was canceled (pause), state is already set by Pause()
		s.mu.Unlock()
		if onDone != nil {
			onDone(s.record.ID)
		}
	}()
}

// Pause cancels the running download.
func (s *Session) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.record.State == StateDownloading || s.record.State == StateMetadata {
		s.record.State = StatePaused
	}
}

// run executes the torrent lifecycle.
// For .torrent files: resolve → find peers → download.
// For magnets: tracker/DHT/metadata fetch run in parallel for speed.
func (s *Session) run(ctx context.Context) error {
	// Step 1: Resolve TorrentFile if we don't have one
	if s.tf == nil {
		slog.Info("resolving metadata", "id", s.record.ID)
		if err := s.resolveMetadata(ctx); err != nil {
			return err
		}
		slog.Info("metadata resolved", "id", s.record.ID, "name", s.tf.Info.Name, "pieces", len(s.tf.Info.PieceHashes))
	}

	// Step 2: Find peers (skipped for magnets — already collected during resolveMetadata)
	s.mu.Lock()
	s.record.State = StateDownloading
	s.mu.Unlock()

	var peers []tracker.Peer
	if len(s.cachedPeers) > 0 {
		peers = s.cachedPeers
		s.cachedPeers = nil
		slog.Info("using cached peers", "id", s.record.ID, "count", len(peers))
	} else {
		var err error
		peers, err = s.findPeers(ctx)
		if err != nil {
			return err
		}
		slog.Info("peers discovered", "id", s.record.ID, "count", len(peers))
	}

	// Step 3: Download
	tl := download.TotalSize(s.tf)
	numPieces := len(s.tf.Info.PieceHashes)

	progress := download.NewProgress(numPieces, tl)
	s.mu.Lock()
	s.progress = progress
	s.record.TotalBytes = tl
	s.mu.Unlock()

	savePath := filepath.Join(s.downloadDir, s.tf.Info.Name)

	// Create peer channel for dynamic injection (used by re-announce in the future)
	peerCh := make(chan []tracker.Peer, 16)
	s.mu.Lock()
	s.peerCh = peerCh
	s.mu.Unlock()

	// Start announcer for periodic re-announce + peer injection
	announceCtx, announceCancel := context.WithCancel(ctx)
	announcer := NewAnnouncer(AnnounceConfig{
		TF:       s.tf,
		PeerID:   s.peerID,
		Port:     s.port,
		NumWant:  s.numWant,
		DHT:      s.dht,
		PeerSink: peerCh,
		Downloaded: func() int64 {
			if s.progress != nil {
				return s.progress.Snap().Downloaded
			}
			return 0
		},
		Left: func() int64 {
			if s.progress != nil {
				snap := s.progress.Snap()
				return snap.Total - snap.Downloaded
			}
			return tl
		},
	})
	go announcer.Run(announceCtx)

	// Close peerCh and stop announcer when download finishes
	defer func() {
		announceCancel()
		s.mu.Lock()
		if s.peerCh != nil {
			close(s.peerCh)
			s.peerCh = nil
		}
		s.mu.Unlock()
	}()

	slog.Info("download starting",
		"id", s.record.ID,
		"name", s.tf.Info.Name,
		"total_bytes", tl,
		"pieces", numPieces,
		"peers", len(peers),
		"save_path", savePath,
	)
	if err := download.DownloadWithParams(ctx, download.DownloadParams{
		TF:       s.tf,
		PeerID:   s.peerID,
		Peers:    peers,
		PeerCh:   peerCh,
		Path:     savePath,
		Progress: progress,
		Cfg:      s.dlCfg,
	}); err != nil {
		return err
	}

	s.mu.Lock()
	s.record.State = StateComplete
	s.record.CompletedAt = time.Now().Unix()
	s.record.SavePath = savePath
	s.mu.Unlock()

	slog.Info("download complete", "id", s.record.ID, "name", s.tf.Info.Name, "save_path", savePath)
	return nil
}

// resolveMetadata gets the TorrentFile either from stored data or via magnet.
// For magnets, tracker queries, DHT lookup, and metadata fetch run in parallel.
// Discovered peers are cached in s.cachedPeers to avoid re-querying trackers.
func (s *Session) resolveMetadata(ctx context.Context) error {
	// If we have stored torrent data, parse it
	if len(s.record.TorrentData) > 0 {
		tf, err := torrent.Parse(s.record.TorrentData)
		if err != nil {
			return fmt.Errorf("session: parse stored torrent: %w", err)
		}
		s.tf = tf
		return nil
	}

	// Try parsing source as a .torrent file path
	if data, err := os.ReadFile(s.record.Source); err == nil {
		if tf, err := torrent.Parse(data); err == nil {
			s.tf = tf
			s.mu.Lock()
			s.record.TorrentData = data
			s.record.Name = tf.Info.Name
			s.mu.Unlock()
			return nil
		}
	}

	// Must be a magnet URI
	s.mu.Lock()
	s.record.State = StateMetadata
	s.mu.Unlock()

	m, err := magnet.Parse(s.record.Source)
	if err != nil {
		return fmt.Errorf("session: parse magnet: %w", err)
	}

	slog.Info("resolving magnet",
		"id", s.record.ID,
		"info_hash", hex.EncodeToString(m.InfoHash[:]),
		"trackers", len(m.Trackers),
		"x_pe_peers", len(m.Peers),
	)

	// Collect peers from all sources in parallel.
	// Feed them into metadata fetch as they arrive.
	var peerMu sync.Mutex
	var allPeers []tracker.Peer
	peerCh := make(chan []tracker.Peer, 10)

	var wg sync.WaitGroup

	// Tracker announces (parallel)
	for _, tr := range m.Trackers {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			req := tracker.AnnounceRequest{
				AnnounceURL: tr,
				InfoHash:    m.InfoHash,
				PeerID:      s.peerID,
				Port:        s.port,
				Left:        1,
				Event:       tracker.EventStarted,
				NumWant:     s.numWant,
			}
			resp, err := tracker.Announce(req)
			if err != nil {
				slog.Debug("tracker announce failed", "id", s.record.ID, "tracker", tr, "error", err)
				return
			}
			if len(resp.Peers) == 0 {
				slog.Debug("tracker returned no peers", "id", s.record.ID, "tracker", tr)
				return
			}
			slog.Debug("tracker peers", "id", s.record.ID, "tracker", tr, "peers", len(resp.Peers))
			peerMu.Lock()
			allPeers = append(allPeers, resp.Peers...)
			peerMu.Unlock()
			peerCh <- resp.Peers
		}(tr)
	}

	// DHT lookup (reuse shared instance if available)
	wg.Add(1)
	go func() {
		defer wg.Done()
		dhtPeers := s.dhtLookup(m.InfoHash)
		if len(dhtPeers) > 0 {
			slog.Debug("dht peers", "id", s.record.ID, "peers", len(dhtPeers))
			peerMu.Lock()
			allPeers = append(allPeers, dhtPeers...)
			peerMu.Unlock()
			peerCh <- dhtPeers
		} else {
			slog.Debug("dht returned no peers", "id", s.record.ID)
		}
	}()

	// Add x.pe peers immediately
	var xpePeers []tracker.Peer
	for _, pe := range m.Peers {
		host, portStr, splitErr := net.SplitHostPort(pe)
		if splitErr != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		var port uint16
		if _, scanErr := fmt.Sscanf(portStr, "%d", &port); scanErr == nil {
			xpePeers = append(xpePeers, tracker.Peer{IP: ip, Port: port})
		}
	}
	if len(xpePeers) > 0 {
		peerMu.Lock()
		allPeers = append(allPeers, xpePeers...)
		peerMu.Unlock()
		peerCh <- xpePeers
	}

	// Close peerCh when all sources are done
	go func() {
		wg.Wait()
		close(peerCh)
	}()

	// Metadata fetch: start trying peers as they arrive
	metaCtx, metaCancel := context.WithTimeout(ctx, 60*time.Second)
	defer metaCancel()

	type metaResult struct {
		tf *torrent.TorrentFile
	}
	resultCh := make(chan metaResult, 1)
	metaSem := make(chan struct{}, 20)

	var metaWg sync.WaitGroup
	go func() {
		for batch := range peerCh {
			for _, p := range batch {
				metaWg.Add(1)
				go func(p tracker.Peer) {
					defer metaWg.Done()
					select {
					case <-metaCtx.Done():
						return
					case metaSem <- struct{}{}:
						defer func() { <-metaSem }()
					}
					tf, err := fetchMetadataFromPeer(metaCtx, p, m.InfoHash, s.peerID)
					if err != nil {
						return
					}
					select {
					case resultCh <- metaResult{tf: tf}:
						metaCancel()
					default:
					}
				}(p)
			}
		}
		metaWg.Wait()
		close(resultCh)
	}()

	res, ok := <-resultCh
	if !ok {
		peerMu.Lock()
		totalPeers := len(allPeers)
		peerMu.Unlock()
		slog.Error("metadata fetch failed from all peers", "id", s.record.ID, "peers_tried", totalPeers)
		return fmt.Errorf("session: failed to fetch metadata from any peer")
	}
	tf := res.tf
	slog.Info("metadata fetched", "id", s.record.ID, "name", tf.Info.Name)

	// Restore announce URLs
	if len(m.Trackers) > 0 {
		tf.Announce = m.Trackers[0]
		tiers := make([][]string, len(m.Trackers))
		for i, tr := range m.Trackers {
			tiers[i] = []string{tr}
		}
		tf.AnnounceList = tiers
	}

	// Store the torrent data for resume
	fullDict := map[string]any{"info": mustReencode(tf)}
	if tf.Announce != "" {
		fullDict["announce"] = tf.Announce
	}
	torrentData, _ := bencode.Encode(fullDict)

	s.tf = tf
	s.mu.Lock()
	s.record.TorrentData = torrentData
	s.record.Name = tf.Info.Name
	s.mu.Unlock()

	// Cache all collected peers for download phase (avoids re-querying)
	peerMu.Lock()
	s.cachedPeers = allPeers
	peerMu.Unlock()

	return nil
}

// dhtLookup performs a DHT GetPeers using the shared DHT or a temporary one.
func (s *Session) dhtLookup(infoHash [20]byte) []tracker.Peer {
	var d *dhtpkg.DHT
	var cleanup func()

	if s.dht != nil {
		d = s.dht
		cleanup = func() {}
	} else {
		var err error
		d, err = dhtpkg.New(":0")
		if err != nil {
			slog.Debug("dht lookup: failed to create node", "error", err)
			return nil
		}
		_ = d.Bootstrap(dhtBootstrapNodes)
		cleanup = func() { _ = d.Close() }
	}
	defer cleanup()

	peerAddrs, err := d.GetPeers(infoHash)
	if err != nil {
		slog.Debug("dht GetPeers failed", "info_hash", hex.EncodeToString(infoHash[:]), "error", err)
		return nil
	}

	return parsePeerAddrs(peerAddrs)
}

func mustReencode(tf *torrent.TorrentFile) map[string]any {
	// Re-parse the stored torrent data to get info dict
	// This is a simplified approach; we reconstruct from the TorrentFile
	d := map[string]any{
		"name":         tf.Info.Name,
		"piece length": tf.Info.PieceLength,
	}

	// Reconstruct pieces string
	pieces := make([]byte, len(tf.Info.PieceHashes)*20)
	for i, h := range tf.Info.PieceHashes {
		copy(pieces[i*20:], h[:])
	}
	d["pieces"] = string(pieces)

	if tf.Info.Length > 0 {
		d["length"] = tf.Info.Length
	} else {
		files := make([]any, len(tf.Info.Files))
		for i, f := range tf.Info.Files {
			pathList := make([]any, len(f.Path))
			for j, p := range f.Path {
				pathList[j] = p
			}
			files[i] = map[string]any{
				"length": f.Length,
				"path":   pathList,
			}
		}
		d["files"] = files
	}

	return d
}

// findPeers discovers peers from trackers and DHT concurrently.
func (s *Session) findPeers(ctx context.Context) ([]tracker.Peer, error) {
	if s.tf == nil {
		return nil, fmt.Errorf("session: no torrent file")
	}

	var allPeers []tracker.Peer
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Collect tracker URLs
	trackers := s.tf.TrackerURLs()

	slog.Info("finding peers", "id", s.record.ID, "trackers", len(trackers))

	// Tracker announces
	for _, tr := range trackers {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			tl := download.TotalSize(s.tf)
			req := tracker.AnnounceRequest{
				AnnounceURL: tr,
				InfoHash:    s.tf.InfoHash,
				PeerID:      s.peerID,
				Port:        s.port,
				Left:        tl,
				Event:       tracker.EventStarted,
				NumWant:     s.numWant,
			}
			resp, err := tracker.Announce(req)
			if err != nil {
				slog.Debug("tracker announce failed", "id", s.record.ID, "tracker", tr, "error", err)
				return
			}
			slog.Debug("tracker peers", "id", s.record.ID, "tracker", tr, "peers", len(resp.Peers))
			mu.Lock()
			allPeers = append(allPeers, resp.Peers...)
			mu.Unlock()
		}(tr)
	}

	// DHT lookup (reuse shared instance)
	wg.Add(1)
	go func() {
		defer wg.Done()
		dhtPeers := s.dhtLookup(s.tf.InfoHash)
		if len(dhtPeers) > 0 {
			slog.Debug("dht peers", "id", s.record.ID, "peers", len(dhtPeers))
			mu.Lock()
			allPeers = append(allPeers, dhtPeers...)
			mu.Unlock()
		}
	}()

	wg.Wait()

	if len(allPeers) == 0 {
		slog.Warn("no peers found", "id", s.record.ID)
		return nil, fmt.Errorf("session: no peers found")
	}
	return allPeers, nil
}

func fetchMetadataFromPeer(ctx context.Context, p tracker.Peer, infoHash, peerID [20]byte) (*torrent.TorrentFile, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", p.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	hs := &peer.Handshake{InfoHash: infoHash, PeerID: peerID, Extensions: true}
	if err := peer.WriteHandshake(conn, hs); err != nil {
		return nil, err
	}

	resp, err := peer.ReadHandshake(conn)
	if err != nil {
		return nil, err
	}
	if resp.InfoHash != infoHash {
		return nil, fmt.Errorf("info hash mismatch")
	}
	if !resp.Extensions {
		return nil, fmt.Errorf("peer does not support extensions")
	}

	ourHS := &peer.ExtHandshake{
		M: map[string]int64{"ut_metadata": 1},
		V: "stor/0.1.0",
	}
	extConn, peerHS, err := peer.NegotiateExtension(
		conn.(io.ReadWriter), "ut_metadata", 1, ourHS,
	)
	if err != nil {
		return nil, err
	}

	if peerHS.MetadataSize <= 0 {
		return nil, fmt.Errorf("peer reported no metadata")
	}

	metadata, err := magnet.FetchMetadata(extConn, int(peerHS.MetadataSize), infoHash)
	if err != nil {
		return nil, err
	}

	decoded, err := bencode.Decode(metadata)
	if err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	infoDict, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata is not a dict")
	}

	fullDict := map[string]any{"info": infoDict}
	fullData, err := bencode.Encode(fullDict)
	if err != nil {
		return nil, err
	}

	return torrent.Parse(fullData)
}

// InfoHashHex returns the hex-encoded info hash for a source string.
func InfoHashHex(source string) (string, error) {
	// Try magnet
	if m, err := magnet.Parse(source); err == nil {
		return hex.EncodeToString(m.InfoHash[:]), nil
	}

	// Try .torrent file
	data, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("cannot read source: %w", err)
	}
	tf, err := torrent.Parse(data)
	if err != nil {
		return "", fmt.Errorf("cannot parse torrent: %w", err)
	}
	return hex.EncodeToString(tf.InfoHash[:]), nil
}
