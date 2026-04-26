// Package events provides a non-blocking, fan-out event bus for engine
// observation. Publishers (engine, session, announcer, peer manager) emit
// typed events; subscribers (SSE endpoint, metrics, UI, tests) drain them
// from per-subscription channels. A slow subscriber drops events for itself
// only — the hot path is never blocked.
package events

import "time"

// Type identifies what kind of event was emitted.
type Type string

const (
	TypeTorrentAdded          Type = "torrent.added"
	TypeTorrentRemoved        Type = "torrent.removed"
	TypeTorrentPaused         Type = "torrent.paused"
	TypeTorrentResumed        Type = "torrent.resumed"
	TypeStateChanged          Type = "torrent.state_changed"
	TypeMetadataFetched       Type = "metadata.fetched"
	TypeAnnounceReply         Type = "tracker.reply"
	TypeAnnounceError         Type = "tracker.error"
	TypeDHTReply              Type = "dht.reply"
	TypePeerConnected         Type = "peer.connected"
	TypePeerDisconnected      Type = "peer.disconnected"
	TypePieceComplete         Type = "piece.complete"
	TypeSessionError          Type = "session.error"
	TypePeerSearchFailed      Type = "peer_search.failed"
	TypeMetadataAttemptFailed Type = "metadata_attempt.failed"
	TypeDHTLookupFailed       Type = "dht.lookup_failed"
	TypeStalled               Type = "torrent.stalled"
	TypeStallCleared          Type = "torrent.stall_cleared"
	// Request-vs-confirm split (Deluge parity):
	//   *Requested = API was called (intent recorded)
	//   Paused/Resumed = state actually transitioned (confirmation)
	// Both are emitted; consumers can pick which semantic they need.
	TypeTorrentPauseRequested  Type = "torrent.pause_requested"
	TypeTorrentResumeRequested Type = "torrent.resume_requested"
)

// Event is a single observation published to the bus.
type Event struct {
	Type      Type      `json:"type"`
	TorrentID string    `json:"torrent_id,omitempty"`
	Time      time.Time `json:"time"`
	Payload   any       `json:"payload,omitempty"`
}

type TorrentAddedPayload struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
}

type TorrentRemovedPayload struct {
	DeletedFiles bool `json:"deleted_files"`
}

// StateChangedPayload uses string for State to avoid an events->engine import cycle.
type StateChangedPayload struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type MetadataFetchedPayload struct {
	Name       string `json:"name"`
	NumPieces  int    `json:"num_pieces"`
	TotalBytes int64  `json:"total_bytes"`
}

type AnnounceReplyPayload struct {
	Tracker         string `json:"tracker"`
	NumPeers        int    `json:"num_peers"`
	IntervalSeconds int    `json:"interval_seconds"`
}

type AnnounceErrorPayload struct {
	Tracker string `json:"tracker"`
	Error   string `json:"error"`
}

type DHTReplyPayload struct {
	NumPeers int `json:"num_peers"`
}

type PeerConnectedPayload struct {
	Addr      string `json:"addr"`
	Direction string `json:"direction"` // "in" or "out"
	Transport string `json:"transport"` // "tcp" or "utp"
}

type PeerDisconnectedPayload struct {
	Addr   string `json:"addr"`
	Reason string `json:"reason,omitempty"`
}

type PieceCompletePayload struct {
	Index int `json:"index"`
	Have  int `json:"have"`
	Total int `json:"total"`
}

type SessionErrorPayload struct {
	Error string `json:"error"`
}

// PeerSearchFailedPayload is emitted when a tracker/DHT peer search round
// returned zero usable peers and the session is about to back off and retry.
type PeerSearchFailedPayload struct {
	AttemptCount int    `json:"attempt_count"`
	NextRetryIn  int    `json:"next_retry_in_seconds"`
	Error        string `json:"error,omitempty"`
}

// MetadataAttemptFailedPayload is emitted when one round of magnet metadata
// fetch (tracker + DHT + ut_metadata exchange across discovered peers) failed
// to produce a parsed TorrentFile.
type MetadataAttemptFailedPayload struct {
	AttemptCount int    `json:"attempt_count"`
	NextRetryIn  int    `json:"next_retry_in_seconds"`
	Error        string `json:"error,omitempty"`
}

// DHTLookupFailedPayload is emitted when a DHT GetPeers call returned an
// error (timeout, no responses, etc.).
type DHTLookupFailedPayload struct {
	Error string `json:"error"`
}

// StalledPayload is emitted when a downloading session has been stuck at zero
// connected peers for the configured detection window. It is fired exactly
// once per stall episode; recovery emits StallCleared.
type StalledPayload struct {
	DurationSeconds int    `json:"duration_seconds"`
	Reason          string `json:"reason,omitempty"`
}

// StallClearedPayload is emitted when a previously-stalled session
// reconnects to at least one peer.
type StallClearedPayload struct {
	StalledForSeconds int `json:"stalled_for_seconds"`
}
