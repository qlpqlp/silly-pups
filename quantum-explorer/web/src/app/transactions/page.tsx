"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { Skeleton } from "@/components/ui/Skeleton";
import { Badge } from "@/components/ui/Badge";
import { shortenHash, satsToDoge, timeAgo } from "@/lib/format";

export default function TransactionsPage() {
  const q = useQuery({
    queryKey: ["tx-stream"],
    queryFn: () => fetchJSON<{ rows: Record<string, unknown>[] }>("/api/public/core/recent-txs/?limit=120&lite=1"),
  });

  if (q.isLoading) return <Skeleton className="h-96 w-full" />;

  return (
    <div className="space-y-6">
      <h1 className="font-comic text-4xl font-bold text-doge-ink dark:text-amber-50">Transactions</h1>
      <div className="glass-card overflow-x-auto">
        <table className="table-modern min-w-full">
          <thead>
            <tr>
              <th>Txid</th>
              <th>Age</th>
              <th>Value out</th>
              <th>PQ</th>
            </tr>
          </thead>
          <tbody>
            {(q.data?.rows || []).map((t) => (
              <tr key={String(t.txid)}>
                <td className="font-mono text-xs">
                  <Link href={`/tx/?txid=${t.txid}`} className="text-amber-700 hover:underline dark:text-amber-300">
                    {shortenHash(String(t.txid), 18, 14)}
                  </Link>
                </td>
                <td>{timeAgo(Number(t.time_unix))}</td>
                <td>{satsToDoge(Number(t.value_out_sats))}</td>
                <td>
                  {String(t.pq_carrier_role) === "tx_c" && <Badge variant="pq_carrier">Carrier</Badge>}
                  {String(t.pq_carrier_role) === "tx_r" && <Badge variant="pq_reveal">Reveal</Badge>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
