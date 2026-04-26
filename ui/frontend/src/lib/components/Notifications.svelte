<script lang="ts">
	import { notifications } from "$lib/stores/notifications.svelte";

	const kindStyles: Record<string, string> = {
		info: "border-zinc-700 bg-zinc-900/95 text-zinc-100",
		success: "border-emerald-700 bg-emerald-950/95 text-emerald-100",
		warn: "border-yellow-700 bg-yellow-950/95 text-yellow-100",
		error: "border-red-700 bg-red-950/95 text-red-100",
	};

	const kindAccent: Record<string, string> = {
		info: "text-zinc-400",
		success: "text-emerald-400",
		warn: "text-yellow-400",
		error: "text-red-400",
	};
</script>

<div class="pointer-events-none fixed bottom-12 right-4 z-50 flex w-80 flex-col gap-2">
	{#each notifications.items as n (n.id)}
		<div
			class="pointer-events-auto rounded border px-3 py-2 text-xs shadow-lg {kindStyles[n.kind] ??
				kindStyles.info}"
		>
			<div class="flex items-start gap-2">
				<span class="mt-0.5 text-[10px] uppercase tracking-wider {kindAccent[n.kind] ?? ''}">
					{n.kind}
				</span>
				<div class="min-w-0 flex-1">
					<div class="truncate font-medium">{n.title}</div>
					{#if n.body}
						<div class="mt-0.5 break-words text-[11px] opacity-80">{n.body}</div>
					{/if}
				</div>
				<button
					class="text-zinc-500 hover:text-zinc-200"
					onclick={() => notifications.dismiss(n.id)}
					aria-label="Dismiss notification"
				>
					&times;
				</button>
			</div>
		</div>
	{/each}
</div>
