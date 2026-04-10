<script lang="ts">
	import { api } from "$lib/rpc";

	let {
		selectedCount = 0,
		onAction,
	}: {
		selectedCount: number;
		onAction: (type: string) => void;
	} = $props();

	let source = $state("");
	let fileInput: HTMLInputElement;
	let message = $state<{ text: string; ok: boolean } | null>(null);

	function flash(text: string, ok: boolean) {
		message = { text, ok };
		setTimeout(() => (message = null), 3000);
	}

	async function addMagnet() {
		if (!source.trim()) return;
		try {
			await api.add(source.trim());
			flash("Torrent added", true);
			source = "";
		} catch (e: unknown) {
			flash("Add failed: " + (e instanceof Error ? e.message : "unknown"), false);
		}
	}

	function encodeFile(file: File): Promise<string> {
		return file.arrayBuffer().then((buf) => {
			const bytes = new Uint8Array(buf);
			let raw = "";
			for (let i = 0; i < bytes.length; i += 8192) {
				raw += String.fromCharCode(...bytes.subarray(i, i + 8192));
			}
			return btoa(raw);
		});
	}

	async function handleFiles(files: FileList | File[]) {
		const list = Array.from(files).filter((f) => f.name.endsWith(".torrent"));
		if (!list.length) return;
		let ok = 0,
			fail = 0;
		await Promise.all(
			list.map(async (file) => {
				try {
					await api.addFile(await encodeFile(file));
					ok++;
				} catch {
					fail++;
				}
			}),
		);
		flash(`${ok} added${fail ? `, ${fail} failed` : ""}`, fail === 0);
	}

	async function onFileChange(e: Event) {
		const target = e.target as HTMLInputElement;
		if (target.files) await handleFiles(target.files);
		target.value = "";
	}

	const has = $derived(selectedCount > 0);
</script>

<svelte:window
	ondragover={(e) => e.preventDefault()}
	ondrop={async (e) => {
		e.preventDefault();
		if (e.dataTransfer?.files) await handleFiles(e.dataTransfer.files);
	}}
/>

<div class="flex items-center gap-2 border-b border-zinc-800 px-3 py-1.5">
	<!-- Add section -->
	<form
		class="flex items-center gap-1.5 flex-1 min-w-0"
		onsubmit={(e) => {
			e.preventDefault();
			addMagnet();
		}}
	>
		<input
			type="text"
			placeholder="magnet:?xt=urn:btih:... or URL"
			bind:value={source}
			class="flex-1 min-w-0 rounded border border-zinc-700 bg-zinc-900 px-2.5 py-1 text-sm outline-none placeholder:text-zinc-600 focus:border-zinc-500"
		/>
		<button
			type="submit"
			class="rounded bg-zinc-100 px-3 py-1 text-sm font-medium text-zinc-900 hover:bg-zinc-200 shrink-0"
		>
			Add
		</button>
		<button
			type="button"
			class="rounded border border-zinc-700 px-2.5 py-1 text-sm text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200 shrink-0"
			onclick={() => fileInput.click()}
		>
			.torrent
		</button>
		<input
			bind:this={fileInput}
			type="file"
			accept=".torrent"
			multiple
			class="hidden"
			onchange={onFileChange}
		/>
	</form>

	<!-- Separator -->
	<div class="h-5 w-px bg-zinc-700 shrink-0"></div>

	<!-- Action buttons -->
	<div class="flex items-center gap-1 shrink-0">
		<button
			class="rounded px-2.5 py-1 text-sm hover:bg-zinc-800 disabled:opacity-30 disabled:cursor-default text-green-400"
			disabled={!has}
			onclick={() => onAction("resume")}
			title="Resume"
		>
			Resume
		</button>
		<button
			class="rounded px-2.5 py-1 text-sm hover:bg-zinc-800 disabled:opacity-30 disabled:cursor-default text-yellow-400"
			disabled={!has}
			onclick={() => onAction("pause")}
			title="Pause"
		>
			Pause
		</button>
		<button
			class="rounded px-2.5 py-1 text-sm hover:bg-zinc-800 disabled:opacity-30 disabled:cursor-default text-red-400"
			disabled={!has}
			onclick={() => onAction("remove")}
			title="Remove"
		>
			Remove
		</button>

		<div class="h-5 w-px bg-zinc-800 mx-0.5"></div>

		<button
			class="rounded px-2 py-1 text-sm text-zinc-400 hover:bg-zinc-800 disabled:opacity-30 disabled:cursor-default"
			disabled={!has}
			onclick={() => onAction("queueTop")}
			title="Move to top"
		>
			Top
		</button>
		<button
			class="rounded px-2 py-1 text-sm text-zinc-400 hover:bg-zinc-800 disabled:opacity-30 disabled:cursor-default"
			disabled={!has}
			onclick={() => onAction("queueBottom")}
			title="Move to bottom"
		>
			Bottom
		</button>
	</div>
</div>

{#if message}
	<div
		class="fixed bottom-8 right-4 z-50 rounded-md px-4 py-2 text-sm shadow-lg
			{message.ok ? 'bg-green-900/80 text-green-200' : 'bg-red-900/80 text-red-200'}"
	>
		{message.text}
	</div>
{/if}
