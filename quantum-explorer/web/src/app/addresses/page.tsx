"use client";

import type { ReactNode } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { satsToDoge } from "@/lib/format";
import { Skeleton } from "@/components/ui/Skeleton";

type AddressLeadersResponse = {
  limit?: number;
  note?: string;
  richest_by_received?: Record<string, unknown>[];
  quantum_addresses?: Record<string, unknown>[];
};

export default function AddressesPage() {
  const q = useQuery({
    queryKey: ["core-address-leaders", 100],
    queryFn: () => fetchJSON<AddressLeadersResponse>("/api/public/core/address-leaders/?limit=100"),
  });

  const rich = q.data?.richest_by_received ?? [];
  const quantum = q.data?.quantum_addresses ?? [];

  return (
    <div className="space-y-8">
      <div className="glass-card space-y-4 p-8 md:p-10">
        <h1 className="font-comic text-4xl font-bold text-doge-ink dark:text-amber-50">Addresses</h1>
        <p className="text-lg text-slate-600 dark:text-slate-300">
          Leaderboards from the indexed chain. Open any row for detail on{" "}
          <code className="rounded bg-black/5 px-2 py-1 dark:bg-white/10">/address/?a=…</code> or use{" "}
          <Link href="/search/?q=D" className="font-semibold text-amber-700 hover:underline dark:text-amber-300">
            search
          </Link>
          .
        </p>
        {q.data?.note ? <p className="text-xs text-slate-500 dark:text-slate-400">{q.data.note}</p> : null}
      </div>

      {q.isLoading ? (
        <div className="grid gap-6 lg:grid-cols-2">
          <Skeleton className="h-[520px] w-full" />
          <Skeleton className="h-[520px] w-full" />
        </div>
      ) : q.error ? (
        <p className="text-red-600 dark:text-red-400">{(q.error as Error).message}</p>
      ) : (
        <div className="grid gap-8 lg:grid-cols-2">
          <LeaderTable
            title="Top 100 by indexed amount received"
            subtitle="Sum of output credits seen in the indexer (not wallet balance after spends)."
            rows={rich}
            columns={[
              {
                key: "addr",
                header: "Address",
                cell: (row) => (
                  <Link className="font-mono text-xs" href={`/address/?a=${encodeURIComponent(String(row.address))}`}>
                    {String(row.address_short)}
                  </Link>
                ),
              },
              {
                key: "doge",
                header: "Amount (DOGE)",
                className: "text-right tabular-nums",
                cell: (row) => satsToDoge(Number(row.total_received_sats)),
              },
              {
                key: "outs",
                header: "Outputs",
                className: "text-right tabular-nums text-slate-500",
                cell: (row) => String(row.indexed_output_rows ?? "—"),
              },
            ]}
          />
          <LeaderTable
            title="Top 100 quantum Dogecoin addresses"
            subtitle="Addresses appearing in transactions classified as quantum, ranked by distinct quantum tx count."
            rows={quantum}
            columns={[
              {
                key: "addr",
                header: "Address",
                cell: (row) => (
                  <Link className="font-mono text-xs" href={`/address/?a=${encodeURIComponent(String(row.address))}`}>
                    {String(row.address_short)}
                  </Link>
                ),
              },
              {
                key: "pqtx",
                header: "PQ txs",
                className: "text-right tabular-nums",
                cell: (row) => String(row.pq_tx_count ?? "—"),
              },
              {
                key: "pqd",
                header: "PQ outputs (DOGE)",
                className: "text-right tabular-nums",
                cell: (row) => satsToDoge(Number(row.pq_value_sats ?? 0)),
              },
              {
                key: "first",
                header: "First seen",
                className: "text-xs text-slate-500",
                cell: (row) => String(row.first_seen_iso8601 ?? "—"),
              },
            ]}
          />
        </div>
      )}
    </div>
  );
}

function LeaderTable({
  title,
  subtitle,
  rows,
  columns,
}: {
  title: string;
  subtitle: string;
  rows: Record<string, unknown>[];
  columns: { key: string; header: string; className?: string; cell: (row: Record<string, unknown>) => ReactNode }[];
}) {
  return (
    <div className="glass-card overflow-hidden">
      <div className="border-b border-white/40 px-4 py-3 dark:border-white/10">
        <div className="font-semibold text-doge-ink dark:text-amber-50">{title}</div>
        <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">{subtitle}</p>
      </div>
      <div className="max-h-[min(70vh,560px)] overflow-auto">
        <table className="table-modern min-w-full">
          <thead>
            <tr>
              <th className="w-10 text-right text-slate-500">#</th>
              {columns.map((c) => (
                <th key={c.key} className={c.className}>
                  {c.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, i) => (
              <tr key={i}>
                <td className="text-right text-xs text-slate-400">{i + 1}</td>
                {columns.map((c) => (
                  <td key={c.key} className={c.className}>
                    {c.cell(row)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
