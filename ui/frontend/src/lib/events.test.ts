import { describe, expect, test, beforeEach, mock } from "bun:test";
import { EventBus } from "./events";
import type { AnyEvent } from "./types";

// --- Minimal in-process fake of the browser EventSource interface so we can
// drive the bus deterministically from tests. The real EventSource cannot be
// instantiated under bun without a network round-trip.

type Handler = (ev: MessageEvent) => void;

class FakeEventSource {
	static instances: FakeEventSource[] = [];

	url: string;
	withCredentials: boolean;
	readyState = 0;
	onopen: ((ev: Event) => void) | null = null;
	onerror: ((ev: Event) => void) | null = null;
	onmessage: Handler | null = null;
	closed = false;

	private listeners = new Map<string, Set<Handler>>();

	constructor(url: string, init?: { withCredentials?: boolean }) {
		this.url = url;
		this.withCredentials = init?.withCredentials ?? false;
		FakeEventSource.instances.push(this);
		// Defer "open" so the bus can attach listeners synchronously.
		queueMicrotask(() => {
			if (this.closed) return;
			this.readyState = 1;
			this.onopen?.(new Event("open"));
		});
	}

	addEventListener(type: string, fn: EventListener) {
		let set = this.listeners.get(type);
		if (!set) {
			set = new Set();
			this.listeners.set(type, set);
		}
		set.add(fn as Handler);
	}

	removeEventListener(type: string, fn: EventListener) {
		this.listeners.get(type)?.delete(fn as Handler);
	}

	close() {
		this.closed = true;
		this.readyState = 2;
	}

	// --- test helpers ---
	emit(type: string, data: unknown) {
		const ev = new MessageEvent(type, { data: JSON.stringify(data) });
		this.listeners.get(type)?.forEach((h) => h(ev));
		if (type === "message") this.onmessage?.(ev);
	}

	error() {
		this.onerror?.(new Event("error"));
	}
}

beforeEach(() => {
	FakeEventSource.instances = [];
});

describe("EventBus", () => {
	test("dispatches typed events to subscribers", async () => {
		const bus = new EventBus({
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		const seen: AnyEvent[] = [];
		bus.on("torrent.added", (ev) => seen.push(ev));
		bus.start();

		const src = FakeEventSource.instances[0];
		src.emit("torrent.added", {
			type: "torrent.added",
			torrent_id: "abc",
			time: "2026-04-26T00:00:00Z",
			payload: { source: "magnet:?xt=...", name: "foo" },
		});

		expect(seen).toHaveLength(1);
		expect(seen[0].type).toBe("torrent.added");
		expect(seen[0].torrent_id).toBe("abc");

		bus.stop();
	});

	test("onAny receives every event regardless of type", async () => {
		const bus = new EventBus({
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		const seen: string[] = [];
		bus.onAny((ev) => seen.push(ev.type));
		bus.start();

		const src = FakeEventSource.instances[0];
		src.emit("peer.connected", {
			type: "peer.connected",
			torrent_id: "abc",
			time: "t",
			payload: { addr: "1.2.3.4:6881", direction: "out", transport: "tcp" },
		});
		src.emit("dht.reply", {
			type: "dht.reply",
			time: "t",
			payload: { num_peers: 3 },
		});

		expect(seen).toEqual(["peer.connected", "dht.reply"]);
		bus.stop();
	});

	test("unsubscribe stops further deliveries", async () => {
		const bus = new EventBus({
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		let count = 0;
		const off = bus.on("dht.reply", () => count++);
		bus.start();

		const src = FakeEventSource.instances[0];
		src.emit("dht.reply", {
			type: "dht.reply",
			time: "t",
			payload: { num_peers: 1 },
		});
		off();
		src.emit("dht.reply", {
			type: "dht.reply",
			time: "t",
			payload: { num_peers: 2 },
		});
		expect(count).toBe(1);
		bus.stop();
	});

	test("ignores malformed payloads", async () => {
		const bus = new EventBus({
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		let count = 0;
		bus.onAny(() => count++);
		bus.start();

		const src = FakeEventSource.instances[0];
		// Bypass our emit() helper to send invalid JSON.
		const ev = new MessageEvent("message", { data: "not json {" });
		src.onmessage?.(ev);
		expect(count).toBe(0);
		bus.stop();
	});

	test("listener exceptions do not break the bus", async () => {
		const bus = new EventBus({
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		bus.on("dht.reply", () => {
			throw new Error("boom");
		});
		let okCount = 0;
		bus.on("dht.reply", () => okCount++);
		bus.start();

		const src = FakeEventSource.instances[0];
		src.emit("dht.reply", {
			type: "dht.reply",
			time: "t",
			payload: { num_peers: 7 },
		});
		expect(okCount).toBe(1);
		bus.stop();
	});

	test("error triggers reconnect after backoff", async () => {
		const bus = new EventBus({
			initialBackoffMs: 5,
			maxBackoffMs: 20,
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		const stateSeen: string[] = [];
		bus.onState((s) => stateSeen.push(s));
		bus.start();

		expect(FakeEventSource.instances).toHaveLength(1);
		FakeEventSource.instances[0].error();

		// Wait a touch longer than backoff for the reconnect timer to fire.
		await new Promise((r) => setTimeout(r, 30));
		expect(FakeEventSource.instances.length).toBeGreaterThanOrEqual(2);
		expect(FakeEventSource.instances[0].closed).toBe(true);
		expect(stateSeen).toContain("closed");
		expect(stateSeen).toContain("connecting");
		bus.stop();
	});

	test("stop() prevents reconnect", async () => {
		const bus = new EventBus({
			initialBackoffMs: 5,
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		bus.start();
		FakeEventSource.instances[0].error();
		bus.stop();
		await new Promise((r) => setTimeout(r, 30));
		expect(FakeEventSource.instances).toHaveLength(1);
	});

	test("appends ?types= filter when configured", () => {
		const bus = new EventBus({
			types: ["peer.connected", "tracker.reply"],
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		bus.start();
		expect(FakeEventSource.instances[0].url).toBe(
			"/api/events?types=peer.connected,tracker.reply",
		);
		bus.stop();
	});

	test("start() is idempotent", () => {
		const bus = new EventBus({
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		bus.start();
		bus.start();
		bus.start();
		expect(FakeEventSource.instances).toHaveLength(1);
		bus.stop();
	});

	test("onState replays current state immediately", () => {
		const bus = new EventBus({
			eventSourceCtor: FakeEventSource as unknown as typeof EventSource,
		});
		const seen: string[] = [];
		bus.onState((s) => seen.push(s));
		expect(seen).toEqual(["idle"]);
		bus.start();
		expect(seen).toContain("connecting");
		bus.stop();
	});

	test("when EventSource is unavailable, transitions to closed", () => {
		const bus = new EventBus({
			// Force the missing-EventSource branch.
			eventSourceCtor: undefined as unknown as typeof EventSource,
		});
		bus.start();
		expect(bus.state).toBe("closed");
		expect(bus.lastError).toMatch(/EventSource/);
	});
});

// Silence "mock is unused" lint warning when mock helpers aren't referenced.
void mock;
