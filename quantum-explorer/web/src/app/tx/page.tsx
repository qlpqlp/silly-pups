"use client";

import { Suspense } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { Skeleton } from "@/components/ui/Skeleton";
import { Badge } from "@/components/ui/Badge";
import { Tip } from "@/components/ui/Tooltip";
import { satsToDoge } from "@/lib/format";

export default function TxPage() {
  return (
    <Suspense fallback={<Skeleton className="h-96 w-full" />}>
      <TxBody />
    </Suspense>
  );
}

function TxBody() {
  const sp = useSearchParams();
  const txid = (sp.get("txid") || "").toLowerCase();
  const active = txid.length === 64;

  const q = useQuery({
    queryKey: ["tx", txid],
    enabled: active,
    queryFn: () => fetchJSON<Record<string, unknown>>(`/api/public/tx/?txid=${encodeURIComponent(txid)}`),
  });

  if (!active) return <p className="text-slate-600 dark:text-slate-300">Provide a 64-character txid.</p>;
  if (q.isLoading) return <Skeleton className="h-96 w-full" />;
  if (q.error) return <p className="text-red-600">{(q.error as Error).message}</p>;

  const pq = q.data?.pq_verification as Record<string, unknown> | undefined;
  const decode = pq?.decode as Record<string, unknown> | undefined;
  const inputs = (decode?.inputs as Record<string, unknown>[] | undefined) || [];
  const outputs = (decode?.outputs as Record<string, unknown>[] | undefined) || [];
  const carrier = pq?.carrier_phase1 as Record<string, unknown> | undefined;
  const reverse = pq?.carrier_reverse_phase1 as Record<string, unknown> | undefined;
  const matchedTxc = String(carrier?.matched_txc_txid || "").trim();
  const matchedTxr = String(reverse?.matched_txr_txid || "").trim();

  const core = q.data?.core as Record<string, unknown> | undefined;
  const apiRole = String(q.data?.pq_carrier_role || "").trim();

  return (
    <div className="space-y-8">
      <div className="glass-card space-y-6 p-8">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="break-all text-2xl font-semibold text-[#1a1814] dark:text-[#f4f0e6] md:text-3xl">{txid}</h1>
          {apiRole === "tx_c" && (
            <Badge variant="pq_carrier" title="This transaction carries OP_RETURN PQ commitments (phase 1).">
              Post-quantum carrier
            </Badge>
          )}
          {apiRole === "tx_r" && (
            <Badge variant="pq_reveal" title="This transaction reveals PQ material locked behind a carrier spend.">
              Post-quantum reveal
            </Badge>
          )}
        </div>

        <section className="rounded-2xl border border-[#dad6cf] bg-[#faf8f4] p-5 dark:border-[#1e2630] dark:bg-[#0f141c]">
          <h2 className="flex items-center gap-2 text-lg font-semibold text-[#1a1814] dark:text-[#f4f0e6]">
            <Tip label="Dogecoin PQ signatures use a carrier commitment (TX_C) and an explicit reveal (TX_R). Linkage is inferred from script markers and Falcon verification when raw hex is present.">
              <span>PQC detection</span>
            </Tip>
          </h2>
          <div className="mt-4 grid gap-3 md:grid-cols-2">
            <div className="rounded-xl bg-white/70 p-4 text-sm dark:bg-black/30">
              <div className="text-xs font-semibold uppercase text-slate-500">Linked carrier tx</div>
              <div className="mt-1 font-mono">
                {matchedTxc ? (
                  <Link href={`/tx/?txid=${matchedTxc}`} className="text-[#8a7020] hover:underline dark:text-[#e8c96a]">
                    {matchedTxc}
                  </Link>
                ) : (
                  "—"
                )}
              </div>
            </div>
            <div className="rounded-xl bg-white/70 p-4 text-sm dark:bg-black/30">
              <div className="text-xs font-semibold uppercase text-slate-500">Linked reveal tx</div>
              <div className="mt-1 font-mono">
                {matchedTxr ? (
                  <Link href={`/tx/?txid=${matchedTxr}`} className="text-[#8a7020] hover:underline dark:text-[#e8c96a]">
                    {matchedTxr}
                  </Link>
                ) : (
                  "—"
                )}
              </div>
            </div>
          </div>
          <p className="mt-3 text-xs text-slate-600 dark:text-slate-400">
            This panel uses the Quantum Explorer strict verifier over decoded prevouts when available.
          </p>
        </section>

        <div className="grid gap-4 md:grid-cols-3">
          <Field label="Confirmations" value={core ? "Confirmed (indexed)" : "—"} />
          <Field label="Block height" value={String(core?.block_height ?? "—")} />
          <Field label="Value out" value={`${satsToDoge(Number(core?.value_out_sats || 0))} DOGE`} />
        </div>
      </div>

      <div className="grid gap-8 lg:grid-cols-2">
        <IOCard title="Inputs" rows={inputs} />
        <IOCard title="Outputs" rows={outputs} />
      </div>
    </div>
  );
}

function Field({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="rounded-xl border border-white/50 bg-white/40 p-4 dark:border-white/10 dark:bg-white/5">
      <div className="text-xs font-semibold uppercase tracking-wide text-slate-500">{label}</div>
      <div className="mt-1 font-semibold">{value}</div>
    </div>
  );
}

function IOCard({ title, rows }: { title: string; rows: Record<string, unknown>[] }) {
  return (
    <div className="glass-card p-5">
      <h3 className="mb-4 text-lg font-bold">{title}</h3>
      <div className="space-y-3">
        {rows.length === 0 && <p className="text-sm text-slate-500">No decoded rows (fetch raw hex via indexer).</p>}
        {rows.map((row, i) => (
          <div key={i} className="rounded-xl border border-slate-200/70 bg-white/50 p-3 text-sm dark:border-white/10 dark:bg-white/5">
            <pre className="max-h-48 overflow-auto whitespace-pre-wrap font-mono text-xs">{JSON.stringify(row, null, 2)}</pre>
          </div>
        ))}
      </div>
    </div>
  );
}
