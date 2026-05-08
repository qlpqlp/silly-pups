"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { StatCard } from "@/components/ui/StatCard";
import { Skeleton } from "@/components/ui/Skeleton";
import { shortenHash, satsToDoge, timeAgo } from "@/lib/format";
import { Badge } from "@/components/ui/Badge";

export default function HomePage() {
  const net = useQuery({
    queryKey: ["network-overview"],
    queryFn: () => fetchJSON<Record<string, unknown>>("/api/public/network-overview"),
  });
  const blocks = useQuery({
    queryKey: ["recent-blocks"],
    queryFn: () => fetchJSON<{ rows: Record<string, unknown>[] }>("/api/public/core/recent-blocks/?limit=18"),
  });
  const txs = useQuery({
    queryKey: ["recent-txs"],
    queryFn: () =>
      fetchJSON<{ rows: Record<string, unknown>[] }>("/api/public/core/recent-txs/?limit=22&lite=1"),
  });

  const chain = net.data?.chain as Record<string, unknown> | undefined;
  const mempool = net.data?.mempool as Record<string, unknown> | undefined;
  const market = net.data?.market as Record<string, unknown> | undefined;
  const hashps = net.data?.network_hashps_120 as number | undefined;

  return (
    <div className="space-y-12">
      <section className="glass-card relative overflow-hidden p-10">
        <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top,_rgba(242,201,76,0.35),transparent_55%)] dark:bg-[radial-gradient(circle_at_top,_rgba(242,201,76,0.12),transparent_50%)]" />
        <div className="relative space-y-4">
          <h1 className="font-comic text-4xl font-bold tracking-tight text-doge-ink dark:text-amber-50 md:text-5xl">
            The friendly blockchain, professionally readable.
          </h1>
          <p className="max-w-3xl text-lg text-slate-600 dark:text-slate-300">
            Live mempool and chain snapshots from your Dogecoin Core node, fused with a PostgreSQL indexer for instant search,
            miner analytics, and post-quantum carrier insights.
          </p>
        </div>
      </section>

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="DOGE price (USD)"
          loading={net.isLoading}
          value={market?.price_usd != null ? `$${Number(market.price_usd).toFixed(6)}` : "—"}
          hint="CoinGecko simple price (server-side)"
        />
        <StatCard
          label="Market cap"
          loading={net.isLoading}
          value={market?.market_cap_usd != null ? `$${(Number(market.market_cap_usd) / 1e9).toFixed(2)}B` : "—"}
        />
        <StatCard
          label="Block height"
          loading={net.isLoading}
          value={chain?.blocks != null ? String(chain.blocks) : "—"}
          hint={chain?.verificationprogress != null ? `Verification ${((Number(chain.verificationprogress) || 0) * 100).toFixed(2)}%` : undefined}
        />
        <StatCard label="Network hashrate (120 blk est.)" loading={net.isLoading} value={hashps != null ? hashps.toExponential(2) : "—"} />
        <StatCard label="Difficulty" loading={net.isLoading} value={chain?.difficulty != null ? Number(chain.difficulty).toExponential(2) : "—"} />
        <StatCard
          label="Mempool (vbytes)"
          loading={net.isLoading}
          value={mempool?.usage != null ? String(mempool.usage) : mempool?.size != null ? `${mempool.size} txs` : "—"}
        />
        <StatCard
          label="Indexed PQ txs"
          loading={false}
          value={<PrefetchPQ />}
          hint="Opens analytics tab"
        />
        <StatCard label="Chain" loading={net.isLoading} value={String(chain?.chain || "—")} hint={String(chain?.chain || "")} />
      </section>

      <section className="grid gap-8 lg:grid-cols-2">
        <div className="glass-card overflow-hidden">
          <div className="flex items-center justify-between border-b border-white/40 px-6 py-4 dark:border-white/10">
            <h2 className="text-xl font-bold text-doge-ink dark:text-amber-50">Latest blocks</h2>
            <Link href="/blocks/" className="text-sm font-semibold text-amber-700 hover:underline dark:text-amber-300">
              View all
            </Link>
          </div>
          <div className="overflow-x-auto px-4 py-2">
            <table className="table-modern min-w-full">
              <thead>
                <tr>
                  <th className="py-3">Height</th>
                  <th>Time</th>
                  <th>Txs</th>
                  <th>Miner</th>
                  <th>Reward</th>
                  <th>Size</th>
                </tr>
              </thead>
              <tbody>
                {blocks.isLoading &&
                  Array.from({ length: 8 }).map((_, i) => (
                    <tr key={i}>
                      <td colSpan={6}>
                        <Skeleton className="my-2 h-10 w-full" />
                      </td>
                    </tr>
                  ))}
                {!blocks.isLoading &&
                  (blocks.data?.rows || []).map((b) => (
                    <tr key={String(b.hash)}>
                      <td className="font-mono">
                        <Link className="text-amber-700 hover:underline dark:text-amber-300" href={`/block/?height=${b.height}`}>
                          {String(b.height)}
                        </Link>
                      </td>
                      <td className="text-slate-600 dark:text-slate-300">{timeAgo(Number(b.time_unix))}</td>
                      <td>{String(b.tx_count)}</td>
                      <td className="font-mono text-xs">
                        {(b.miner_address_short as string) || shortenHash(String(b.miner_address || ""), 8, 6)}
                      </td>
                      <td>{satsToDoge(Number(b.coinbase_value_sats || 0))}</td>
                      <td>{String(b.block_size || "—")}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className="glass-card overflow-hidden">
          <div className="flex items-center justify-between border-b border-white/40 px-6 py-4 dark:border-white/10">
            <h2 className="text-xl font-bold text-doge-ink dark:text-amber-50">Latest transactions</h2>
            <Link href="/transactions/" className="text-sm font-semibold text-amber-700 hover:underline dark:text-amber-300">
              Stream
            </Link>
          </div>
          <div className="overflow-x-auto px-4 py-2">
            <table className="table-modern min-w-full">
              <thead>
                <tr>
                  <th className="py-3">Tx</th>
                  <th>Time</th>
                  <th>Out</th>
                  <th>PQ</th>
                </tr>
              </thead>
              <tbody>
                {txs.isLoading &&
                  Array.from({ length: 8 }).map((_, i) => (
                    <tr key={i}>
                      <td colSpan={4}>
                        <Skeleton className="my-2 h-10 w-full" />
                      </td>
                    </tr>
                  ))}
                {!txs.isLoading &&
                  (txs.data?.rows || []).map((t) => {
                    const role = String(t.pq_carrier_role || "");
                    return (
                      <tr key={String(t.txid)}>
                        <td className="font-mono text-xs">
                          <Link href={`/tx/?txid=${t.txid}`} className="text-amber-700 hover:underline dark:text-amber-300">
                            {shortenHash(String(t.txid), 12, 10)}
                          </Link>
                        </td>
                        <td className="text-slate-600 dark:text-slate-300">{timeAgo(Number(t.time_unix))}</td>
                        <td>{satsToDoge(Number(t.value_out_sats))}</td>
                        <td>
                          {role === "tx_c" && (
                            <Badge variant="pq_carrier" title="Phase-1 PQ carrier / commitment flow">
                              Carrier
                            </Badge>
                          )}
                          {role === "tx_r" && (
                            <Badge variant="pq_reveal" title="PQ reveal spends carrier material">
                              Reveal
                            </Badge>
                          )}
                          {!role && String(t.quantum_state) === "quantum" && <Badge variant="pq_carrier">Quantum</Badge>}
                        </td>
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </div>
        </div>
      </section>
    </div>
  );
}

function PrefetchPQ() {
  const pq = useQuery({
    queryKey: ["pq-quick"],
    queryFn: () => fetchJSON<{ aggregates: Record<string, number> }>("/api/public/pq/analytics/?hours=48&pairs_limit=5&addr_limit=5"),
  });
  if (pq.isLoading) return <Skeleton className="h-8 w-24" />;
  const n = pq.data?.aggregates?.quantum;
  return (
    <Link href="/post-quantum/" className="hover:underline">
      {n != null ? n.toLocaleString() : "—"}
    </Link>
  );
}
