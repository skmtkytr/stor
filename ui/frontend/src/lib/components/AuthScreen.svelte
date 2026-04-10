<script lang="ts">
	import { Button } from "$lib/components/ui/button";
	import { Input } from "$lib/components/ui/input";
	import { api, setApiKey } from "$lib/rpc";

	let { onAuth }: { onAuth: () => void } = $props();

	let key = $state("");
	let error = $state("");
	let loading = $state(false);

	async function connect() {
		if (!key.trim()) return;
		loading = true;
		error = "";
		setApiKey(key.trim());
		try {
			await api.version();
			onAuth();
		} catch (e: unknown) {
			error = e instanceof Error ? e.message : "Connection failed";
			setApiKey("");
		} finally {
			loading = false;
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center">
	<div class="w-full max-w-sm space-y-6 text-center">
		<div>
			<h1 class="text-3xl font-bold">stor</h1>
			<p class="text-muted-foreground mt-2">Enter your API key to connect.</p>
		</div>
		<div class="flex gap-2">
			<Input
				type="text"
				placeholder="sk-..."
				bind:value={key}
				onkeydown={(e: KeyboardEvent) => e.key === "Enter" && connect()}
				spellcheck="false"
				autocomplete="off"
			/>
			<Button onclick={connect} disabled={loading}>
				{loading ? "..." : "Connect"}
			</Button>
		</div>
		{#if error}
			<p class="text-destructive text-sm">{error}</p>
		{/if}
	</div>
</div>
