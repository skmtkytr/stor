package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/skmtkytr/stor/dht"
	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/events"
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
)

// MaxSessions is the maximum number of torrents the engine will track.
const MaxSessions = 10000

// Config holds engine configuration.
type Config struct {
	DownloadDir string
	TmpDir      string // temp dir for in-progress downloads; empty = use DownloadDir
	StatePath   string // path to state.json
	ListenPort  uint16
	MaxActive   int // max concurrent downloading torrents

	// Peer connection tuning
	MaxPeers    int // max concurrent peers per torrent (default: 100)
	MaxPipeline int // outstanding requests per peer (default: 16)
	DialTimeout int // peer dial timeout in seconds (default: 3)

	// Tracker tuning
	NumWant int // peers requested from tracker (default: 200)

	// DHT tuning
	DHTAlpha int // DHT lookup concurrency (default: 8)

	// Transport
	EnableUTP bool // enable uTP transport (try uTP before TCP)

	// Testing flags
	DisableDHT      bool // skip DHT startup
	DisableListener bool // skip peer TCP listener
}

// TorrentInfo is the full info returned to API clients.
type TorrentInfo struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Source        string                `json:"source"`
	State         State                 `json:"state"`
	QueuePosition int                   `json:"queue_position"`
	Progress      download.ProgressSnap `json:"progress"`
	SavePath      string                `json:"save_path"`
	TotalBytes    int64                 `json:"total_bytes"`
	AddedAt       int64                 `json:"added_at"`
	CompletedAt   int64                 `json:"completed_at"`
	Error         string                `json:"error,omitempty"`
}

// FileEntry describes one file in a torrent.
type FileEntry struct {
	Path       string `json:"path"`
	Length     int64  `json:"length"`
	Downloaded int64  `json:"downloaded"`
	// Priority is 0 (normal) or -1 (skip). See PriorityNormal / PrioritySkip.
	Priority int8 `json:"priority"`
}

// EngineConfig is the subset of Config exposed to API clients.
type EngineConfig struct {
	DownloadDir string `json:"download_dir"`
	TmpDir      string `json:"tmp_dir"`
	MaxActive   int    `json:"max_active"`
	MaxPeers    int    `json:"max_peers"`
	MaxPipeline int    `json:"max_pipeline"`
	DialTimeout int    `json:"dial_timeout"`
	NumWant     int    `json:"numwant"`
	LogLevel    string `json:"log_level"`
	EnableUTP   bool   `json:"enable_utp"`
}

// EngineStats is global daemon stats.
type EngineStats struct {
	TotalDownSpeed  int64        `json:"total_down_speed"`
	TotalUpSpeed    int64        `json:"total_up_speed"`
	TotalDownloaded int64        `json:"total_downloaded"`
	TotalUploaded   int64        `json:"total_uploaded"`
	ActiveTorrents  int          `json:"active_torrents"`
	SeedingTorrents int          `json:"seeding_torrents"`
	TotalTorrents   int          `json:"total_torrents"`
	MaxActive       int          `json:"max_active"`
	TotalPeers      int          `json:"total_peers"`
	TotalKnownPeers int          `json:"total_known_peers"`
	DHTNodes        int          `json:"dht_nodes"`
	FreeSpace       int64        `json:"free_space"`
	Config          EngineConfig `json:"config"`
}

// Engine manages all torrent sessions.
type Engine struct {
	mu       sync.RWMutex
	cfg      Config
	peerID   [20]byte
	store    *Store
	sessions map[string]*Session
	dht      *dht.DHT
	listener *PeerListener
	ctx      context.Context
	cancel   context.CancelFunc

	// Global dial semaphore shared across all torrents
	dialSem chan struct{}

	// Periodic save
	saveTicker *time.Ticker
	saveStop   chan struct{}

	// Tracks running session goroutines so Stop can drain them.
	sessionWG sync.WaitGroup

	// Observation event bus. Always non-nil.
	bus *events.Bus
}

// Bus returns the engine's event bus. Subscribers receive observation events
// (state transitions, peer connect/disconnect, tracker replies, piece
// completion, etc.) until they cancel their context or the engine stops.
func (e *Engine) Bus() *events.Bus { return e.bus }

// startSession launches a session, tracking it so Stop can wait for it.
func (e *Engine) startSession(s *Session) {
	e.sessionWG.Add(1)
	s.Start(e.ctx, func(id string) {
		defer e.sessionWG.Done()
		e.onSessionDone(id)
	})
}

// New creates a new engine.
func New(cfg Config) (*Engine, error) {
	if cfg.MaxActive <= 0 {
		cfg.MaxActive = 5
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 6881
	}
	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = download.DefaultMaxPeers
	}
	if cfg.MaxPipeline <= 0 {
		cfg.MaxPipeline = 16
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 3
	}
	if cfg.NumWant <= 0 {
		cfg.NumWant = 200
	}
	if cfg.DHTAlpha <= 0 {
		cfg.DHTAlpha = 8
	}

	var peerID [20]byte
	copy(peerID[:], "-ST0001-")
	_, _ = rand.Read(peerID[8:])

	store := NewStore(cfg.StatePath)
	if err := store.Load(); err != nil {
		return nil, fmt.Errorf("engine: load store: %w", err)
	}

	slog.Info("engine created",
		"download_dir", cfg.DownloadDir,
		"max_active", cfg.MaxActive,
		"max_peers", cfg.MaxPeers,
		"listen_port", cfg.ListenPort,
	)

	ctx, cancel := context.WithCancel(context.Background())

	// Global dial semaphore: limits total concurrent outgoing dials across all torrents
	const maxGlobalDials = 50
	dialSem := make(chan struct{}, maxGlobalDials)

	e := &Engine{
		cfg:      cfg,
		peerID:   peerID,
		store:    store,
		sessions: make(map[string]*Session),
		dialSem:  dialSem,
		ctx:      ctx,
		cancel:   cancel,
		bus:      events.New(),
	}

	return e, nil
}

// Start restores persisted torrents and starts periodic save.
func (e *Engine) Start() error {
	if err := os.MkdirAll(e.cfg.DownloadDir, 0o755); err != nil {
		return fmt.Errorf("engine: create download dir: %w", err)
	}
	if e.cfg.TmpDir != "" {
		if err := os.MkdirAll(e.cfg.TmpDir, 0o755); err != nil {
			return fmt.Errorf("engine: create tmp dir: %w", err)
		}
	}

	// Start shared DHT node. Bootstrap (DNS lookups + ~5s wait for bootstrap
	// node responses + up to 15s self-lookup) runs in the background so the
	// daemon HTTP server can come up immediately. Persisted nodes are loaded
	// synchronously so warm starts have a usable table right away.
	if !e.cfg.DisableDHT {
		d, err := dht.New(":0", dht.WithAlpha(e.cfg.DHTAlpha))
		if err == nil {
			loaded, loadErr := d.LoadNodes(e.dhtNodesPath())
			if loadErr != nil {
				slog.Warn("dht: failed to load persisted nodes", "error", loadErr)
			} else if loaded > 0 {
				slog.Info("dht: loaded persisted nodes", "count", loaded)
			}
			e.dht = d
			slog.Info("dht starting", "persisted_nodes", loaded)
			go func() {
				_ = d.Bootstrap(dhtBootstrapNodes)
				slog.Info("dht bootstrap complete", "table_size", d.TableSize())
			}()
		} else {
			slog.Warn("dht failed to start", "error", err)
		}
	}

	// Start peer listener for incoming connections (TCP, and optionally uTP)
	if !e.cfg.DisableListener {
		addr := fmt.Sprintf(":%d", e.cfg.ListenPort)
		var pl *PeerListener
		var err error
		if e.cfg.EnableUTP {
			pl, err = NewPeerListenerWithUTP(addr)
		} else {
			pl, err = NewPeerListener(addr)
		}
		if err != nil {
			slog.Warn("peer listener failed to start", "error", err)
		} else {
			e.listener = pl
			go pl.Run()
			slog.Info("peer listener started", "addr", pl.Addr(), "utp", e.cfg.EnableUTP)
		}
	}

	// Restore sessions from store
	records := e.store.All()
	slog.Info("restoring sessions", "count", len(records))
	for _, r := range records {
		s := NewSession(r, e.peerID, e.cfg.DownloadDir, e.cfg.TmpDir, e.cfg.ListenPort, e.downloadConfig(), e.cfg.NumWant, e.dht, e.listener, e.bus)

		e.sessions[r.ID] = s

		// Auto-resume active torrents (downloading, metadata, queued, or seeding)
		if r.State == StateDownloading || r.State == StateMetadata || r.State == StateVerifying || r.State == StateAdding || r.State == StateSeeding {
			slog.Info("resuming torrent", "id", r.ID, "name", r.Name, "state", r.State)
			if e.activeCount() < e.cfg.MaxActive {
				e.startSession(s)
			}
		}
	}

	// Periodic save every 30 seconds
	e.saveTicker = time.NewTicker(30 * time.Second)
	e.saveStop = make(chan struct{})
	go func() {
		for {
			select {
			case <-e.saveTicker.C:
				e.saveState()
			case <-e.saveStop:
				return
			}
		}
	}()

	return nil
}

// Stop gracefully shuts down all sessions and persists state.
func (e *Engine) Stop() error {
	slog.Info("engine stopping", "sessions", len(e.sessions))
	e.cancel()

	if e.saveTicker != nil {
		e.saveTicker.Stop()
		close(e.saveStop)
	}

	if e.listener != nil {
		_ = e.listener.Close()
	}

	if e.dht != nil {
		if err := e.dht.SaveNodes(e.dhtNodesPath()); err != nil {
			slog.Warn("dht: failed to save nodes", "error", err)
		} else {
			slog.Info("dht: saved nodes", "count", e.dht.TableSize())
		}
		_ = e.dht.Close()
	}

	// Wait for in-flight session goroutines to finish so their final
	// onSessionDone (and saveState) doesn't race with shutdown / cleanup.
	e.sessionWG.Wait()

	e.saveState()
	// Close the bus after sessions drain so any final emissions land.
	e.bus.Close()
	slog.Info("engine stopped, state saved")
	return nil
}

// dhtNodesPath returns the file path for persisted DHT routing table nodes.
func (e *Engine) dhtNodesPath() string {
	return filepath.Join(filepath.Dir(e.cfg.StatePath), "dht_nodes.dat")
}

// AddTorrent adds a torrent from a magnet URI or .torrent file path.
func (e *Engine) AddTorrent(source string) (string, error) {
	id, err := InfoHashHex(source)
	if err != nil {
		return "", fmt.Errorf("engine: %w", err)
	}

	e.mu.Lock()

	if _, exists := e.sessions[id]; exists {
		e.mu.Unlock()
		slog.Debug("torrent already exists", "id", id)
		return id, nil // already exists
	}

	if len(e.sessions) >= MaxSessions {
		e.mu.Unlock()
		return "", fmt.Errorf("engine: session limit reached (%d)", MaxSessions)
	}

	slog.Info("adding torrent", "id", id, "source", source)

	record := &TorrentRecord{
		ID:            id,
		Source:        source,
		State:         StateAdding,
		QueuePosition: e.nextQueuePosition(),
		AddedAt:       time.Now().Unix(),
	}

	// Pre-populate name and torrent data for .torrent files
	if data, readErr := os.ReadFile(source); readErr == nil {
		if tf, parseErr := torrent.Parse(data); parseErr == nil {
			record.Name = tf.Info.Name
			record.TorrentData = data
			record.TotalBytes = storage.TotalSize(tf)
		}
	}

	s := NewSession(record, e.peerID, e.cfg.DownloadDir, e.cfg.TmpDir, e.cfg.ListenPort, e.downloadConfig(), e.cfg.NumWant, e.dht, e.listener, e.bus)
	e.sessions[id] = s
	e.saveStateLocked()

	e.startQueuedLocked()
	emittedName := record.Name
	e.mu.Unlock()

	// Emit TypeTorrentAdded after releasing e.mu so subscribers can call back
	// into Engine without a self-deadlock.
	e.bus.Publish(events.Event{
		Type:      events.TypeTorrentAdded,
		TorrentID: id,
		Payload: events.TorrentAddedPayload{
			Source: source,
			Name:   emittedName,
		},
	})

	return id, nil
}

// AddTorrentFile adds a torrent from raw .torrent data.
func (e *Engine) AddTorrentFile(data []byte) (string, error) {
	// Write to a temp file and use it as source
	tmp, err := os.CreateTemp("", "stor-*.torrent")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	_ = tmp.Close()

	id, err := e.AddTorrent(tmpName)
	if err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return id, nil
}

// RemoveTorrent removes a torrent. If deleteFiles is true, also deletes downloaded data.
func (e *Engine) RemoveTorrent(id string, deleteFiles bool) error {
	e.mu.Lock()
	s, ok := e.sessions[id]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("engine: torrent %s not found", id)
	}

	slog.Info("removing torrent", "id", id, "delete_files", deleteFiles)
	s.Pause()
	delete(e.sessions, id)
	e.store.Delete(id)
	e.mu.Unlock()

	_ = e.store.Save()

	if deleteFiles {
		r := s.Record()
		if r.SavePath != "" {
			slog.Info("deleting torrent files", "id", id, "path", r.SavePath)
			_ = os.RemoveAll(r.SavePath)
		}
		// Also clean up tmpDir if configured (in-progress downloads live there)
		if e.cfg.TmpDir != "" && r.Name != "" {
			tmpPath := filepath.Join(e.cfg.TmpDir, r.Name)
			if tmpPath != r.SavePath {
				_ = os.RemoveAll(tmpPath)
			}
		}
	}

	e.bus.Publish(events.Event{
		Type:      events.TypeTorrentRemoved,
		TorrentID: id,
		Payload:   events.TorrentRemovedPayload{DeletedFiles: deleteFiles},
	})

	e.startQueued()
	return nil
}

// PauseTorrent pauses a torrent.
func (e *Engine) PauseTorrent(id string) error {
	e.mu.RLock()
	s, ok := e.sessions[id]
	e.mu.RUnlock()

	if !ok {
		return fmt.Errorf("engine: torrent %s not found", id)
	}

	slog.Info("pausing torrent", "id", id)
	// Emit the *requested* event at the API entry point. The *confirmed*
	// TypeTorrentPaused fires later from setState() once the state actually
	// transitions to StatePaused (Deluge `on_alert_torrent_paused` parity).
	e.bus.Publish(events.Event{
		Type:      events.TypeTorrentPauseRequested,
		TorrentID: id,
	})
	s.Pause()
	e.saveState()
	e.startQueued()
	return nil
}

// ResumeTorrent resumes a paused torrent.
func (e *Engine) ResumeTorrent(id string) error {
	e.mu.RLock()
	s, ok := e.sessions[id]
	e.mu.RUnlock()

	if !ok {
		return fmt.Errorf("engine: torrent %s not found", id)
	}

	r := s.Record()
	if r.State != StatePaused && r.State != StateError {
		return fmt.Errorf("engine: torrent %s is %s, cannot resume", id, r.State)
	}

	slog.Info("resuming torrent", "id", id, "previous_state", r.State)
	// Emit the *requested* event at the API entry point. The *confirmed*
	// TypeTorrentResumed fires later from setState() once the state actually
	// transitions out of StatePaused into StateDownloading (Deluge
	// `on_alert_torrent_resumed` parity).
	e.bus.Publish(events.Event{
		Type:      events.TypeTorrentResumeRequested,
		TorrentID: id,
	})

	s.mu.Lock()
	s.record.Error = ""
	s.mu.Unlock()
	s.setState(StateAdding)

	if e.activeCount() < e.cfg.MaxActive {
		e.startSession(s)
	}

	e.saveState()
	return nil
}

// GetTorrent returns info about a single torrent.
func (e *Engine) GetTorrent(id string) (*TorrentInfo, error) {
	e.mu.RLock()
	s, ok := e.sessions[id]
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("engine: torrent %s not found", id)
	}

	return e.sessionToInfo(s), nil
}

// TorrentFiles returns the file list for a torrent. An empty slice is
// returned if the metadata has not yet been resolved (magnet without
// metainfo yet).
func (e *Engine) TorrentFiles(id string) ([]FileEntry, error) {
	e.mu.RLock()
	s, ok := e.sessions[id]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("engine: torrent %s not found", id)
	}
	return s.Files(), nil
}

// TorrentPeers returns the live peer list for a torrent, or an error if the
// torrent is not found. An empty slice is returned if the torrent has no
// active connections (e.g. paused).
func (e *Engine) TorrentPeers(id string) ([]download.PeerSnap, error) {
	e.mu.RLock()
	s, ok := e.sessions[id]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("engine: torrent %s not found", id)
	}
	return s.PeerList(), nil
}

// SetFilePriority sets the priority for a single file in a torrent. The
// change is persisted to the record so it survives restarts. Priority
// changes take effect on next pause→resume — no hot requeue is attempted.
//
// Valid priorities: 0 (normal), -1 (skip). Any other value is rejected.
func (e *Engine) SetFilePriority(id string, fileIndex int, priority int8) error {
	if priority != PriorityNormal && priority != PrioritySkip {
		return fmt.Errorf("engine: invalid priority %d (must be 0 or -1)", priority)
	}
	if fileIndex < 0 {
		return fmt.Errorf("engine: invalid file_index %d", fileIndex)
	}

	e.mu.RLock()
	s, ok := e.sessions[id]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("engine: torrent %s not found", id)
	}

	// Determine file count for the torrent. Require metadata to be
	// resolved, otherwise we can't bounds-check fileIndex.
	s.mu.Lock()
	tf := s.tf
	if tf == nil && len(s.record.TorrentData) > 0 {
		if parsed, err := torrent.Parse(s.record.TorrentData); err == nil {
			tf = parsed
		}
	}
	if tf == nil {
		s.mu.Unlock()
		return fmt.Errorf("engine: torrent %s metadata not yet resolved", id)
	}

	fileCount := len(tf.Info.Files)
	if fileCount == 0 {
		fileCount = 1 // single-file torrent has one implicit file
	}
	if fileIndex >= fileCount {
		s.mu.Unlock()
		return fmt.Errorf("engine: file_index %d out of range (file count %d)", fileIndex, fileCount)
	}

	// Grow the priorities slice if needed; default entries stay at 0 (normal).
	if len(s.record.FilePriorities) < fileCount {
		grown := make([]int8, fileCount)
		copy(grown, s.record.FilePriorities)
		s.record.FilePriorities = grown
	}
	s.record.FilePriorities[fileIndex] = priority

	// Hot requeue: if a download is active, push the new skip mask to the
	// live PieceQueue so the change takes effect without pause/resume.
	// Pieces already in-flight complete; future Picks honour the new
	// mask. Pieces that were filtered out at phaseDownload construction
	// (initially-skipped priorities at resume time) stay excluded — those
	// still need pause/resume to reappear.
	pq := s.pq
	var newMask peer.Bitfield
	if pq != nil {
		newMask = computeSkipMaskFromRecord(tf, s.record.FilePriorities)
	}
	s.mu.Unlock()

	if pq != nil {
		pq.UpdateSkipMask(newMask)
	}

	// Persist immediately so the change survives a crash before the next
	// periodic save.
	e.saveState()
	return nil
}

// ListTorrents returns info about all torrents.
func (e *Engine) ListTorrents() []*TorrentInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]*TorrentInfo, 0, len(e.sessions))
	for _, s := range e.sessions {
		result = append(result, e.sessionToInfo(s))
	}
	return result
}

// GetStats returns global engine stats.
func (e *Engine) GetStats() *EngineStats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	stats := &EngineStats{
		TotalTorrents: len(e.sessions),
		MaxActive:     e.cfg.MaxActive,
	}
	for _, s := range e.sessions {
		snap := s.Snap()
		stats.TotalPeers += int(snap.ActivePeers)
		stats.TotalKnownPeers += snap.KnownPeers
		stats.TotalDownloaded += snap.Downloaded
		stats.TotalUploaded += snap.Uploaded
		stats.TotalUpSpeed += snap.UpSpeed
		if snap.State == string(StateDownloading) || snap.State == string(StateVerifying) {
			stats.ActiveTorrents++
			stats.TotalDownSpeed += snap.DownSpeed
		}
		if snap.State == string(StateSeeding) {
			stats.SeedingTorrents++
		}
	}

	if e.dht != nil {
		stats.DHTNodes = e.dht.TableSize()
	}

	stats.FreeSpace = diskFreeSpace(e.cfg.DownloadDir)

	stats.Config = EngineConfig{
		DownloadDir: e.cfg.DownloadDir,
		TmpDir:      e.cfg.TmpDir,
		MaxActive:   e.cfg.MaxActive,
		MaxPeers:    e.cfg.MaxPeers,
		MaxPipeline: e.cfg.MaxPipeline,
		DialTimeout: e.cfg.DialTimeout,
		NumWant:     e.cfg.NumWant,
		LogLevel:    "", // filled by daemon layer
		EnableUTP:   e.cfg.EnableUTP,
	}

	return stats
}

// PeerID returns the engine's peer ID as hex string.
func (e *Engine) PeerID() string {
	return hex.EncodeToString(e.peerID[:])
}

// QueueTop moves a torrent to the top of the queue (position 0).
func (e *Engine) QueueTop(id string) error {
	return e.setQueuePosition(id, 0)
}

// QueueUp moves a torrent one position up in the queue.
func (e *Engine) QueueUp(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.sessions[id]
	if !ok {
		return fmt.Errorf("engine: torrent %s not found", id)
	}
	r := s.Record()
	if r.QueuePosition <= 0 {
		return nil
	}
	return e.swapQueuePositions(r.QueuePosition, r.QueuePosition-1)
}

// QueueDown moves a torrent one position down in the queue.
func (e *Engine) QueueDown(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.sessions[id]
	if !ok {
		return fmt.Errorf("engine: torrent %s not found", id)
	}
	r := s.Record()
	maxPos := e.maxQueuePosition()
	if r.QueuePosition >= maxPos {
		return nil
	}
	return e.swapQueuePositions(r.QueuePosition, r.QueuePosition+1)
}

// QueueBottom moves a torrent to the bottom of the queue.
func (e *Engine) QueueBottom(id string) error {
	e.mu.Lock()
	maxPos := e.maxQueuePosition()
	// Hold the lock and call the internal version to avoid TOCTOU race.
	// setQueuePosition also acquires e.mu, so call the logic directly.
	s, ok := e.sessions[id]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("engine: torrent %s not found", id)
	}
	oldPos := s.Record().QueuePosition
	if oldPos == maxPos {
		e.mu.Unlock()
		return nil
	}
	// Shift items between (oldPos, maxPos] up by 1
	for _, other := range e.sessions {
		if other == s {
			continue
		}
		or := other.Record()
		if or.QueuePosition > oldPos && or.QueuePosition <= maxPos {
			other.mu.Lock()
			other.record.QueuePosition--
			other.mu.Unlock()
		}
	}
	s.mu.Lock()
	s.record.QueuePosition = maxPos
	s.mu.Unlock()
	e.mu.Unlock()
	return nil
}

func (e *Engine) setQueuePosition(id string, newPos int) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.sessions[id]
	if !ok {
		return fmt.Errorf("engine: torrent %s not found", id)
	}

	oldPos := s.Record().QueuePosition
	if oldPos == newPos {
		return nil
	}

	// Shift other torrents
	for _, other := range e.sessions {
		if other == s {
			continue
		}
		or := other.Record()
		if oldPos < newPos {
			// Moving down: shift items between (oldPos, newPos] up by 1
			if or.QueuePosition > oldPos && or.QueuePosition <= newPos {
				other.mu.Lock()
				other.record.QueuePosition--
				other.mu.Unlock()
			}
		} else {
			// Moving up: shift items between [newPos, oldPos) down by 1
			if or.QueuePosition >= newPos && or.QueuePosition < oldPos {
				other.mu.Lock()
				other.record.QueuePosition++
				other.mu.Unlock()
			}
		}
	}

	s.mu.Lock()
	s.record.QueuePosition = newPos
	s.mu.Unlock()

	e.saveStateLocked()
	// startQueued needs lock, but we already hold it — call the inner logic directly
	e.startQueuedLocked()
	return nil
}

func (e *Engine) swapQueuePositions(posA, posB int) error {
	var sa, sb *Session
	for _, s := range e.sessions {
		r := s.Record()
		if r.QueuePosition == posA {
			sa = s
		}
		if r.QueuePosition == posB {
			sb = s
		}
	}
	if sa != nil {
		sa.mu.Lock()
		sa.record.QueuePosition = posB
		sa.mu.Unlock()
	}
	if sb != nil {
		sb.mu.Lock()
		sb.record.QueuePosition = posA
		sb.mu.Unlock()
	}
	e.saveStateLocked()
	e.startQueuedLocked()
	return nil
}

func (e *Engine) nextQueuePosition() int {
	return e.maxQueuePosition() + 1
}

func (e *Engine) maxQueuePosition() int {
	maxPos := -1
	for _, s := range e.sessions {
		r := s.Record()
		if r.QueuePosition > maxPos {
			maxPos = r.QueuePosition
		}
	}
	return maxPos
}

// SetMaxActive changes the max concurrent downloads and adjusts the queue.
func (e *Engine) SetMaxActive(n int) {
	if n < 1 {
		n = 1
	}
	e.mu.Lock()
	e.cfg.MaxActive = n
	e.mu.Unlock()
	e.startQueued()
}

// MaxActive returns the current max concurrent downloads.
func (e *Engine) MaxActive() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg.MaxActive
}

// SetDownloadDir changes the download directory for new torrents.
func (e *Engine) SetDownloadDir(dir string) {
	e.mu.Lock()
	e.cfg.DownloadDir = dir
	e.mu.Unlock()
}

// SetTmpDir changes the temp directory for in-progress downloads.
func (e *Engine) SetTmpDir(dir string) {
	e.mu.Lock()
	e.cfg.TmpDir = dir
	e.mu.Unlock()
}

// SetMaxPeers changes the max peers per torrent (applies to new sessions).
func (e *Engine) SetMaxPeers(n int) {
	e.mu.Lock()
	e.cfg.MaxPeers = n
	e.mu.Unlock()
}

// SetMaxPipeline changes the pipeline depth (applies to new sessions).
func (e *Engine) SetMaxPipeline(n int) {
	e.mu.Lock()
	e.cfg.MaxPipeline = n
	e.mu.Unlock()
}

// SetDialTimeout changes the peer dial timeout (applies to new sessions).
func (e *Engine) SetDialTimeout(n int) {
	e.mu.Lock()
	e.cfg.DialTimeout = n
	e.mu.Unlock()
}

// SetNumWant changes the number of peers requested from trackers.
func (e *Engine) SetNumWant(n int) {
	e.mu.Lock()
	e.cfg.NumWant = n
	e.mu.Unlock()
}

// SetEnableUTP enables or disables uTP transport.
func (e *Engine) SetEnableUTP(b bool) {
	e.mu.Lock()
	e.cfg.EnableUTP = b
	e.mu.Unlock()
}

func (e *Engine) sessionToInfo(s *Session) *TorrentInfo {
	r := s.Record()
	snap := s.Snap()
	return &TorrentInfo{
		ID:            r.ID,
		Name:          r.Name,
		Source:        r.Source,
		State:         r.State,
		QueuePosition: r.QueuePosition,
		Progress:      snap,
		SavePath:      r.SavePath,
		TotalBytes:    r.TotalBytes,
		AddedAt:       r.AddedAt,
		CompletedAt:   r.CompletedAt,
		Error:         r.Error,
	}
}

func (e *Engine) downloadConfig() download.DownloadConfig {
	return download.DownloadConfig{
		MaxPeers:    e.cfg.MaxPeers,
		MaxPipeline: e.cfg.MaxPipeline,
		DialTimeout: e.cfg.DialTimeout,
		Encryption:  true, // always try MSE/PE in production
		EnableUTP:   e.cfg.EnableUTP,
		DialSem:     e.dialSem, // shared across all torrents
	}
}

func (e *Engine) activeCount() int {
	count := 0
	for _, s := range e.sessions {
		s.mu.RLock()
		state := s.record.State
		started := s.cancel != nil
		s.mu.RUnlock()
		switch state {
		case StateDownloading, StateMetadata, StateVerifying:
			count++
		case StateAdding:
			if started {
				count++
			}
		}
	}
	return count
}

func (e *Engine) onSessionDone(id string) {
	e.mu.RLock()
	s, ok := e.sessions[id]
	e.mu.RUnlock()
	if ok {
		r := s.Record()
		slog.Info("session done", "id", id, "name", r.Name, "state", r.State, "error", r.Error)
		if r.State == StateError {
			e.bus.Publish(events.Event{
				Type:      events.TypeSessionError,
				TorrentID: id,
				Payload:   events.SessionErrorPayload{Error: r.Error},
			})
		}
	}
	e.saveState()
	e.startQueued()
}

// startQueued starts queued torrents in queue position order if under MaxActive.
func (e *Engine) startQueued() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startQueuedLocked()
}

// startQueuedLocked is startQueued without acquiring the lock. Caller must hold e.mu.Lock().
func (e *Engine) startQueuedLocked() {
	active := e.activeCount()
	if active >= e.cfg.MaxActive {
		return
	}

	// Collect queued sessions, sort by queue position
	var queued []*Session
	for _, s := range e.sessions {
		r := s.Record()
		if r.State == StateAdding {
			queued = append(queued, s)
		}
	}

	sortByQueuePosition(queued)

	for _, s := range queued {
		if active >= e.cfg.MaxActive {
			break
		}
		if s.isRunning() {
			continue
		}
		r := s.Record()
		slog.Info("starting queued torrent", "id", r.ID, "name", r.Name, "queue_pos", r.QueuePosition)
		e.startSession(s)
		active++
	}
}

func sortByQueuePosition(sessions []*Session) {
	for i := 1; i < len(sessions); i++ {
		key := sessions[i]
		kpos := key.Record().QueuePosition
		j := i - 1
		for j >= 0 && sessions[j].Record().QueuePosition > kpos {
			sessions[j+1] = sessions[j]
			j--
		}
		sessions[j+1] = key
	}
}

func (e *Engine) saveState() {
	e.mu.RLock()
	e.saveStateLocked()
	e.mu.RUnlock()
}

// saveStateLocked persists state. Caller must hold at least e.mu.RLock.
func (e *Engine) saveStateLocked() {
	for _, s := range e.sessions {
		r := s.Record()
		e.store.Put(r)
	}
	if err := e.store.Save(); err != nil {
		slog.Error("failed to save state", "error", err)
	} else {
		slog.Debug("state saved", "sessions", len(e.sessions))
	}
}
