<script lang="ts">
	import type { TorrentInfo } from "$lib/types";
	import { torrents } from "$lib/stores/torrents.svelte";
	import { api } from "$lib/rpc";
	import TorrentRow from "./TorrentRow.svelte";
	import ContextActions from "./ContextActions.svelte";

	let {
		filter = "all",
		onSelectionChange,
	}: {
		filter: string;
		onSelectionChange: (ids: string[]) => void;
	} = $props();

	let selected = $state(new Set<string>());
	let lastIdx = $state<number | null>(null);
	let sortKey = $state<string>("queue");
	let sortDesc = $state(false);
	let ctxPos = $state<{ x: number; y: number } | null>(null);

	// Notify parent of selection changes
	$effect(() => {
		onSelectionChange([...selected]);
	});

	// --- Filtering ---
	const filterFn: Record<string, (t: TorrentInfo) => boolean> = {
		all: () => true,
		downloading: (t) => t.state === "downloading" || t.state === "metadata" || t.state === "adding",
		seeding: (t) => t.state === "seeding",
		complete: (t) => t.state === "complete",
		paused: (t) => t.state === "paused",
		error: (t) => t.state === "error",
	};

	const filtered = $derived(torrents.list.filter(filterFn[filter] ?? filterFn.all));

	// --- Sorting (dynamic: re-sort every poll) ---
	const stateOrder: Record<string, number> = {
		downloading: 0, metadata: 1, adding: 2, seeding: 3, paused: 4, error: 5, complete: 6,
	};

	function getSortValue(t: TorrentInfo, key: string): number | string {
		switch (key) {
			case "name": return t.name ?? t.id;
			case "size": return t.total_bytes ?? 0;
			case "progress": return Math.round(t.progress.percent ?? 0);
			case "down": {
				// Bucket by 100KB/s to avoid jitter from small speed fluctuations
				const raw = t.state === "downloading" ? (t.progress.down_speed ?? 0) : 0;
				return Math.round(raw / 102400);
			}
			case "up": {
				const raw = t.progress.up_speed ?? 0;
				return Math.round(raw / 102400);
			}
			case "eta": {
				const p = t.progress;
				if (t.state !== "downloading" || !p.down_speed) return Infinity;
				// Bucket by 10s to avoid jitter
				return Math.round((p.total - p.downloaded) / p.down_speed / 10);
			}
			case "peers": return t.progress.active_peers ?? 0;
			case "state": return stateOrder[t.state] ?? 9;
			case "queue": return t.queue_position ?? 9999;
			default: return 0;
		}
	}

	// Always re-sort on every data update — read torrents.list directly to
	// create a reactive dependency on the raw polling data, not just the
	// filtered reference. This ensures progress/speed changes trigger re-sort.
	const sorted = $derived.by(() => {
		// Explicit dependency: read the raw list length + first item to ensure
		// Svelte tracks the array identity change from polling.
		const _raw = torrents.list;
		const key = sortKey;
		const desc = sortDesc;
		return [...filtered].sort((a, b) => {
			const va = getSortValue(a, key);
			const vb = getSortValue(b, key);
			const cmp = typeof va === "string"
				? va.localeCompare(vb as string)
				: (va as number) - (vb as number);
			const result = desc ? -cmp : cmp;
			// Stable sort: tie-break by ID to prevent row jumping
			return result !== 0 ? result : a.id.localeCompare(b.id);
		});
	});

	function toggleSort(key: string) {
		if (sortKey === key) sortDesc = !sortDesc;
		else { sortKey = key; sortDesc = false; }
	}

	// --- Selection ---
	function handleRowClick(e: MouseEvent, id: string, idx: number) {
		if ((e.target as HTMLElement).closest("button")) return;
		if (e.shiftKey && lastIdx !== null) {
			const from = Math.min(lastIdx, idx);
			const to = Math.max(lastIdx, idx);
			if (!e.ctrlKey && !e.metaKey) selected = new Set();
			const next = new Set(selected);
			for (let i = from; i <= to; i++) next.add(sorted[i].id);
			selected = next;
		} else if (e.ctrlKey || e.metaKey) {
			const next = new Set(selected);
			if (next.has(id)) next.delete(id); else next.add(id);
			selected = next;
		} else {
			selected = new Set([id]);
		}
		lastIdx = idx;
	}

	function handleContextMenu(e: MouseEvent, id: string) {
		e.preventDefault();
		if (!selected.has(id)) selected = new Set([id]);
		ctxPos = { x: e.clientX, y: e.clientY };
	}

	// --- Batch actions (called from parent via exported function) ---
	export async function batchAction(type: string) {
		const ids = [...selected];
		if (!ids.length) return;

		if (type === "remove") {
			if (!confirm(`Remove ${ids.length} torrent(s)?`)) return;
			const del = confirm("Also delete downloaded files?");
			await Promise.all(ids.map((id) => api.remove(id, del).catch(() => {})));
			selected = new Set();
		} else if (type === "pause") {
			await Promise.all(ids.map((id) => api.pause(id).catch(() => {})));
		} else if (type === "resume") {
			await Promise.all(ids.map((id) => api.resume(id).catch(() => {})));
		} else if (type === "queueTop") {
			for (const id of [...ids].reverse()) await api.queueTop(id).catch(() => {});
		} else if (type === "queueBottom") {
			for (const id of ids) await api.queueBottom(id).catch(() => {});
		}
	}

	const columns = [
		{ id: "queue", label: "#", align: "center" },
		{ id: "name", label: "Name", align: "left" },
		{ id: "size", label: "Size", align: "right" },
		{ id: "progress", label: "Progress", align: "left" },
		{ id: "down", label: "Down", align: "right" },
		{ id: "up", label: "Up", align: "right" },
		{ id: "eta", label: "ETA", align: "right" },
		{ id: "peers", label: "Peers", align: "right" },
	] as const;

	// Column widths
	const colWidths: Record<string, string> = {
		queue: "w-10",
		name: "",
		size: "w-20",
		progress: "w-48",
		down: "w-24",
		up: "w-24",
		eta: "w-20",
		peers: "w-14",
	};
</script>

<svelte:window
	onkeydown={(e) => {
		if (e.key === "a" && (e.ctrlKey || e.metaKey) && !(e.target instanceof HTMLInputElement)) {
			e.preventDefault();
			selected = new Set(sorted.map((t) => t.id));
		}
		if (e.key === "Escape") {
			selected = new Set();
			lastIdx = null;
			ctxPos = null;
		}
		if (e.key === "Delete" && selected.size > 0) batchAction("remove");
	}}
/>

<div class="flex-1 overflow-auto">
	<table class="w-full border-collapse">
		<thead class="sticky top-0 z-10">
			<tr class="bg-zinc-900 border-b border-zinc-800">
				{#each columns as col}
					<th
						class="h-8 select-none px-3 text-[11px] font-medium uppercase tracking-wider text-zinc-500 {colWidths[col.id] ?? ''}
							{col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : 'text-left'}"
					>
						<button
							class="inline-flex items-center gap-1 hover:text-zinc-200 transition-colors"
							onclick={() => toggleSort(col.id)}
						>
							{col.label}
							{#if sortKey === col.id}
								<span class="text-zinc-200">{sortDesc ? "\u2193" : "\u2191"}</span>
							{/if}
						</button>
					</th>
				{/each}
			</tr>
		</thead>
		<tbody>
			{#each sorted as t, idx (t.id)}
				<TorrentRow
					torrent={t}
					selected={selected.has(t.id)}
					onclick={(e) => handleRowClick(e, t.id, idx)}
					oncontextmenu={(e) => handleContextMenu(e, t.id)}
				/>
			{:else}
				<tr>
					<td colspan={columns.length} class="h-32 text-center text-zinc-600 text-sm">
						No torrents. Add a magnet link or drop a .torrent file.
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

{#if selected.size > 0}
	<div class="border-t border-zinc-800 px-3 py-1 text-xs text-zinc-500">
		{selected.size} selected — Ctrl+A all &middot; Esc deselect &middot; Del remove
	</div>
{/if}

<ContextActions
	selectedIds={[...selected]}
	position={ctxPos}
	onClose={() => (ctxPos = null)}
/>
