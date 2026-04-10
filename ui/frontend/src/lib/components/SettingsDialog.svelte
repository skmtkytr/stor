<script lang="ts">
	import { torrents } from "$lib/stores/torrents.svelte";
	import { auth } from "$lib/stores/auth.svelte";
	import { api } from "$lib/rpc";

	let { open = $bindable(false) }: { open: boolean } = $props();

	let maxActive = $state(5);
	let saveMsg = $state("");

	$effect(() => {
		if (torrents.stats) maxActive = torrents.stats.max_active;
	});

	async function saveMaxActive() {
		try {
			await api.setMaxActive(maxActive);
			saveMsg = "Saved";
			setTimeout(() => (saveMsg = ""), 2000);
		} catch (e: unknown) {
			saveMsg = e instanceof Error ? e.message : "Failed";
		}
	}

	function logout() {
		auth.logout();
		open = false;
	}
</script>

{#if open}
	<!-- Backdrop -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="fixed inset-0 z-40 bg-black/60"
		onclick={() => (open = false)}
		onkeydown={(e) => e.key === "Escape" && (open = false)}
	></div>

	<!-- Dialog -->
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<div class="w-full max-w-md rounded-lg border border-zinc-700 bg-zinc-900 shadow-xl">
			<div class="flex items-center justify-between border-b border-zinc-800 px-5 py-4">
				<div>
					<h2 class="text-lg font-semibold">Settings</h2>
					<p class="text-sm text-zinc-400">Configure daemon settings.</p>
				</div>
				<button
					class="text-zinc-400 hover:text-zinc-100"
					onclick={() => (open = false)}
				>
					&times;
				</button>
			</div>

			<div class="max-h-[70vh] space-y-5 overflow-y-auto p-5">
				<!-- Max active downloads -->
				<div class="space-y-2">
					<label class="text-sm font-medium" for="max-active">Max concurrent downloads</label>
					<div class="flex items-center gap-2">
						<input
							id="max-active"
							type="number"
							min="1"
							max="50"
							bind:value={maxActive}
							class="w-20 rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
						/>
						<button
							class="rounded-md bg-zinc-100 px-3 py-1.5 text-sm font-medium text-zinc-900 hover:bg-zinc-200"
							onclick={saveMaxActive}
						>
							Save
						</button>
						{#if saveMsg}
							<span class="text-xs text-zinc-400">{saveMsg}</span>
						{/if}
					</div>
				</div>

				<div class="border-t border-zinc-800"></div>

				<!-- Stats -->
				{#if torrents.stats}
					{@const s = torrents.stats}
					<div class="space-y-1 text-sm">
						<p><span class="text-zinc-400">Active:</span> {s.active_torrents}</p>
						<p><span class="text-zinc-400">Total:</span> {s.total_torrents}</p>
					</div>
				{/if}

				<div class="border-t border-zinc-800"></div>

				<div>
					<button
						class="rounded-md bg-red-900/60 px-3 py-1.5 text-sm text-red-300 hover:bg-red-900/80"
						onclick={logout}
					>
						Disconnect
					</button>
					<p class="mt-1 text-xs text-zinc-500">Clear API key and return to login.</p>
				</div>
			</div>
		</div>
	</div>
{/if}
