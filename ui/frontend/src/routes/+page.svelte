<script lang="ts">
	import { onMount, onDestroy } from "svelte";
	import type { TorrentInfo, EngineStats } from "$lib/types";
	import { api, getApiKey, setApiKey } from "$lib/rpc";
	import { formatSpeed } from "$lib/format";
	import AuthScreen from "$lib/components/AuthScreen.svelte";
	import TorrentTable from "$lib/components/TorrentTable.svelte";
	import AddBar from "$lib/components/AddBar.svelte";
	import { Input } from "$lib/components/ui/input";
	import { Toaster, toast } from "svelte-sonner";

	let authenticated = $state(false);
	let torrents = $state<TorrentInfo[]>([]);
	let stats = $state<EngineStats | null>(null);
	let interval: ReturnType<typeof setInterval>;

	async function refresh() {
		try {
			const [t, s] = await Promise.all([api.list(), api.stats()]);
			torrents = t ?? [];
			stats = s;
		} catch {
			// ignore polling errors
		}
	}

	function startPolling() {
		refresh();
		interval = setInterval(refresh, 1000);
	}

	async function tryAutoAuth() {
		if (!getApiKey()) return;
		try {
			await api.version();
			authenticated = true;
			startPolling();
		} catch {
			setApiKey("");
		}
	}

	function onAuth() {
		authenticated = true;
		startPolling();
	}

	async function setMaxActive(e: Event) {
		const val = parseInt((e.target as HTMLInputElement).value);
		if (!val || val < 1) return;
		try {
			await api.setMaxActive(val);
			toast.success(`Max active set to ${val}`);
		} catch (err: unknown) {
			toast.error(err instanceof Error ? err.message : "Failed");
		}
	}

	onMount(() => tryAutoAuth());
	onDestroy(() => clearInterval(interval));
</script>

<Toaster richColors position="bottom-right" />

{#if !authenticated}
	<AuthScreen {onAuth} />
{:else}
	<div class="dark flex min-h-screen flex-col bg-background text-foreground">
		<!-- Header -->
		<header class="flex items-center justify-between border-b px-4 py-3">
			<div class="flex items-center gap-4">
				<h1 class="text-lg font-bold">stor</h1>
				{#if stats}
					<span class="text-sm text-muted-foreground">
						{stats.active_torrents}/{stats.max_active} active
						· {stats.total_torrents} total
						· {formatSpeed(stats.total_down_speed)}
					</span>
				{/if}
			</div>
			<div class="flex items-center gap-2">
				<label class="flex items-center gap-1.5 text-xs text-muted-foreground">
					Max active:
					<Input
						type="number"
						min={1}
						max={50}
						value={stats?.max_active ?? 5}
						onchange={setMaxActive}
						class="h-7 w-14 text-center text-xs"
					/>
				</label>
			</div>
		</header>

		<!-- Add bar -->
		<div class="border-b px-4 py-3">
			<AddBar />
		</div>

		<!-- Torrent list -->
		<div class="flex-1 overflow-auto p-4">
			<TorrentTable {torrents} />
		</div>
	</div>
{/if}
