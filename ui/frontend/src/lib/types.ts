export interface TorrentInfo {
	id: string;
	name: string;
	source: string;
	state: TorrentState;
	queue_position: number;
	progress: ProgressSnap;
	save_path: string;
	total_bytes: number;
	added_at: number;
	completed_at: number;
	error?: string;
}

export type TorrentState =
	| "adding"
	| "metadata"
	| "verifying"
	| "downloading"
	| "seeding"
	| "complete"
	| "paused"
	| "error";

export interface EngineConfig {
	download_dir: string;
	tmp_dir: string;
	max_active: number;
	max_peers: number;
	max_pipeline: number;
	dial_timeout: number;
	numwant: number;
	log_level: string;
	enable_utp: boolean;
}

export interface ProgressSnap {
	state: string;
	downloaded: number;
	uploaded: number;
	total: number;
	percent: number;
	down_speed: number;
	up_speed: number;
	active_peers: number;
	known_peers: number;
	total_pieces: number;
	done_pieces: number;
	// Most recent tracker failure (Deluge status_message parity). Empty
	// when the most recent announce on any tracker succeeded.
	last_tracker_error?: string;
}

export interface FileEntry {
	path: string;
	length: number;
	downloaded: number;
	// 0 = normal, -1 = skip. Missing in older responses → treat as normal.
	priority: number;
}

export interface PeerSnap {
	addr: string;
	ip_version: number;
	incoming: boolean;
	using_utp: boolean;
	encrypted: boolean;
	client: string;
	peer_id: string;
	down_rate: number;
	up_rate: number;
	choked: boolean;
	choking: boolean;
	progress: number;
}

export interface EngineStats {
	total_down_speed: number;
	total_up_speed: number;
	total_downloaded: number;
	total_uploaded: number;
	active_torrents: number;
	seeding_torrents: number;
	total_torrents: number;
	max_active: number;
	total_peers: number;
	total_known_peers: number;
	dht_nodes: number;
	free_space: number;
	config: EngineConfig;
}

// --- Event bus types (mirror of events.Event in Go) ---
//
// SSE wire format: each frame carries `event: <type>` and a `data:` JSON
// payload matching the Event<T> envelope below. Server contract is documented
// at /api/events; see the Go `events` package for the source of truth.

export type EventType =
	| "torrent.added"
	| "torrent.removed"
	| "torrent.paused"
	| "torrent.resumed"
	| "torrent.pause_requested"
	| "torrent.resume_requested"
	| "torrent.state_changed"
	| "metadata.fetched"
	| "tracker.reply"
	| "tracker.error"
	| "dht.reply"
	| "peer.connected"
	| "peer.disconnected"
	| "piece.complete"
	| "session.error"
	| "peer_search.failed"
	| "metadata_attempt.failed"
	| "dht.lookup_failed"
	| "torrent.stalled"
	| "torrent.stall_cleared"
	| "storage.moved"
	| "torrent.fastresume_rejected"
	| "file.completed";

export interface TorrentAddedPayload {
	source: string;
	name?: string;
}

export interface TorrentRemovedPayload {
	deleted_files: boolean;
}

export interface StateChangedPayload {
	from: TorrentState | string;
	to: TorrentState | string;
}

export interface MetadataFetchedPayload {
	name: string;
	num_pieces: number;
	total_bytes: number;
}

export interface AnnounceReplyPayload {
	tracker: string;
	num_peers: number;
	interval_seconds: number;
}

export interface AnnounceErrorPayload {
	tracker: string;
	error: string;
}

export interface DHTReplyPayload {
	num_peers: number;
}

export interface PeerConnectedPayload {
	addr: string;
	direction: "in" | "out";
	transport: "tcp" | "utp";
}

export interface PeerDisconnectedPayload {
	addr: string;
	reason?: string;
}

export interface PieceCompletePayload {
	index: number;
	have: number;
	total: number;
}

export interface SessionErrorPayload {
	error: string;
}

export interface PeerSearchFailedPayload {
	attempt_count: number;
	next_retry_in_seconds: number;
	error?: string;
}

export interface MetadataAttemptFailedPayload {
	attempt_count: number;
	next_retry_in_seconds: number;
	error?: string;
}

export interface DHTLookupFailedPayload {
	error: string;
}

export interface StalledPayload {
	duration_seconds: number;
	reason?: string;
}

export interface StallClearedPayload {
	stalled_for_seconds: number;
}

export interface StorageMovedPayload {
	from: string;
	to: string;
}

export interface FastresumeRejectedPayload {
	reason: string;
	detail?: string;
}

export interface FileCompletedPayload {
	index: number;
	path: string;
	size: number;
}

export interface EventPayloadMap {
	"torrent.added": TorrentAddedPayload;
	"torrent.removed": TorrentRemovedPayload;
	// Confirmed: emitted AFTER state actually transitions (Deluge alert parity).
	// Receiving this means the engine has acknowledged the new state, not just
	// that the API was called. The naive "API entry" semantics live in the
	// *_requested events below.
	"torrent.paused": Record<string, never>;
	"torrent.resumed": Record<string, never>;
	// Requested: emitted at API entry as soon as PauseTorrent / ResumeTorrent
	// is called. Use these for optimistic UI updates; reconcile with the
	// confirmed event for authoritative state.
	"torrent.pause_requested": Record<string, never>;
	"torrent.resume_requested": Record<string, never>;
	"torrent.state_changed": StateChangedPayload;
	"metadata.fetched": MetadataFetchedPayload;
	"tracker.reply": AnnounceReplyPayload;
	"tracker.error": AnnounceErrorPayload;
	"dht.reply": DHTReplyPayload;
	"peer.connected": PeerConnectedPayload;
	"peer.disconnected": PeerDisconnectedPayload;
	"piece.complete": PieceCompletePayload;
	"session.error": SessionErrorPayload;
	"peer_search.failed": PeerSearchFailedPayload;
	"metadata_attempt.failed": MetadataAttemptFailedPayload;
	"dht.lookup_failed": DHTLookupFailedPayload;
	"torrent.stalled": StalledPayload;
	"torrent.stall_cleared": StallClearedPayload;
	"storage.moved": StorageMovedPayload;
	"torrent.fastresume_rejected": FastresumeRejectedPayload;
	"file.completed": FileCompletedPayload;
}

export interface Event<T extends EventType = EventType> {
	type: T;
	torrent_id?: string;
	time: string;
	payload: EventPayloadMap[T];
}

export type AnyEvent = { [T in EventType]: Event<T> }[EventType];
