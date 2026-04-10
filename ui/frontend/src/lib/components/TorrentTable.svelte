<script lang="ts">
	import type { TorrentInfo } from "$lib/types";
	import { formatBytes, formatSpeed, formatETA } from "$lib/format";
	import { api } from "$lib/rpc";
	import { type ColDef, ALL_COLUMNS, loadColConfig, saveColConfig } from "$lib/columns";
	import { Badge } from "$lib/components/ui/badge";
	import { Progress } from "$lib/components/ui/progress";
	import * as ContextMenu from "$lib/components/ui/context-menu";
	import { toast } from "svelte-sonner";

	let {
		torrents,
		columns = $bindable<ColDef[]>([]),
	}: {
		torrents: TorrentInfo[];
		columns: ColDef[];
	} = $props();

	let selected = $state(new Set<string>());
	let lastIdx = $state<number | null>(null);
	let sortKey = $state<string>("queue_position");
	let sortDesc = $state(false);
	let container: HTMLDivElement;
	let initialized = $state(false);

	const visibleCols = $derived(columns.filter((c) => c.visible));

	// --- Init: fit Name to container ---
	$effect(() => {
		if (container && !initialized && columns.length > 0) {
			const saved = loadColConfig();
			if (!saved) {
				// First load: stretch Name to fill
				const containerW = container.clientWidth;
				const others = columns.filter((c) => c.id !== "name" && c.visible);
				const fixedSum = others.reduce((s, c) => s + c.width, 0);
				const nameIdx = columns.findIndex((c) => c.id === "name");
				if (nameIdx >= 0) {
					const nameW = Math.max(columns[nameIdx].minWidth, containerW - fixedSum);
					columns[nameIdx] = { ...columns[nameIdx], width: nameW };
				}
			}
			initialized = true;
		}
	});

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
			case "queue": return t.queue_position ?? 9999;
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

	function startResize(e: MouseEvent, colIdx: number) {
		e.preventDefault();
		e.stopPropagation();
		// Find the actual column in the full array
		const col = visibleCols[colIdx];
		const realIdx = columns.findIndex((c) => c.id === col.id);
		resizing = colIdx;
		const startX = e.pageX;
		const startW = col.width;

		const onMove = (ev: MouseEvent) => {
			columns[realIdx] = { ...columns[realIdx], width: Math.max(col.minWidth, startW + ev.pageX - startX) };
		};
		const onUp = () => {
			resizing = null;
			document.removeEventListener("mousemove", onMove);
			document.removeEventListener("mouseup", onUp);
			saveColConfig(columns);
		};
		document.addEventListener("mousemove", onMove);
		document.addEventListener("mouseup", onUp);
	}

	const tableWidth = $derived(visibleCols.reduce((s, c) => s + c.width, 0));

	// --- Cell renderer ---
	function cellValue(col: ColDef, t: TorrentInfo): string {
		switch (col.id) {
			case "name": return t.name || t.id.slice(0, 16) + "...";
			case "size": return t.total_bytes ? formatBytes(t.total_bytes) : "-";
			case "speed": return t.state === "downloading" && t.progress.down_speed ? formatSpeed(t.progress.down_speed) : "-";
			case "eta": return t.state === "downloading" ? formatETA(t.progress.downloaded, t.progress.total, t.progress.down_speed) : "-";
			case "peers": return t.state === "downloading" && t.progress.active_peers ? String(t.progress.active_peers) : "-";
			case "queue": return String(t.queue_position ?? "-");
			default: return "";
		}
	}

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
			<div class="overflow-x-auto" bind:this={container}>
				<table style="table-layout: fixed; width: max({tableWidth}px, 100%);">
					{#each visibleCols as col}
						<col style="width: {col.width}px;" />
					{/each}

					<thead>
						<tr class="border-b bg-muted/40">
							{#each visibleCols as col, ci}
								<th
									class="relative h-9 select-none px-3 text-xs font-medium uppercase tracking-wider text-muted-foreground"
									class:text-right={col.align === "right"}
									class:text-center={col.align === "center"}
								>
									<button
										class="inline-flex items-center gap-1 hover:text-foreground transition-colors"
										onclick={() => toggleSort(col.id)}
									>
										{col.label}
										{#if sortKey === col.id}
											<span class="text-foreground">{sortDesc ? "↓" : "↑"}</span>
										{/if}
									</button>
									{#if ci < visibleCols.length - 1}
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
								{#each visibleCols as col}
									<td
										class="truncate px-3 text-sm"
										class:text-right={col.align === "right"}
										class:text-center={col.align === "center"}
										class:tabular-nums={col.align === "right" || col.id === "queue"}
										class:text-muted-foreground={col.id !== "name"}
									>
										{#if col.id === "name"}
											<span class="font-medium" title={t.name || t.id}>{cellValue(col, t)}</span>
											{#if t.error}
												<span class="block truncate text-xs text-destructive">{t.error}</span>
											{/if}
										{:else if col.id === "progress"}
											<div class="flex items-center gap-2">
												<Progress value={p.percent} class="h-1.5 flex-1" />
												<span class="w-10 text-right text-xs tabular-nums">{(p.percent ?? 0).toFixed(0)}%</span>
											</div>
										{:else if col.id === "state"}
											<Badge variant={stateVariant[t.state] ?? "outline"} class="text-[10px]">{t.state}</Badge>
										{:else}
											{cellValue(col, t)}
										{/if}
									</td>
								{/each}
							</tr>
						{:else}
							<tr>
								<td colspan={visibleCols.length} class="h-32 text-center text-muted-foreground">
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
