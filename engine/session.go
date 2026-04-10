package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	dht         *dhtpkg.DHT    // shared DHT instance from engine (may be nil)
	cachedPeers []tracker.Peer // peers collected during metadata phase (avoids double query)
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
		err := s.run(ctx)
		s.mu.Lock()
		if err != nil && !errors.Is(err, context.Canceled) {
			s.err = err
			s.record.State = StateError
			s.record.Error = err.Error()
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
		if err := s.resolveMetadata(ctx); err != nil {
			return err
		}
	}

	// Step 2: Find peers (skipped for magnets — already collected during resolveMetadata)
	s.mu.Lock()
	s.record.State = StateDownloading
	s.mu.Unlock()

	var peers []tracker.Peer
	if len(s.cachedPeers) > 0 {
		peers = s.cachedPeers
		s.cachedPeers = nil
	} else {
		var err error
		peers, err = s.findPeers(ctx)
		if err != nil {
			return err
		}
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
	if err := download.DownloadToFileCtxWithConfig(ctx, s.tf, s.peerID, peers, savePath, progress, s.dlCfg); err != nil {
		return err
	}

	s.mu.Lock()
	s.record.State = StateComplete
	s.record.CompletedAt = time.Now().Unix()
	s.record.SavePath = savePath
	s.mu.Unlock()

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
			if err != nil || len(resp.Peers) == 0 {
				return
			}
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
			peerMu.Lock()
			allPeers = append(allPeers, dhtPeers...)
			peerMu.Unlock()
			peerCh <- dhtPeers
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
		return fmt.Errorf("session: failed to fetch metadata from any peer")
	}
	tf := res.tf

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
			return nil
		}
		_ = d.Bootstrap(dhtBootstrapNodes)
		cleanup = func() { _ = d.Close() }
	}
	defer cleanup()

	peerAddrs, err := d.GetPeers(infoHash)
	if err != nil {
		return nil
	}

	var peers []tracker.Peer
	for _, addr := range peerAddrs {
		host, portStr, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		var port uint16
		if _, scanErr := fmt.Sscanf(portStr, "%d", &port); scanErr == nil {
			peers = append(peers, tracker.Peer{IP: ip, Port: port})
		}
	}
	return peers
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
				return
			}
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
			mu.Lock()
			allPeers = append(allPeers, dhtPeers...)
			mu.Unlock()
		}
	}()

	wg.Wait()

	if len(allPeers) == 0 {
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
