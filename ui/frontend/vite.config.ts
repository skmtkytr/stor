import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, type Plugin } from 'vite';

// SvelteKit's plugin sets content-hashed filenames for everything under
// `_app/immutable/`. With ui/dist tracked in git, that produces dozens of
// rename diffs per commit. Force stable filenames by overriding the output
// options *after* SvelteKit has set them — we use `enforce: 'post'` so this
// plugin's config hook runs last in the chain.
//
// The cache-safety guarantee that hashes provide is replaced by
// `Cache-Control: no-cache, must-revalidate` on `/_app/` (see daemon.go),
// which forces conditional revalidation on every load. That trades a tiny
// per-chunk round-trip (LAN, ~200B 304 response) for a clean git history.
const stableOutputNames = (): Plugin => ({
	name: 'stable-output-names',
	enforce: 'post',
	config(cfg) {
		// Only override the client build. SvelteKit's SSR pass needs its
		// own predictable filenames (e.g. internal.js); touching them
		// breaks the postbuild analyse step.
		if (cfg.build?.ssr) return;
		cfg.build ??= {};
		cfg.build.rollupOptions ??= {};
		const output = cfg.build.rollupOptions.output;
		const merge = (o: Record<string, unknown>) => {
			// SvelteKit feeds entry [name] values that already include the
			// subdirectory (e.g. "entry/app", "nodes/0"), so we just drop
			// the [hash] from the default template and keep [name] as-is.
			//
			// Note: only JS entries / chunks get stabilised. Asset names
			// (fonts, CSS embedded by tailwind) are left to SvelteKit/Vite
			// because (a) those files have content-stable hashes already
			// — fonts barely change — and (b) overriding assetFileNames
			// causes the tailwindcss plugin to emit duplicate
			// hashed+unhashed copies of every woff2.
			o.entryFileNames = '_app/immutable/[name].js';
			o.chunkFileNames = '_app/immutable/chunks/[name].js';
		};
		if (Array.isArray(output)) {
			for (const o of output) merge(o as Record<string, unknown>);
		} else {
			cfg.build.rollupOptions.output = output ?? {};
			merge(cfg.build.rollupOptions.output as Record<string, unknown>);
		}
	},
});

export default defineConfig({
	plugins: [tailwindcss(), sveltekit(), stableOutputNames()],
});
