"use client";

import { Suspense } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { Skeleton } from "@/components/ui/Skeleton";
import { Badge } from "@/components/ui/Badge";
import { Tip } from "@/components/ui/Tooltip";
import { satsToDoge, shortenHash } from "@/lib/format";

export default function BlockPage() {
  return (
    <Suspense fallback={<Skeleton className="h-96 w-full" />}>
      <BlockBody />
    </Suspense>
  );
}

function BlockBody() {
  const sp = useSearchParams();
  const height = sp.get("height");
  const hash = sp.get("hash");
  const qs = height ? `height=${encodeURIComponent(height)}` : hash ? `hash=${encodeURIComponent(hash)}` : "";
  const active = qs !== "";

  const q = useQuery({
    queryKey: ["block", qs],
    enabled: active,
    queryFn: () => fetchJSON<Record<string, unknown>>(`/api/public/block/?${qs}&decode_limit=80`),
  });

  if (!active) {
    return <p className="text-slate-600 dark:text-slate-300">Provide <code className="rounded bg-black/5 px-2 py-1 dark:bg-white/10">height</code> or <code className="rounded bg-black/5 px-2 py-1 dark:bg-white/10">hash</code> query parameter.</p>;
  }

  if (q.isLoading) return <Skeleton className="h-96 w-full" />;

  const payload = q.data;
  if (!payload || (payload as { error?: string }).error) {
    return <p className="text-red-600">{String((payload as { error?: string })?.error || "Block not found")}</p>;
  }

  const blk = (payload as { block?: Record<string, unknown> }).block || {};
  const txs = ((payload as { associated_transactions?: unknown[] }).associated_transactions || []) as Record<string, unknown>[];

  return (
    <div className="space-y-8">
      <div className="glass-card space-y-4 p-8">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="font-comic text-3xl font-bold text-doge-ink dark:text-amber-50">Block #{String(blk.height)}</h1>
          <Badge variant="coinbase" title="First transaction pays miners">
            Coinbase flow available
          </Badge>
        </div>
        <div className="grid gap-4 md:grid-cols-2">
          <Field label="Hash" value={<span className="break-all font-mono text-sm">{String(blk.hash)}</span>} />
          <Field label="Timestamp" value={String(blk.timestamp || blk.time_unix || "—")} />
          <Field label="Transactions" value={String((payload as { tx_count?: number }).tx_count ?? txs.length)} />
          <Field label="Size (bytes)" value={String(blk.block_size ?? "—")} />
          <Field label="Difficulty" value={blk.difficulty != null ? Number(blk.difficulty).toExponential(4) : "—"} />
          <Field
            label="Miner (coinbase payee)"
            value={
              <Tip label="First decoded address paying out of the coinbase transaction — useful for mining decentralization views.">
                <span className="font-mono text-sm">{String(blk.miner_address || blk.miner_address_short || "—")}</span>
              </Tip>
            }
          />
          <Field label="Block reward (outputs)" value={`${satsToDoge(Number(blk.coinbase_value_sats || 0))} DOGE`} />
        </div>
      </div>

      <section className="glass-card overflow-hidden">
        <div className="border-b border-white/40 px-6 py-4 dark:border-white/10">
          <h2 className="text-xl font-semibold">Transactions</h2>
        </div>
        <div className="overflow-x-auto px-4 py-3">
          <table className="table-modern min-w-full">
            <thead>
              <tr>
                <th className="py-3">Txid</th>
                <th>PQ</th>
                <th>Value out</th>
              </tr>
            </thead>
            <tbody>
              {txs.map((t, idx) => (
                <tr key={idx}>
                  <td className="font-mono text-xs">
                    <Link className="text-amber-700 hover:underline dark:text-amber-300" href={`/tx/?txid=${t.txid}`}>
                      {shortenHash(String(t.txid), 16, 14)}
                    </Link>
                  </td>
                  <td>{String(t.quantum_state || "")}</td>
                  <td>{satsToDoge(Number(t.value_out_sats))}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

function Field({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div>
      <div className="text-xs font-semibold uppercase tracking-wide text-slate-500">{label}</div>
      <div className="mt-1 text-base text-doge-ink dark:text-slate-100">{value}</div>
    </div>
  );
}
