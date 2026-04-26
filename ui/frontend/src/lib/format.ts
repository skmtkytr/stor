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

export function formatRatio(downloaded: number, uploaded: number): string {
	if (!downloaded || downloaded <= 0) return uploaded > 0 ? "\u221e" : "0.000";
	return (uploaded / downloaded).toFixed(3);
}

export function formatUnixDate(unix: number): string {
	if (!unix) return "-";
	return new Date(unix * 1000).toLocaleString();
}

// formatRelativeTime returns a short "X ago" string for a past ms-epoch
// timestamp. Future timestamps clamp to "just now".
export function formatRelativeTime(ms: number, now = Date.now()): string {
	const diff = Math.max(0, Math.floor((now - ms) / 1000));
	if (diff < 5) return "just now";
	if (diff < 60) return `${diff}s ago`;
	if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
	if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
	return `${Math.floor(diff / 86400)}d ago`;
}
