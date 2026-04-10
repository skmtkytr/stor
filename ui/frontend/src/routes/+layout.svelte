<script lang="ts">
	import "../app.css";
	let { children } = $props();
</script>

<svelte:head>
	<title>stor</title>
</svelte:head>

<div class="dark">
	{@render children()}
</div>
