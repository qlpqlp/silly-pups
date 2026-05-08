"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { StatCard } from "@/components/ui/StatCard";
import { Skeleton } from "@/components/ui/Skeleton";
import { shortenHash, satsToDoge, timeAgo } from "@/lib/format";
import { Badge } from "@/components/ui/Badge";
import { QuantumGlyph } from "@/components/ui/QuantumGlyph";
import { RotatingPQCaption } from "@/components/home/RotatingPQCaption";

type PQAnalytics = {
  aggregates: Record<string, number>;
  pq_adoption_percent: number;
  pq_carrier_role_counts: Record<string, number>;
};

export default function HomePage() {
  const pq = useQuery({
    queryKey: ["pq-home"],
    queryFn: () => fetchJSON<PQAnalytics>("/api/public/pq/analytics/?hours=72&pairs_limit=40&addr_limit=24"),
  });
  const net = useQuery({
    queryKey: ["net-tip"],
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
  const agg = pq.data?.aggregates;
  const roles = pq.data?.pq_carrier_role_counts || {};

  return (
    <div className="space-y-12">
      <section className="glass-card relative overflow-hidden p-8 md:p-12">
        <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(ellipse_at_top,_rgba(139,92,246,0.2),transparent_55%),radial-gradient(ellipse_at_bottom,_rgba(242,201,76,0.25),transparent_50%)] dark:bg-[radial-gradient(ellipse_at_top,_rgba(139,92,246,0.15),transparent_50%),radial-gradient(ellipse_at_bottom,_rgba(242,201,76,0.08),transparent_45%)]" />
        <div className="relative flex flex-col gap-6 md:flex-row md:items-start md:gap-10">
          <QuantumGlyph className="h-16 w-16 shrink-0 drop-shadow-lg md:h-24 md:w-24" />
          <div className="space-y-5">
            <p className="text-sm font-semibold uppercase tracking-[0.2em] text-violet-700 dark:text-violet-300">Quantum layer</p>
            <h1 className="font-comic text-4xl font-bold tracking-tight text-doge-ink dark:text-amber-50 md:text-5xl lg:text-[2.75rem] lg:leading-tight">
              Dogecoin meets quantum security
            </h1>
            <RotatingPQCaption />
            <div className="flex flex-wrap gap-3 pt-2">
              <a
                href="https://github.com/edtubbs/libdogecoin/blob/0.1.5-dev-pqc-carrier/doc/spec/bip-post-quantum-signature-commitments.mediawiki"
                target="_blank"
                rel="noopener noreferrer"
                className="rounded-xl border border-violet-300/80 bg-white/70 px-4 py-2 text-sm font-semibold text-violet-900 shadow-sm transition hover:bg-violet-50 dark:border-violet-700 dark:bg-violet-950/40 dark:text-violet-100 dark:hover:bg-violet-900/60"
              >
                Draft BIP — PQ commitments
              </a>
              <a
                href="https://suchquantum.com/"
                target="_blank"
                rel="noopener noreferrer"
                className="rounded-xl border border-amber-300/80 bg-amber-50/90 px-4 py-2 text-sm font-semibold text-amber-950 shadow-sm transition hover:bg-amber-100 dark:border-amber-700 dark:bg-amber-950/50 dark:text-amber-50 dark:hover:bg-amber-900/40"
              >
                Such Quantum — verifier &amp; playground
              </a>
              <Link
                href="/post-quantum/"
                className="rounded-xl border border-slate-300 bg-white/80 px-4 py-2 text-sm font-semibold text-doge-ink shadow-sm transition hover:bg-white dark:border-white/20 dark:bg-white/10 dark:text-white dark:hover:bg-white/20"
              >
                Open PQ analytics
              </Link>
            </div>
          </div>
        </div>
      </section>

      <section className="glass-card space-y-5 p-8">
        <h2 className="font-comic text-2xl font-bold text-doge-ink dark:text-amber-50">Quantum transactions on Dogecoin</h2>
        <div className="grid gap-6 md:grid-cols-2">
          <div className="space-y-3 text-slate-700 dark:text-slate-300">
            <p>
              Post-quantum sends split the story across <strong>two on-chain steps</strong>: a{" "}
              <strong>carrier</strong> transaction (TX_C) that publishes a compact commitment and locks funds, and a{" "}
              <strong>reveal</strong> transaction (TX_R) that spends that carrier path and discloses the lattice signature
              material validators expect — the same conceptual flow highlighted on{" "}
              <a href="https://suchquantum.com/" className="font-semibold text-amber-700 underline-offset-2 hover:underline dark:text-amber-300">
                Such Quantum
              </a>
              .
            </p>
            <p className="text-sm leading-relaxed text-slate-600 dark:text-slate-400">
              OP_RETURN lines advertise scheme tags (<span className="font-mono text-violet-700 dark:text-violet-300">FLC1</span>,{" "}
              <span className="font-mono text-violet-700 dark:text-violet-300">DIL2</span>,{" "}
              <span className="font-mono text-violet-700 dark:text-violet-300">RCG4</span>) so wallets and indexers can spot Falcon, Dilithium, or Raccoon-shaped
              payloads. Production wallets bind the real signature to the sighash; this explorer classifies what already landed in blocks from raw hex.
            </p>
          </div>
          <ul className="space-y-3 rounded-2xl border border-violet-200/80 bg-violet-50/50 p-5 text-sm text-slate-800 dark:border-violet-900/50 dark:bg-violet-950/30 dark:text-slate-200">
            <li>
              <strong className="text-violet-900 dark:text-violet-200">Carrier (TX_C)</strong> — commitment + locked carrier output; often the first hop you broadcast.
            </li>
            <li>
              <strong className="text-emerald-900 dark:text-emerald-200">Reveal (TX_R)</strong> — spends the carrier script path and pairs back to the commitment; watch for the green “Reveal” badge in tx lists.
            </li>
            <li>
              <strong className="text-doge-ink dark:text-amber-100">Why it matters</strong> — you can audit PQ adoption on Dogecoin without trusting a single API: everything here is decoded from your own indexed chain data.
            </li>
          </ul>
        </div>
      </section>

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <StatCard
          label="Post-quantum transactions (indexed)"
          loading={pq.isLoading}
          value={agg?.quantum != null ? agg.quantum.toLocaleString() : "—"}
          hint="Rows classified as quantum from stored raw transaction bytes."
        />
        <StatCard
          label="PQ carrier (TX_C)"
          loading={pq.isLoading}
          value={roles.tx_c != null ? roles.tx_c.toLocaleString() : "—"}
          hint="Strict Phase-1 commitment pattern detected on-chain."
        />
        <StatCard
          label="PQ reveal (TX_R)"
          loading={pq.isLoading}
          value={roles.tx_r != null ? roles.tx_r.toLocaleString() : "—"}
          hint="Carrier reveal scriptSig pattern detected on-chain."
        />
        <StatCard
          label="Invalid / noisy PQ markers"
          loading={pq.isLoading}
          value={agg?.invalid_quantum != null ? agg.invalid_quantum.toLocaleString() : "—"}
          hint="OP_RETURN looked PQ-adjacent but failed strict checks."
        />
        <StatCard
          label="PQ share of indexed txs"
          loading={pq.isLoading}
          value={pq.data?.pq_adoption_percent != null ? `${pq.data.pq_adoption_percent.toFixed(4)}%` : "—"}
          hint="Quantum rows ÷ all indexed transactions."
        />
        <StatCard
          label="Chain tip (blocks)"
          loading={net.isLoading}
          value={chain?.blocks != null ? String(chain.blocks) : "—"}
          hint="From your Dogecoin Core node (network overview)."
        />
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
