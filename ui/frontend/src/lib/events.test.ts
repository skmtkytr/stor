import { describe, expect, test, beforeEach, mock } from "bun:test";
import { EventBus } from "./events";
import type { AnyEvent } from "./types";

// FakeFetch returns Response objects backed by a ReadableStream we control,
// so tests can push raw SSE frames into the bus deterministically.

class FakeStream {
	body: ReadableStream<Uint8Array>;
	controller!: ReadableStreamDefaultController<Uint8Array>;
	aborted = false;

	constructor(signal?: AbortSignal | null) {
		this.body = new ReadableStream<Uint8Array>({
			start: (c) => {
				this.controller = c;
			},
		});
		if (signal) {
			const onAbort = () => {
				this.aborted = true;
				try {
					this.controller.error(new DOMException("aborted", "AbortError"));
				} catch {
					/* already closed */
				}
			};
			if (signal.aborted) onAbort();
			else signal.addEventListener("abort", onAbort);
		}
	}

	push(frame: string) {
		this.controller.enqueue(new TextEncoder().encode(frame));
	}

	end() {
		try {
			this.controller.close();
		} catch {
			/* already closed */
		}
	}

	fail(err: Error) {
		try {
			this.controller.error(err);
		} catch {
			/* already closed */
		}
	}
}

class FakeFetch {
	calls: Array<{ url: string; init?: RequestInit }> = [];
	streams: FakeStream[] = [];
	// Optional override per-call: returns a Response (or rejects) instead of
	// the default 200 + stream.
	nextResponses: Array<
		(url: string, init?: RequestInit) => Response | Promise<Response>
	> = [];

	fetch: typeof fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
		const url = input.toString();
		this.calls.push({ url, init });
		const override = this.nextResponses.shift();
		if (override) return Promise.resolve(override(url, init));
		const stream = new FakeStream(init?.signal ?? null);
		this.streams.push(stream);
		return Promise.resolve(
			new Response(stream.body, {
				status: 200,
				headers: { "Content-Type": "text/event-stream" },
			}),
		);
	}) as typeof fetch;
}

// Wait until the EventBus has consumed any pending stream chunks. We yield
// the event loop a couple of times so reader.read() completes and the parser
// drains.
async function flush(times = 4) {
	for (let i = 0; i < times; i++) {
		await new Promise((r) => setTimeout(r, 0));
	}
}

function frame(type: string, payload: unknown): string {
	const data = JSON.stringify({
		type,
		torrent_id: "abc",
		time: "2026-04-26T00:00:00Z",
		payload,
	});
	return `event: ${type}\nid: 1\ndata: ${data}\n\n`;
}

let ff: FakeFetch;
beforeEach(() => {
	ff = new FakeFetch();
});

describe("EventBus", () => {
	test("dispatches typed events to subscribers", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		const seen: AnyEvent[] = [];
		bus.on("torrent.added", (ev) => seen.push(ev));
		bus.start();
		await flush();

		ff.streams[0].push(frame("torrent.added", { source: "magnet:?xt=...", name: "foo" }));
		await flush();

		expect(seen).toHaveLength(1);
		expect(seen[0].type).toBe("torrent.added");
		expect(seen[0].torrent_id).toBe("abc");

		bus.stop();
	});

	test("onAny receives every event regardless of type", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		const seen: string[] = [];
		bus.onAny((ev) => seen.push(ev.type));
		bus.start();
		await flush();

		ff.streams[0].push(frame("peer.connected", {
			addr: "1.2.3.4:6881",
			direction: "out",
			transport: "tcp",
		}));
		ff.streams[0].push(frame("dht.reply", { num_peers: 3 }));
		await flush();

		expect(seen).toEqual(["peer.connected", "dht.reply"]);
		bus.stop();
	});

	test("unsubscribe stops further deliveries", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		let count = 0;
		const off = bus.on("dht.reply", () => count++);
		bus.start();
		await flush();

		ff.streams[0].push(frame("dht.reply", { num_peers: 1 }));
		await flush();
		off();
		ff.streams[0].push(frame("dht.reply", { num_peers: 2 }));
		await flush();
		expect(count).toBe(1);
		bus.stop();
	});

	test("ignores malformed payloads", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		let count = 0;
		bus.onAny(() => count++);
		bus.start();
		await flush();

		ff.streams[0].push("event: garbage\ndata: not json {\n\n");
		await flush();
		expect(count).toBe(0);
		bus.stop();
	});

	test("listener exceptions do not break the bus", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		bus.on("dht.reply", () => {
			throw new Error("boom");
		});
		let okCount = 0;
		bus.on("dht.reply", () => okCount++);
		bus.start();
		await flush();

		ff.streams[0].push(frame("dht.reply", { num_peers: 7 }));
		await flush();
		expect(okCount).toBe(1);
		bus.stop();
	});

	test("ignores SSE keepalive comments", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		let count = 0;
		bus.onAny(() => count++);
		bus.start();
		await flush();

		ff.streams[0].push(": keepalive\n\n");
		await flush();
		ff.streams[0].push(frame("dht.reply", { num_peers: 1 }));
		await flush();

		expect(count).toBe(1);
		bus.stop();
	});

	test("handles frames split across chunks", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		const seen: AnyEvent[] = [];
		bus.onAny((ev) => seen.push(ev));
		bus.start();
		await flush();

		const f = frame("dht.reply", { num_peers: 9 });
		ff.streams[0].push(f.slice(0, 10));
		await flush();
		ff.streams[0].push(f.slice(10));
		await flush();

		expect(seen).toHaveLength(1);
		expect(seen[0].type).toBe("dht.reply");
		bus.stop();
	});

	test("stream end triggers reconnect after backoff", async () => {
		const bus = new EventBus({
			initialBackoffMs: 5,
			maxBackoffMs: 20,
			fetchImpl: ff.fetch,
			apiKeyFn: () => "k",
		});
		const stateSeen: string[] = [];
		bus.onState((s) => stateSeen.push(s));
		bus.start();
		await flush();

		expect(ff.streams).toHaveLength(1);
		ff.streams[0].end();
		await new Promise((r) => setTimeout(r, 30));

		expect(ff.streams.length).toBeGreaterThanOrEqual(2);
		expect(stateSeen).toContain("closed");
		expect(stateSeen).toContain("connecting");
		bus.stop();
	});

	test("stream error triggers reconnect after backoff", async () => {
		const bus = new EventBus({
			initialBackoffMs: 5,
			maxBackoffMs: 20,
			fetchImpl: ff.fetch,
			apiKeyFn: () => "k",
		});
		bus.start();
		await flush();

		ff.streams[0].fail(new Error("network broke"));
		await new Promise((r) => setTimeout(r, 30));

		expect(ff.streams.length).toBeGreaterThanOrEqual(2);
		bus.stop();
	});

	test("stop() prevents reconnect", async () => {
		const bus = new EventBus({
			initialBackoffMs: 5,
			fetchImpl: ff.fetch,
			apiKeyFn: () => "k",
		});
		bus.start();
		await flush();
		ff.streams[0].fail(new Error("boom"));
		bus.stop();
		await new Promise((r) => setTimeout(r, 30));
		expect(ff.streams).toHaveLength(1);
	});

	test("appends ?types= filter when configured", async () => {
		const bus = new EventBus({
			types: ["peer.connected", "tracker.reply"],
			fetchImpl: ff.fetch,
			apiKeyFn: () => "k",
		});
		bus.start();
		await flush();
		expect(ff.calls[0].url).toBe(
			"/api/events?types=peer.connected,tracker.reply",
		);
		bus.stop();
	});

	test("start() is idempotent", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		bus.start();
		bus.start();
		bus.start();
		await flush();
		expect(ff.calls).toHaveLength(1);
		bus.stop();
	});

	test("onState replays current state immediately", () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "k" });
		const seen: string[] = [];
		bus.onState((s) => seen.push(s));
		expect(seen).toEqual(["idle"]);
		bus.start();
		expect(seen).toContain("connecting");
		bus.stop();
	});

	test("when fetch is unavailable, transitions to closed", () => {
		const bus = new EventBus({ fetchImpl: null, apiKeyFn: () => "k" });
		bus.start();
		expect(bus.state).toBe("closed");
		expect(bus.lastError).toMatch(/fetch/);
	});

	test("sends Authorization: Bearer header from apiKeyFn", async () => {
		const bus = new EventBus({
			fetchImpl: ff.fetch,
			apiKeyFn: () => "  the-secret  ", // verify trim()
		});
		bus.start();
		await flush();
		const headers = ff.calls[0].init?.headers as Record<string, string>;
		expect(headers.Authorization).toBe("Bearer the-secret");
		expect(headers.Accept).toBe("text/event-stream");
		bus.stop();
	});

	test("omits Authorization when apiKey is empty", async () => {
		const bus = new EventBus({ fetchImpl: ff.fetch, apiKeyFn: () => "" });
		bus.start();
		await flush();
		const headers = ff.calls[0].init?.headers as Record<string, string>;
		expect(headers.Authorization).toBeUndefined();
		bus.stop();
	});

	test("401 response stops without reconnecting", async () => {
		ff.nextResponses.push(() => new Response("nope", { status: 401 }));
		const bus = new EventBus({
			initialBackoffMs: 5,
			fetchImpl: ff.fetch,
			apiKeyFn: () => "wrong",
		});
		bus.start();
		await flush();
		await new Promise((r) => setTimeout(r, 30));

		expect(ff.calls).toHaveLength(1);
		expect(bus.state).toBe("closed");
		expect(bus.lastError).toMatch(/unauthorized/);
		bus.stop();
	});

	test("non-2xx (not 401/403) reconnects", async () => {
		ff.nextResponses.push(() => new Response("server error", { status: 500 }));
		const bus = new EventBus({
			initialBackoffMs: 5,
			maxBackoffMs: 20,
			fetchImpl: ff.fetch,
			apiKeyFn: () => "k",
		});
		bus.start();
		await flush();
		await new Promise((r) => setTimeout(r, 30));

		expect(ff.calls.length).toBeGreaterThanOrEqual(2);
		bus.stop();
	});
});

void mock;
