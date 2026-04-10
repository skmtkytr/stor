<script lang="ts">
	let { onSettingsClick }: { onSettingsClick: () => void } = $props();
</script>

<header class="flex items-center justify-between border-b border-zinc-800 px-3 py-1.5">
	<h1 class="text-base font-bold tracking-tight">stor</h1>
	<button
		class="rounded px-2.5 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
		onclick={onSettingsClick}
	>
		Settings
	</button>
</header>
