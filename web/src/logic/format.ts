const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

export function ageCompact(ms: number, now = Date.now()): string {
  const d = Math.max(0, now - ms);
  if (d < HOUR) return `${Math.max(1, Math.round(d / MIN))}m`;
  if (d < DAY) return `${Math.floor(d / HOUR)}h`;
  return `${Math.floor(d / DAY)}d`;
}

export function ageAgo(ms: number, now = Date.now()): string {
  const d = Math.max(0, now - ms);
  if (d < HOUR) return `${Math.max(1, Math.round(d / MIN))} min ago`;
  if (d < DAY) return `${Math.floor(d / HOUR)} h ago`;
  return `${Math.floor(d / DAY)} d ago`;
}

export function formatTime(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

export function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

export const formatCost = (usd: number) => `$${usd.toFixed(2)}`;
export const sha7 = (sha: string) => sha.slice(0, 7);
export const truncate = (s: string, max: number) => (s.length <= max ? s : `${s.slice(0, max - 1)}…`);
