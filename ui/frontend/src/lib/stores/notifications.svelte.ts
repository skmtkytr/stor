// Two-tier notification store.
//
//   toasts:  short-lived right-bottom popups, auto-dismiss after kind-specific
//            TTL (info/success: 5s, warn: 10s, error: never).
//   history: full event log (capped LRU). Items are NOT removed on toast
//            dismissal; the user can review past notifications via the
//            notification panel. Capped at MAX_HISTORY entries.
//
// Both layers are populated by the same push() call. The toast layer drives
// the floating UI; the history layer drives the bell-icon panel.

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

// kind-specific TTL in milliseconds. 0 = no auto-dismiss (user must click ×).
const TTL_BY_KIND: Record<NotificationKind, number> = {
	info: 5_000,
	success: 5_000,
	warn: 10_000,
	error: 0,
};

let nextId = 1;

class NotificationStore {
	toasts = $state<Notification[]>([]);
	history = $state<Notification[]>([]);
	panelOpen = $state(false);

	// Plain getter — Svelte 5 re-runs class getters when their reactive
	// dependencies (`history`) change, so `notifications.unreadCount`
	// stays live in templates without an explicit $derived.
	get unreadCount(): number {
		return this.history.filter((n) => !n.read).length;
	}

	push(kind: NotificationKind, title: string, body?: string, ttlMs?: number): number {
		const id = nextId++;
		const n: Notification = {
			id,
			kind,
			title,
			body,
			createdAt: Date.now(),
			read: false,
		};

		// Append to history (LRU cap, newest first).
		const next = [n, ...this.history];
		if (next.length > MAX_HISTORY) next.length = MAX_HISTORY;
		this.history = next;

		// Append to toasts and schedule auto-dismiss. setTimeout exists in
		// every relevant environment (browser, Node SSR, bun); the check we
		// used to do on `typeof window` was unnecessary and prevented unit
		// tests from observing dismissal.
		this.toasts = [...this.toasts, n];
		const effectiveTtl = ttlMs ?? TTL_BY_KIND[kind];
		if (effectiveTtl > 0) {
			setTimeout(() => this.dismissToast(id), effectiveTtl);
		}

		return id;
	}

	dismissToast(id: number) {
		this.toasts = this.toasts.filter((n) => n.id !== id);
	}

	dismissAllToasts() {
		this.toasts = [];
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

	// Backwards-compatible aliases for any caller using the old API.
	dismiss(id: number) {
		this.dismissToast(id);
	}
	clear() {
		this.dismissAllToasts();
	}
}

export const notifications = new NotificationStore();
