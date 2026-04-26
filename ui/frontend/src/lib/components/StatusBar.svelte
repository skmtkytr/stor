<script lang="ts">
	import { torrents } from "$lib/stores/torrents.svelte";
	import { notifications } from "$lib/stores/notifications.svelte";
	import { formatBytes, formatSpeed } from "$lib/format";

	const seedingCount = $derived(torrents.list.filter((t) => t.state === "seeding").length);

	const liveLabel: Record<string, string> = {
		idle: "idle",
		connecting: "connecting",
		open: "live",
		closed: "offline",
	};
	const liveColor: Record<string, string> = {
		idle: "text-zinc-500",
		connecting: "text-yellow-400",
		open: "text-emerald-400",
		closed: "text-red-400",
	};
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
				Peers: <span class="text-zinc-400">{s.total_peers}</span>{#if s.total_known_peers > 0}<span
						class="text-zinc-600">/{s.total_known_peers}</span
					>{/if}
			</span>
			<span>
				DHT: <span class="text-zinc-400">{s.dht_nodes}</span>
			</span>
			<span title="Live event stream connection state">
				<span class="text-zinc-600">·</span>
				<span class={liveColor[torrents.liveState] ?? "text-zinc-500"}>
					&bull; {liveLabel[torrents.liveState] ?? torrents.liveState}
				</span>
			</span>
		</div>
		<div class="flex items-center gap-4 tabular-nums">
			{#if s.free_space >= 0}
				<span>
					Free: <span class="text-zinc-300">{formatBytes(s.free_space)}</span>
				</span>
			{/if}
			<span title="Download speed / Total downloaded">
				DL: <span class="text-zinc-300">{formatSpeed(s.total_down_speed)}</span>
				<span class="text-zinc-500">({formatBytes(s.total_downloaded)})</span>
			</span>
			<span title="Upload speed / Total uploaded">
				UL: <span class="text-emerald-300">{formatSpeed(s.total_up_speed)}</span>
				<span class="text-zinc-500">({formatBytes(s.total_uploaded)})</span>
			</span>
			<button
				class="relative ml-1 flex items-center text-zinc-400 hover:text-zinc-100"
				onclick={() => notifications.togglePanel()}
				title="Activity log"
				aria-label="Open activity log ({notifications.unreadCount} unread)"
			>
				<svg
					xmlns="http://www.w3.org/2000/svg"
					viewBox="0 0 16 16"
					fill="currentColor"
					class="h-3.5 w-3.5"
					aria-hidden="true"
				>
					<path d="M2.5 2.75A.75.75 0 0 1 3.25 2h9.5a.75.75 0 0 1 0 1.5h-9.5a.75.75 0 0 1-.75-.75zm0 5A.75.75 0 0 1 3.25 7h9.5a.75.75 0 0 1 0 1.5h-9.5a.75.75 0 0 1-.75-.75zm0 5a.75.75 0 0 1 .75-.75h9.5a.75.75 0 0 1 0 1.5h-9.5a.75.75 0 0 1-.75-.75z" />
				</svg>
				{#if notifications.unreadCount > 0}
					<span
						class="absolute -right-1 -top-1 flex h-3.5 min-w-3.5 items-center justify-center rounded-full bg-red-500 px-1 text-[9px] font-semibold leading-none text-white"
					>
						{notifications.unreadCount > 99 ? "99+" : notifications.unreadCount}
					</span>
				{/if}
			</button>
		</div>
	{:else}
		<span>Connecting...</span>
		<span></span>
	{/if}
</footer>
