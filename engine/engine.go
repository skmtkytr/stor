package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/skmtkytr/stor/download"
)

// Config holds engine configuration.
type Config struct {
	DownloadDir string
	StatePath   string // path to state.json
	ListenPort  uint16
	MaxActive   int // max concurrent downloading torrents
}

// TorrentInfo is the full info returned to API clients.
type TorrentInfo struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Source      string                `json:"source"`
	State       State                 `json:"state"`
	Progress    download.ProgressSnap `json:"progress"`
	SavePath    string                `json:"save_path"`
	TotalBytes  int64                 `json:"total_bytes"`
	AddedAt     int64                 `json:"added_at"`
	CompletedAt int64                 `json:"completed_at"`
	Error       string                `json:"error,omitempty"`
}

// EngineStats is global daemon stats.
type EngineStats struct {
	TotalDownSpeed int64 `json:"total_down_speed"`
	ActiveTorrents int   `json:"active_torrents"`
	TotalTorrents  int   `json:"total_torrents"`
}

// Engine manages all torrent sessions.
type Engine struct {
	mu       sync.RWMutex
	cfg      Config
	peerID   [20]byte
	store    *Store
	sessions map[string]*Session
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

	var peerID [20]byte
	copy(peerID[:], "-ST0001-")
	_, _ = rand.Read(peerID[8:])

	store := NewStore(cfg.StatePath)
	if err := store.Load(); err != nil {
		return nil, fmt.Errorf("engine: load store: %w", err)
	}

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

	// Restore sessions from store
	for _, r := range e.store.All() {
		s := NewSession(r, e.peerID, e.cfg.DownloadDir, e.cfg.ListenPort)

		e.sessions[r.ID] = s

		// Auto-resume downloading torrents
		if r.State == StateDownloading || r.State == StateMetadata {
			s.Start(e.ctx, e.onSessionDone)
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
	e.cancel()

	if e.saveTicker != nil {
		e.saveTicker.Stop()
		close(e.saveStop)
	}

	e.saveState()
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
		return id, nil // already exists
	}

	record := &TorrentRecord{
		ID:      id,
		Source:  source,
		State:   StateAdding,
		AddedAt: time.Now().Unix(),
	}

	s := NewSession(record, e.peerID, e.cfg.DownloadDir, e.cfg.ListenPort)
	e.sessions[id] = s
	e.store.Put(record)
	_ = e.store.Save()

	// Start if under MaxActive limit
	if e.activeCount() < e.cfg.MaxActive {
		s.Start(e.ctx, e.onSessionDone)
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

	s.Pause()
	delete(e.sessions, id)
	e.store.Delete(id)
	e.mu.Unlock()

	_ = e.store.Save()

	if deleteFiles {
		r := s.Record()
		if r.SavePath != "" {
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
	}
	for _, s := range e.sessions {
		snap := s.Snap()
		if snap.State == string(StateDownloading) {
			stats.ActiveTorrents++
			stats.TotalDownSpeed += snap.DownSpeed
		}
	}
	return stats
}

// PeerID returns the engine's peer ID as hex string.
func (e *Engine) PeerID() string {
	return hex.EncodeToString(e.peerID[:])
}

func (e *Engine) sessionToInfo(s *Session) *TorrentInfo {
	r := s.Record()
	snap := s.Snap()
	return &TorrentInfo{
		ID:          r.ID,
		Name:        r.Name,
		Source:      r.Source,
		State:       r.State,
		Progress:    snap,
		SavePath:    r.SavePath,
		TotalBytes:  r.TotalBytes,
		AddedAt:     r.AddedAt,
		CompletedAt: r.CompletedAt,
		Error:       r.Error,
	}
}

func (e *Engine) activeCount() int {
	count := 0
	for _, s := range e.sessions {
		r := s.Record()
		if r.State == StateDownloading || r.State == StateMetadata || r.State == StateAdding {
			count++
		}
	}
	return count
}

func (e *Engine) onSessionDone(id string) {
	e.saveState()
	e.startQueued()
}

// startQueued starts queued (paused/adding) torrents if under MaxActive.
func (e *Engine) startQueued() {
	e.mu.Lock()
	defer e.mu.Unlock()

	active := e.activeCount()
	for _, s := range e.sessions {
		if active >= e.cfg.MaxActive {
			break
		}
		r := s.Record()
		if r.State == StateAdding {
			s.Start(e.ctx, e.onSessionDone)
			active++
		}
	}
}

func (e *Engine) saveState() {
	e.mu.RLock()
	for _, s := range e.sessions {
		r := s.Record()
		e.store.Put(r)
	}
	e.mu.RUnlock()
	_ = e.store.Save()
}
