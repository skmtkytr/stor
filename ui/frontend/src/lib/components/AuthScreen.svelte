<script lang="ts">
	import { auth } from "$lib/stores/auth.svelte";

	let key = $state("");
</script>

<div class="flex min-h-screen items-center justify-center bg-zinc-950 text-zinc-100">
	<div class="w-full max-w-sm space-y-6 text-center">
		<div>
			<h1 class="text-3xl font-bold tracking-tight">stor</h1>
			<p class="mt-2 text-sm text-zinc-400">Enter your API key to connect.</p>
		</div>
		<form
			class="flex gap-2"
			onsubmit={(e) => {
				e.preventDefault();
				auth.connect(key);
			}}
		>
			<input
				type="text"
				placeholder="sk-..."
				bind:value={key}
				spellcheck="false"
				autocomplete="off"
				class="flex-1 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm outline-none placeholder:text-zinc-500 focus:border-zinc-500"
			/>
			<button
				type="submit"
				disabled={auth.loading}
				class="rounded-md bg-zinc-100 px-4 py-2 text-sm font-medium text-zinc-900 hover:bg-zinc-200 disabled:opacity-50"
			>
				{auth.loading ? "..." : "Connect"}
			</button>
		</form>
		{#if auth.error}
			<p class="text-sm text-red-400">{auth.error}</p>
		{/if}
	</div>
</div>
