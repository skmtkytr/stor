<script lang="ts">
	import { onMount, onDestroy } from "svelte";
	import { notifications, type NotificationKind } from "$lib/stores/notifications.svelte";
	import { formatRelativeTime } from "$lib/format";

	type FilterKind = "all" | NotificationKind;
	let filter = $state<FilterKind>("all");

	// Tick once a minute so "Xm ago" labels update without forcing renders on
	// every event arrival. We store a number that the derived value reads to
	// participate in reactivity.
	let nowTick = $state(Date.now());
	let timer: ReturnType<typeof setInterval> | null = null;
	onMount(() => {
		timer = setInterval(() => (nowTick = Date.now()), 30_000);
	});
	onDestroy(() => {
		if (timer) clearInterval(timer);
	});

	const visible = $derived.by(() => {
		const list = notifications.history;
		if (filter === "all") return list;
		return list.filter((n) => n.kind === filter);
	});

	const kindBorder: Record<NotificationKind, string> = {
		info: "border-l-zinc-600",
		success: "border-l-emerald-500",
		warn: "border-l-yellow-500",
		error: "border-l-red-500",
	};
	const kindLabel: Record<NotificationKind, string> = {
		info: "text-zinc-400",
		success: "text-emerald-400",
		warn: "text-yellow-400",
		error: "text-red-400",
	};

	function close() {
		notifications.closePanel();
	}

	function onBackdropKey(e: KeyboardEvent) {
		if (e.key === "Escape") close();
	}
</script>

{#if notifications.panelOpen}
	<!-- Backdrop -->
	<button
		type="button"
		class="fixed inset-0 z-40 bg-black/40"
		aria-label="Close notification panel"
		onclick={close}
		onkeydown={onBackdropKey}
	></button>

	<!-- Panel -->
	<aside
		class="fixed bottom-7 right-0 top-0 z-50 flex w-96 flex-col border-l border-zinc-800 bg-zinc-950 text-zinc-100 shadow-2xl"
		aria-label="Notification history"
	>
		<header class="flex items-center justify-between border-b border-zinc-800 px-3 py-2">
			<h2 class="text-sm font-semibold">Notifications</h2>
			<div class="flex items-center gap-2">
				<button
					class="text-xs text-zinc-400 hover:text-zinc-200 disabled:opacity-40"
					onclick={() => notifications.clearHistory()}
					disabled={notifications.history.length === 0}
					title="Clear all notifications from history"
				>
					Clear
				</button>
				<button
					class="text-zinc-400 hover:text-zinc-100"
					onclick={close}
					aria-label="Close panel"
				>
					&times;
				</button>
			</div>
		</header>

		<nav class="flex gap-1 border-b border-zinc-800 px-3 py-1.5 text-[11px]">
			{#each ["all", "error", "warn", "success", "info"] as f (f)}
				<button
					class="rounded px-2 py-0.5 {filter === f
						? 'bg-zinc-800 text-zinc-100'
						: 'text-zinc-500 hover:text-zinc-300'}"
					onclick={() => (filter = f as FilterKind)}
				>
					{f}
				</button>
			{/each}
		</nav>

		<div class="flex-1 overflow-y-auto">
			{#if visible.length === 0}
				<div class="flex h-full items-center justify-center text-xs text-zinc-600">
					{filter === "all" ? "No notifications yet" : `No ${filter} notifications`}
				</div>
			{:else}
				<ul>
					{#each visible as n (n.id)}
						{@const _tick = nowTick}
						<li
							class="border-b border-l-2 border-zinc-900 border-zinc-800 px-3 py-2 text-xs {kindBorder[
								n.kind
							]}"
						>
							<div class="flex items-baseline justify-between gap-2">
								<span class="text-[10px] uppercase tracking-wider {kindLabel[n.kind]}">
									{n.kind}
								</span>
								<span class="text-[10px] text-zinc-500" title={new Date(n.createdAt).toLocaleString()}>
									{formatRelativeTime(n.createdAt, _tick)}
								</span>
							</div>
							<div class="mt-0.5 font-medium">{n.title}</div>
							{#if n.body}
								<div class="mt-0.5 break-words text-[11px] text-zinc-400">{n.body}</div>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	</aside>
{/if}
