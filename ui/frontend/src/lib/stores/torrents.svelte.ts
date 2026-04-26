import type { TorrentInfo, EngineStats, TorrentState } from "$lib/types";
import { api } from "$lib/rpc";
import { EventBus, type ConnectionState } from "$lib/events";
import { notifications } from "$lib/stores/notifications.svelte";

class TorrentStore {
	list = $state<TorrentInfo[]>([]);
	stats = $state<EngineStats | null>(null);
	/** Connection state of the live event stream. */
	liveState = $state<ConnectionState>("idle");

	private interval: ReturnType<typeof setInterval> | null = null;
	private bus: EventBus | null = null;
	private busUnsubs: Array<() => void> = [];
	/** Pending refresh debounce timer. */
	private refreshTimer: ReturnType<typeof setTimeout> | null = null;

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
		if (!this.interval) {
			this.interval = setInterval(() => this.refresh(), 1000);
		}
		this.startEvents();
	}

	stopPolling() {
		if (this.interval) {
			clearInterval(this.interval);
			this.interval = null;
		}
		this.stopEvents();
	}

	// --- live event stream --------------------------------------------------

	startEvents() {
		if (this.bus) return;
		const bus = new EventBus();
		this.bus = bus;

		this.busUnsubs.push(
			bus.onState((s) => {
				this.liveState = s;
			}),
		);

		// Live state badge: patch local list optimistically so the UI updates
		// without waiting for the next 1s poll. The next refresh() will
		// reconcile.
		this.busUnsubs.push(
			bus.on("torrent.state_changed", (ev) => {
				const id = ev.torrent_id;
				if (!id) return;
				const to = ev.payload.to as TorrentState;
				this.patch(id, (t) => ({
					...t,
					state: to,
					progress: { ...t.progress, state: to },
				}));
				if (to === "seeding" || to === "complete") {
					const t = this.list.find((x) => x.id === id);
					notifications.push(
						"success",
						"Download complete",
						t?.name ?? id.slice(0, 16) + "...",
					);
				} else if (to === "error") {
					const t = this.list.find((x) => x.id === id);
					notifications.push(
						"error",
						"Torrent error",
						t?.name ?? id.slice(0, 16) + "...",
					);
				}
			}),
		);

		// Peer connect/disconnect: bump active_peers locally so the count moves
		// at peer-event resolution rather than 1s poll resolution. The next
		// refresh() corrects any drift.
		this.busUnsubs.push(
			bus.on("peer.connected", (ev) => {
				const id = ev.torrent_id;
				if (!id) return;
				this.patch(id, (t) => ({
					...t,
					progress: {
						...t.progress,
						active_peers: (t.progress.active_peers ?? 0) + 1,
					},
				}));
			}),
		);
		this.busUnsubs.push(
			bus.on("peer.disconnected", (ev) => {
				const id = ev.torrent_id;
				if (!id) return;
				this.patch(id, (t) => ({
					...t,
					progress: {
						...t.progress,
						active_peers: Math.max(0, (t.progress.active_peers ?? 0) - 1),
					},
				}));
			}),
		);

		// Adds and removes change list shape; trigger a coalesced refresh.
		this.busUnsubs.push(bus.on("torrent.added", () => this.scheduleRefresh()));
		this.busUnsubs.push(bus.on("torrent.removed", (ev) => {
			// Optimistically drop from the list so the row disappears immediately.
			if (ev.torrent_id) {
				this.list = this.list.filter((t) => t.id !== ev.torrent_id);
			}
			this.scheduleRefresh();
		}));
		this.busUnsubs.push(bus.on("metadata.fetched", () => this.scheduleRefresh()));

		// Tracker errors are intentionally NOT logged here: they recur on
		// every announce interval per failing tracker (often once a minute
		// across many trackers), which floods the activity log with noise.
		// The current per-torrent failure is exposed instead via
		// progress.last_tracker_error, shown in the detail pane (Deluge
		// status_message parity).
		this.busUnsubs.push(
			bus.on("session.error", (ev) => {
				notifications.push("error", "Session error", ev.payload.error);
			}),
		);
		this.busUnsubs.push(
			bus.on("torrent.fastresume_rejected", (ev) => {
				const t = this.list.find((x) => x.id === ev.torrent_id);
				notifications.push(
					"warn",
					"Fastresume rejected",
					`${t?.name ?? ev.torrent_id ?? ""}: ${ev.payload.reason}`,
				);
			}),
		);
		this.busUnsubs.push(
			bus.on("torrent.stalled", (ev) => {
				const t = this.list.find((x) => x.id === ev.torrent_id);
				notifications.push(
					"warn",
					"Torrent stalled",
					`${t?.name ?? ev.torrent_id ?? ""} (${ev.payload.duration_seconds}s with no peers)`,
				);
			}),
		);
		this.busUnsubs.push(
			bus.on("storage.moved", (ev) => {
				const t = this.list.find((x) => x.id === ev.torrent_id);
				notifications.push(
					"info",
					"Files moved",
					`${t?.name ?? ev.torrent_id ?? ""} → ${ev.payload.to}`,
				);
			}),
		);
		this.busUnsubs.push(
			bus.on("file.completed", (ev) => {
				notifications.push(
					"info",
					"File completed",
					`${ev.payload.path} (${ev.payload.size} bytes)`,
				);
			}),
		);

		bus.start();
	}

	stopEvents() {
		for (const u of this.busUnsubs) u();
		this.busUnsubs = [];
		if (this.bus) {
			this.bus.stop();
			this.bus = null;
		}
		if (this.refreshTimer) {
			clearTimeout(this.refreshTimer);
			this.refreshTimer = null;
		}
	}

	private patch(id: string, fn: (t: TorrentInfo) => TorrentInfo) {
		const idx = this.list.findIndex((t) => t.id === id);
		if (idx < 0) return;
		const next = this.list.slice();
		next[idx] = fn(next[idx]);
		this.list = next;
	}

	/** Coalesce refreshes triggered by bursts of events. */
	private scheduleRefresh() {
		if (this.refreshTimer) return;
		this.refreshTimer = setTimeout(() => {
			this.refreshTimer = null;
			this.refresh();
		}, 100);
	}
}

export const torrents = new TorrentStore();
