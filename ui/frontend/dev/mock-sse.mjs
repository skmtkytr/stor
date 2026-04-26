#!/usr/bin/env node
// Mock SSE server for local UI development.
//
// Until the daemon's `/api/events` endpoint lands, this serves both `/api/rpc`
// (with a stubbed torrent list / stats) and `/api/events` (a deterministic
// stream of fake events) on a single port. Run alongside `vite dev` and point
// the dev proxy at it, or set up a simple reverse proxy.
//
//   node ui/frontend/dev/mock-sse.mjs           # default :8765
//   PORT=4000 node ui/frontend/dev/mock-sse.mjs

import http from "node:http";

const PORT = Number(process.env.PORT ?? 8765);

const torrents = [
	{
		id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		name: "ubuntu-24.04-desktop-amd64.iso",
		source: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		state: "downloading",
		queue_position: 0,
		progress: {
			state: "downloading", downloaded: 524288000, uploaded: 0, total: 5_368_709_120,
			percent: 9.77, down_speed: 4_500_000, up_speed: 0,
			active_peers: 12, known_peers: 80, total_pieces: 1280, done_pieces: 125,
		},
		save_path: "/downloads", total_bytes: 5_368_709_120,
		added_at: Math.floor(Date.now() / 1000) - 600, completed_at: 0,
	},
	{
		id: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		name: "debian-13.0-netinst.iso",
		source: "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		state: "seeding",
		queue_position: 1,
		progress: {
			state: "seeding", downloaded: 700_000_000, uploaded: 1_500_000_000, total: 700_000_000,
			percent: 100, down_speed: 0, up_speed: 250_000,
			active_peers: 4, known_peers: 30, total_pieces: 600, done_pieces: 600,
		},
		save_path: "/downloads", total_bytes: 700_000_000,
		added_at: Math.floor(Date.now() / 1000) - 7200, completed_at: Math.floor(Date.now() / 1000) - 600,
	},
];

const stats = () => ({
	total_down_speed: torrents.reduce((s, t) => s + t.progress.down_speed, 0),
	total_up_speed: torrents.reduce((s, t) => s + t.progress.up_speed, 0),
	total_downloaded: 524288000, total_uploaded: 1_500_000_000,
	active_torrents: torrents.filter((t) => t.state !== "paused").length,
	seeding_torrents: torrents.filter((t) => t.state === "seeding").length,
	total_torrents: torrents.length, max_active: 5,
	total_peers: torrents.reduce((s, t) => s + t.progress.active_peers, 0),
	total_known_peers: torrents.reduce((s, t) => s + t.progress.known_peers, 0),
	dht_nodes: 47, free_space: 100_000_000_000,
	config: {
		download_dir: "/downloads", tmp_dir: "/tmp/stor", max_active: 5,
		max_peers: 60, max_pipeline: 5, dial_timeout: 5, numwant: 200,
		log_level: "info", enable_utp: true,
	},
});

function rpcResult(method) {
	switch (method) {
		case "daemon.version": return { version: "0.0.0-dev-mock" };
		case "daemon.stats": return stats();
		case "torrent.list": return torrents;
		default: return null;
	}
}

const server = http.createServer((req, res) => {
	res.setHeader("Access-Control-Allow-Origin", "*");
	res.setHeader("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
	res.setHeader("Access-Control-Allow-Headers", "Content-Type, Authorization");

	if (req.method === "OPTIONS") {
		res.writeHead(204).end();
		return;
	}

	if (req.url?.startsWith("/api/rpc") && req.method === "POST") {
		let body = "";
		req.on("data", (c) => (body += c));
		req.on("end", () => {
			let parsed;
			try { parsed = JSON.parse(body); } catch { parsed = {}; }
			res.writeHead(200, { "Content-Type": "application/json" });
			res.end(JSON.stringify({
				jsonrpc: "2.0", id: parsed.id ?? 1, result: rpcResult(parsed.method),
			}));
		});
		return;
	}

	if (req.url?.startsWith("/api/events")) {
		res.writeHead(200, {
			"Content-Type": "text/event-stream",
			"Cache-Control": "no-cache",
			Connection: "keep-alive",
		});
		const send = (type, payload, torrentId) => {
			const ev = {
				type, torrent_id: torrentId, time: new Date().toISOString(), payload,
			};
			res.write(`event: ${type}\n`);
			res.write(`id: ${Date.now()}\n`);
			res.write(`data: ${JSON.stringify(ev)}\n\n`);
		};
		// Initial burst
		send("dht.reply", { num_peers: 12 });
		send("tracker.reply", { tracker: "udp://tracker.example:6969", num_peers: 30, interval_seconds: 1800 }, torrents[0].id);

		// Periodic activity
		let i = 0;
		const iv = setInterval(() => {
			i++;
			if (i % 4 === 0) {
				send("peer.connected", {
					addr: `10.0.0.${i % 255}:6881`, direction: "out", transport: i % 2 ? "tcp" : "utp",
				}, torrents[0].id);
			}
			if (i % 6 === 0) {
				send("peer.disconnected", { addr: `10.0.0.${(i - 4) % 255}:6881`, reason: "idle" }, torrents[0].id);
			}
			if (i % 12 === 0) {
				send("piece.complete", { index: i, have: 125 + i, total: 1280 }, torrents[0].id);
			}
			if (i === 30) {
				send("torrent.state_changed", { from: "downloading", to: "seeding" }, torrents[0].id);
			}
			if (i === 45) {
				send("tracker.error", { tracker: "udp://flaky.example:6969", error: "i/o timeout" }, torrents[1].id);
			}
		}, 500);

		req.on("close", () => clearInterval(iv));
		return;
	}

	res.writeHead(404).end("not found");
});

server.listen(PORT, () => {
	console.log(`mock stor daemon listening on http://localhost:${PORT}`);
	console.log(`  POST /api/rpc      JSON-RPC stub`);
	console.log(`  GET  /api/events   SSE event stream`);
});
