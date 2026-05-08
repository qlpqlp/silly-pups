export function satsToDoge(sats: number): string {
  if (!Number.isFinite(sats)) return "—";
  return (sats / 1e8).toLocaleString(undefined, { maximumFractionDigits: 8 });
}

export function shortenHash(hex: string, left = 10, right = 8): string {
  const h = hex.trim();
  if (h.length <= left + right + 1) return h;
  return `${h.slice(0, left)}…${h.slice(-right)}`;
}

export function timeAgo(sec: number): string {
  const d = Date.now() / 1000 - sec;
  if (d < 60) return `${Math.floor(d)}s ago`;
  if (d < 3600) return `${Math.floor(d / 60)}m ago`;
  if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
  return `${Math.floor(d / 86400)}d ago`;
}
