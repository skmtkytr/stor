<script lang="ts">
	import { api } from "$lib/rpc";
	import { formatBytes } from "$lib/format";
	import type { FileEntry } from "$lib/types";
	import FileContextMenu from "./FileContextMenu.svelte";

	const PRIORITY_NORMAL = 0;
	const PRIORITY_SKIP = -1;

	let {
		torrentId,
	}: {
		torrentId: string;
	} = $props();

	let files = $state<FileEntry[]>([]);
	let error = $state<string | null>(null);
	let initialized = $state(false);
	// Track file indices currently being toggled so we don't let refresh() flicker
	// the button state before the server-confirmed value comes back.
	let pending = $state<Set<number>>(new Set());
	// Context menu state: index + current priority + screen position.
	let ctx = $state<{
		index: number;
		priority: number;
		path: string;
		position: { x: number; y: number };
	} | null>(null);

	function handleContextMenu(e: MouseEvent, f: FileEntry, i: number) {
		e.preventDefault();
		ctx = {
			index: i,
			priority: f.priority ?? 0,
			path: f.path,
			position: { x: e.clientX, y: e.clientY },
		};
	}

	async function refresh() {
		try {
			files = await api.files(torrentId);
			error = null;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			initialized = true;
		}
	}

	async function togglePriority(index: number, current: number) {
		const next = current === PRIORITY_SKIP ? PRIORITY_NORMAL : PRIORITY_SKIP;
		const nextPending = new Set(pending);
		nextPending.add(index);
		pending = nextPending;
		try {
			await api.setFilePriority(torrentId, index, next);
			await refresh();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			const after = new Set(pending);
			after.delete(index);
			pending = after;
		}
	}

	$effect(() => {
		void torrentId;
		initialized = false;
		refresh();
		const iv = setInterval(refresh, 2000);
		return () => clearInterval(iv);
	});

	const totalSize = $derived(files.reduce((s, f) => s + f.length, 0));
	const totalDownloaded = $derived(files.reduce((s, f) => s + (f.downloaded ?? 0), 0));
	const totalPercent = $derived(totalSize > 0 ? (totalDownloaded / totalSize) * 100 : 0);

	function pct(f: FileEntry): number {
		if (!f.length) return 0;
		return Math.min(100, Math.max(0, ((f.downloaded ?? 0) / f.length) * 100));
	}
</script>

<div class="flex h-full flex-col">
	<div class="flex shrink-0 items-center gap-4 border-b border-zinc-800 px-4 py-2 text-xs text-zinc-400">
		<span class="uppercase tracking-wider text-zinc-500">
			Files ({files.length})
		</span>
		{#if files.length > 0}
			<span class="text-zinc-600">·</span>
			<span class="tabular-nums">
				{formatBytes(totalDownloaded)} / {formatBytes(totalSize)}
			</span>
			<span class="tabular-nums text-zinc-200">{totalPercent.toFixed(1)}%</span>
		{/if}
	</div>
	<div class="min-h-0 flex-1 overflow-auto">
		{#if !initialized && files.length === 0}
			<div class="px-4 py-3 text-xs text-zinc-500">Loading…</div>
		{:else if error && files.length === 0}
			<div class="px-4 py-3 text-xs text-red-400">Error: {error}</div>
		{:else if files.length === 0}
			<div class="px-4 py-3 text-xs text-zinc-500">
				No file metadata yet. Waiting for magnet resolution.
			</div>
		{:else}
			<table class="w-full border-collapse text-xs">
				<thead>
					<tr class="border-b border-zinc-800 text-[10px] uppercase tracking-wider text-zinc-500">
						<th class="px-3 py-1 text-left font-medium">Path</th>
						<th class="px-3 py-1 text-left font-medium w-48">Progress</th>
						<th class="px-3 py-1 text-right font-medium w-24">Size</th>
						<th class="px-3 py-1 text-center font-medium w-20">Priority</th>
					</tr>
				</thead>
				<tbody>
					{#each files as f, i (f.path)}
						{@const percent = pct(f)}
						{@const done = percent >= 99.99}
						{@const skipped = (f.priority ?? 0) === PRIORITY_SKIP}
						{@const busy = pending.has(i)}
						<tr
							class="border-b border-zinc-900/80 hover:bg-zinc-900/40 {skipped ? 'opacity-60' : ''}"
							oncontextmenu={(e) => handleContextMenu(e, f, i)}
						>
							<td class="break-all px-3 py-1 font-mono {skipped ? 'text-zinc-500 line-through' : 'text-zinc-300'}">{f.path}</td>
							<td class="px-3 py-1">
								<div class="relative h-4 w-full overflow-hidden rounded {skipped ? 'bg-zinc-700/20' : done ? 'bg-green-500/15' : 'bg-blue-500/15'}">
									<div
										class="absolute inset-y-0 left-0 rounded {skipped ? 'bg-zinc-500' : done ? 'bg-green-500' : 'bg-blue-500'}"
										style="width: {percent}%; opacity: 0.35;"
									></div>
									<div class="relative flex h-full items-center justify-center text-[10px] tabular-nums {skipped ? 'text-zinc-400' : done ? 'text-green-300' : 'text-blue-300'}">
										{percent.toFixed(1)}%
									</div>
								</div>
							</td>
							<td class="px-3 py-1 text-right tabular-nums text-zinc-400">
								{formatBytes(f.length)}
							</td>
							<td class="px-3 py-1 text-center">
								<button
									type="button"
									class="rounded px-2 py-0.5 text-[10px] font-medium uppercase tracking-wider transition-colors disabled:opacity-50 {skipped ? 'bg-zinc-700 text-zinc-300 hover:bg-zinc-600' : 'bg-blue-500/20 text-blue-300 hover:bg-blue-500/30'}"
									disabled={busy}
									onclick={() => togglePriority(i, f.priority ?? 0)}
									title={skipped ? "Currently skipped — click to include in downloads" : "Currently normal priority — click to skip"}
								>
									{skipped ? "Skip" : "Normal"}
								</button>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</div>

	{#if ctx}
		<FileContextMenu
			torrentId={torrentId}
			fileIndex={ctx.index}
			currentPriority={ctx.priority}
			path={ctx.path}
			position={ctx.position}
			onClose={() => (ctx = null)}
			onApplied={() => refresh()}
		/>
	{/if}
</div>
