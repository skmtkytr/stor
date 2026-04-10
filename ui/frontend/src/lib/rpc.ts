import type { TorrentInfo, EngineStats } from "./types";

const API_KEY_STORAGE = "stor_api_key";

let apiKey = $state(typeof localStorage !== "undefined" ? localStorage.getItem(API_KEY_STORAGE) ?? "" : "");

export function getApiKey(): string {
	return apiKey;
}

export function setApiKey(key: string) {
	apiKey = key;
	if (typeof localStorage !== "undefined") {
		if (key) localStorage.setItem(API_KEY_STORAGE, key);
		else localStorage.removeItem(API_KEY_STORAGE);
	}
}

export class RPCError extends Error {
	code: number;
	constructor(code: number, message: string) {
		super(message);
		this.code = code;
	}
}

async function rpc<T>(method: string, params?: Record<string, unknown>): Promise<T> {
	const res = await fetch("/api/rpc", {
		method: "POST",
		headers: {
			"Content-Type": "application/json",
			Authorization: `Bearer ${apiKey.trim()}`,
		},
		body: JSON.stringify({ jsonrpc: "2.0", method, params, id: 1 }),
	});

	if (res.status === 401) {
		setApiKey("");
		throw new RPCError(401, "unauthorized");
	}

	const data = await res.json();
	if (data.error) {
		throw new RPCError(data.error.code, data.error.message);
	}
	return data.result as T;
}

// --- API methods ---

export const api = {
	version: () => rpc<{ version: string }>("daemon.version"),
	stats: () => rpc<EngineStats>("daemon.stats"),
	setMaxActive: (max_active: number) =>
		rpc<{ max_active: number }>("daemon.setMaxActive", { max_active }),

	list: () => rpc<TorrentInfo[]>("torrent.list"),
	get: (id: string) => rpc<TorrentInfo>("torrent.get", { id }),
	add: (source: string) => rpc<{ id: string }>("torrent.add", { source }),
	addFile: (data: string) => rpc<{ id: string }>("torrent.addFile", { data }),
	remove: (id: string, delete_files: boolean) =>
		rpc<object>("torrent.remove", { id, delete_files }),
	pause: (id: string) => rpc<object>("torrent.pause", { id }),
	resume: (id: string) => rpc<object>("torrent.resume", { id }),

	queueTop: (id: string) => rpc<object>("torrent.queueTop", { id }),
	queueUp: (id: string) => rpc<object>("torrent.queueUp", { id }),
	queueDown: (id: string) => rpc<object>("torrent.queueDown", { id }),
	queueBottom: (id: string) => rpc<object>("torrent.queueBottom", { id }),
};
