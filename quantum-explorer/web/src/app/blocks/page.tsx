"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { Skeleton } from "@/components/ui/Skeleton";
import { shortenHash, satsToDoge, timeAgo } from "@/lib/format";

export default function BlocksPage() {
  const q = useQuery({
    queryKey: ["blocks-all"],
    queryFn: () => fetchJSON<{ rows: Record<string, unknown>[] }>("/api/public/core/recent-blocks/?limit=80"),
  });

  if (q.isLoading) return <Skeleton className="h-96 w-full" />;

  return (
    <div className="space-y-6">
      <h1 className="font-comic text-4xl font-bold text-doge-ink dark:text-amber-50">Blocks</h1>
      <div className="glass-card overflow-x-auto">
        <table className="table-modern min-w-full">
          <thead>
            <tr>
              <th>Height</th>
              <th>Hash</th>
              <th>Age</th>
              <th>Txs</th>
              <th>Miner</th>
              <th>Reward</th>
            </tr>
          </thead>
          <tbody>
            {(q.data?.rows || []).map((b) => (
              <tr key={String(b.hash)}>
                <td className="font-mono">
                  <Link className="text-amber-700 hover:underline dark:text-amber-300" href={`/block/?height=${b.height}`}>
                    {String(b.height)}
                  </Link>
                </td>
                <td className="font-mono text-xs">{shortenHash(String(b.hash), 16, 14)}</td>
                <td>{timeAgo(Number(b.time_unix))}</td>
                <td>{String(b.tx_count)}</td>
                <td className="font-mono text-xs">{String(b.miner_address_short || shortenHash(String(b.miner_address || ""), 8, 6))}</td>
                <td>{satsToDoge(Number(b.coinbase_value_sats || 0))}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
