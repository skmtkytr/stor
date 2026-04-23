<script lang="ts">
	import { api } from "$lib/rpc";

	const PRIORITY_NORMAL = 0;
	const PRIORITY_SKIP = -1;

	let {
		torrentId,
		fileIndex,
		currentPriority,
		path,
		position,
		onClose,
		onApplied,
	}: {
		torrentId: string;
		fileIndex: number;
		currentPriority: number;
		path: string;
		position: { x: number; y: number } | null;
		onClose: () => void;
		onApplied?: () => void;
	} = $props();

	async function setPriority(p: number) {
		try {
			await api.setFilePriority(torrentId, fileIndex, p);
			onApplied?.();
		} catch {
			/* surfaced by FileList's own error handling on next refresh */
		}
		onClose();
	}

	async function copyPath() {
		try {
			await navigator.clipboard.writeText(path);
		} catch {
			/* ignore — older browsers / non-https */
		}
		onClose();
	}
</script>

{#if position}
	<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
	<div
		class="fixed inset-0 z-40"
		onclick={onClose}
		oncontextmenu={(e) => {
			e.preventDefault();
			onClose();
		}}
	></div>
	<div
		class="fixed z-50 min-w-[200px] rounded-md border border-zinc-700 bg-zinc-900 py-1 shadow-xl"
		style="left: {position.x}px; top: {position.y}px;"
	>
		<div class="px-3 py-1 text-[10px] uppercase tracking-wider text-zinc-500">
			Priority
		</div>
		<button
			class="flex w-full items-center px-3 py-1.5 text-sm hover:bg-zinc-800
				{currentPriority === PRIORITY_NORMAL
				? 'text-zinc-100'
				: 'text-zinc-300 hover:text-zinc-100'}"
			onclick={() => setPriority(PRIORITY_NORMAL)}
		>
			<span class="mr-2 w-3 text-blue-400">
				{currentPriority === PRIORITY_NORMAL ? "✓" : ""}
			</span>
			Normal
		</button>
		<button
			class="flex w-full items-center px-3 py-1.5 text-sm hover:bg-zinc-800
				{currentPriority === PRIORITY_SKIP
				? 'text-zinc-100'
				: 'text-zinc-300 hover:text-zinc-100'}"
			onclick={() => setPriority(PRIORITY_SKIP)}
		>
			<span class="mr-2 w-3 text-blue-400">
				{currentPriority === PRIORITY_SKIP ? "✓" : ""}
			</span>
			Skip
		</button>
		<div class="my-1 border-t border-zinc-800"></div>
		<button
			class="flex w-full px-3 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
			onclick={copyPath}
		>
			Copy path
		</button>
	</div>
{/if}
