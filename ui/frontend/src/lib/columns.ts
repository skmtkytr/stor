const STORAGE_KEY = "stor_col_config";

export interface ColDef {
	id: string;
	label: string;
	width: number;
	minWidth: number;
	align?: "left" | "right" | "center";
	visible: boolean;
}

export const ALL_COLUMNS: ColDef[] = [
	{ id: "name", label: "Name", width: 400, minWidth: 150, visible: true },
	{ id: "size", label: "Size", width: 85, minWidth: 60, align: "right", visible: true },
	{ id: "progress", label: "Progress", width: 170, minWidth: 100, visible: true },
	{ id: "speed", label: "Speed", width: 90, minWidth: 60, align: "right", visible: true },
	{ id: "eta", label: "ETA", width: 75, minWidth: 50, align: "right", visible: true },
	{ id: "peers", label: "Peers", width: 55, minWidth: 40, align: "right", visible: true },
	{ id: "state", label: "State", width: 95, minWidth: 65, visible: true },
	{ id: "queue", label: "#", width: 45, minWidth: 35, align: "center", visible: true },
];

interface SavedCol {
	id: string;
	width: number;
	visible: boolean;
}

export function loadColConfig(): ColDef[] | null {
	try {
		const raw = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null");
		if (!Array.isArray(raw)) return null;
		const saved = raw as SavedCol[];
		// Rebuild from saved order, matching by id
		const lookup = new Map(ALL_COLUMNS.map((c) => [c.id, c]));
		const result: ColDef[] = [];
		for (const s of saved) {
			const def = lookup.get(s.id);
			if (def) {
				result.push({ ...def, width: s.width ?? def.width, visible: s.visible ?? true });
				lookup.delete(s.id);
			}
		}
		// Append any new columns not in saved config
		for (const def of lookup.values()) {
			result.push({ ...def });
		}
		return result;
	} catch {
		return null;
	}
}

export function saveColConfig(cols: ColDef[]) {
	const data: SavedCol[] = cols.map((c) => ({ id: c.id, width: c.width, visible: c.visible }));
	localStorage.setItem(STORAGE_KEY, JSON.stringify(data));
}
