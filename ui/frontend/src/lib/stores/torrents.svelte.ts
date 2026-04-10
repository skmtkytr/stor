import type { TorrentInfo, EngineStats } from "$lib/types";
import { api } from "$lib/rpc";

class TorrentStore {
	list = $state<TorrentInfo[]>([]);
	stats = $state<EngineStats | null>(null);

	private interval: ReturnType<typeof setInterval> | null = null;

	async refresh() {
		try {
			const [t, s] = await Promise.all([api.list(), api.stats()]);
			this.list = t ?? [];
			this.stats = s;
		} catch {
			/* ignore polling errors */
		}
	}

	startPolling() {
		this.refresh();
		this.interval = setInterval(() => this.refresh(), 1000);
	}

	stopPolling() {
		if (this.interval) {
			clearInterval(this.interval);
			this.interval = null;
		}
	}
}

export const torrents = new TorrentStore();
