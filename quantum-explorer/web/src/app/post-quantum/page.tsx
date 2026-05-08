"use client";

import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { PQHourlyChart } from "@/components/charts/PQHourlyChart";
import { Skeleton } from "@/components/ui/Skeleton";
import Link from "next/link";
import { shortenHash } from "@/lib/format";
import { Badge } from "@/components/ui/Badge";

export default function PostQuantumPage() {
  const q = useQuery({
    queryKey: ["pq-analytics"],
    queryFn: () => fetchJSON<Record<string, unknown>>("/api/public/pq/analytics/?hours=168&pairs_limit=80&addr_limit=60"),
  });

  if (q.isLoading) return <Skeleton className="h-[640px] w-full" />;
  if (q.error) return <p className="text-red-600">{(q.error as Error).message}</p>;

  const agg = (q.data?.aggregates as Record<string, number>) || {};
  const hourly = (q.data?.hourly as Record<string, unknown>[]) || [];
  const leaderboard = (q.data?.pq_address_leaderboard as Record<string, unknown>[]) || [];
  const pairs = (q.data?.carrier_reveal_activity as Record<string, unknown>[]) || [];

  return (
    <div className="space-y-10">
      <header className="glass-card space-y-3 p-8">
        <h1 className="font-comic text-4xl font-bold text-doge-ink dark:text-amber-50">Post-quantum observatory</h1>
        <p className="max-w-3xl text-slate-600 dark:text-slate-300">
          Carrier commitments and reveals are classified straight from stored raw transactions — paired Falcon flows surface automatically when the indexer has complete hex.
        </p>
        <p className="text-xs text-slate-500">{String(q.data?.protocol_note || "")}</p>
      </header>

      <div className="grid gap-4 md:grid-cols-4">
        <Stat label="PQ txs" value={agg.quantum ?? 0} />
        <Stat label="All indexed txs" value={agg.all ?? 0} />
        <Stat label="Invalid PQ markers" value={agg.invalid_quantum ?? 0} />
        <Stat label="Adoption %" value={`${Number(q.data?.pq_adoption_percent || 0).toFixed(4)}%`} />
      </div>

      <PQHourlyChart data={hourly as { hour_unix: number; quantum_count: number; pq_adoption_percent: number }[]} />

      <div className="grid gap-8 lg:grid-cols-2">
        <div className="glass-card overflow-hidden">
          <div className="border-b border-white/40 px-4 py-3 font-semibold dark:border-white/10">PQ address leaderboard</div>
          <div className="max-h-[420px] overflow-auto">
            <table className="table-modern min-w-full">
              <thead>
                <tr>
                  <th>Address</th>
                  <th>PQ touches</th>
                  <th>First seen</th>
                </tr>
              </thead>
              <tbody>
                {leaderboard.map((row, i) => (
                  <tr key={i}>
                    <td className="font-mono text-xs">
                      <Link href={`/address/?a=${encodeURIComponent(String(row.address))}`}>{String(row.address_short)}</Link>
                    </td>
                    <td>{String(row.pq_tx_count)}</td>
                    <td className="text-xs text-slate-500">{String(row.first_seen_iso8601)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className="glass-card overflow-hidden">
          <div className="border-b border-white/40 px-4 py-3 font-semibold dark:border-white/10">Carrier / reveal activity</div>
          <div className="max-h-[420px] overflow-auto">
            <table className="table-modern min-w-full">
              <thead>
                <tr>
                  <th>Tx</th>
                  <th>Role</th>
                </tr>
              </thead>
              <tbody>
                {pairs.map((row, i) => (
                  <tr key={i}>
                    <td className="font-mono text-xs">
                      <Link href={`/tx/?txid=${row.txid}`}>{shortenHash(String(row.txid), 14, 12)}</Link>
                    </td>
                    <td>
                      {String(row.pq_carrier_role) === "tx_c" ? (
                        <Badge variant="pq_carrier">Carrier</Badge>
                      ) : (
                        <Badge variant="pq_reveal">Reveal</Badge>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="glass-card p-5">
      <div className="text-xs font-semibold uppercase text-slate-500">{label}</div>
      <div className="mt-2 font-comic text-3xl font-bold text-doge-ink dark:text-amber-50">{value}</div>
    </div>
  );
}
