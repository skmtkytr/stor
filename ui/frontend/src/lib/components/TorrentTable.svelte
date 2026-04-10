<script lang="ts">
	import type { TorrentInfo } from "$lib/types";
	import { formatBytes, formatSpeed, formatETA } from "$lib/format";
	import { api } from "$lib/rpc";
	import { Badge } from "$lib/components/ui/badge";
	import { Progress } from "$lib/components/ui/progress";
	import * as ContextMenu from "$lib/components/ui/context-menu";
	import * as Tooltip from "$lib/components/ui/tooltip";
	import { toast } from "svelte-sonner";

	let { torrents }: { torrents: TorrentInfo[] } = $props();

	let selected = $state(new Set<string>());
	let lastClicked = $state<number | null>(null);

	const sorted = $derived(
		[...torrents].sort((a, b) => {
			const so: Record<string, number> = { downloading: 0, metadata: 0, adding: 1, paused: 2, error: 3, complete: 4 };
			const sa = so[a.state] ?? 5, sb = so[b.state] ?? 5;
			if (sa !== sb) return sa - sb;
			return (a.queue_position ?? 999) - (b.queue_position ?? 999);
		})
	);

	function handleRowClick(e: MouseEvent, id: string, idx: number) {
		if ((e.target as HTMLElement).closest("button")) return;
		if (e.shiftKey && lastClicked !== null) {
			const from = Math.min(lastClicked, idx);
			const to = Math.max(lastClicked, idx);
			if (!e.ctrlKey && !e.metaKey) selected = new Set();
			const next = new Set(selected);
			for (let i = from; i <= to; i++) next.add(sorted[i].id);
			selected = next;
		} else if (e.ctrlKey || e.metaKey) {
			const next = new Set(selected);
			if (next.has(id)) next.delete(id); else next.add(id);
			selected = next;
		} else {
			selected = new Set([id]);
		}
		lastClicked = idx;
	}

	function handleContextTarget(id: string) {
		if (!selected.has(id)) {
			selected = new Set([id]);
		}
	}

	const stateVariant: Record<string, "default" | "secondary" | "destructive" | "outline"> = {
		downloading: "default",
		complete: "secondary",
		paused: "outline",
		error: "destructive",
		metadata: "outline",
		adding: "outline",
	};

	async function batchAction(action: "pause" | "resume" | "remove" | "queueTop" | "queueBottom") {
		const ids = [...selected];
		if (!ids.length) return;

		if (action === "remove") {
			const del = confirm("Also delete downloaded files?");
			await Promise.all(ids.map((id) => api.remove(id, del).catch(() => {})));
			selected = new Set();
			toast.success(`${ids.length} removed`);
		} else if (action === "pause") {
			await Promise.all(ids.map((id) => api.pause(id).catch(() => {})));
			toast.success(`${ids.length} paused`);
		} else if (action === "resume") {
			await Promise.all(ids.map((id) => api.resume(id).catch(() => {})));
			toast.success(`${ids.length} resumed`);
		} else if (action === "queueTop") {
			for (const id of ids.reverse()) await api.queueTop(id).catch(() => {});
			toast.success(`${ids.length} moved to top`);
		} else if (action === "queueBottom") {
			for (const id of ids) await api.queueBottom(id).catch(() => {});
			toast.success(`${ids.length} moved to bottom`);
		}
	}

	async function queueMove(id: string, dir: "top" | "up" | "down" | "bottom") {
		const fn = { top: api.queueTop, up: api.queueUp, down: api.queueDown, bottom: api.queueBottom };
		await fn[dir](id).catch(() => {});
	}
</script>

<svelte:window
	onkeydown={(e) => {
		if (e.key === "a" && (e.ctrlKey || e.metaKey) && !(e.target instanceof HTMLInputElement)) {
			e.preventDefault();
			selected = new Set(sorted.map((t) => t.id));
		}
		if (e.key === "Escape") {
			selected = new Set();
			lastClicked = null;
		}
		if (e.key === "Delete" && selected.size > 0) {
			batchAction("remove");
		}
	}}
/>

<ContextMenu.Root>
	<ContextMenu.Trigger class="w-full">
		<div class="rounded-md border">
			<table class="w-full text-sm">
				<thead>
					<tr class="border-b bg-muted/50">
						<th class="px-3 py-2 text-left font-medium text-muted-foreground">Name</th>
						<th class="w-[80px] px-3 py-2 text-right font-medium text-muted-foreground">Size</th>
						<th class="w-[160px] px-3 py-2 font-medium text-muted-foreground">Progress</th>
						<th class="w-[80px] px-3 py-2 text-right font-medium text-muted-foreground">Speed</th>
						<th class="w-[70px] px-3 py-2 text-right font-medium text-muted-foreground">ETA</th>
						<th class="w-[50px] px-3 py-2 text-right font-medium text-muted-foreground">Peers</th>
						<th class="w-[90px] px-3 py-2 font-medium text-muted-foreground">State</th>
						<th class="w-[100px] px-3 py-2 text-center font-medium text-muted-foreground">Queue</th>
					</tr>
				</thead>
				<tbody>
					{#each sorted as t, idx (t.id)}
						{@const p = t.progress}
						<tr
							class="border-b transition-colors hover:bg-muted/50 cursor-default select-none {selected.has(t.id) ? 'bg-primary/10' : ''}"
							onclick={(e) => handleRowClick(e, t.id, idx)}
							oncontextmenu={() => handleContextTarget(t.id)}
						>
							<td class="px-3 py-2 max-w-[300px]">
								<Tooltip.Root>
									<Tooltip.Trigger class="block truncate text-left">
										{t.name || t.id.slice(0, 12) + "..."}
									</Tooltip.Trigger>
									<Tooltip.Content>
										<p class="max-w-md break-all">{t.name || t.id}</p>
										{#if t.error}
											<p class="text-destructive mt-1">{t.error}</p>
										{/if}
									</Tooltip.Content>
								</Tooltip.Root>
							</td>
							<td class="px-3 py-2 text-right tabular-nums text-muted-foreground">
								{t.total_bytes ? formatBytes(t.total_bytes) : "-"}
							</td>
							<td class="px-3 py-2">
								<div class="flex items-center gap-2">
									<Progress value={p.percent} class="h-2 flex-1" />
									<span class="w-[40px] text-right tabular-nums text-xs text-muted-foreground">
										{p.percent?.toFixed(0) ?? 0}%
									</span>
								</div>
							</td>
							<td class="px-3 py-2 text-right tabular-nums text-muted-foreground">
								{t.state === "downloading" && p.down_speed ? formatSpeed(p.down_speed) : "-"}
							</td>
							<td class="px-3 py-2 text-right tabular-nums text-muted-foreground">
								{t.state === "downloading" ? formatETA(p.downloaded, p.total, p.down_speed) : "-"}
							</td>
							<td class="px-3 py-2 text-right tabular-nums text-muted-foreground">
								{t.state === "downloading" && p.active_peers ? p.active_peers : "-"}
							</td>
							<td class="px-3 py-2">
								<Badge variant={stateVariant[t.state] ?? "outline"}>{t.state}</Badge>
							</td>
							<td class="px-3 py-2 text-center">
								<span class="text-xs text-muted-foreground mr-1">#{t.queue_position}</span>
								<span class="inline-flex gap-0.5">
									<button class="text-muted-foreground hover:text-foreground text-xs" onclick={() => queueMove(t.id, "top")} title="Top">⏫</button>
									<button class="text-muted-foreground hover:text-foreground text-xs" onclick={() => queueMove(t.id, "up")} title="Up">▲</button>
									<button class="text-muted-foreground hover:text-foreground text-xs" onclick={() => queueMove(t.id, "down")} title="Down">▼</button>
									<button class="text-muted-foreground hover:text-foreground text-xs" onclick={() => queueMove(t.id, "bottom")} title="Bottom">⏬</button>
								</span>
							</td>
						</tr>
					{:else}
						<tr>
							<td colspan="8" class="px-3 py-12 text-center text-muted-foreground">
								No torrents. Add one above.
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</ContextMenu.Trigger>

	<ContextMenu.Content class="w-48">
		<ContextMenu.Item onclick={() => batchAction("resume")}>Resume</ContextMenu.Item>
		<ContextMenu.Item onclick={() => batchAction("pause")}>Pause</ContextMenu.Item>
		<ContextMenu.Separator />
		<ContextMenu.Item onclick={() => batchAction("queueTop")}>Move to Top</ContextMenu.Item>
		<ContextMenu.Item onclick={() => batchAction("queueBottom")}>Move to Bottom</ContextMenu.Item>
		<ContextMenu.Separator />
		<ContextMenu.Item class="text-destructive" onclick={() => batchAction("remove")}>
			Remove ({selected.size})
		</ContextMenu.Item>
	</ContextMenu.Content>
</ContextMenu.Root>

{#if selected.size > 0}
	<p class="text-muted-foreground mt-2 text-xs">
		{selected.size} selected — Right-click for actions, Ctrl+A select all, Esc deselect, Delete remove
	</p>
{/if}
