<script lang="ts">
	import { onMount, onDestroy } from "svelte";
	import type { TorrentInfo, EngineStats } from "$lib/types";
	import { api, getApiKey, setApiKey } from "$lib/rpc";
	import { formatSpeed } from "$lib/format";
	import AuthScreen from "$lib/components/AuthScreen.svelte";
	import TorrentTable from "$lib/components/TorrentTable.svelte";
	import AddBar from "$lib/components/AddBar.svelte";
	import SettingsDialog from "$lib/components/SettingsDialog.svelte";
	import { Button } from "$lib/components/ui/button";
	import { Toaster } from "svelte-sonner";

	let authenticated = $state(false);
	let torrents = $state<TorrentInfo[]>([]);
	let stats = $state<EngineStats | null>(null);
	let settingsOpen = $state(false);
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

	onMount(() => tryAutoAuth());
	onDestroy(() => clearInterval(interval));
</script>

<Toaster richColors position="bottom-right" />

{#if !authenticated}
	<AuthScreen {onAuth} />
{:else}
	<div class="dark flex min-h-screen flex-col bg-background text-foreground">
		<!-- Header -->
		<header class="flex items-center justify-between border-b px-4 py-2.5">
			<div class="flex items-center gap-4">
				<h1 class="text-lg font-bold tracking-tight">stor</h1>
				{#if stats}
					<div class="flex items-center gap-3 text-sm text-muted-foreground">
						<span>{stats.active_torrents}/{stats.max_active} active</span>
						<span class="text-border">·</span>
						<span>{stats.total_torrents} total</span>
						<span class="text-border">·</span>
						<span class="tabular-nums">{formatSpeed(stats.total_down_speed)}</span>
					</div>
				{/if}
			</div>
			<Button variant="ghost" size="sm" onclick={() => (settingsOpen = true)}>
				Settings
			</Button>
		</header>

		<!-- Add bar -->
		<div class="border-b px-4 py-2.5">
			<AddBar />
		</div>

		<!-- Torrent list -->
		<div class="flex-1 overflow-auto p-4">
			<TorrentTable {torrents} />
		</div>
	</div>

	<SettingsDialog {stats} bind:open={settingsOpen} />
{/if}
