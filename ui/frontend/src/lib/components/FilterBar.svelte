<script lang="ts">
	import type { TorrentInfo, TorrentState } from "$lib/types";

	let {
		torrents,
		activeFilter = $bindable("all"),
	}: {
		torrents: TorrentInfo[];
		activeFilter: string;
	} = $props();

	type Filter = { id: string; label: string; match: (t: TorrentInfo) => boolean };

	const filters: Filter[] = [
		{ id: "all", label: "All", match: () => true },
		{ id: "downloading", label: "Downloading", match: (t) => t.state === "downloading" || t.state === "metadata" || t.state === "adding" },
		{ id: "complete", label: "Complete", match: (t) => t.state === "complete" },
		{ id: "paused", label: "Paused", match: (t) => t.state === "paused" },
		{ id: "error", label: "Error", match: (t) => t.state === "error" },
	];

	function count(f: Filter): number {
		return torrents.filter(f.match).length;
	}
</script>

<div class="flex items-center gap-1 border-b border-zinc-800 px-3 py-1">
	{#each filters as f}
		{@const n = count(f)}
		<button
			class="rounded px-2.5 py-1 text-xs font-medium transition-colors
				{activeFilter === f.id
				? 'bg-zinc-700 text-zinc-100'
				: 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/50'}"
			onclick={() => (activeFilter = f.id)}
		>
			{f.label}
			<span class="ml-1 tabular-nums {activeFilter === f.id ? 'text-zinc-300' : 'text-zinc-600'}">{n}</span>
		</button>
	{/each}
</div>
