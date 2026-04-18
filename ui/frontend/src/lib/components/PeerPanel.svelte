<script lang="ts">
	import { api } from "$lib/rpc";
	import { formatSpeed } from "$lib/format";
	import type { PeerSnap } from "$lib/types";

	let {
		torrentId,
	}: {
		torrentId: string;
	} = $props();

	let peers = $state<PeerSnap[]>([]);
	let error = $state<string | null>(null);
	let initialized = $state(false);

	async function refresh() {
		try {
			peers = await api.peers(torrentId);
			error = null;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			initialized = true;
		}
	}

	$effect(() => {
		// Re-poll when torrentId changes. Do NOT clear peers here — let the
		// previous data stay visible until the first refresh completes so the
		// panel doesn't flash "Loading…" on every open.
		void torrentId;
		initialized = false;
		refresh();
		const iv = setInterval(refresh, 2000);
		return () => clearInterval(iv);
	});

	function directionLabel(p: PeerSnap): string {
		return p.incoming ? "\u2193 in" : "\u2191 out";
	}

	const v4Count = $derived(peers.filter((p) => p.ip_version === 4).length);
	const v6Count = $derived(peers.filter((p) => p.ip_version === 6).length);
	const inCount = $derived(peers.filter((p) => p.incoming).length);
	const outCount = $derived(peers.length - inCount);
	const encCount = $derived(peers.filter((p) => p.encrypted).length);
	const utpCount = $derived(peers.filter((p) => p.using_utp).length);
</script>

<div class="border-b border-zinc-800 bg-zinc-950/60">
	<div class="flex items-center gap-4 px-4 py-2 text-xs text-zinc-400">
		<span class="uppercase tracking-wider text-zinc-500">
			Peers ({peers.length})
		</span>
		{#if peers.length > 0}
			<span class="text-zinc-600">·</span>
			<span>v4 <span class="text-zinc-200">{v4Count}</span></span>
			<span>v6 <span class="text-zinc-200">{v6Count}</span></span>
			<span class="text-zinc-600">·</span>
			<span class="text-blue-400">↑ out {outCount}</span>
			<span class="text-emerald-400">↓ in {inCount}</span>
			<span class="text-zinc-600">·</span>
			<span>uTP {utpCount}</span>
			<span>🔒 {encCount}</span>
		{/if}
	</div>
	{#if !initialized && peers.length === 0}
		<div class="px-4 py-3 text-xs text-zinc-500">Loading…</div>
	{:else if error && peers.length === 0}
		<div class="px-4 py-3 text-xs text-red-400">Error: {error}</div>
	{:else if peers.length === 0}
		<div class="px-4 py-3 text-xs text-zinc-500">No connected peers.</div>
	{:else}
		<table class="w-full border-collapse text-xs">
			<thead>
				<tr class="border-b border-zinc-800 text-[10px] uppercase tracking-wider text-zinc-500">
					<th class="px-3 py-1 text-left font-medium">Address</th>
					<th class="px-2 py-1 text-center font-medium w-10">IP</th>
					<th class="px-2 py-1 text-center font-medium w-12">Dir</th>
					<th class="px-2 py-1 text-center font-medium w-12">Xport</th>
					<th class="px-2 py-1 text-center font-medium w-10">Enc</th>
					<th class="px-3 py-1 text-left font-medium">Client</th>
					<th class="px-3 py-1 text-right font-medium w-16">Progress</th>
					<th class="px-3 py-1 text-right font-medium w-20">Down</th>
					<th class="px-3 py-1 text-right font-medium w-20">Up</th>
					<th class="px-2 py-1 text-center font-medium w-16">State</th>
				</tr>
			</thead>
			<tbody>
				{#each peers as p (p.addr)}
					<tr class="border-b border-zinc-900/80 hover:bg-zinc-900/40">
						<td class="px-3 py-1 font-mono text-zinc-300">{p.addr}</td>
						<td class="px-2 py-1 text-center text-zinc-500">v{p.ip_version}</td>
						<td
							class="px-2 py-1 text-center font-medium tabular-nums
								{p.incoming ? 'text-emerald-400' : 'text-blue-400'}"
						>
							{directionLabel(p)}
						</td>
						<td class="px-2 py-1 text-center">
							<span
								class="rounded px-1 py-0.5 text-[10px] font-mono
									{p.using_utp ? 'bg-purple-500/20 text-purple-300' : 'bg-zinc-700/40 text-zinc-400'}"
							>
								{p.using_utp ? "uTP" : "TCP"}
							</span>
						</td>
						<td class="px-2 py-1 text-center">
							{#if p.encrypted}
								<span title="MSE/PE RC4">🔒</span>
							{:else}
								<span class="text-zinc-700" title="plaintext">—</span>
							{/if}
						</td>
						<td class="px-3 py-1 text-zinc-300">{p.client}</td>
						<td class="px-3 py-1 text-right tabular-nums text-zinc-400">
							{(p.progress * 100).toFixed(1)}%
						</td>
						<td class="px-3 py-1 text-right tabular-nums text-blue-400">
							{p.down_rate > 0 ? formatSpeed(p.down_rate) : ""}
						</td>
						<td class="px-3 py-1 text-right tabular-nums text-emerald-400">
							{p.up_rate > 0 ? formatSpeed(p.up_rate) : ""}
						</td>
						<td class="px-2 py-1 text-center text-[10px] text-zinc-500">
							{#if p.choked}<span title="remote choked us">⊘↓</span>{/if}
							{#if p.choking}<span title="we choked remote">⊘↑</span>{/if}
							{#if !p.choked && !p.choking}<span class="text-emerald-500">●</span>{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</div>
