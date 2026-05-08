"use client";

import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { MinerShareChart } from "@/components/charts/MinerShareChart";
import { Skeleton } from "@/components/ui/Skeleton";
import Link from "next/link";

export default function MiningPage() {
  const q = useQuery({
    queryKey: ["mining-stats"],
    queryFn: () => fetchJSON<Record<string, unknown>>("/api/public/mining/stats/?lookback_blocks=4320&top=40"),
  });

  if (q.isLoading) return <Skeleton className="h-[520px] w-full" />;
  if (q.error) return <p className="text-red-600">{(q.error as Error).message}</p>;

  const top = (q.data?.top_miners as Record<string, unknown>[]) || [];
  const recent = (q.data?.recent_blocks as Record<string, unknown>[]) || [];

  return (
    <div className="space-y-10">
      <header className="glass-card space-y-3 p-8">
        <h1 className="font-comic text-4xl font-bold text-doge-ink dark:text-amber-50">Mining analytics</h1>
        <p className="max-w-3xl text-slate-600 dark:text-slate-300">
          We summarize coinbase payees decoded during indexing — a pragmatic lens into reward payout clustering (not a perfect miner identity oracle).
        </p>
        <p className="text-xs text-slate-500">{String(q.data?.note || "")}</p>
      </header>

      <div className="grid gap-8 lg:grid-cols-2">
        <MinerShareChart rows={top as { miner_short: string; blocks_mined: number }[]} />
        <div className="glass-card overflow-hidden">
          <div className="border-b border-white/40 px-4 py-3 text-lg font-semibold dark:border-white/10">Top miners</div>
          <div className="max-h-[340px] overflow-auto px-2 py-2">
            <table className="table-modern min-w-full">
              <thead>
                <tr>
                  <th className="py-2">Miner</th>
                  <th>Blocks</th>
                  <th>Rewards (DOGE)</th>
                </tr>
              </thead>
              <tbody>
                {top.map((row, i) => (
                  <tr key={i}>
                    <td className="font-mono text-xs">
                      <Link href={`/address/?a=${encodeURIComponent(String(row.miner_address))}`}>
                        {String(row.miner_short || row.miner_address)}
                      </Link>
                    </td>
                    <td>{String(row.blocks_mined)}</td>
                    <td>{Number(row.rewards_doge || 0).toFixed(4)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <section className="glass-card overflow-hidden">
        <div className="border-b border-white/40 px-4 py-3 font-semibold dark:border-white/10">Recent blocks (snapshot)</div>
        <div className="overflow-x-auto px-3 py-2">
          <table className="table-modern min-w-full">
            <thead>
              <tr>
                <th>Height</th>
                <th>Miner</th>
                <th>Reward</th>
              </tr>
            </thead>
            <tbody>
              {recent.map((b) => (
                <tr key={String(b.hash)}>
                  <td className="font-mono">
                    <Link href={`/block/?height=${b.height}`}>{String(b.height)}</Link>
                  </td>
                  <td className="font-mono text-xs">{String(b.miner_address_short || b.miner_address)}</td>
                  <td>{Number(Number(b.coinbase_value_sats || 0) / 1e8).toFixed(4)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
