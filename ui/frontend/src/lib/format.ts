export function formatBytes(b: number): string {
	if (b >= 1 << 30) return (b / (1 << 30)).toFixed(2) + " GB";
	if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + " MB";
	if (b >= 1 << 10) return (b / (1 << 10)).toFixed(0) + " KB";
	return b + " B";
}

export function formatSpeed(b: number): string {
	return formatBytes(b) + "/s";
}

export function formatETA(downloaded: number, total: number, speed: number): string {
	if (!speed || speed <= 0 || downloaded >= total) return "-";
	const secs = Math.round((total - downloaded) / speed);
	if (secs < 60) return `${secs}s`;
	if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`;
	return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
}
