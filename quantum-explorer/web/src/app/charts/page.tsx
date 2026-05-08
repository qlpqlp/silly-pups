"use client";

import Link from "next/link";
import { PQHourlyChart } from "@/components/charts/PQHourlyChart";
import { MinerShareChart } from "@/components/charts/MinerShareChart";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";

export default function ChartsPage() {
  const pq = useQuery({
    queryKey: ["pq-analytics-charts"],
    queryFn: () => fetchJSON<Record<string, unknown>>("/api/public/pq/analytics/?hours=240"),
  });
  const mn = useQuery({
    queryKey: ["mining-charts"],
    queryFn: () => fetchJSON<Record<string, unknown>>("/api/public/mining/stats/?lookback_blocks=2000&top=12"),
  });

  const hourly = (pq.data?.hourly as Record<string, unknown>[]) || [];
  const miners = (mn.data?.top_miners as { miner_short: string; blocks_mined: number }[]) || [];

  return (
    <div className="space-y-8">
      <h1 className="font-comic text-4xl font-bold text-doge-ink dark:text-amber-50">Charts</h1>
      <p className="text-slate-600 dark:text-slate-300">
        Visual layers mirror what your indexer already stores — open{" "}
        <Link href="/mining/" className="font-semibold text-amber-700 hover:underline dark:text-amber-300">
          Mining analytics
        </Link>{" "}
        or{" "}
        <Link href="/post-quantum/" className="font-semibold text-amber-700 hover:underline dark:text-amber-300">
          PQ observatory
        </Link>{" "}
        for narrative context.
      </p>
      <PQHourlyChart data={hourly as { hour_unix: number; quantum_count: number; pq_adoption_percent: number }[]} />
      <MinerShareChart rows={miners} />
    </div>
  );
}
