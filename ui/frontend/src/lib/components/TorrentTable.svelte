<script lang="ts">
	import type { TorrentInfo } from "$lib/types";
	import { torrents } from "$lib/stores/torrents.svelte";
	import { api } from "$lib/rpc";
	import TorrentRow from "./TorrentRow.svelte";
	import ContextActions from "./ContextActions.svelte";
	import { useTable } from "./useTable.svelte";
	import {
		getCoreRowModel,
		getSortedRowModel,
		type ColumnDef,
		type ColumnSizingState,
		type SortingFn,
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

	// --- Bucketing helpers (carried over from the pre-TanStack table) -----
	// Speeds rounded to 100 KB/s and ETA to 10 s so jitter from polling
	// doesn't reorder rows on every refresh.
	const speedBucket = (b: number) => Math.round((b ?? 0) / 102400);
	const etaBucket = (t: TorrentInfo): number => {
		const p = t.progress;
		if (t.state !== "downloading" || !p.down_speed) return Number.POSITIVE_INFINITY;
		return Math.round((p.total - p.downloaded) / p.down_speed / 10);
	};

	const stableNumeric: SortingFn<TorrentInfo> = (a, b, columnId) => {
		const va = a.getValue<number>(columnId);
		const vb = b.getValue<number>(columnId);
		if (va === vb) return a.original.id.localeCompare(b.original.id);
		return va - vb;
	};

	const stableString: SortingFn<TorrentInfo> = (a, b, columnId) => {
		const va = String(a.getValue(columnId));
		const vb = String(b.getValue(columnId));
		const cmp = va.localeCompare(vb);
		return cmp !== 0 ? cmp : a.original.id.localeCompare(b.original.id);
	};

	// --- Column model ----------------------------------------------------
	const columns: ColumnDef<TorrentInfo>[] = [
		{
			id: "queue",
			header: "#",
			accessorFn: (t) => t.queue_position ?? 9999,
			size: 40,
			minSize: 30,
			sortingFn: stableNumeric,
			meta: { align: "center" },
		},
		{
			id: "name",
			header: "Name",
			accessorFn: (t) => t.name ?? t.id,
			size: 360,
			minSize: 100,
			sortingFn: stableString,
			meta: { align: "left" },
		},
		{
			id: "size",
			header: "Size",
			accessorFn: (t) => t.total_bytes ?? 0,
			size: 80,
			minSize: 60,
			sortingFn: stableNumeric,
			meta: { align: "right" },
		},
		{
			id: "progress",
			header: "Progress",
			accessorFn: (t) => Math.round(t.progress.percent ?? 0),
			size: 192,
			minSize: 100,
			sortingFn: stableNumeric,
			meta: { align: "left" },
		},
		{
			id: "down",
			header: "Down",
			accessorFn: (t) =>
				t.state === "downloading" ? speedBucket(t.progress.down_speed) : 0,
			size: 96,
			minSize: 60,
			sortingFn: stableNumeric,
			meta: { align: "right" },
		},
		{
			id: "up",
			header: "Up",
			accessorFn: (t) => speedBucket(t.progress.up_speed),
			size: 96,
			minSize: 60,
			sortingFn: stableNumeric,
			meta: { align: "right" },
		},
		{
			id: "eta",
			header: "ETA",
			accessorFn: etaBucket,
			size: 80,
			minSize: 60,
			sortingFn: stableNumeric,
			meta: { align: "right" },
		},
		{
			id: "peers",
			header: "Peers",
			accessorFn: (t) => t.progress.active_peers ?? 0,
			size: 56,
			minSize: 50,
			sortingFn: stableNumeric,
			meta: { align: "right" },
		},
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
		getSortedRowModel: getSortedRowModel(),
		enableColumnResizing: true,
		columnResizeMode: "onChange",
		initialColumnSizing: initialSizes,
		initialSorting: [{ id: "queue", desc: false }],
	});

	$effect(() => {
		tbl.setData(filtered);
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

	// Explicit reactive deps: table-core's getters don't read Svelte $state
	// internally, so the only way to make these recompute when filtered /
	// sorting / column sizes change is to read those slices in the
	// $derived expression. Without this, the table renders once and then
	// freezes.
	const sortedRows = $derived.by(() => {
		void filtered;
		void tbl.sorting;
		return tbl.table.getRowModel().rows;
	});
	const headers = $derived.by(() => {
		void tbl.columnSizing;
		void tbl.sorting;
		return tbl.table.getHeaderGroups()[0]?.headers ?? [];
	});
	const visibleCols = $derived.by(() => {
		void tbl.columnSizing;
		return tbl.table.getVisibleLeafColumns();
	});

	// --- Selection (kept from previous implementation) -------------------
	function handleRowClick(e: MouseEvent, id: string, idx: number) {
		if ((e.target as HTMLElement).closest("button")) return;
		if (e.shiftKey && lastIdx !== null) {
			const from = Math.min(lastIdx, idx);
			const to = Math.max(lastIdx, idx);
			if (!e.ctrlKey && !e.metaKey) selected = new Set();
			const next = new Set(selected);
			for (let i = from; i <= to; i++) next.add(sortedRows[i].original.id);
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
			selected = new Set(sortedRows.map((r) => r.original.id));
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
	<table class="w-full table-fixed border-collapse">
		<colgroup>
			{#each visibleCols as col (col.id)}
				<col style="width: {col.getSize()}px" />
			{/each}
		</colgroup>
		<thead class="sticky top-0 z-10">
			<tr class="bg-zinc-900 border-b border-zinc-800">
				{#each headers as header (header.id)}
					{@const align = (header.column.columnDef.meta as { align?: string } | undefined)?.align}
					<th
						class="group relative h-8 select-none px-3 text-[11px] font-medium uppercase tracking-wider text-zinc-500 {alignClass(align)}"
						style="width: {header.getSize()}px"
					>
						<button
							class="inline-flex items-center gap-1 hover:text-zinc-200 transition-colors"
							onclick={header.column.getToggleSortingHandler()}
						>
							{header.column.columnDef.header}
							{#if header.column.getIsSorted() === "asc"}
								<span class="text-zinc-200">&uarr;</span>
							{:else if header.column.getIsSorted() === "desc"}
								<span class="text-zinc-200">&darr;</span>
							{/if}
						</button>
						{#if header.column.getCanResize()}
							<!--
								The resize handle: a thin column on the right edge of the
								header. We attach pointerdown so a drag updates column size
								via TanStack's getResizeHandler; double-click resets to the
								column's default size.
							-->
							<div
								role="separator"
								aria-orientation="vertical"
								class="absolute right-0 top-0 z-20 h-full w-1 cursor-col-resize touch-none select-none bg-blue-500/0 hover:bg-blue-500/60 {header.column.getIsResizing() ? 'bg-blue-500' : ''}"
								onpointerdown={(e) => {
									e.stopPropagation();
									header.getResizeHandler()(e);
								}}
								ondblclick={() => header.column.resetSize()}
							></div>
						{/if}
					</th>
				{/each}
			</tr>
		</thead>
		<tbody>
			{#each sortedRows as row, idx (row.original.id)}
				<TorrentRow
					torrent={row.original}
					selected={selected.has(row.original.id)}
					onclick={(e) => handleRowClick(e, row.original.id, idx)}
					oncontextmenu={(e) => handleContextMenu(e, row.original.id)}
				/>
			{:else}
				<tr>
					<td colspan={visibleCols.length} class="h-32 text-center text-zinc-600 text-sm">
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

<ContextActions selectedIds={[...selected]} position={ctxPos} onClose={() => (ctxPos = null)} />
