package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/skmtkytr/stor/dht"
	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/torrent"
)

// Config holds engine configuration.
type Config struct {
	DownloadDir string
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

// EngineStats is global daemon stats.
type EngineStats struct {
	TotalDownSpeed int64 `json:"total_down_speed"`
	ActiveTorrents int   `json:"active_torrents"`
	TotalTorrents  int   `json:"total_torrents"`
	MaxActive      int   `json:"max_active"`
	TotalPeers     int   `json:"total_peers"`
	DHTNodes       int   `json:"dht_nodes"`
	FreeSpace      int64 `json:"free_space"`
}

// Engine manages all torrent sessions.
type Engine struct {
	mu       sync.RWMutex
	cfg      Config
	peerID   [20]byte
	store    *Store
	sessions map[string]*Session
	dht      *dht.DHT
	ctx      context.Context
	cancel   context.CancelFunc

	// Periodic save
	saveTicker *time.Ticker
	saveStop   chan struct{}
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
		cfg.MaxPeers = 100
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

	e := &Engine{
		cfg:      cfg,
		peerID:   peerID,
		store:    store,
		sessions: make(map[string]*Session),
		ctx:      ctx,
		cancel:   cancel,
	}

	return e, nil
}

// Start restores persisted torrents and starts periodic save.
func (e *Engine) Start() error {
	if err := os.MkdirAll(e.cfg.DownloadDir, 0o755); err != nil {
		return fmt.Errorf("engine: create download dir: %w", err)
	}

	// Start shared DHT node
	d, err := dht.New(":0", dht.WithAlpha(e.cfg.DHTAlpha))
	if err == nil {
		_ = d.Bootstrap(dhtBootstrapNodes)
		e.dht = d
		slog.Info("dht started", "bootstrap_nodes", len(dhtBootstrapNodes))
	} else {
		slog.Warn("dht failed to start", "error", err)
	}

	// Restore sessions from store
	records := e.store.All()
	slog.Info("restoring sessions", "count", len(records))
	for _, r := range records {
		s := NewSession(r, e.peerID, e.cfg.DownloadDir, e.cfg.ListenPort, e.downloadConfig(), e.cfg.NumWant, e.dht)

		e.sessions[r.ID] = s

		// Auto-resume active torrents (downloading, metadata, or queued)
		if r.State == StateDownloading || r.State == StateMetadata || r.State == StateAdding {
			slog.Info("resuming torrent", "id", r.ID, "name", r.Name, "state", r.State)
			if e.activeCount() < e.cfg.MaxActive {
				s.Start(e.ctx, e.onSessionDone)
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

	if e.dht != nil {
		_ = e.dht.Close()
	}

	e.saveState()
	slog.Info("engine stopped, state saved")
	return nil
}

// AddTorrent adds a torrent from a magnet URI or .torrent file path.
func (e *Engine) AddTorrent(source string) (string, error) {
	id, err := InfoHashHex(source)
	if err != nil {
		return "", fmt.Errorf("engine: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.sessions[id]; exists {
		slog.Debug("torrent already exists", "id", id)
		return id, nil // already exists
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
			record.TotalBytes = download.TotalSize(tf)
		}
	}

	s := NewSession(record, e.peerID, e.cfg.DownloadDir, e.cfg.ListenPort, e.downloadConfig(), e.cfg.NumWant, e.dht)
	e.sessions[id] = s
	e.store.Put(record)
	_ = e.store.Save()

	// Start if under MaxActive limit
	active := e.activeCount()
	if active < e.cfg.MaxActive {
		slog.Info("starting torrent", "id", id, "active", active, "max_active", e.cfg.MaxActive)
		s.Start(e.ctx, e.onSessionDone)
	} else {
		slog.Info("torrent queued", "id", id, "active", active, "max_active", e.cfg.MaxActive, "queue_pos", record.QueuePosition)
	}

	return id, nil
}

// AddTorrentFile adds a torrent from raw .torrent data.
func (e *Engine) AddTorrentFile(data []byte) (string, error) {
	// Write to a temp file and use it as source
	tmp, err := os.CreateTemp("", "stor-*.torrent")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	_ = tmp.Close()

	return e.AddTorrent(tmp.Name())
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
	}

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
	s.mu.Lock()
	s.record.State = StateAdding
	s.record.Error = ""
	s.mu.Unlock()

	if e.activeCount() < e.cfg.MaxActive {
		s.Start(e.ctx, e.onSessionDone)
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
		if snap.State == string(StateDownloading) {
			stats.ActiveTorrents++
			stats.TotalDownSpeed += snap.DownSpeed
		}
	}

	if e.dht != nil {
		stats.DHTNodes = e.dht.TableSize()
	}

	stats.FreeSpace = diskFreeSpace(e.cfg.DownloadDir)

	return stats
}

// diskFreeSpace returns the available bytes on the filesystem containing path.
func diskFreeSpace(path string) int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return -1
	}
	//nolint:unconvert // Bavail type varies by platform
	return int64(stat.Bavail) * int64(stat.Bsize)
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
	e.mu.Unlock()
	return e.setQueuePosition(id, maxPos)
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
	}
}

func (e *Engine) activeCount() int {
	count := 0
	for _, s := range e.sessions {
		r := s.Record()
		if r.State == StateDownloading || r.State == StateMetadata {
			count++
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
		r := s.Record()
		slog.Info("starting queued torrent", "id", r.ID, "name", r.Name, "queue_pos", r.QueuePosition)
		s.Start(e.ctx, e.onSessionDone)
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
