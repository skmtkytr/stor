<script lang="ts">
	import { onMount, onDestroy } from "svelte";
	import { auth } from "$lib/stores/auth.svelte";
	import { torrents } from "$lib/stores/torrents.svelte";
	import AuthScreen from "$lib/components/AuthScreen.svelte";
	import Header from "$lib/components/Header.svelte";
	import Toolbar from "$lib/components/Toolbar.svelte";
	import FilterBar from "$lib/components/FilterBar.svelte";
	import TorrentTable from "$lib/components/TorrentTable.svelte";
	import TorrentDetailPanel from "$lib/components/TorrentDetailPanel.svelte";
	import StatusBar from "$lib/components/StatusBar.svelte";
	import SettingsDialog from "$lib/components/SettingsDialog.svelte";
	import Notifications from "$lib/components/Notifications.svelte";

	let settingsOpen = $state(false);
	let activeFilter = $state("all");
	let selectedIds = $state<string[]>([]);
	let table = $state<TorrentTable>();

	const detailId = $derived(selectedIds.length === 1 ? selectedIds[0] : null);

	onMount(async () => {
		await auth.tryAutoAuth();
		if (auth.authenticated) torrents.startPolling();
	});

	onDestroy(() => torrents.stopPolling());

	$effect(() => {
		if (auth.authenticated) torrents.startPolling();
	});

	function handleAction(type: string) {
		table?.batchAction(type);
	}
</script>

{#if !auth.authenticated}
	<AuthScreen />
{:else}
	<div class="flex h-screen flex-col bg-zinc-950 text-zinc-100">
		<Header onSettingsClick={() => (settingsOpen = true)} />
		<Toolbar selectedCount={selectedIds.length} onAction={handleAction} />
		<FilterBar torrents={torrents.list} bind:activeFilter />
		<TorrentTable
			bind:this={table}
			filter={activeFilter}
			onSelectionChange={(ids) => (selectedIds = ids)}
		/>
		<div class="h-72 shrink-0">
			<TorrentDetailPanel torrentId={detailId} />
		</div>
		<StatusBar />
	</div>
	<SettingsDialog bind:open={settingsOpen} />
	<Notifications />
{/if}
