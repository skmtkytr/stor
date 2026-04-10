<script lang="ts">
	import type { TorrentInfo } from "$lib/types";
	import { formatBytes, formatSpeed, formatETA } from "$lib/format";
	import { api } from "$lib/rpc";
	import { Badge } from "$lib/components/ui/badge";
	import { Progress } from "$lib/components/ui/progress";
	import * as ContextMenu from "$lib/components/ui/context-menu";
	import { toast } from "svelte-sonner";

	let { torrents }: { torrents: TorrentInfo[] } = $props();

	// --- Column definitions ---
	interface ColDef {
		id: string;
		label: string;
		width: number;
		minWidth: number;
		align?: "left" | "right" | "center";
	}

	const defaultCols: ColDef[] = [
		{ id: "name", label: "Name", width: 0, minWidth: 120 },  // 0 = flex
		{ id: "size", label: "Size", width: 85, minWidth: 60, align: "right" },
		{ id: "progress", label: "Progress", width: 170, minWidth: 100 },
		{ id: "speed", label: "Speed", width: 90, minWidth: 60, align: "right" },
		{ id: "eta", label: "ETA", width: 75, minWidth: 50, align: "right" },
		{ id: "peers", label: "Peers", width: 55, minWidth: 40, align: "right" },
		{ id: "state", label: "State", width: 95, minWidth: 65 },
		{ id: "queue", label: "#", width: 45, minWidth: 35, align: "center" },
	];

	let cols = $state<ColDef[]>(loadCols());
	let selected = $state(new Set<string>());
	let lastIdx = $state<number | null>(null);
	let sortKey = $state<string>("queue_position");
	let sortDesc = $state(false);

	// --- Persistence ---
	function loadCols(): ColDef[] {
		try {
			const saved = JSON.parse(localStorage.getItem("stor_cols") ?? "null");
			if (Array.isArray(saved) && saved.length === defaultCols.length) {
				return defaultCols.map((d, i) => ({ ...d, width: saved[i] ?? d.width }));
			}
		} catch { /* ignore */ }
		return defaultCols.map((c) => ({ ...c }));
	}

	function saveCols() {
		localStorage.setItem("stor_cols", JSON.stringify(cols.map((c) => c.width)));
	}

	// --- Sorting ---
	const stateOrder: Record<string, number> = {
		downloading: 0, metadata: 1, adding: 2, paused: 3, error: 4, complete: 5,
	};

	function getSortValue(t: TorrentInfo, key: string): number | string {
		switch (key) {
			case "name": return t.name ?? t.id;
			case "size": return t.total_bytes ?? 0;
			case "progress": return t.progress.percent ?? 0;
			case "speed": return t.state === "downloading" ? (t.progress.down_speed ?? 0) : 0;
			case "eta": {
				const p = t.progress;
				if (t.state !== "downloading" || !p.down_speed) return Infinity;
				return (p.total - p.downloaded) / p.down_speed;
			}
			case "peers": return t.progress.active_peers ?? 0;
			case "state": return stateOrder[t.state] ?? 9;
			case "queue_position": return t.queue_position ?? 9999;
			default: return 0;
		}
	}

	const sorted = $derived(
		[...torrents].sort((a, b) => {
			const va = getSortValue(a, sortKey);
			const vb = getSortValue(b, sortKey);
			const cmp = typeof va === "string" ? va.localeCompare(vb as string) : (va as number) - (vb as number);
			return sortDesc ? -cmp : cmp;
		})
	);

	function toggleSort(key: string) {
		if (sortKey === key) sortDesc = !sortDesc;
		else { sortKey = key; sortDesc = false; }
	}

	// --- Selection ---
	function handleRowClick(e: MouseEvent, id: string, idx: number) {
		if ((e.target as HTMLElement).closest("button")) return;
		if (e.shiftKey && lastIdx !== null) {
			const from = Math.min(lastIdx, idx);
			const to = Math.max(lastIdx, idx);
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
		lastIdx = idx;
	}

	function handleCtxTarget(id: string) {
		if (!selected.has(id)) selected = new Set([id]);
	}

	// --- Resize ---
	let resizing = $state<number | null>(null);

	function startResize(e: MouseEvent, idx: number) {
		e.preventDefault();
		e.stopPropagation();
		resizing = idx;
		const startX = e.pageX;
		const th = (e.target as HTMLElement).parentElement!;
		const startW = th.offsetWidth;

		const onMove = (ev: MouseEvent) => {
			const c = cols[idx];
			cols[idx] = { ...c, width: Math.max(c.minWidth, startW + ev.pageX - startX) };
		};
		const onUp = () => {
			resizing = null;
			document.removeEventListener("mousemove", onMove);
			document.removeEventListener("mouseup", onUp);
			saveCols();
		};
		document.addEventListener("mousemove", onMove);
		document.addEventListener("mouseup", onUp);
	}

	// --- Fixed widths for non-flex columns ---
	const fixedTotal = $derived(cols.reduce((s, c) => s + (c.width > 0 ? c.width : 0), 0));

	// --- State badge variant ---
	const stateVariant: Record<string, "default" | "secondary" | "destructive" | "outline"> = {
		downloading: "default", complete: "secondary", paused: "outline",
		error: "destructive", metadata: "outline", adding: "outline",
	};

	// --- Batch actions ---
	function selIds(): string[] { return [...selected]; }

	async function batchAction(action: string) {
		const ids = selIds();
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
			toast.success(`Moved to top`);
		} else if (action === "queueBottom") {
			for (const id of ids) await api.queueBottom(id).catch(() => {});
			toast.success(`Moved to bottom`);
		}
	}
</script>

<svelte:window
	onkeydown={(e) => {
		if (e.key === "a" && (e.ctrlKey || e.metaKey) && !(e.target instanceof HTMLInputElement)) {
			e.preventDefault();
			selected = new Set(sorted.map((t) => t.id));
		}
		if (e.key === "Escape") { selected = new Set(); lastIdx = null; }
		if (e.key === "Delete" && selected.size > 0) batchAction("remove");
	}}
/>

<ContextMenu.Root>
	<ContextMenu.Trigger class="w-full">
		<div class="rounded-lg border bg-card overflow-hidden">
			<div class="overflow-x-auto">
				<table class="w-full" style="table-layout: fixed; min-width: {fixedTotal + 200}px;">
					<!-- Column widths -->
					{#each cols as col}
						{#if col.width > 0}
							<col style="width: {col.width}px;" />
						{:else}
							<col />
						{/if}
					{/each}

					<thead>
						<tr class="border-b bg-muted/40">
							{#each cols as col, ci}
								<th
									class="relative h-9 select-none px-3 text-xs font-medium uppercase tracking-wider text-muted-foreground"
									class:text-right={col.align === "right"}
									class:text-center={col.align === "center"}
								>
									<button
										class="inline-flex items-center gap-1 hover:text-foreground transition-colors"
										onclick={() => toggleSort(col.id === "queue" ? "queue_position" : col.id)}
									>
										{col.label}
										{#if sortKey === col.id || (col.id === "queue" && sortKey === "queue_position")}
											<span class="text-foreground">{sortDesc ? "↓" : "↑"}</span>
										{/if}
									</button>
									<!-- Resize handle -->
									{#if ci < cols.length - 1}
										<!-- svelte-ignore a11y_no_static_element_interactions -->
										<div
											class="absolute right-0 top-0 h-full w-1.5 cursor-col-resize select-none {resizing === ci ? 'bg-primary' : 'hover:bg-primary/40'}"
											onmousedown={(e) => startResize(e, ci)}
										></div>
									{/if}
								</th>
							{/each}
						</tr>
					</thead>

					<tbody>
						{#each sorted as t, idx (t.id)}
							{@const p = t.progress}
							<tr
								class="h-11 border-b transition-colors hover:bg-muted/40 cursor-default select-none {selected.has(t.id) ? 'bg-accent' : ''}"
								onclick={(e) => handleRowClick(e, t.id, idx)}
								oncontextmenu={() => handleCtxTarget(t.id)}
							>
								<!-- Name -->
								<td class="truncate px-3 text-sm font-medium" title={t.name || t.id}>
									{t.name || t.id.slice(0, 16) + "..."}
									{#if t.error}
										<span class="block truncate text-xs text-destructive">{t.error}</span>
									{/if}
								</td>
								<!-- Size -->
								<td class="px-3 text-right text-sm tabular-nums text-muted-foreground">
									{t.total_bytes ? formatBytes(t.total_bytes) : "-"}
								</td>
								<!-- Progress -->
								<td class="px-3">
									<div class="flex items-center gap-2">
										<Progress value={p.percent} class="h-1.5 flex-1" />
										<span class="w-10 text-right text-xs tabular-nums text-muted-foreground">
											{(p.percent ?? 0).toFixed(0)}%
										</span>
									</div>
								</td>
								<!-- Speed -->
								<td class="px-3 text-right text-sm tabular-nums text-muted-foreground">
									{t.state === "downloading" && p.down_speed ? formatSpeed(p.down_speed) : "-"}
								</td>
								<!-- ETA -->
								<td class="px-3 text-right text-sm tabular-nums text-muted-foreground">
									{t.state === "downloading" ? formatETA(p.downloaded, p.total, p.down_speed) : "-"}
								</td>
								<!-- Peers -->
								<td class="px-3 text-right text-sm tabular-nums text-muted-foreground">
									{t.state === "downloading" && p.active_peers ? p.active_peers : "-"}
								</td>
								<!-- State -->
								<td class="px-3">
									<Badge variant={stateVariant[t.state] ?? "outline"} class="text-[10px]">
										{t.state}
									</Badge>
								</td>
								<!-- Queue -->
								<td class="px-3 text-center text-xs tabular-nums text-muted-foreground">
									{t.queue_position}
								</td>
							</tr>
						{:else}
							<tr>
								<td colspan={cols.length} class="h-32 text-center text-muted-foreground">
									No torrents yet. Add a magnet link or upload a .torrent file.
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	</ContextMenu.Trigger>

	<ContextMenu.Content class="w-52">
		<ContextMenu.Item onclick={() => batchAction("resume")}>Resume</ContextMenu.Item>
		<ContextMenu.Item onclick={() => batchAction("pause")}>Pause</ContextMenu.Item>
		<ContextMenu.Separator />
		<ContextMenu.Item onclick={() => batchAction("queueTop")}>Move to Top</ContextMenu.Item>
		<ContextMenu.Item onclick={() => batchAction("queueBottom")}>Move to Bottom</ContextMenu.Item>
		<ContextMenu.Separator />
		<ContextMenu.Item class="text-destructive focus:text-destructive" onclick={() => batchAction("remove")}>
			Remove ({selected.size})
		</ContextMenu.Item>
	</ContextMenu.Content>
</ContextMenu.Root>

{#if selected.size > 0}
	<p class="mt-2 text-xs text-muted-foreground">
		{selected.size} selected — Right-click for actions · Ctrl+A all · Esc deselect · Delete remove
	</p>
{/if}
