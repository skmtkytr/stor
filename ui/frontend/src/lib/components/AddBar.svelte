<script lang="ts">
	import { Button } from "$lib/components/ui/button";
	import { Input } from "$lib/components/ui/input";
	import { api } from "$lib/rpc";
	import { toast } from "svelte-sonner";

	let source = $state("");
	let fileInput: HTMLInputElement;

	async function addMagnet() {
		if (!source.trim()) return;
		try {
			await api.add(source.trim());
			toast.success("Torrent added");
			source = "";
		} catch (e: unknown) {
			toast.error("Add failed: " + (e instanceof Error ? e.message : "unknown"));
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
		let ok = 0, fail = 0;
		await Promise.all(
			list.map(async (file) => {
				try {
					await api.addFile(await encodeFile(file));
					ok++;
				} catch {
					fail++;
				}
			})
		);
		toast[fail ? "error" : "success"](`${ok} added${fail ? `, ${fail} failed` : ""}`);
	}

	async function onFileChange(e: Event) {
		const target = e.target as HTMLInputElement;
		if (target.files) await handleFiles(target.files);
		target.value = "";
	}
</script>

<svelte:window
	ondragover={(e) => e.preventDefault()}
	ondrop={async (e) => {
		e.preventDefault();
		if (e.dataTransfer?.files) await handleFiles(e.dataTransfer.files);
	}}
/>

<div class="flex gap-2">
	<Input
		type="text"
		placeholder="magnet:?xt=urn:btih:... or torrent URL"
		bind:value={source}
		onkeydown={(e: KeyboardEvent) => e.key === "Enter" && addMagnet()}
		class="flex-1"
	/>
	<Button onclick={addMagnet}>Add</Button>
	<Button variant="outline" onclick={() => fileInput.click()}>
		Upload .torrent
	</Button>
	<input
		bind:this={fileInput}
		type="file"
		accept=".torrent"
		multiple
		class="hidden"
		onchange={onFileChange}
	/>
</div>
