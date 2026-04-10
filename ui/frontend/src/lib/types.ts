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
	| "downloading"
	| "complete"
	| "paused"
	| "error";

export interface ProgressSnap {
	state: string;
	downloaded: number;
	total: number;
	percent: number;
	down_speed: number;
	active_peers: number;
	total_pieces: number;
	done_pieces: number;
}

export interface EngineStats {
	total_down_speed: number;
	active_torrents: number;
	total_torrents: number;
	max_active: number;
}
