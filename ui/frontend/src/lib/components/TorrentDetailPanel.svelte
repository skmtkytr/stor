<script lang="ts">
	import type { TorrentInfo } from "$lib/types";
	import { torrents } from "$lib/stores/torrents.svelte";
	import { formatBytes, formatSpeed, formatETA, formatRatio, formatUnixDate } from "$lib/format";
	import PeerPanel from "./PeerPanel.svelte";
	import FileList from "./FileList.svelte";

	let {
		torrentId,
	}: {
		torrentId: string | null;
	} = $props();

	type Tab = "status" | "details" | "files" | "peers";
	let active = $state<Tab>("status");

	const torrent = $derived<TorrentInfo | null>(
		torrentId ? (torrents.list.find((t) => t.id === torrentId) ?? null) : null,
	);

	const tabs: { id: Tab; label: string }[] = [
		{ id: "status", label: "Status" },
		{ id: "details", label: "Details" },
		{ id: "files", label: "Files" },
		{ id: "peers", label: "Peers" },
	];
</script>

<div class="flex h-full flex-col border-t border-zinc-800 bg-zinc-950">
	<!-- Tab header -->
	<div class="flex shrink-0 items-center border-b border-zinc-800 bg-zinc-900">
		{#each tabs as tab}
			<button
				class="border-r border-zinc-800 px-4 py-1.5 text-xs uppercase tracking-wider transition-colors
					{active === tab.id
					? 'bg-zinc-950 text-zinc-100'
					: 'text-zinc-500 hover:bg-zinc-800/50 hover:text-zinc-300'}"
				onclick={() => (active = tab.id)}
			>
				{tab.label}
			</button>
		{/each}
		<div class="flex-1"></div>
		{#if torrent}
			<div class="truncate px-3 text-xs text-zinc-500" title={torrent.name || torrent.id}>
				{torrent.name || torrent.id.slice(0, 20) + "..."}
			</div>
		{/if}
	</div>

	<!-- Content -->
	<div class="min-h-0 flex-1 overflow-auto">
		{#if !torrent}
			<div class="flex h-full items-center justify-center text-xs text-zinc-600">
				Select a torrent to see details.
			</div>
		{:else if active === "status"}
			{@const p = torrent.progress}
			<div class="grid grid-cols-2 gap-x-8 gap-y-1.5 px-4 py-3 text-xs">
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">State</span>
					<span class="text-zinc-200">{torrent.state}</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Queue</span>
					<span class="tabular-nums text-zinc-200">{torrent.queue_position ?? "-"}</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Progress</span>
					<span class="tabular-nums text-zinc-200">{(p.percent ?? 0).toFixed(2)}%</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Pieces</span>
					<span class="tabular-nums text-zinc-200">{p.done_pieces} / {p.total_pieces}</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Downloaded</span>
					<span class="tabular-nums text-zinc-200">
						{formatBytes(p.downloaded)} / {formatBytes(p.total)}
					</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Uploaded</span>
					<span class="tabular-nums text-zinc-200">{formatBytes(p.uploaded)}</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Down Speed</span>
					<span class="tabular-nums text-blue-400">{formatSpeed(p.down_speed)}</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Up Speed</span>
					<span class="tabular-nums text-emerald-400">{formatSpeed(p.up_speed)}</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">ETA</span>
					<span class="tabular-nums text-zinc-200">
						{torrent.state === "downloading"
							? formatETA(p.downloaded, p.total, p.down_speed)
							: "-"}
					</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Ratio</span>
					<span class="tabular-nums text-zinc-200">{formatRatio(p.downloaded, p.uploaded)}</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Peers</span>
					<span class="tabular-nums text-zinc-200">{p.active_peers}</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Error</span>
					<span class="truncate text-red-400" title={torrent.error ?? ""}>
						{torrent.error || "-"}
					</span>
				</div>
				<div class="flex justify-between gap-4">
					<span class="text-zinc-500">Tracker</span>
					<span
						class="truncate {p.last_tracker_error ? 'text-yellow-400' : 'text-emerald-400'}"
						title={p.last_tracker_error ?? "all trackers OK"}
					>
						{p.last_tracker_error || "OK"}
					</span>
				</div>
			</div>
		{:else if active === "details"}
			<div class="space-y-1.5 px-4 py-3 text-xs">
				<div class="flex gap-4">
					<span class="w-28 shrink-0 text-zinc-500">Name</span>
					<span class="break-all text-zinc-200">{torrent.name || "-"}</span>
				</div>
				<div class="flex gap-4">
					<span class="w-28 shrink-0 text-zinc-500">Info Hash</span>
					<span class="break-all font-mono text-zinc-300">{torrent.id}</span>
				</div>
				<div class="flex gap-4">
					<span class="w-28 shrink-0 text-zinc-500">Total Size</span>
					<span class="tabular-nums text-zinc-200">
						{torrent.total_bytes ? formatBytes(torrent.total_bytes) : "-"}
					</span>
				</div>
				<div class="flex gap-4">
					<span class="w-28 shrink-0 text-zinc-500">Save Path</span>
					<span class="break-all text-zinc-300">{torrent.save_path || "-"}</span>
				</div>
				<div class="flex gap-4">
					<span class="w-28 shrink-0 text-zinc-500">Source</span>
					<span class="break-all font-mono text-zinc-400">{torrent.source || "-"}</span>
				</div>
				<div class="flex gap-4">
					<span class="w-28 shrink-0 text-zinc-500">Added</span>
					<span class="text-zinc-300">{formatUnixDate(torrent.added_at)}</span>
				</div>
				<div class="flex gap-4">
					<span class="w-28 shrink-0 text-zinc-500">Completed</span>
					<span class="text-zinc-300">{formatUnixDate(torrent.completed_at)}</span>
				</div>
			</div>
		{:else if active === "files"}
			<FileList torrentId={torrent.id} />
		{:else if active === "peers"}
			<PeerPanel torrentId={torrent.id} />
		{/if}
	</div>
</div>
