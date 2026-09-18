// Shared mono formatting for the admin console rows. Sizes follow the
// my-space meta conventions: compact DM Mono, never wrapping long IDs.
export function dwell(seconds: number) {
	if (seconds < 90) return `${seconds}s`;
	if (seconds < 5400) return `${Math.round(seconds / 60)}m`;
	return `${Math.round(seconds / 3600)}h`;
}

export function relative(value: string) {
	const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000));
	if (seconds < 60) return `${seconds}s 前`;
	if (seconds < 3600) return `${Math.round(seconds / 60)}m 前`;
	if (seconds < 86400) return `${Math.round(seconds / 3600)}h 前`;
	return `${Math.round(seconds / 86400)}d 前`;
}

export function shortId(id: string) {
	return id.length > 18 ? `${id.slice(0, 15)}…` : id;
}

export function clock(value: string) {
	return new Date(value).toLocaleString();
}
