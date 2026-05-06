// Svelte 5 runes adapter around @tanstack/table-core.
//
// The official @tanstack/svelte-table is Svelte 3/4 only — it relies on
// `writable` stores. table-core is framework-agnostic; we wire its state
// hooks into $state runes here so the table's getters stay reactive in
// templates without dragging the legacy store API into runes mode.
//
// Usage:
//
//   const tbl = useTable<TorrentInfo>({
//       columns,
//       data: [],
//       getCoreRowModel: getCoreRowModel(),
//       getSortedRowModel: getSortedRowModel(),
//       enableColumnResizing: true,
//       columnResizeMode: "onChange",
//       initialColumnSizing: savedSizes,
//   });
//   $effect(() => tbl.setData(filtered));   // push reactive data
//   {#each tbl.table.getHeaderGroups()[0].headers as h}...
//   {#each tbl.table.getRowModel().rows as r}...

import {
	createTable,
	type ColumnSizingInfoState,
	type ColumnSizingState,
	type RowData,
	type SortingState,
	type Table,
	type TableOptions,
	type Updater,
} from "@tanstack/table-core";

export interface UseTableOptions<TData extends RowData>
	extends Omit<TableOptions<TData>, "state" | "onStateChange" | "renderFallbackValue"> {
	initialColumnSizing?: ColumnSizingState;
	initialSorting?: SortingState;
}

function applyUpdater<T>(u: Updater<T>, prev: T): T {
	return typeof u === "function" ? (u as (p: T) => T)(prev) : u;
}

export function useTable<TData extends RowData>(opts: UseTableOptions<TData>) {
	let columnSizing = $state<ColumnSizingState>(opts.initialColumnSizing ?? {});
	let columnSizingInfo = $state<ColumnSizingInfoState>({
		startOffset: null,
		startSize: null,
		deltaOffset: null,
		deltaPercentage: null,
		isResizingColumn: false,
		columnSizingStart: [],
	});
	let sorting = $state<SortingState>(opts.initialSorting ?? []);

	// table-core reads `state` and the on*Change callbacks from its
	// options object. We give it the initial set, and then sync our
	// reactive state slices into the options on every change via the
	// $effect below.
	const tbl: Table<TData> = createTable<TData>({
		...opts,
		state: { columnSizing, columnSizingInfo, sorting },
		onColumnSizingChange: (u) => {
			columnSizing = applyUpdater(u, columnSizing);
		},
		onColumnSizingInfoChange: (u) => {
			columnSizingInfo = applyUpdater(u, columnSizingInfo);
		},
		onSortingChange: (u) => {
			sorting = applyUpdater(u, sorting);
		},
		onStateChange: () => {
			// table-core requires this to be present even when we manage
			// individual slices (columnSizing / sorting / ...) ourselves.
		},
		renderFallbackValue: null,
	});

	// Whenever any reactive slice changes, re-feed it into the table's
	// options. Without this, table.getHeaderGroups() etc. would keep
	// reading the original snapshot and ignore the user's resize/sort.
	$effect(() => {
		tbl.setOptions((prev) => ({
			...prev,
			state: {
				...prev.state,
				columnSizing,
				columnSizingInfo,
				sorting,
			},
		}));
	});

	return {
		table: tbl,
		get columnSizing() {
			return columnSizing;
		},
		get sorting() {
			return sorting;
		},
		setData(data: TData[]) {
			tbl.setOptions((prev) => ({ ...prev, data }));
		},
		setColumnSizing(v: ColumnSizingState) {
			columnSizing = v;
		},
	};
}
