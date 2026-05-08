const endpoints = [
  ["/api/public/network-overview", "Chain + mempool snapshot, optional CoinGecko market fields"],
  ["/api/public/core/recent-blocks/", "Paginated recent blocks (indexed)"],
  ["/api/public/core/recent-txs/", "Recent txs (?lite=1 for fast PQ badges)"],
  ["/api/public/core/search/", "Omnibox search (txid, height, hash, address)"],
  ["/api/public/block/", "Block detail (?height= or ?hash=)"],
  ["/api/public/tx/", "Transaction decode + PQ verification"],
  ["/api/public/mining/stats/", "Coinbase miner leaderboard"],
  ["/api/public/pq/analytics/", "PQ aggregates + hourly buckets + leaderboards"],
];

export default function ApiDocsPage() {
  return (
    <div className="glass-card space-y-6 p-10">
      <h1 className="font-comic text-4xl font-bold text-doge-ink dark:text-amber-50">Public HTTP API</h1>
      <p className="text-slate-600 dark:text-slate-300">All routes inherit rate limits & optional token gates configured on the Go service.</p>
      <ul className="space-y-3">
        {endpoints.map(([path, note]) => (
          <li key={path} className="rounded-xl border border-white/40 bg-white/40 p-4 dark:border-white/10 dark:bg-white/5">
            <code className="font-mono text-sm text-amber-800 dark:text-amber-200">{path}</code>
            <div className="mt-1 text-sm text-slate-600 dark:text-slate-300">{note}</div>
          </li>
        ))}
      </ul>
    </div>
  );
}
