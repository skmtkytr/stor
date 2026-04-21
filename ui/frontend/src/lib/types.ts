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
	total_pieces: number;
	done_pieces: number;
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
	dht_nodes: number;
	free_space: number;
	config: EngineConfig;
}
