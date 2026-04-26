// Live event bus client.
//
// Connects to the daemon's SSE endpoint at `/api/events` and fans out typed
// events to subscribers. The native EventSource already auto-reconnects, but
// it does so with no backoff and re-throws transport errors silently. This
// wrapper layers explicit exponential backoff on top so a long-down daemon
// doesn't get hammered, and exposes a typed pub/sub API that mirrors the Go
// `events` package one-to-one.

import type { AnyEvent, Event, EventType, EventPayloadMap } from "./types";

type Listener<T extends EventType> = (ev: Event<T>) => void;
type AnyListener = (ev: AnyEvent) => void;

export type ConnectionState = "idle" | "connecting" | "open" | "closed";

export interface EventBusOptions {
	/** Endpoint to connect to. Defaults to `/api/events`. */
	url?: string;
	/** Optional `?types=...` filter. Empty array → no filter. */
	types?: EventType[];
	/** Initial backoff delay in ms. */
	initialBackoffMs?: number;
	/** Maximum backoff delay in ms. */
	maxBackoffMs?: number;
	/**
	 * EventSource constructor override (for tests). Falls back to the global
	 * `EventSource` when omitted.
	 */
	eventSourceCtor?: typeof EventSource;
}

/**
 * Typed wrapper over EventSource. Single connection, fan-out to N listeners,
 * with exponential backoff on disconnect.
 */
export class EventBus {
	state: ConnectionState = "idle";
	lastError: string | null = null;

	private source: EventSource | null = null;
	private readonly url: string;
	private readonly types: EventType[];
	private readonly initialBackoff: number;
	private readonly maxBackoff: number;
	private readonly EventSourceCtor: typeof EventSource;

	private backoffMs: number;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private stopped = false;

	private readonly typed = new Map<EventType, Set<Listener<EventType>>>();
	private readonly anyListeners = new Set<AnyListener>();
	private readonly stateListeners = new Set<(s: ConnectionState) => void>();

	constructor(opts: EventBusOptions = {}) {
		this.url = opts.url ?? "/api/events";
		this.types = opts.types ?? [];
		this.initialBackoff = opts.initialBackoffMs ?? 500;
		this.maxBackoff = opts.maxBackoffMs ?? 30_000;
		this.backoffMs = this.initialBackoff;
		// Defer to the global EventSource at call time so SSR (where it's
		// undefined) doesn't blow up at module load.
		this.EventSourceCtor =
			opts.eventSourceCtor ??
			(typeof EventSource !== "undefined"
				? EventSource
				: (undefined as unknown as typeof EventSource));
	}

	/** Open the connection if not already open. Idempotent. */
	start(): void {
		if (this.stopped) this.stopped = false;
		if (this.source) return;
		if (!this.EventSourceCtor) {
			this.lastError = "EventSource is not available in this environment";
			this.setState("closed");
			return;
		}
		this.connect();
	}

	/** Close the connection and stop reconnecting. */
	stop(): void {
		this.stopped = true;
		if (this.reconnectTimer) {
			clearTimeout(this.reconnectTimer);
			this.reconnectTimer = null;
		}
		if (this.source) {
			this.source.close();
			this.source = null;
		}
		this.setState("closed");
	}

	/**
	 * Subscribe to a single event type. Returns an unsubscribe function.
	 */
	on<T extends EventType>(type: T, listener: Listener<T>): () => void {
		let set = this.typed.get(type);
		if (!set) {
			set = new Set();
			this.typed.set(type, set);
		}
		set.add(listener as Listener<EventType>);
		return () => {
			set!.delete(listener as Listener<EventType>);
			if (set!.size === 0) this.typed.delete(type);
		};
	}

	/** Subscribe to every event. */
	onAny(listener: AnyListener): () => void {
		this.anyListeners.add(listener);
		return () => this.anyListeners.delete(listener);
	}

	/** Observe connection state transitions. */
	onState(listener: (s: ConnectionState) => void): () => void {
		this.stateListeners.add(listener);
		listener(this.state);
		return () => this.stateListeners.delete(listener);
	}

	// --- internals ------------------------------------------------------

	private connect(): void {
		this.setState("connecting");
		const url = this.buildUrl();
		const src = new this.EventSourceCtor(url, { withCredentials: false });
		this.source = src;

		src.onopen = () => {
			this.backoffMs = this.initialBackoff;
			this.lastError = null;
			this.setState("open");
		};

		src.onerror = () => {
			// EventSource will keep retrying on its own, but our policy is to
			// reset the connection and apply explicit exponential backoff so a
			// dead daemon doesn't generate a tight reconnect loop.
			this.lastError = "connection lost";
			this.scheduleReconnect();
		};

		// Bind one listener per event type (browsers only fire `message` for
		// frames without an explicit `event:` field; our server always sets it).
		const handler = (raw: MessageEvent) => this.dispatch(raw);
		const types = this.types.length > 0 ? this.types : allEventTypes;
		for (const t of types) {
			src.addEventListener(t, handler as EventListener);
		}
		// Also catch the default channel as a safety net.
		src.onmessage = handler;
	}

	private scheduleReconnect(): void {
		if (this.stopped) return;
		if (this.source) {
			this.source.close();
			this.source = null;
		}
		this.setState("closed");
		const delay = this.backoffMs;
		this.backoffMs = Math.min(this.backoffMs * 2, this.maxBackoff);
		this.reconnectTimer = setTimeout(() => {
			this.reconnectTimer = null;
			if (!this.stopped) this.connect();
		}, delay);
	}

	private dispatch(raw: MessageEvent): void {
		let parsed: AnyEvent;
		try {
			parsed = JSON.parse(raw.data) as AnyEvent;
		} catch {
			return;
		}
		if (!parsed || typeof parsed.type !== "string") return;

		// Fan out to typed listeners.
		const set = this.typed.get(parsed.type);
		if (set) {
			for (const l of set) {
				try {
					l(parsed);
				} catch {
					/* listener errors must not break the bus */
				}
			}
		}
		for (const l of this.anyListeners) {
			try {
				l(parsed);
			} catch {
				/* ignore */
			}
		}
	}

	private setState(s: ConnectionState): void {
		if (this.state === s) return;
		this.state = s;
		for (const l of this.stateListeners) {
			try {
				l(s);
			} catch {
				/* ignore */
			}
		}
	}

	private buildUrl(): string {
		if (this.types.length === 0) return this.url;
		const sep = this.url.includes("?") ? "&" : "?";
		return `${this.url}${sep}types=${this.types.map(encodeURIComponent).join(",")}`;
	}
}

const allEventTypes: EventType[] = [
	"torrent.added",
	"torrent.removed",
	"torrent.paused",
	"torrent.resumed",
	"torrent.state_changed",
	"metadata.fetched",
	"tracker.reply",
	"tracker.error",
	"dht.reply",
	"peer.connected",
	"peer.disconnected",
	"piece.complete",
	"session.error",
];

// Re-export for ergonomic typing in subscribers.
export type { Event, EventType, EventPayloadMap };
