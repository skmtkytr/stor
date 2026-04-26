// Activity log store. Captures observation events (state changes, errors,
// completions) into an LRU history. There is intentionally NO toast layer:
// per Deluge's notification philosophy (Notifications/core.py only fires
// for TorrentFinishedEvent and only when the user has explicitly enabled a
// channel), routine events are not worth interrupting the user. Tracker
// errors are surfaced via per-torrent last_tracker_error in the detail
// pane; everything else lives here, viewable on demand via the panel.

export type NotificationKind = "info" | "success" | "warn" | "error";

export interface Notification {
	id: number;
	kind: NotificationKind;
	title: string;
	body?: string;
	createdAt: number;
	read: boolean;
}

const MAX_HISTORY = 200;
let nextId = 1;

class ActivityLogStore {
	history = $state<Notification[]>([]);
	panelOpen = $state(false);

	// Plain getter — Svelte 5 re-runs class getters when their reactive
	// dependencies (`history`) change.
	get unreadCount(): number {
		return this.history.filter((n) => !n.read).length;
	}

	push(kind: NotificationKind, title: string, body?: string): number {
		const id = nextId++;
		const n: Notification = {
			id,
			kind,
			title,
			body,
			createdAt: Date.now(),
			read: false,
		};
		const next = [n, ...this.history];
		if (next.length > MAX_HISTORY) next.length = MAX_HISTORY;
		this.history = next;
		return id;
	}

	openPanel() {
		this.panelOpen = true;
		// Mark everything as read when the panel opens.
		this.history = this.history.map((n) => (n.read ? n : { ...n, read: true }));
	}

	closePanel() {
		this.panelOpen = false;
	}

	togglePanel() {
		if (this.panelOpen) this.closePanel();
		else this.openPanel();
	}

	clearHistory() {
		this.history = [];
	}
}

export const notifications = new ActivityLogStore();
