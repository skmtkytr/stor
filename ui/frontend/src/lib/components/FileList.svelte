<script lang="ts">
	import { api } from "$lib/rpc";
	import { formatBytes } from "$lib/format";
	import type { FileEntry } from "$lib/types";

	let {
		torrentId,
	}: {
		torrentId: string;
	} = $props();

	let files = $state<FileEntry[]>([]);
	let error = $state<string | null>(null);
	let initialized = $state(false);

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
					</tr>
				</thead>
				<tbody>
					{#each files as f (f.path)}
						{@const percent = pct(f)}
						{@const done = percent >= 99.99}
						<tr class="border-b border-zinc-900/80 hover:bg-zinc-900/40">
							<td class="break-all px-3 py-1 font-mono text-zinc-300">{f.path}</td>
							<td class="px-3 py-1">
								<div class="relative h-4 w-full overflow-hidden rounded {done ? 'bg-green-500/15' : 'bg-blue-500/15'}">
									<div
										class="absolute inset-y-0 left-0 rounded {done ? 'bg-green-500' : 'bg-blue-500'}"
										style="width: {percent}%; opacity: 0.35;"
									></div>
									<div class="relative flex h-full items-center justify-center text-[10px] tabular-nums {done ? 'text-green-300' : 'text-blue-300'}">
										{percent.toFixed(1)}%
									</div>
								</div>
							</td>
							<td class="px-3 py-1 text-right tabular-nums text-zinc-400">
								{formatBytes(f.length)}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</div>
</div>
