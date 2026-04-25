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
	TypeTorrentAdded     Type = "torrent.added"
	TypeTorrentRemoved   Type = "torrent.removed"
	TypeTorrentPaused    Type = "torrent.paused"
	TypeTorrentResumed   Type = "torrent.resumed"
	TypeStateChanged     Type = "torrent.state_changed"
	TypeMetadataFetched  Type = "metadata.fetched"
	TypeAnnounceReply    Type = "tracker.reply"
	TypeAnnounceError    Type = "tracker.error"
	TypeDHTReply         Type = "dht.reply"
	TypePeerConnected    Type = "peer.connected"
	TypePeerDisconnected Type = "peer.disconnected"
	TypePieceComplete    Type = "piece.complete"
	TypeSessionError     Type = "session.error"
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
