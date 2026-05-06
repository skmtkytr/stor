<script lang="ts">
	import type { TorrentInfo } from "$lib/types";
	import { torrents } from "$lib/stores/torrents.svelte";
	import { api } from "$lib/rpc";
	import TorrentRow from "./TorrentRow.svelte";
	import ContextActions from "./ContextActions.svelte";
	import { useTable } from "./useTable.svelte";
	import {
		getCoreRowModel,
		type ColumnDef,
		type ColumnSizingState,
	} from "@tanstack/table-core";

	let {
		filter = "all",
		onSelectionChange,
	}: {
		filter: string;
		onSelectionChange: (ids: string[]) => void;
	} = $props();

	let selected = $state(new Set<string>());
	let lastIdx = $state<number | null>(null);
	let ctxPos = $state<{ x: number; y: number } | null>(null);
	// Sort state lives outside table-core. Driving sort through TanStack
	// would require pushing torrents.list → table.setOptions on every poll,
	// and Svelte's $effect/$derived ordering meant the displayed rows
	// could lag a tick behind the latest data. Doing sort in JS keeps
	// data flow strictly inside one $derived and stays correct on every
	// poll; TanStack's role is now scoped to columns + resizing.
	let sortKey = $state<string>("queue");
	let sortDesc = $state(false);

	function toggleSort(key: string) {
		if (sortKey === key) sortDesc = !sortDesc;
		else {
			sortKey = key;
			sortDesc = false;
		}
	}

	$effect(() => {
		onSelectionChange([...selected]);
	});

	// --- Filtering --------------------------------------------------------
	const filterFn: Record<string, (t: TorrentInfo) => boolean> = {
		all: () => true,
		downloading: (t) => t.state === "downloading" || t.state === "metadata" || t.state === "adding",
		seeding: (t) => t.state === "seeding",
		complete: (t) => t.state === "complete",
		paused: (t) => t.state === "paused",
		error: (t) => t.state === "error",
	};

	const filtered = $derived(torrents.list.filter(filterFn[filter] ?? filterFn.all));

	// --- Sort value extraction (with bucketing) ---------------------------
	// Speeds rounded to 100 KB/s and ETA to 10 s so jitter from polling
	// doesn't reorder rows on every refresh.
	const speedBucket = (b: number) => Math.round((b ?? 0) / 102400);
	const stateOrder: Record<string, number> = {
		downloading: 0,
		metadata: 1,
		adding: 2,
		seeding: 3,
		paused: 4,
		error: 5,
		complete: 6,
	};

	function getSortValue(t: TorrentInfo, key: string): number | string {
		switch (key) {
			case "queue":
				return t.queue_position ?? 9999;
			case "name":
				return t.name ?? t.id;
			case "size":
				return t.total_bytes ?? 0;
			case "progress":
				return Math.round(t.progress.percent ?? 0);
			case "down":
				return t.state === "downloading" ? speedBucket(t.progress.down_speed) : 0;
			case "up":
				return speedBucket(t.progress.up_speed);
			case "eta": {
				const p = t.progress;
				if (t.state !== "downloading" || !p.down_speed) return Number.POSITIVE_INFINITY;
				return Math.round((p.total - p.downloaded) / p.down_speed / 10);
			}
			case "peers":
				return t.progress.active_peers ?? 0;
			case "state":
				return stateOrder[t.state] ?? 9;
			default:
				return 0;
		}
	}

	// --- Column model (TanStack — sizing only) ---------------------------
	// accessorFn / sortingFn are intentionally absent: sorting is done in
	// JS (see sortedRows below) so it can't lag behind polling. TanStack
	// here is purely the source of truth for column sizes / resize state.
	const columns: ColumnDef<TorrentInfo>[] = [
		{ id: "queue", header: "#", size: 40, minSize: 30, meta: { align: "center", label: "#" } },
		{ id: "name", header: "Name", size: 480, minSize: 100, meta: { align: "left", label: "Name" } },
		{ id: "size", header: "Size", size: 80, minSize: 60, meta: { align: "right", label: "Size" } },
		{ id: "progress", header: "Progress", size: 192, minSize: 100, meta: { align: "left", label: "Progress" } },
		{ id: "down", header: "Down", size: 96, minSize: 60, meta: { align: "right", label: "Down" } },
		{ id: "up", header: "Up", size: 96, minSize: 60, meta: { align: "right", label: "Up" } },
		{ id: "eta", header: "ETA", size: 80, minSize: 60, meta: { align: "right", label: "ETA" } },
		{ id: "peers", header: "Peers", size: 56, minSize: 50, meta: { align: "right", label: "Peers" } },
	];

	// --- Persisted column widths -----------------------------------------
	const STORAGE_KEY = "stor.torrentTable.colSizes";
	let initialSizes: ColumnSizingState = {};
	if (typeof localStorage !== "undefined") {
		try {
			const raw = localStorage.getItem(STORAGE_KEY);
			if (raw) initialSizes = JSON.parse(raw) as ColumnSizingState;
		} catch {
			/* corrupt entry, ignore */
		}
	}

	const tbl = useTable<TorrentInfo>({
		data: [],
		columns,
		getCoreRowModel: getCoreRowModel(),
		enableColumnResizing: true,
		columnResizeMode: "onChange",
		initialColumnSizing: initialSizes,
	});

	// Persist user-resized widths so they survive reloads.
	$effect(() => {
		const sizes = tbl.columnSizing;
		if (typeof localStorage === "undefined") return;
		try {
			localStorage.setItem(STORAGE_KEY, JSON.stringify(sizes));
		} catch {
			/* quota or disabled */
		}
	});

	// JS-side sort (no TanStack involvement). One $derived: read filtered
	// + sort key/direction → return a freshly sorted array. No effect/
	// derived ordering pitfalls; rows always reflect the current poll.
	const sortedRows = $derived.by(() => {
		const data = filtered;
		const key = sortKey;
		const desc = sortDesc;
		return [...data].sort((a, b) => {
			const va = getSortValue(a, key);
			const vb = getSortValue(b, key);
			const cmp =
				typeof va === "string"
					? va.localeCompare(vb as string)
					: (va as number) - (vb as number);
			const result = desc ? -cmp : cmp;
			// Tie-break by ID so equal-key rows don't reshuffle on each poll.
			return result !== 0 ? result : a.id.localeCompare(b.id);
		});
	});

	// table-core memoises getHeaderGroups / getVisibleLeafColumns and
	// returns the same array reference across renders unless the column
	// set / pinning state changes. Width lives in columnSizing, which is
	// NOT a memo input, so the plain getter would hand back a stale array
	// and Svelte (===-equality on $derived outputs) would skip the
	// re-render. Build fresh descriptors that include the live size to
	// force {#each} to iterate.
	const headers = $derived.by(() => {
		const sizing = tbl.columnSizing;
		return (tbl.table.getHeaderGroups()[0]?.headers ?? []).map((h) => ({
			id: h.id,
			ref: h,
			size: h.getSize(),
			sizingTouch: sizing[h.id],
		}));
	});
	const visibleCols = $derived.by(() => {
		const sizing = tbl.columnSizing;
		return tbl.table.getVisibleLeafColumns().map((c) => ({
			id: c.id,
			ref: c,
			size: c.getSize(),
			sizingTouch: sizing[c.id],
		}));
	});

	// Grid template that drives both header and rows. Rebuilt whenever any
	// column size changes; passed down as a CSS variable so every row picks
	// it up without prop plumbing.
	const gridTemplate = $derived(visibleCols.map((c) => `${c.size}px`).join(" "));

	// --- Selection (kept from previous implementation) -------------------
	function handleRowClick(e: MouseEvent, id: string, idx: number) {
		if ((e.target as HTMLElement).closest("button")) return;
		if (e.shiftKey && lastIdx !== null) {
			const from = Math.min(lastIdx, idx);
			const to = Math.max(lastIdx, idx);
			if (!e.ctrlKey && !e.metaKey) selected = new Set();
			const next = new Set(selected);
			for (let i = from; i <= to; i++) next.add(sortedRows[i].id);
			selected = next;
		} else if (e.ctrlKey || e.metaKey) {
			const next = new Set(selected);
			if (next.has(id)) next.delete(id);
			else next.add(id);
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

	function alignClass(align: unknown): string {
		switch (align) {
			case "right":
				return "text-right";
			case "center":
				return "text-center";
			default:
				return "text-left";
		}
	}
</script>

<svelte:window
	onkeydown={(e) => {
		if (e.key === "a" && (e.ctrlKey || e.metaKey) && !(e.target instanceof HTMLInputElement)) {
			e.preventDefault();
			selected = new Set(sortedRows.map((r) => r.id));
		}
		if (e.key === "Escape") {
			selected = new Set();
			lastIdx = null;
			ctxPos = null;
		}
		if (e.key === "Delete" && selected.size > 0) batchAction("remove");
	}}
/>

<!--
	CSS Grid layout (not <table>). Column widths come straight from
	grid-template-columns, which we set per-row from a single CSS
	variable. Dragging a column updates that one entry; the other
	entries stay literal pixel values, so no rebalancing or rescaling
	can happen. The wrapper's overflow-x scrolls when total > viewport.
-->
<div class="flex-1 overflow-auto" style="--grid-cols: {gridTemplate}">
	<div role="table" class="min-w-max">
		<!-- Header -->
		<div
			role="row"
			class="sticky top-0 z-10 grid h-8 border-b border-zinc-800 bg-zinc-900"
			style="grid-template-columns: var(--grid-cols)"
		>
			{#each headers as h (h.id)}
				{@const align = (h.ref.column.columnDef.meta as { align?: string } | undefined)?.align}
				<div
					role="columnheader"
					class="group relative flex h-full select-none items-center px-3 text-[11px] font-medium uppercase tracking-wider text-zinc-500 {alignClass(align) === 'text-right' ? 'justify-end' : alignClass(align) === 'text-center' ? 'justify-center' : 'justify-start'}"
				>
					<button
						class="inline-flex items-center gap-1 hover:text-zinc-200 transition-colors"
						onclick={() => toggleSort(h.id)}
					>
						{h.ref.column.columnDef.header}
						{#if sortKey === h.id}
							<span class="text-zinc-200">{sortDesc ? "\u2193" : "\u2191"}</span>
						{/if}
					</button>
					{#if h.ref.column.getCanResize()}
						<!--
							12px hit area centred on the column boundary, with a
							1px zinc divider that brightens on hover and turns
							blue while dragging. onmousedown + ontouchstart are
							the pair table-core's getResizeHandler natively
							supports (pointerdown fails its touch-vs-mouse
							detection). Double-click resets to default width.
						-->
						<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
						<div
							role="separator"
							aria-orientation="vertical"
							class="absolute top-0 z-20 flex h-full w-3 cursor-col-resize touch-none select-none items-center justify-center"
							style="right: -6px"
							onmousedown={h.ref.getResizeHandler()}
							ontouchstart={h.ref.getResizeHandler()}
							ondblclick={() => h.ref.column.resetSize()}
						>
							<span
								class="h-4 w-px transition-colors {h.ref.column.getIsResizing()
									? 'w-0.5 bg-blue-400'
									: 'bg-zinc-700 group-hover:bg-zinc-500'}"
							></span>
						</div>
					{/if}
				</div>
			{/each}
		</div>

		<!-- Body -->
		<div role="rowgroup">
			{#each sortedRows as t, idx (t.id)}
				<TorrentRow
					torrent={t}
					selected={selected.has(t.id)}
					onclick={(e) => handleRowClick(e, t.id, idx)}
					oncontextmenu={(e) => handleContextMenu(e, t.id)}
				/>
			{:else}
				<div role="row" class="flex h-32 items-center justify-center text-center text-sm text-zinc-600">
					No torrents. Add a magnet link or drop a .torrent file.
				</div>
			{/each}
		</div>
	</div>
</div>

{#if selected.size > 0}
	<div class="border-t border-zinc-800 px-3 py-1 text-xs text-zinc-500">
		{selected.size} selected — Ctrl+A all &middot; Esc deselect &middot; Del remove
	</div>
{/if}

<ContextActions selectedIds={[...selected]} position={ctxPos} onClose={() => (ctxPos = null)} />
