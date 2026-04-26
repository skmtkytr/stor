// Live event bus client.
//
// Connects to the daemon's SSE endpoint at `/api/events` over fetch +
// ReadableStream (NOT EventSource) so we can send the same
// `Authorization: Bearer <apiKey>` header that the rest of the UI uses.
// Browser EventSource cannot set custom headers, which is the only reason
// we don't use it. The wire format is still standard SSE; we just parse
// it ourselves.

import type { AnyEvent, Event, EventType, EventPayloadMap } from "./types";
import { getApiKey } from "./rpc";

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
	 * fetch override (for tests). Defaults to the global fetch.
	 * Pass `null` to explicitly disable (simulates SSR / unavailable env).
	 */
	fetchImpl?: typeof fetch | null;
	/** API key resolver (for tests). Defaults to getApiKey from rpc. */
	apiKeyFn?: () => string;
}

/**
 * Typed wrapper over fetch+ReadableStream that consumes an SSE-formatted
 * response. Single connection, fan-out to N listeners, with exponential
 * backoff on disconnect. Sends Authorization: Bearer so the daemon's
 * auth middleware accepts the request.
 */
export class EventBus {
	state: ConnectionState = "idle";
	lastError: string | null = null;

	private readonly url: string;
	private readonly types: EventType[];
	private readonly initialBackoff: number;
	private readonly maxBackoff: number;
	private readonly fetchImpl: typeof fetch | null;
	private readonly apiKeyFn: () => string;

	private backoffMs: number;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private stopped = false;
	private abortController: AbortController | null = null;

	private readonly typed = new Map<EventType, Set<Listener<EventType>>>();
	private readonly anyListeners = new Set<AnyListener>();
	private readonly stateListeners = new Set<(s: ConnectionState) => void>();

	constructor(opts: EventBusOptions = {}) {
		this.url = opts.url ?? "/api/events";
		this.types = opts.types ?? [];
		this.initialBackoff = opts.initialBackoffMs ?? 500;
		this.maxBackoff = opts.maxBackoffMs ?? 30_000;
		this.backoffMs = this.initialBackoff;
		// Distinguish "not provided" (use environment) from explicit `null`
		// ("disable, simulate SSR"): only the former falls back to the global.
		this.fetchImpl =
			opts.fetchImpl !== undefined
				? opts.fetchImpl
				: typeof fetch !== "undefined"
					? fetch.bind(globalThis)
					: null;
		this.apiKeyFn = opts.apiKeyFn ?? getApiKey;
	}

	/** Open the connection if not already open. Idempotent. */
	start(): void {
		if (this.stopped) this.stopped = false;
		if (this.abortController) return;
		if (!this.fetchImpl) {
			this.lastError = "fetch is not available in this environment";
			this.setState("closed");
			return;
		}
		void this.connect();
	}

	/** Close the connection and stop reconnecting. */
	stop(): void {
		this.stopped = true;
		if (this.reconnectTimer) {
			clearTimeout(this.reconnectTimer);
			this.reconnectTimer = null;
		}
		if (this.abortController) {
			this.abortController.abort();
			this.abortController = null;
		}
		this.setState("closed");
	}

	/** Subscribe to a single event type. Returns an unsubscribe function. */
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

	private async connect(): Promise<void> {
		this.setState("connecting");
		const ac = new AbortController();
		this.abortController = ac;
		const url = this.buildUrl();
		const apiKey = this.apiKeyFn().trim();

		let res: Response;
		try {
			res = await this.fetchImpl!(url, {
				method: "GET",
				headers: {
					Accept: "text/event-stream",
					"Cache-Control": "no-cache",
					...(apiKey ? { Authorization: `Bearer ${apiKey}` } : {}),
				},
				signal: ac.signal,
				cache: "no-store",
			});
		} catch (err) {
			if (ac.signal.aborted) return;
			this.lastError = `connect failed: ${(err as Error).message}`;
			this.scheduleReconnect();
			return;
		}

		if (res.status === 401 || res.status === 403) {
			// Auth failure — don't auto-retry. The user has to re-authenticate
			// (rpc.ts owns the API key lifecycle); they should call start()
			// again afterwards.
			this.lastError = `unauthorized (${res.status})`;
			this.abortController = null;
			this.setState("closed");
			return;
		}

		if (!res.ok || !res.body) {
			this.lastError = `connect failed: HTTP ${res.status}`;
			this.scheduleReconnect();
			return;
		}

		this.backoffMs = this.initialBackoff;
		this.lastError = null;
		this.setState("open");

		try {
			await this.readStream(res.body);
			if (!this.stopped) {
				// Stream ended cleanly (server closed connection). Reconnect.
				this.lastError = "stream ended";
				this.scheduleReconnect();
			}
		} catch (err) {
			if (ac.signal.aborted) return;
			this.lastError = `stream error: ${(err as Error).message}`;
			this.scheduleReconnect();
		}
	}

	private async readStream(body: ReadableStream<Uint8Array>): Promise<void> {
		const reader = body.getReader();
		const decoder = new TextDecoder();
		let buffer = "";
		for (;;) {
			const { value, done } = await reader.read();
			if (done) return;
			buffer += decoder.decode(value, { stream: true });
			// SSE events are separated by a blank line. Our server only
			// emits LF, so we don't need to normalize CRLF here.
			let sepIdx: number;
			while ((sepIdx = buffer.indexOf("\n\n")) !== -1) {
				const raw = buffer.slice(0, sepIdx);
				buffer = buffer.slice(sepIdx + 2);
				this.dispatchFrame(raw);
			}
		}
	}

	private dispatchFrame(frame: string): void {
		// A frame may contain multiple `data:` lines (per the SSE spec they
		// are joined by \n) plus advisory `event:`, `id:`, `retry:` fields,
		// and `:` comments (e.g. ": keepalive"). The event type is carried
		// inside the JSON payload, so we only need the data lines.
		const dataLines: string[] = [];
		for (const line of frame.split("\n")) {
			if (line === "" || line.startsWith(":")) continue;
			const idx = line.indexOf(":");
			const field = idx < 0 ? line : line.slice(0, idx);
			let value = idx < 0 ? "" : line.slice(idx + 1);
			if (value.startsWith(" ")) value = value.slice(1);
			if (field === "data") dataLines.push(value);
		}
		if (dataLines.length === 0) return;
		const data = dataLines.join("\n");
		let parsed: AnyEvent;
		try {
			parsed = JSON.parse(data) as AnyEvent;
		} catch {
			return;
		}
		if (!parsed || typeof parsed.type !== "string") return;
		this.fanOut(parsed);
	}

	private fanOut(ev: AnyEvent): void {
		const set = this.typed.get(ev.type);
		if (set) {
			for (const l of set) {
				try {
					l(ev);
				} catch {
					/* listener errors must not break the bus */
				}
			}
		}
		for (const l of this.anyListeners) {
			try {
				l(ev);
			} catch {
				/* ignore */
			}
		}
	}

	private scheduleReconnect(): void {
		if (this.stopped) return;
		if (this.abortController) {
			this.abortController.abort();
			this.abortController = null;
		}
		this.setState("closed");
		const delay = this.backoffMs;
		this.backoffMs = Math.min(this.backoffMs * 2, this.maxBackoff);
		this.reconnectTimer = setTimeout(() => {
			this.reconnectTimer = null;
			if (!this.stopped) void this.connect();
		}, delay);
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

// Re-export for ergonomic typing in subscribers.
export type { Event, EventType, EventPayloadMap };
