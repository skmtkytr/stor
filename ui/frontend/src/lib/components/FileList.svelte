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
	});

	const totalSize = $derived(files.reduce((s, f) => s + f.length, 0));
</script>

<div class="flex h-full flex-col">
	<div class="flex shrink-0 items-center gap-4 border-b border-zinc-800 px-4 py-2 text-xs text-zinc-400">
		<span class="uppercase tracking-wider text-zinc-500">
			Files ({files.length})
		</span>
		{#if files.length > 0}
			<span class="text-zinc-600">·</span>
			<span class="tabular-nums">{formatBytes(totalSize)}</span>
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
						<th class="px-3 py-1 text-right font-medium w-24">Size</th>
					</tr>
				</thead>
				<tbody>
					{#each files as f (f.path)}
						<tr class="border-b border-zinc-900/80 hover:bg-zinc-900/40">
							<td class="break-all px-3 py-1 font-mono text-zinc-300">{f.path}</td>
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
