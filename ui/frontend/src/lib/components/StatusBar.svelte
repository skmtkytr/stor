<script lang="ts">
	import { torrents } from "$lib/stores/torrents.svelte";
	import { formatBytes, formatSpeed } from "$lib/format";

	const seedingCount = $derived(torrents.list.filter((t) => t.state === "seeding").length);
</script>

<footer class="flex items-center justify-between border-t border-zinc-800 bg-zinc-900 px-3 py-1 text-xs text-zinc-500">
	{#if torrents.stats}
		{@const s = torrents.stats}
		<div class="flex items-center gap-4">
			<span>
				<span class="text-zinc-400">{s.active_torrents}</span>/{s.max_active} active
			</span>
			{#if seedingCount > 0}
				<span>
					<span class="text-emerald-400">{seedingCount}</span> seeding
				</span>
			{/if}
			<span>
				<span class="text-zinc-400">{s.total_torrents}</span> total
			</span>
			<span>
				Peers: <span class="text-zinc-400">{s.total_peers}</span>
			</span>
			<span>
				DHT: <span class="text-zinc-400">{s.dht_nodes}</span>
			</span>
		</div>
		<div class="flex items-center gap-4 tabular-nums">
			{#if s.free_space >= 0}
				<span>
					Free: <span class="text-zinc-300">{formatBytes(s.free_space)}</span>
				</span>
			{/if}
			<span>
				DL: <span class="text-zinc-300">{formatSpeed(s.total_down_speed)}</span>
			</span>
			<span>
				UL: <span class="text-emerald-300">{formatSpeed(s.total_up_speed)}</span>
			</span>
		</div>
	{:else}
		<span>Connecting...</span>
		<span></span>
	{/if}
</footer>
