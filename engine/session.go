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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/skmtkytr/stor/bencode"
	dhtpkg "github.com/skmtkytr/stor/dht"
	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/events"
	"github.com/skmtkytr/stor/magnet"
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
	"github.com/skmtkytr/stor/utp"
)

// Well-known DHT bootstrap nodes.
var dhtBootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"router.utorrent.com:6881",
	"dht.libtorrent.org:25401",
}

// Retry parameters for indefinite peer/metadata search. Match Deluge's
// behavior: don't fail the torrent when peers/metadata are temporarily
// unavailable — keep trying with exponential backoff until the user
// pauses or removes the torrent.
const (
	metadataRetryInitial = 30 * time.Second
	metadataRetryMax     = 5 * time.Minute
	peerSearchInitial    = 30 * time.Second
	peerSearchMax        = 5 * time.Minute
	metadataAttemptTTL   = 60 * time.Second
)

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
	tmpDir      string // temp dir for in-progress downloads; empty = use downloadDir
	port        uint16
	dlCfg       download.DownloadConfig
	numWant     int
	dht         *dhtpkg.DHT           // shared DHT instance from engine (may be nil)
	cachedPeers []tracker.Peer        // peers collected during metadata phase (avoids double query)
	peerCh      chan []tracker.Peer   // dynamic peer injection channel (active during download)
	uploader    *download.Uploader    // active during seeding
	peerMgr     *download.PeerManager // active during downloading (nil when seeding)
	pq          *download.PieceQueue  // active during downloading; target of hot skip-mask updates
	listener    *PeerListener         // reference to engine's listener for registration

	// Live have-bitfield used for per-file progress reporting. Updated by
	// OnPiece during download and set to a full bitfield in seeding phases.
	// Shares storage with the download/upload HaveBF in the session
	// goroutine, so mutations happen in that goroutine; snapshots taken by
	// Files() copy the bytes under s.mu.
	haveBF peer.Bitfield

	// knownPeers is the cumulative count of peers received from tracker/DHT/PEX.
	// Written atomically by the download goroutine; read by Snap().
	knownPeers atomic.Int32

	enableUTP bool // try uTP before TCP for all outgoing connections

	// bus is the engine event bus. May be nil for tests that build a Session
	// directly via &Session{}; setState and other emitters must guard for nil.
	bus *events.Bus

	// dhtGetPeersFn, when non-nil, replaces s.dht.GetPeers in dhtLookup.
	// Production code leaves this nil; tests inject a stub to drive the
	// failure-event code path without a live DHT.
	dhtGetPeersFn func([20]byte) ([]string, error)
}

// NewSession creates a session from a persisted record.
func NewSession(record *TorrentRecord, peerID [20]byte, downloadDir, tmpDir string, port uint16, dlCfg download.DownloadConfig, numWant int, d *dhtpkg.DHT, pl *PeerListener, bus *events.Bus) *Session {
	s := &Session{
		record:      record,
		peerID:      peerID,
		downloadDir: downloadDir,
		tmpDir:      tmpDir,
		port:        port,
		dlCfg:       dlCfg,
		numWant:     numWant,
		dht:         d,
		listener:    pl,
		enableUTP:   dlCfg.EnableUTP,
		bus:         bus,
	}
	s.knownPeers.Store(int32(record.KnownPeers))
	return s
}

// setState mutates s.record.State and, if it actually changes, publishes
// events after releasing the lock. Centralising state transitions through
// this helper guarantees we never emit while holding s.mu (a known deadlock
// source: subscribers running on the same goroutine would re-enter Session
// methods).
//
// Always emits TypeStateChanged on a real transition. Additionally emits
// the *confirmation* events:
//   - TypeTorrentPaused when entering StatePaused from any other state
//   - TypeTorrentResumed on the first transition out of StatePaused
//     (typically paused → adding, since ResumeTorrent re-queues through
//     StateAdding before reaching downloading)
//
// These complement the *requested* events emitted at API entry points
// (PauseTorrent / ResumeTorrent in engine.go) — Deluge alert parity.
// No-op self-transitions (newState == oldState) emit nothing.
func (s *Session) setState(newState State) {
	s.mu.Lock()
	old := s.record.State
	if old == newState {
		s.mu.Unlock()
		return
	}
	s.record.State = newState
	bus := s.bus
	id := s.record.ID
	s.mu.Unlock()

	if bus == nil {
		return
	}

	bus.Publish(events.Event{
		Type:      events.TypeStateChanged,
		TorrentID: id,
		Payload: events.StateChangedPayload{
			From: string(old),
			To:   string(newState),
		},
	})

	// Confirmation events: fire AFTER state has actually flipped.
	switch {
	case newState == StatePaused && old != StatePaused:
		bus.Publish(events.Event{
			Type:      events.TypeTorrentPaused,
			TorrentID: id,
		})
	case old == StatePaused && newState != StatePaused:
		bus.Publish(events.Event{
			Type:      events.TypeTorrentResumed,
			TorrentID: id,
		})
	}
}

// popCount returns the number of set bits in the bitfield.
func popCount(bf peer.Bitfield) int {
	n := 0
	for _, b := range bf {
		// Brian Kernighan's bit count
		for b != 0 {
			b &= b - 1
			n++
		}
	}
	return n
}

// Record returns the current record (thread-safe copy of state fields).
func (s *Session) Record() *TorrentRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := *s.record
	r.KnownPeers = int(s.knownPeers.Load())
	return &r
}

// isRunning reports whether this session has an active goroutine.
func (s *Session) isRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cancel != nil
}

// Snap returns a progress snapshot. Safe to call from any goroutine.
func (s *Session) Snap() download.ProgressSnap {
	s.mu.RLock()
	p := s.progress
	u := s.uploader
	state := s.record.State
	totalBytes := s.record.TotalBytes
	s.mu.RUnlock()

	if p != nil {
		snap := p.Snap()
		snap.State = string(state)
		snap.KnownPeers = int(s.knownPeers.Load())
		// Clear speeds for inactive states
		if state == StatePaused || state == StateComplete || state == StateError {
			snap.DownSpeed = 0
			snap.UpSpeed = 0
		}
		// Upload speed: outgoing peers (via Progress) + incoming peers (via Uploader)
		if state != StatePaused && state != StateComplete && state != StateError {
			snap.UpSpeed = p.UploadRate() // upload via download peer connections
			if u != nil {
				snap.UpSpeed += u.TotalUploadSpeed() // upload via incoming peers
				snap.ActivePeers += u.PeerCount()
			}
		}
		return snap
	}

	// Seeding without download progress (resumed as seeder)
	if u != nil {
		return download.ProgressSnap{
			State:       string(state),
			Total:       totalBytes,
			Downloaded:  totalBytes,
			Percent:     100,
			UpSpeed:     u.TotalUploadSpeed(),
			ActivePeers: u.PeerCount(),
			KnownPeers:  int(s.knownPeers.Load()),
		}
	}

	pct := 0.0
	downloaded := int64(0)
	if state == StateComplete {
		pct = 100
		downloaded = totalBytes
	}
	return download.ProgressSnap{
		State:      string(state),
		Total:      totalBytes,
		Downloaded: downloaded,
		Percent:    pct,
		KnownPeers: int(s.knownPeers.Load()),
	}
}

// Files returns the file list for the torrent. Returns an empty slice when
// metadata has not yet been resolved.
func (s *Session) Files() []FileEntry {
	s.mu.RLock()
	tf := s.tf
	data := s.record.TorrentData
	// Snapshot the have-bitfield. Prefer the live one; fall back to the
	// persisted record bitfield so a paused/unstarted torrent still shows
	// file progress from the last session.
	var bf peer.Bitfield
	if s.haveBF != nil {
		bf = append(peer.Bitfield(nil), s.haveBF...)
	} else if len(s.record.Bitfield) > 0 {
		bf = append(peer.Bitfield(nil), s.record.Bitfield...)
	}
	prios := append([]int8(nil), s.record.FilePriorities...)
	s.mu.RUnlock()
	if tf == nil && len(data) > 0 {
		if parsed, err := torrent.Parse(data); err == nil {
			tf = parsed
		}
	}
	if tf == nil {
		return []FileEntry{}
	}

	numPieces := len(tf.Info.PieceHashes)
	pieceLength := tf.Info.PieceLength
	totalBytes := storage.TotalSize(tf)

	// priorityOf returns the priority for file index i (default: normal).
	priorityOf := func(i int) int8 {
		if i < len(prios) {
			return prios[i]
		}
		return PriorityNormal
	}

	// Single-file torrent: info.length > 0, files list is empty.
	if len(tf.Info.Files) == 0 {
		entry := FileEntry{
			Path:     tf.Info.Name,
			Length:   tf.Info.Length,
			Priority: priorityOf(0),
		}
		entry.Downloaded = computeFileDownloaded(bf, 0, tf.Info.Length, pieceLength, totalBytes, numPieces)
		return []FileEntry{entry}
	}

	out := make([]FileEntry, 0, len(tf.Info.Files))
	var offset int64
	for i, f := range tf.Info.Files {
		out = append(out, FileEntry{
			Path:       filepath.ToSlash(filepath.Join(f.Path...)),
			Length:     f.Length,
			Downloaded: computeFileDownloaded(bf, offset, f.Length, pieceLength, totalBytes, numPieces),
			Priority:   priorityOf(i),
		})
		offset += f.Length
	}
	return out
}

// PeerList returns snapshots of all currently connected peers
// (both downloading and seeding/incoming). Safe to call from any goroutine.
func (s *Session) PeerList() []download.PeerSnap {
	s.mu.RLock()
	pm := s.peerMgr
	up := s.uploader
	tf := s.tf
	s.mu.RUnlock()

	numPieces := 0
	if tf != nil {
		numPieces = len(tf.Info.PieceHashes)
	}

	var all []download.PeerSnap
	if pm != nil {
		all = append(all, pm.Snapshot(numPieces)...)
	}
	if up != nil {
		all = append(all, up.Snapshot(numPieces)...)
	}
	return all
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
		var transitionedToError bool
		s.mu.Lock()
		if err != nil && !errors.Is(err, context.Canceled) {
			s.err = err
			s.record.Error = err.Error()
			transitionedToError = s.record.State != StateError
			slog.Error("session failed", "id", s.record.ID, "name", s.record.Name, "error", err)
		}
		// If context was canceled (pause), state is already set by Pause()
		s.cancel = nil
		s.mu.Unlock()
		if transitionedToError {
			s.setState(StateError)
		}
		if onDone != nil {
			onDone(s.record.ID)
		}
	}()
}

// Pause cancels the running download.
func (s *Session) Pause() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	var shouldTransition bool
	switch s.record.State {
	case StateDownloading, StateMetadata, StateSeeding, StateVerifying, StateAdding:
		shouldTransition = true
	}
	s.mu.Unlock()
	if shouldTransition {
		s.setState(StatePaused)
	}
}

// run executes the torrent lifecycle: resolve → download (with upload) → seed.
// If resumed from StateSeeding/StateComplete, skips straight to seed.
func (s *Session) run(ctx context.Context) error {
	if err := s.phaseResolve(ctx); err != nil {
		return err
	}

	// If already completed (resumed seeding), skip download
	s.mu.RLock()
	state := s.record.State
	savePath := s.record.SavePath
	s.mu.RUnlock()
	if state == StateSeeding || state == StateComplete {
		if savePath == "" {
			savePath = filepath.Join(s.downloadDir, s.tf.Info.Name)
		}
		// Verify we actually have the file
		if _, err := os.Stat(savePath); err == nil {
			slog.Info("resuming as seeder", "id", s.record.ID, "name", s.tf.Info.Name)
			s.mu.Lock()
			s.record.SavePath = savePath
			s.mu.Unlock()
			s.setState(StateSeeding)
			return s.phaseSeedDirect(ctx, savePath)
		}
		// File missing — need to re-download
		slog.Warn("seeding file missing, re-downloading", "id", s.record.ID, "path", savePath)
	}

	if err := s.phaseDownload(ctx); err != nil {
		return err
	}
	return s.phaseSeed(ctx)
}

// phaseResolve obtains the TorrentFile. If not yet seeding, also finds peers.
func (s *Session) phaseResolve(ctx context.Context) error {
	if s.tf == nil {
		slog.Info("resolving metadata", "id", s.record.ID)
		if err := s.resolveMetadata(ctx); err != nil {
			return err
		}
		slog.Info("metadata resolved", "id", s.record.ID, "name", s.tf.Info.Name, "pieces", len(s.tf.Info.PieceHashes))
		if s.bus != nil {
			s.bus.Publish(events.Event{
				Type:      events.TypeMetadataFetched,
				TorrentID: s.record.ID,
				Payload: events.MetadataFetchedPayload{
					Name:       s.tf.Info.Name,
					NumPieces:  len(s.tf.Info.PieceHashes),
					TotalBytes: storage.TotalSize(s.tf),
				},
			})
		}
	}

	// Validate torrent name to prevent path traversal attacks.
	// This must happen before any filesystem operations.
	if err := sanitizeTorrentName(s.tf.Info.Name); err != nil {
		return fmt.Errorf("session: %w", err)
	}

	// If resuming as seeder, don't change state or look for peers
	s.mu.RLock()
	resuming := s.record.State == StateSeeding || s.record.State == StateComplete
	s.mu.RUnlock()
	if resuming {
		return nil
	}

	s.setState(StateDownloading)

	if len(s.cachedPeers) > 0 {
		slog.Info("using cached peers", "id", s.record.ID, "count", len(s.cachedPeers))
	} else {
		peers, err := s.findPeers(ctx)
		if err != nil {
			return err
		}
		s.cachedPeers = peers
		slog.Info("peers discovered", "id", s.record.ID, "count", len(peers))
	}
	s.knownPeers.Store(int32(len(s.cachedPeers)))
	return nil
}

// phaseDownload downloads pieces while serving already-downloaded pieces to other peers.
func (s *Session) phaseDownload(ctx context.Context) error {
	tl := storage.TotalSize(s.tf)
	numPieces := len(s.tf.Info.PieceHashes)

	// Resolve paths
	dlDir := s.downloadDir
	if s.tmpDir != "" {
		dlDir = s.tmpDir
	}
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		return fmt.Errorf("session: create download dir: %w", err)
	}
	savePath := filepath.Join(dlDir, s.tf.Info.Name)

	// Resume: use persisted bitfield if available (clean shutdown), otherwise verify from disk.
	var haveBitfield peer.Bitfield
	if len(s.record.Bitfield) == (numPieces+7)/8 {
		haveBitfield = peer.Bitfield(s.record.Bitfield)
		slog.Info("resume: using persisted bitfield", "id", s.record.ID, "total", numPieces)
	} else {
		s.setState(StateVerifying)

		bf, verified, _ := storage.VerifyPieces(savePath, s.tf, func(checked, total int) {
			slog.Debug("verifying pieces", "id", s.record.ID, "checked", checked, "total", total)
		})
		if verified > 0 {
			haveBitfield = bf
			s.mu.Lock()
			s.record.Bitfield = []byte(bf)
			s.mu.Unlock()
			slog.Info("resume: verified pieces", "id", s.record.ID, "verified", verified, "total", numPieces)
		}
	}

	s.setState(StateDownloading)

	// Progress
	progress := download.NewProgress(numPieces, tl)
	s.mu.Lock()
	s.progress = progress
	s.record.TotalBytes = tl
	s.mu.Unlock()

	// --- Start shared services: announcer + uploader ---
	peerCh := make(chan []tracker.Peer, 16)
	s.mu.Lock()
	s.peerCh = peerCh
	s.mu.Unlock()

	announcer := s.newAnnouncer(peerCh)
	announceCtx, announceCancel := context.WithCancel(ctx)
	go announcer.Run(announceCtx)

	uploadBF := make(peer.Bitfield, (numPieces+7)/8)
	if haveBitfield != nil {
		copy(uploadBF, haveBitfield)
	}
	up := download.NewUploader(s.tf, savePath, s.peerID, uploadBF)
	s.mu.Lock()
	s.uploader = up
	s.haveBF = uploadBF
	s.mu.Unlock()
	go up.Run(ctx)

	if s.listener != nil {
		s.listener.Register(s.tf.InfoHash, s)
	}

	// --- Stall detector ---
	// Watches PeerCount() during the download phase. Emits TypeStalled when
	// 0 peers persists for stallThreshold, TypeStallCleared on recovery.
	// Stops automatically when ctx is cancelled (Pause / Stop) or when the
	// download phase exits (cleanup calls stallCancel).
	stallCtx, stallCancel := context.WithCancel(ctx)
	go func() {
		(&stallDetector{
			bus:       s.bus,
			torrentID: s.record.ID,
			peers: func() int {
				s.mu.RLock()
				pm := s.peerMgr
				s.mu.RUnlock()
				if pm == nil {
					return 0
				}
				return pm.PeerCount()
			},
		}).run(stallCtx)
	}()

	// --- Cleanup on exit (error or success) ---
	// Note: we do NOT close peerCh here. Closing it while workers are still
	// running causes "send on closed channel" panics from PEX (handleExtended).
	// The feeder goroutine in runWorkers also listens on ctx.Done(), so
	// cancelling the context is sufficient to stop all consumers.
	// The channel will be GC'd once all references are gone.
	cleanup := func() {
		announceCancel()
		stallCancel()
		s.mu.Lock()
		s.peerCh = nil
		// PieceQueue is only meaningful during the download phase; the
		// seed phase doesn't consult it. Drop the ref so SetFilePriority
		// can short-circuit once we leave download.
		s.pq = nil
		// Intentionally keep s.peerMgr non-nil: the PeerManager's worker
		// goroutines remain registered until the session ctx is cancelled,
		// so PeerList should continue to surface those connections during
		// the seed phase.
		s.mu.Unlock()
	}

	// --- Download ---
	peers := s.cachedPeers
	s.cachedPeers = nil

	slog.Info("download starting", "id", s.record.ID, "name", s.tf.Info.Name,
		"total_bytes", tl, "pieces", numPieces, "peers", len(peers), "save_path", savePath)

	// BEP 27: disable PEX for private torrents
	var peerSink chan<- []tracker.Peer
	if !s.tf.Info.Private {
		peerSink = peerCh
	}

	// Wrap peerCh: count dynamic peers (from announcer/DHT/PEX) as they arrive.
	countedPeerCh := make(chan []tracker.Peer, 16)
	go func() {
		for {
			select {
			case batch, ok := <-peerCh:
				if !ok {
					return
				}
				s.knownPeers.Add(int32(len(batch)))
				select {
				case countedPeerCh <- batch:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	dlCfg := s.dlCfg
	if s.tf.Info.Private {
		dlCfg.DisablePEX = true
	}

	// Compute the skip-piece mask from per-file priorities. Bits for pieces
	// fully inside skipped files will be omitted from the download queue.
	// Priority changes take effect on next pause→resume; we don't hot-requeue.
	skipMask := computeSkipMaskFromRecord(s.tf, s.record.FilePriorities)

	dlErr := download.DownloadWithParams(ctx, download.DownloadParams{
		TF:          s.tf,
		PeerID:      s.peerID,
		Peers:       peers,
		PeerCh:      countedPeerCh,
		PeerSink:    peerSink,
		Path:        savePath,
		Progress:    progress,
		Cfg:         dlCfg,
		Have:        haveBitfield,
		SkipMask:    skipMask,
		WebSeedURLs: s.tf.WebSeedURLs,
		HaveBF:      uploadBF, // live bitfield for OnRequest (auto-wired in DownloadWithParams)
		OnPiece: func(index int) {
			s.mu.Lock()
			uploadBF.SetPiece(index)
			// Persist the have-bitfield so a resume picks up where we
			// left off without re-verifying already-downloaded pieces.
			s.record.Bitfield = append(s.record.Bitfield[:0], uploadBF...)
			have := popCount(uploadBF)
			bus := s.bus
			id := s.record.ID
			s.mu.Unlock()
			up.SetPiece(index)
			if bus != nil {
				bus.Publish(events.Event{
					Type:      events.TypePieceComplete,
					TorrentID: id,
					Payload: events.PieceCompletePayload{
						Index: index,
						Have:  have,
						Total: numPieces,
					},
				})
			}
		},
		OnPeerMgr: func(pm *download.PeerManager) {
			s.mu.Lock()
			s.peerMgr = pm
			bus := s.bus
			id := s.record.ID
			s.mu.Unlock()
			// Inject observation hooks; the download package itself stays free
			// of any engine/events dependency.
			pm.Bus = bus
			pm.TorrentID = id
		},
		OnPieceQueue: func(pq *download.PieceQueue) {
			s.mu.Lock()
			s.pq = pq
			s.mu.Unlock()
		},
	})
	if dlErr != nil {
		cleanup()
		return dlErr
	}

	// --- Download complete: transition to seeding ---
	cleanup() // stop download-phase announcer

	// Move from tmpDir if configured
	finalPath := filepath.Join(s.downloadDir, s.tf.Info.Name)
	if s.tmpDir != "" && savePath != finalPath {
		slog.Info("moving completed download", "id", s.record.ID, "from", savePath, "to", finalPath)
		if err := os.Rename(savePath, finalPath); err != nil {
			return fmt.Errorf("session: move to download dir: %w", err)
		}
		savePath = finalPath
		// Recreate uploader with new path
		up = download.NewUploader(s.tf, savePath, s.peerID, uploadBF)
		s.mu.Lock()
		s.uploader = up
		s.mu.Unlock()
		go up.Run(ctx)
	}

	// Mark all pieces available
	for i := range numPieces {
		up.SetPiece(i)
	}

	s.mu.Lock()
	s.record.CompletedAt = time.Now().Unix()
	s.record.SavePath = savePath
	s.mu.Unlock()
	s.setState(StateSeeding)

	slog.Info("download complete", "id", s.record.ID, "name", s.tf.Info.Name, "save_path", savePath)
	return nil
}

// phaseSeedDirect starts seeding from a known-complete file (resume path).
func (s *Session) phaseSeedDirect(ctx context.Context, savePath string) error {
	numPieces := len(s.tf.Info.PieceHashes)
	fullBF := make(peer.Bitfield, (numPieces+7)/8)
	for i := range numPieces {
		fullBF.SetPiece(i)
	}

	// Set up progress for stats
	tl := storage.TotalSize(s.tf)
	progress := download.NewProgress(numPieces, tl)
	progress.SetInitial(numPieces, tl, int64(s.tf.Info.PieceLength))
	s.mu.Lock()
	s.progress = progress
	s.record.TotalBytes = tl
	s.mu.Unlock()

	// Start uploader
	up := download.NewUploader(s.tf, savePath, s.peerID, fullBF)
	s.mu.Lock()
	s.uploader = up
	s.haveBF = fullBF
	s.mu.Unlock()
	go up.Run(ctx)

	if s.listener != nil {
		s.listener.Register(s.tf.InfoHash, s)
	}

	return s.phaseSeed(ctx)
}

// phaseSeed keeps the session alive, serving pieces until stopped.
func (s *Session) phaseSeed(ctx context.Context) error {
	slog.Info("seeding started", "id", s.record.ID, "name", s.tf.Info.Name)

	// Announce completed + start seed-phase re-announce.
	// Wire up a peerSink so tracker responses update knownPeers even while seeding.
	seedCtx, seedCancel := context.WithCancel(ctx)
	defer seedCancel()

	peerSink := make(chan []tracker.Peer, 16)
	go func() {
		for {
			select {
			case batch, ok := <-peerSink:
				if !ok {
					return
				}
				s.knownPeers.Add(int32(len(batch)))
			case <-seedCtx.Done():
				return
			}
		}
	}()

	announcer := s.newAnnouncer(peerSink)
	announcer.AnnounceCompleted(ctx)
	go announcer.Run(seedCtx)

	// Block until context is cancelled (pause/stop/shutdown)
	<-ctx.Done()

	// Cleanup
	if s.listener != nil {
		s.listener.Unregister(s.tf.InfoHash)
	}
	s.mu.Lock()
	shouldComplete := s.record.State == StateSeeding
	s.uploader = nil
	s.peerMgr = nil
	s.pq = nil
	s.mu.Unlock()
	if shouldComplete {
		s.setState(StateComplete)
	}

	return nil
}

// newAnnouncer creates an announcer with the session's current state.
// peerSink may be nil (e.g. private torrent with PEX disabled, or callers that don't need peer injection).
func (s *Session) newAnnouncer(peerSink chan<- []tracker.Peer) *Announcer {
	return NewAnnouncer(AnnounceConfig{
		TF:        s.tf,
		PeerID:    s.peerID,
		Port:      s.port,
		NumWant:   s.numWant,
		DHT:       s.dht,
		PeerSink:  peerSink,
		Bus:       s.bus,
		TorrentID: s.record.ID,
		Downloaded: func() int64 {
			s.mu.RLock()
			p := s.progress
			s.mu.RUnlock()
			if p != nil {
				return p.Snap().Downloaded
			}
			return s.record.TotalBytes // seeding: all downloaded
		},
		Left: func() int64 {
			s.mu.RLock()
			p := s.progress
			s.mu.RUnlock()
			if p != nil {
				snap := p.Snap()
				return snap.Total - snap.Downloaded
			}
			return 0 // seeding: nothing left
		},
	})
}

// HandleIncoming implements IncomingPeerHandler. Routes to the uploader if seeding.
func (s *Session) HandleIncoming(conn net.Conn, peerHS *peer.Handshake) {
	s.mu.RLock()
	u := s.uploader
	s.mu.RUnlock()
	if u == nil {
		_ = conn.Close()
		return
	}
	u.HandleIncoming(conn, peerHS)
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
		s.mu.Lock()
		s.tf = tf
		s.mu.Unlock()
		return nil
	}

	// Try parsing source as a .torrent file path
	if data, err := os.ReadFile(s.record.Source); err == nil {
		if tf, err := torrent.Parse(data); err == nil {
			s.mu.Lock()
			s.tf = tf
			s.record.TorrentData = data
			s.record.Name = tf.Info.Name
			s.mu.Unlock()
			return nil
		}
	}

	// Must be a magnet URI
	s.setState(StateMetadata)

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

	var (
		tf       *torrent.TorrentFile
		allPeers []tracker.Peer
	)
	backoff := metadataRetryInitial
	for attempt := 1; ; attempt++ {
		var peers []tracker.Peer
		tf, peers = s.fetchMetadataAttempt(ctx, m)
		allPeers = append(allPeers, peers...)
		if tf != nil {
			slog.Info("metadata fetched", "id", s.record.ID, "name", tf.Info.Name, "attempt", attempt)
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		slog.Warn("metadata fetch attempt failed, retrying",
			"id", s.record.ID, "attempt", attempt, "next_in", backoff,
		)
		if s.bus != nil {
			s.bus.Publish(events.Event{
				Type:      events.TypeMetadataAttemptFailed,
				TorrentID: s.record.ID,
				Payload: events.MetadataAttemptFailedPayload{
					AttemptCount: attempt,
					NextRetryIn:  int(backoff / time.Second),
					// fetchMetadataAttempt swallows per-peer errors and
					// reports aggregated counters via slog; surfacing a single
					// representative error string is not meaningful here.
					Error: "",
				},
			})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < metadataRetryMax {
			backoff *= 2
			if backoff > metadataRetryMax {
				backoff = metadataRetryMax
			}
		}
	}

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
	torrentData, err := bencode.Encode(fullDict)
	if err != nil {
		return fmt.Errorf("session: encode torrent data: %w", err)
	}

	s.mu.Lock()
	s.tf = tf
	s.record.TorrentData = torrentData
	s.record.Name = tf.Info.Name
	s.cachedPeers = allPeers
	s.mu.Unlock()

	return nil
}

// fetchMetadataAttempt runs one round of peer discovery (trackers + DHT +
// x.pe) and tries to fetch the metainfo from any responding peer within
// metadataAttemptTTL. Returns the parsed TorrentFile on success (with the
// peers seen during the attempt), or nil with the peers tried.
func (s *Session) fetchMetadataAttempt(ctx context.Context, m *magnet.Magnet) (*torrent.TorrentFile, []tracker.Peer) {
	var peerMu sync.Mutex
	var allPeers []tracker.Peer
	peerCh := make(chan []tracker.Peer, 10)

	var wg sync.WaitGroup

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
				IPv6:        tracker.LocalIPv6(),
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

	go func() {
		wg.Wait()
		close(peerCh)
	}()

	metaCtx, metaCancel := context.WithTimeout(ctx, metadataAttemptTTL)
	defer metaCancel()

	resultCh := make(chan *torrent.TorrentFile, 1)
	metaSem := make(chan struct{}, 50)

	var (
		metaWg        sync.WaitGroup
		statDialFail  atomic.Int32
		statNoExt     atomic.Int32
		statNoMeta    atomic.Int32
		statFetchFail atomic.Int32
		statAttempted atomic.Int32
	)

	go func() {
		for {
			select {
			case <-metaCtx.Done():
				metaWg.Wait()
				close(resultCh)
				return
			case batch, ok := <-peerCh:
				if !ok {
					metaWg.Wait()
					close(resultCh)
					return
				}
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
						statAttempted.Add(1)
						tf, ferr := fetchMetadataFromPeer(metaCtx, p, m.InfoHash, s.peerID, s.enableUTP)
						if ferr != nil {
							msg := ferr.Error()
							switch {
							case isDialError(msg):
								statDialFail.Add(1)
							case msg == "peer does not support extensions":
								statNoExt.Add(1)
							case msg == "peer reported no metadata":
								statNoMeta.Add(1)
							default:
								statFetchFail.Add(1)
							}
							return
						}
						select {
						case resultCh <- tf:
							metaCancel()
						default:
						}
					}(p)
				}
			}
		}
	}()

	tf, ok := <-resultCh
	peerMu.Lock()
	tried := len(allPeers)
	peers := append([]tracker.Peer(nil), allPeers...)
	peerMu.Unlock()
	if !ok {
		slog.Warn("metadata attempt yielded no result",
			"id", s.record.ID,
			"peers_found", tried,
			"attempted", statAttempted.Load(),
			"dial_fail", statDialFail.Load(),
			"no_ext", statNoExt.Load(),
			"no_meta", statNoMeta.Load(),
			"fetch_fail", statFetchFail.Load(),
		)
		if s.bus != nil {
			// AttemptCount and NextRetryIn live in the outer resolveMetadata
			// loop; this inner emit signals a single metadata round failed.
			// TODO: thread attempt number through fetchMetadataAttempt args
			// if a future consumer needs it.
			errMsg := fmt.Sprintf("attempted=%d dial_fail=%d no_ext=%d no_meta=%d fetch_fail=%d peers_found=%d",
				statAttempted.Load(), statDialFail.Load(), statNoExt.Load(),
				statNoMeta.Load(), statFetchFail.Load(), tried,
			)
			s.bus.Publish(events.Event{
				Type:      events.TypeMetadataAttemptFailed,
				TorrentID: s.record.ID,
				Payload: events.MetadataAttemptFailedPayload{
					AttemptCount: 0,
					NextRetryIn:  0,
					Error:        errMsg,
				},
			})
		}
		return nil, peers
	}
	return tf, peers
}

// dhtLookup performs a DHT GetPeers using the shared DHT. If the shared
// DHT is unavailable (engine started with DisableDHT or DHT failed to
// start), returns nil — we never spin up a temporary DHT just for one
// lookup, since that would leak DNS / Bootstrap latency into every magnet
// add and leave goroutines running past Stop().
//
// Tests inject dhtGetPeersFn to bypass the real DHT. Production code paths
// leave dhtGetPeersFn nil and fall through to s.dht.GetPeers.
func (s *Session) dhtLookup(infoHash [20]byte) []tracker.Peer {
	getPeers := s.dhtGetPeersFn
	if getPeers == nil {
		if s.dht == nil {
			return nil
		}
		getPeers = s.dht.GetPeers
	}
	peerAddrs, err := getPeers(infoHash)
	if err != nil {
		slog.Debug("dht GetPeers failed", "info_hash", hex.EncodeToString(infoHash[:]), "error", err)
		if s.bus != nil {
			s.bus.Publish(events.Event{
				Type:      events.TypeDHTLookupFailed,
				TorrentID: s.record.ID,
				Payload:   events.DHTLookupFailedPayload{Error: err.Error()},
			})
		}
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

// findPeers discovers peers from trackers and DHT, retrying with
// exponential backoff until at least one peer is found or ctx is done.
// Matches Deluge's behavior of staying in the search state rather than
// failing the torrent.
func (s *Session) findPeers(ctx context.Context) ([]tracker.Peer, error) {
	if s.tf == nil {
		return nil, fmt.Errorf("session: no torrent file")
	}

	backoff := peerSearchInitial
	for attempt := 1; ; attempt++ {
		peers := s.findPeersAttempt(ctx)
		if len(peers) > 0 {
			return peers, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		slog.Warn("no peers found, retrying",
			"id", s.record.ID, "attempt", attempt, "next_in", backoff,
		)
		if s.bus != nil {
			s.bus.Publish(events.Event{
				Type:      events.TypePeerSearchFailed,
				TorrentID: s.record.ID,
				Payload: events.PeerSearchFailedPayload{
					AttemptCount: attempt,
					NextRetryIn:  int(backoff / time.Second),
					// findPeersAttempt aggregates per-tracker errors via slog
					// rather than returning them; no single error string here.
					Error: "",
				},
			})
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < peerSearchMax {
			backoff *= 2
			if backoff > peerSearchMax {
				backoff = peerSearchMax
			}
		}
	}
}

// findPeersAttempt runs one parallel tracker + DHT query and returns the
// (filtered) peer list. Returns an empty slice if no peers were found.
func (s *Session) findPeersAttempt(ctx context.Context) []tracker.Peer {
	var allPeers []tracker.Peer
	var mu sync.Mutex
	var wg sync.WaitGroup

	trackers := s.tf.TrackerURLs()

	slog.Info("finding peers", "id", s.record.ID, "trackers", len(trackers))

	for _, tr := range trackers {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			tl := storage.TotalSize(s.tf)
			req := tracker.AnnounceRequest{
				AnnounceURL: tr,
				InfoHash:    s.tf.InfoHash,
				PeerID:      s.peerID,
				Port:        s.port,
				Left:        tl,
				Event:       tracker.EventStarted,
				NumWant:     s.numWant,
				IPv6:        tracker.LocalIPv6(),
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

	if !s.tf.Info.Private {
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
	}

	// Wait for all sources or ctx cancellation.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}

	return tracker.FilterPrivatePeers(allPeers)
}

func isDialError(msg string) bool {
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "context deadline exceeded")
}

func fetchMetadataFromPeer(ctx context.Context, p tracker.Peer, infoHash, peerID [20]byte, enableUTP bool) (*torrent.TorrentFile, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var conn net.Conn
	var err error
	if enableUTP {
		conn, err = utp.DialTimeout(p.String(), 5*time.Second)
		if err != nil {
			// uTP failed, fall back to TCP
			dialer := net.Dialer{Timeout: 5 * time.Second}
			conn, err = dialer.DialContext(ctx, "tcp", p.String())
		}
	} else {
		dialer := net.Dialer{Timeout: 5 * time.Second}
		conn, err = dialer.DialContext(ctx, "tcp", p.String())
	}
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
