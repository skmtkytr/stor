<script lang="ts">
	import type { TorrentInfo } from "$lib/types";
	import { formatBytes, formatSpeed, formatETA } from "$lib/format";

	let {
		torrent,
		selected = false,
		onclick,
		oncontextmenu,
	}: {
		torrent: TorrentInfo;
		selected: boolean;
		onclick: (e: MouseEvent) => void;
		oncontextmenu: (e: MouseEvent) => void;
	} = $props();

	const barColors: Record<string, string> = {
		downloading: "bg-blue-500",
		seeding: "bg-emerald-500",
		complete: "bg-green-500",
		paused: "bg-zinc-500",
		error: "bg-red-500",
		metadata: "bg-yellow-500",
		adding: "bg-yellow-500",
	};

	const barBgs: Record<string, string> = {
		downloading: "bg-blue-500/15",
		seeding: "bg-emerald-500/15",
		complete: "bg-green-500/15",
		paused: "bg-zinc-500/15",
		error: "bg-red-500/15",
		metadata: "bg-yellow-500/15",
		adding: "bg-yellow-500/15",
	};

	const stateLabels: Record<string, string> = {
		downloading: "Downloading",
		seeding: "Seeding",
		complete: "Complete",
		paused: "Paused",
		error: "Error",
		metadata: "Metadata",
		adding: "Adding",
	};

	const stateTextColors: Record<string, string> = {
		downloading: "text-blue-300",
		seeding: "text-emerald-300",
		complete: "text-green-300",
		paused: "text-zinc-400",
		error: "text-red-300",
		metadata: "text-yellow-300",
		adding: "text-yellow-300",
	};
</script>

<tr
	class="h-10 border-b border-zinc-800/50 transition-colors hover:bg-zinc-800/30 cursor-default select-none
		{selected ? 'bg-blue-500/10' : ''}"
	{onclick}
	{oncontextmenu}
>
	<!-- # Queue -->
	<td class="w-10 px-2 text-center text-xs tabular-nums text-zinc-500">
		{torrent.queue_position ?? "-"}
	</td>

	<!-- Name -->
	<td class="truncate px-3 text-sm" title={torrent.name || torrent.id}>
		<span class="font-medium">{torrent.name || torrent.id.slice(0, 16) + "..."}</span>
		{#if torrent.error}
			<span class="ml-2 text-xs text-red-400">{torrent.error}</span>
		{/if}
	</td>

	<!-- Size -->
	<td class="px-3 text-right text-xs tabular-nums text-zinc-400 whitespace-nowrap">
		{torrent.total_bytes ? formatBytes(torrent.total_bytes) : "-"}
	</td>

	<!-- Progress — Deluge style: state + percent inside bar -->
	<td class="px-3">
		<div class="relative h-5 w-full overflow-hidden rounded {barBgs[torrent.state] ?? 'bg-zinc-800'}">
			<div
				class="absolute inset-y-0 left-0 rounded transition-[width] duration-300 {barColors[torrent.state] ?? 'bg-zinc-600'}"
				style="width: {torrent.progress.percent ?? 0}%; opacity: 0.35;"
			></div>
			<div class="relative flex h-full items-center justify-center text-[11px] font-medium tabular-nums {stateTextColors[torrent.state] ?? 'text-zinc-400'}">
				{stateLabels[torrent.state] ?? torrent.state}
				{(torrent.progress.percent ?? 0).toFixed(1)}%
			</div>
		</div>
	</td>

	<!-- Down -->
	<td class="px-3 text-right text-xs tabular-nums whitespace-nowrap">
		{#if torrent.progress.down_speed}
			<span class="text-blue-400">{formatSpeed(torrent.progress.down_speed)}</span>
		{/if}
	</td>

	<!-- Up -->
	<td class="px-3 text-right text-xs tabular-nums whitespace-nowrap">
		{#if torrent.progress.up_speed}
			<span class="text-emerald-400">{formatSpeed(torrent.progress.up_speed)}</span>
		{/if}
	</td>

	<!-- ETA -->
	<td class="px-3 text-right text-xs tabular-nums text-zinc-400 whitespace-nowrap">
		{torrent.state === "downloading"
			? formatETA(torrent.progress.downloaded, torrent.progress.total, torrent.progress.down_speed)
			: ""}
	</td>

	<!-- Peers -->
	<td class="px-3 text-right text-xs tabular-nums text-zinc-400 whitespace-nowrap">
		{#if torrent.state === "downloading" || torrent.state === "seeding"}
			<span>{torrent.progress.active_peers ?? 0}</span>{#if torrent.progress.known_peers > 0}<span class="text-zinc-600">/{torrent.progress.known_peers}</span>{/if}
		{/if}
	</td>
</tr>
