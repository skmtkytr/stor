package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// State represents the torrent lifecycle state.
type State string

const (
	StateAdding      State = "adding"
	StateMetadata    State = "metadata"
	StateDownloading State = "downloading"
	StateSeeding     State = "seeding"
	StateComplete    State = "complete"
	StatePaused      State = "paused"
	StateError       State = "error"
)

// TorrentRecord is the persisted state of a single torrent.
type TorrentRecord struct {
	ID            string `json:"id"` // hex info hash
	Name          string `json:"name"`
	Source        string `json:"source"` // magnet URI or .torrent path
	SavePath      string `json:"save_path"`
	TotalBytes    int64  `json:"total_bytes"`
	State         State  `json:"state"`
	QueuePosition int    `json:"queue_position"` // lower = starts first
	AddedAt       int64  `json:"added_at"`       // unix timestamp
	CompletedAt   int64  `json:"completed_at"`
	TorrentData   []byte `json:"torrent_data"` // raw bencoded .torrent (for resume)
	Bitfield      []byte `json:"bitfield"`     // which pieces we have
	Error         string `json:"error,omitempty"`
}

// Store persists torrent records to a JSON file.
type Store struct {
	path    string
	mu      sync.RWMutex
	records map[string]*TorrentRecord
}

// NewStore creates a new store. Call Load() to read from disk.
func NewStore(path string) *Store {
	return &Store{
		path:    path,
		records: make(map[string]*TorrentRecord),
	}
}

// Load reads the store from disk. If the file doesn't exist, starts empty.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("store: read failed: %w", err)
	}

	var records []*TorrentRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("store: unmarshal failed: %w", err)
	}

	s.records = make(map[string]*TorrentRecord, len(records))
	for _, r := range records {
		s.records[r.ID] = r
	}
	return nil
}

// Save writes the store to disk atomically.
func (s *Store) Save() error {
	s.mu.RLock()
	records := make([]*TorrentRecord, 0, len(s.records))
	for _, r := range s.records {
		records = append(records, r)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal failed: %w", err)
	}

	// Ensure parent directory exists
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("store: mkdir failed: %w", err)
		}
	}

	// Atomic write: write to temp file then rename
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("store: write failed: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("store: rename failed: %w", err)
	}
	return nil
}

// Put adds or updates a record.
func (s *Store) Put(r *TorrentRecord) {
	s.mu.Lock()
	s.records[r.ID] = r
	s.mu.Unlock()
}

// Get returns a record by ID.
func (s *Store) Get(id string) (*TorrentRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	return r, ok
}

// Delete removes a record by ID.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.records, id)
	s.mu.Unlock()
}

// All returns all records.
func (s *Store) All() []*TorrentRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*TorrentRecord, 0, len(s.records))
	for _, r := range s.records {
		result = append(result, r)
	}
	return result
}

// Len returns the number of records.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}
