<script lang="ts">
	import * as Dialog from "$lib/components/ui/dialog";
	import { Button } from "$lib/components/ui/button";
	import { Input } from "$lib/components/ui/input";
	import { Separator } from "$lib/components/ui/separator";
	import { api, setApiKey } from "$lib/rpc";
	import { toast } from "svelte-sonner";
	import type { EngineStats } from "$lib/types";
	import type { ColDef } from "$lib/columns";

	let {
		stats,
		open = $bindable(false),
		columns = $bindable<ColDef[]>([]),
		onColumnsChange,
	}: {
		stats: EngineStats | null;
		open: boolean;
		columns: ColDef[];
		onColumnsChange: () => void;
	} = $props();

	let maxActive = $state(5);
	let dragIdx = $state<number | null>(null);

	$effect(() => {
		if (stats) maxActive = stats.max_active;
	});

	async function saveMaxActive() {
		try {
			await api.setMaxActive(maxActive);
			toast.success(`Max active set to ${maxActive}`);
		} catch (e: unknown) {
			toast.error(e instanceof Error ? e.message : "Failed");
		}
	}

	function toggleCol(idx: number) {
		// Don't allow hiding Name
		if (columns[idx].id === "name") return;
		columns[idx] = { ...columns[idx], visible: !columns[idx].visible };
		onColumnsChange();
	}

	function moveCol(from: number, to: number) {
		if (from === to) return;
		const item = columns[from];
		const next = [...columns];
		next.splice(from, 1);
		next.splice(to, 0, item);
		columns = next;
		onColumnsChange();
	}

	function handleDragStart(idx: number) {
		dragIdx = idx;
	}

	function handleDragOver(e: DragEvent, idx: number) {
		e.preventDefault();
		if (dragIdx !== null && dragIdx !== idx) {
			moveCol(dragIdx, idx);
			dragIdx = idx;
		}
	}

	function handleDragEnd() {
		dragIdx = null;
	}

	function resetColumns() {
		localStorage.removeItem("stor_col_config");
		window.location.reload();
	}

	function logout() {
		setApiKey("");
		window.location.reload();
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content class="sm:max-w-md">
		<Dialog.Header>
			<Dialog.Title>Settings</Dialog.Title>
			<Dialog.Description>Configure daemon and table columns.</Dialog.Description>
		</Dialog.Header>

		<div class="max-h-[70vh] space-y-5 overflow-y-auto py-4">
			<!-- Max active downloads -->
			<div class="space-y-2">
				<label class="text-sm font-medium" for="max-active">Max concurrent downloads</label>
				<div class="flex items-center gap-2">
					<Input id="max-active" type="number" min={1} max={50} bind:value={maxActive} class="w-20" />
					<Button size="sm" onclick={saveMaxActive}>Save</Button>
				</div>
			</div>

			<Separator />

			<!-- Column config -->
			<div class="space-y-2">
				<p class="text-sm font-medium">Table columns</p>
				<p class="text-xs text-muted-foreground">Drag to reorder. Toggle visibility.</p>
				<div class="space-y-1">
					{#each columns as col, idx}
						<!-- svelte-ignore a11y_no_static_element_interactions -->
						<div
							class="flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm {dragIdx === idx ? 'opacity-50' : ''} {col.visible ? '' : 'opacity-40'}"
							draggable="true"
							ondragstart={() => handleDragStart(idx)}
							ondragover={(e) => handleDragOver(e, idx)}
							ondragend={handleDragEnd}
						>
							<span class="cursor-grab text-muted-foreground">⠿</span>
							<span class="flex-1">{col.label}</span>
							<button
								class="text-xs {col.visible ? 'text-foreground' : 'text-muted-foreground'}"
								onclick={() => toggleCol(idx)}
								disabled={col.id === "name"}
							>
								{col.visible ? "✓" : "○"}
							</button>
						</div>
					{/each}
				</div>
				<Button variant="outline" size="sm" onclick={resetColumns}>Reset to default</Button>
			</div>

			<Separator />

			<!-- Stats -->
			{#if stats}
				<div class="space-y-1 text-sm">
					<p><span class="text-muted-foreground">Active:</span> {stats.active_torrents}</p>
					<p><span class="text-muted-foreground">Total:</span> {stats.total_torrents}</p>
				</div>
			{/if}

			<Separator />

			<div>
				<Button variant="destructive" size="sm" onclick={logout}>Disconnect</Button>
				<p class="mt-1 text-xs text-muted-foreground">Clear API key and return to login.</p>
			</div>
		</div>
	</Dialog.Content>
</Dialog.Root>
