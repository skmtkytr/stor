// Lightweight toast / notification store. Components can push messages via
// `notifications.push(...)`; the layout renders them and they auto-dismiss
// after `ttlMs`.

export type NotificationKind = "info" | "success" | "warn" | "error";

export interface Notification {
	id: number;
	kind: NotificationKind;
	title: string;
	body?: string;
	createdAt: number;
}

let nextId = 1;

class NotificationStore {
	items = $state<Notification[]>([]);

	push(kind: NotificationKind, title: string, body?: string, ttlMs = 5000): number {
		const id = nextId++;
		const n: Notification = { id, kind, title, body, createdAt: Date.now() };
		this.items = [...this.items, n];
		if (ttlMs > 0 && typeof window !== "undefined") {
			setTimeout(() => this.dismiss(id), ttlMs);
		}
		return id;
	}

	dismiss(id: number) {
		this.items = this.items.filter((n) => n.id !== id);
	}

	clear() {
		this.items = [];
	}
}

export const notifications = new NotificationStore();
