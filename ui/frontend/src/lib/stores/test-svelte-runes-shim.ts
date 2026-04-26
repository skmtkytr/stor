// Side-effect-only shim that exposes Svelte 5 runes ($state, $derived) as
// globals so .svelte.ts modules can be imported under bun:test (which does
// not run the Svelte compiler). The runes reduce to identity pass-through;
// reactivity is a no-op, fine for unit tests of plain logic.
// Import this BEFORE any .svelte.ts module under test.
//
// eslint-disable @typescript-eslint/no-explicit-any
const g = globalThis as any;
if (typeof g.$state === "undefined") g.$state = <T>(v: T): T => v;
if (typeof g.$derived === "undefined") g.$derived = <T>(v: T): T => v;
export {};
