<script lang="ts">
	import { api } from "$lib/rpc";

	let {
		selectedIds,
		position,
		onClose,
	}: {
		selectedIds: string[];
		position: { x: number; y: number } | null;
		onClose: () => void;
	} = $props();

	let message = $state<{ text: string; ok: boolean } | null>(null);

	function flash(text: string, ok: boolean) {
		message = { text, ok };
		setTimeout(() => (message = null), 3000);
	}

	async function action(type: string) {
		const ids = selectedIds;
		if (!ids.length) return;

		if (type === "remove") {
			if (!confirm(`Remove ${ids.length} torrent(s)?`)) { onClose(); return; }
			const del = confirm("Also delete downloaded files?");
			await Promise.all(ids.map((id) => api.remove(id, del).catch(() => {})));
			flash(`${ids.length} removed`, true);
		} else if (type === "pause") {
			await Promise.all(ids.map((id) => api.pause(id).catch(() => {})));
			flash(`${ids.length} paused`, true);
		} else if (type === "resume") {
			await Promise.all(ids.map((id) => api.resume(id).catch(() => {})));
			flash(`${ids.length} resumed`, true);
		} else if (type === "queueUp") {
			for (const id of ids) await api.queueUp(id).catch(() => {});
			flash("Moved up", true);
		} else if (type === "queueDown") {
			for (const id of [...ids].reverse()) await api.queueDown(id).catch(() => {});
			flash("Moved down", true);
		} else if (type === "queueTop") {
			for (const id of [...ids].reverse()) await api.queueTop(id).catch(() => {});
			flash("Moved to top", true);
		} else if (type === "queueBottom") {
			for (const id of ids) await api.queueBottom(id).catch(() => {});
			flash("Moved to bottom", true);
		}
		onClose();
	}

	const items: { label: string; type: string; danger?: boolean; separator?: boolean }[] = [
		{ label: "Resume", type: "resume" },
		{ label: "Pause", type: "pause" },
		{ label: "", type: "", separator: true },
		{ label: "Queue Up", type: "queueUp" },
		{ label: "Queue Down", type: "queueDown" },
		{ label: "Move to Top", type: "queueTop" },
		{ label: "Move to Bottom", type: "queueBottom" },
		{ label: "", type: "", separator: true },
		{ label: "Remove", type: "remove", danger: true },
	];
</script>

{#if position}
	<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
	<div class="fixed inset-0 z-40" onclick={onClose} oncontextmenu={(e) => { e.preventDefault(); onClose(); }}></div>
	<div
		class="fixed z-50 min-w-[180px] rounded-md border border-zinc-700 bg-zinc-900 py-1 shadow-xl"
		style="left: {position.x}px; top: {position.y}px;"
	>
		{#each items as item}
			{#if item.separator}
				<div class="my-1 border-t border-zinc-800"></div>
			{:else}
				<button
					class="flex w-full px-3 py-1.5 text-sm hover:bg-zinc-800
						{item.danger ? 'text-red-400 hover:text-red-300' : 'text-zinc-300 hover:text-zinc-100'}"
					onclick={() => action(item.type)}
				>
					{item.label}{item.type === "remove" ? ` (${selectedIds.length})` : ""}
				</button>
			{/if}
		{/each}
	</div>
{/if}

{#if message}
	<div
		class="fixed bottom-4 right-4 z-50 rounded-md px-4 py-2 text-sm shadow-lg
			{message.ok ? 'bg-green-900/80 text-green-200' : 'bg-red-900/80 text-red-200'}"
	>
		{message.text}
	</div>
{/if}
