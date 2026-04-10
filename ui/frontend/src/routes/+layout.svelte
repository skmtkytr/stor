<script lang="ts">
	import "../app.css";
	let { children } = $props();
</script>

<svelte:head>
	<title>stor</title>
</svelte:head>

{@render children()}
