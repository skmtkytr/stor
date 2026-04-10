<script lang="ts">
	import * as Dialog from "$lib/components/ui/dialog";
	import { Button } from "$lib/components/ui/button";
	import { Input } from "$lib/components/ui/input";
	import { Separator } from "$lib/components/ui/separator";
	import { api, setApiKey } from "$lib/rpc";
	import { toast } from "svelte-sonner";
	import type { EngineStats } from "$lib/types";

	let { stats, open = $bindable(false) }: { stats: EngineStats | null; open: boolean } = $props();

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

	function logout() {
		setApiKey("");
		window.location.reload();
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Content class="sm:max-w-md">
		<Dialog.Header>
			<Dialog.Title>Settings</Dialog.Title>
			<Dialog.Description>Configure your stor daemon.</Dialog.Description>
		</Dialog.Header>

		<div class="space-y-6 py-4">
			<!-- Max active downloads -->
			<div class="space-y-2">
				<label class="text-sm font-medium" for="max-active">
					Max concurrent downloads
				</label>
				<div class="flex items-center gap-2">
					<Input
						id="max-active"
						type="number"
						min={1}
						max={50}
						bind:value={maxActive}
						class="w-20"
					/>
					<Button size="sm" onclick={saveMaxActive}>Save</Button>
				</div>
				<p class="text-xs text-muted-foreground">
					Torrents beyond this limit will be queued.
				</p>
			</div>

			<Separator />

			<!-- Stats -->
			{#if stats}
				<div class="space-y-1 text-sm">
					<p><span class="text-muted-foreground">Active:</span> {stats.active_torrents}</p>
					<p><span class="text-muted-foreground">Total:</span> {stats.total_torrents}</p>
					<p><span class="text-muted-foreground">Max active:</span> {stats.max_active}</p>
				</div>
			{/if}

			<Separator />

			<!-- Logout -->
			<div>
				<Button variant="destructive" size="sm" onclick={logout}>
					Disconnect
				</Button>
				<p class="mt-1 text-xs text-muted-foreground">
					Clear API key and return to login screen.
				</p>
			</div>
		</div>
	</Dialog.Content>
</Dialog.Root>
