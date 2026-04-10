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
		if (columns[idx].id === "name") return;
		columns[idx] = { ...columns[idx], visible: !columns[idx].visible };
		onColumnsChange();
	}

	function moveUp(idx: number) {
		if (idx <= 0) return;
		const next = [...columns];
		[next[idx - 1], next[idx]] = [next[idx], next[idx - 1]];
		columns = next;
		onColumnsChange();
	}

	function moveDown(idx: number) {
		if (idx >= columns.length - 1) return;
		const next = [...columns];
		[next[idx], next[idx + 1]] = [next[idx + 1], next[idx]];
		columns = next;
		onColumnsChange();
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
						<div
							class="flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm {col.visible ? '' : 'opacity-40'}"
						>
							<div class="flex flex-col gap-0.5">
								<button
									class="text-[10px] leading-none text-muted-foreground hover:text-foreground disabled:opacity-20"
									onclick={() => moveUp(idx)}
									disabled={idx === 0}
								>▲</button>
								<button
									class="text-[10px] leading-none text-muted-foreground hover:text-foreground disabled:opacity-20"
									onclick={() => moveDown(idx)}
									disabled={idx === columns.length - 1}
								>▼</button>
							</div>
							<span class="flex-1">{col.label}</span>
							<button
								class="text-xs {col.visible ? 'text-foreground' : 'text-muted-foreground'} disabled:opacity-30"
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
