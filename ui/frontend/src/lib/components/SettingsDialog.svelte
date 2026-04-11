<script lang="ts">
	import { torrents } from "$lib/stores/torrents.svelte";
	import { auth } from "$lib/stores/auth.svelte";
	import { api } from "$lib/rpc";
	import type { EngineConfig } from "$lib/types";

	let { open = $bindable(false) }: { open: boolean } = $props();

	let cfg = $state<EngineConfig>({
		download_dir: "",
		tmp_dir: "",
		max_active: 5,
		max_peers: 0,
		max_pipeline: 0,
		dial_timeout: 0,
		numwant: 0,
		log_level: "info",
		enable_utp: false,
	});
	let saveMsg = $state("");
	let saving = $state(false);
	let initialized = $state(false);

	$effect(() => {
		if (open && !initialized && torrents.stats?.config) {
			cfg = { ...torrents.stats.config };
			initialized = true;
		}
		if (!open) initialized = false;
	});

	async function save() {
		saving = true;
		saveMsg = "";
		try {
			await api.setConfig(cfg);
			saveMsg = "Saved";
			setTimeout(() => (saveMsg = ""), 2000);
		} catch (e: unknown) {
			saveMsg = e instanceof Error ? e.message : "Failed";
		} finally {
			saving = false;
		}
	}

	function logout() {
		auth.logout();
		open = false;
	}
</script>

{#if open}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="fixed inset-0 z-40 bg-black/60"
		onclick={() => (open = false)}
		onkeydown={(e) => e.key === "Escape" && (open = false)}
	></div>

	<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
		<div class="w-full max-w-lg rounded-lg border border-zinc-700 bg-zinc-900 shadow-xl">
			<div class="flex items-center justify-between border-b border-zinc-800 px-5 py-4">
				<div>
					<h2 class="text-lg font-semibold">Settings</h2>
					<p class="text-sm text-zinc-400">Changes apply to new downloads.</p>
				</div>
				<button
					class="text-zinc-400 hover:text-zinc-100"
					onclick={() => (open = false)}
				>
					&times;
				</button>
			</div>

			<div class="max-h-[70vh] space-y-4 overflow-y-auto p-5">
				<!-- General -->
				<h3 class="text-xs font-semibold uppercase tracking-wider text-zinc-500">General</h3>

				<label class="block space-y-1">
					<span class="text-sm text-zinc-300">Download Directory</span>
					<input
						type="text"
						bind:value={cfg.download_dir}
						class="w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
					/>
				</label>

				<label class="block space-y-1">
					<span class="text-sm text-zinc-300">Temp Directory</span>
					<input
						type="text"
						bind:value={cfg.tmp_dir}
						placeholder="(same as download dir)"
						class="w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
					/>
				</label>

				<div class="grid grid-cols-2 gap-3">
					<label class="space-y-1">
						<span class="text-sm text-zinc-300">Max Active Downloads</span>
						<input
							type="number"
							min="1"
							max="50"
							bind:value={cfg.max_active}
							class="w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
						/>
					</label>
					<label class="space-y-1">
						<span class="text-sm text-zinc-300">Log Level</span>
						<select
							bind:value={cfg.log_level}
							class="w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
						>
							<option value="debug">Debug</option>
							<option value="info">Info</option>
							<option value="warn">Warn</option>
							<option value="error">Error</option>
						</select>
					</label>
				</div>

				<div class="border-t border-zinc-800"></div>

				<!-- Connection -->
				<h3 class="text-xs font-semibold uppercase tracking-wider text-zinc-500">Connection</h3>

				<div class="grid grid-cols-2 gap-3">
					<label class="space-y-1">
						<span class="text-sm text-zinc-300">Max Peers / Torrent</span>
						<input
							type="number"
							min="1"
							max="500"
							bind:value={cfg.max_peers}
							placeholder="100"
							class="w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
						/>
					</label>
					<label class="space-y-1">
						<span class="text-sm text-zinc-300">Pipeline Depth</span>
						<input
							type="number"
							min="1"
							max="64"
							bind:value={cfg.max_pipeline}
							placeholder="16"
							class="w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
						/>
					</label>
					<label class="space-y-1">
						<span class="text-sm text-zinc-300">Dial Timeout (s)</span>
						<input
							type="number"
							min="1"
							max="30"
							bind:value={cfg.dial_timeout}
							placeholder="3"
							class="w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
						/>
					</label>
					<label class="space-y-1">
						<span class="text-sm text-zinc-300">Tracker NumWant</span>
						<input
							type="number"
							min="1"
							max="1000"
							bind:value={cfg.numwant}
							placeholder="200"
							class="w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm outline-none focus:border-zinc-500"
						/>
					</label>
				</div>

				<label class="flex items-center gap-2">
					<input
						type="checkbox"
						bind:checked={cfg.enable_utp}
						class="rounded border-zinc-600 bg-zinc-800"
					/>
					<span class="text-sm text-zinc-300">Enable uTP (low-latency transport)</span>
				</label>

				<div class="border-t border-zinc-800"></div>

				<!-- Save / Status -->
				<div class="flex items-center gap-3">
					<button
						class="rounded-md bg-zinc-100 px-4 py-1.5 text-sm font-medium text-zinc-900 hover:bg-zinc-200 disabled:opacity-50"
						onclick={save}
						disabled={saving}
					>
						{saving ? "Saving..." : "Save"}
					</button>
					{#if saveMsg}
						<span class="text-xs text-zinc-400">{saveMsg}</span>
					{/if}
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
