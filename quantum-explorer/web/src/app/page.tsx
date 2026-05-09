"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { StatCard } from "@/components/ui/StatCard";
import { Skeleton } from "@/components/ui/Skeleton";
import { shortenHash, satsToDoge, timeAgo } from "@/lib/format";
import { Badge } from "@/components/ui/Badge";
import { RotatingPQCaption } from "@/components/home/RotatingPQCaption";
import { Blocks, FileJson2, FlaskConical } from "lucide-react";

type PQAnalytics = {
  aggregates: Record<string, number>;
  pq_adoption_percent: number;
  pq_carrier_role_counts: Record<string, number>;
};

const quickLinks = [
  { href: "/blocks/", label: "Blocks", hint: "Heights, hashes, miners", icon: Blocks },
  { href: "/transactions/", label: "Transactions", hint: "Latest mempool & chain", icon: FileJson2 },
  { href: "/post-quantum/", label: "PQ metrics", hint: "TX_C vs TX_R on-chain", icon: FlaskConical },
];

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
    <div className="space-y-10">
      <section className="glass-card p-8 md:p-10">
        <div className="flex flex-col gap-8 md:flex-row md:items-start md:gap-10">
          <div className="flex shrink-0 justify-center md:justify-start">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              src="/logo.png"
              alt=""
              className="h-20 w-20 rounded-2xl object-cover shadow-md ring-1 ring-black/10 dark:ring-white/10 md:h-24 md:w-24"
            />
          </div>
          <div className="min-w-0 flex-1 space-y-4">
            <p className="text-xs font-semibold uppercase tracking-[0.18em] text-[#6b655c] dark:text-[#8a8580]">
              Dogecoin explorer
            </p>
            <h1 className="text-3xl font-semibold tracking-tight text-[#1a1814] dark:text-[#f4f0e6] md:text-4xl">
              Blocks, transactions, and post-quantum flow
            </h1>
            <RotatingPQCaption />
            <div className="grid gap-3 sm:grid-cols-3">
              {quickLinks.map(({ href, label, hint, icon: Icon }) => (
                <Link
                  key={href}
                  href={href}
                  className="flex items-start gap-3 rounded-xl border border-[#dad6cf] bg-white/80 p-4 transition hover:border-[#c4a035]/60 hover:bg-[#faf8f4] dark:border-[#1e2630] dark:bg-[#0f141c] dark:hover:border-[#3d3420]/80 dark:hover:bg-[#151c26]"
                >
                  <Icon className="mt-0.5 h-5 w-5 shrink-0 text-[#8a7020] dark:text-[#d4b85c]" aria-hidden />
                  <div>
                    <div className="font-semibold text-[#1a1814] dark:text-[#f4f0e6]">{label}</div>
                    <div className="text-xs text-[#5c574f] dark:text-[#9a9a8e]">{hint}</div>
                  </div>
                </Link>
              ))}
            </div>
            <div className="flex flex-wrap gap-2 pt-1 text-sm">
              <a
                href="https://github.com/edtubbs/libdogecoin/blob/0.1.5-dev-pqc-carrier/doc/spec/bip-post-quantum-signature-commitments.mediawiki"
                target="_blank"
                rel="noopener noreferrer"
                className="rounded-lg border border-[#dad6cf] bg-white px-3 py-1.5 font-medium text-[#3d3a34] hover:bg-[#f4f2ee] dark:border-[#1e2630] dark:bg-[#111820] dark:text-[#d4d0c4] dark:hover:bg-[#151c26]"
              >
                Draft BIP — PQ commitments
              </a>
              <a
                href="https://suchquantum.com/"
                target="_blank"
                rel="noopener noreferrer"
                className="rounded-lg border border-[#c4a035]/50 bg-[#f8f0d8] px-3 py-1.5 font-medium text-[#5c4810] hover:bg-[#f2e6c4] dark:border-[#6b5a28] dark:bg-[#2a2310] dark:text-[#f0e0a8] dark:hover:bg-[#3a3020]"
              >
                Such Quantum
              </a>
            </div>
          </div>
        </div>
      </section>

      <section className="glass-card space-y-5 p-8">
        <h2 className="text-lg font-semibold text-[#1a1814] dark:text-[#f4f0e6]">How PQ shows up on-chain</h2>
        <div className="grid gap-6 md:grid-cols-2">
          <div className="space-y-3 text-sm leading-relaxed text-[#3d3a34] dark:text-[#c8c4b8]">
            <p>
              A typical post-quantum send uses two steps: a <strong>carrier</strong> (TX_C) that publishes a commitment and locks funds, then a{" "}
              <strong>reveal</strong> (TX_R) that spends the carrier and exposes lattice signature material validators expect.
            </p>
            <p className="text-[#5c574f] dark:text-[#9a9a8e]">
              Tags like <span className="font-mono text-[#8a7020] dark:text-[#e8c96a]">FLC1</span>,{" "}
              <span className="font-mono text-[#8a7020] dark:text-[#e8c96a]">DIL2</span>,{" "}
              <span className="font-mono text-[#8a7020] dark:text-[#e8c96a]">RCG4</span> in OP_RETURN data identify Falcon, Dilithium, or Raccoon-shaped flows. This site classifies what is already in your indexed blocks.
            </p>
          </div>
          <ul className="space-y-3 rounded-xl border border-[#dad6cf] bg-[#faf8f4] p-5 text-sm text-[#3d3a34] dark:border-[#1e2630] dark:bg-[#0f141c] dark:text-[#d4d0c4]">
            <li>
              <strong className="text-[#5c4810] dark:text-[#e8c96a]">TX_C — commitment</strong> — tagged OP_RETURN + locked carrier output; usually the first broadcast.
            </li>
            <li>
              <strong className="text-emerald-800 dark:text-emerald-300">TX_R — reveal</strong> — spends the carrier path; look for the Reveal badge in lists.
            </li>
            <li>
              <strong className="text-[#1a1814] dark:text-[#f4f0e6]">Audit</strong> — metrics come from decoded raw transactions in Postgres, not a third-party indexer.
            </li>
          </ul>
        </div>
      </section>

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <StatCard
          label="Post-quantum transactions (indexed)"
          loading={pq.isLoading}
          value={agg?.quantum != null ? agg.quantum.toLocaleString() : "—"}
          hint="Rows classified as quantum from stored raw bytes."
        />
        <StatCard
          label="PQ carrier (TX_C)"
          loading={pq.isLoading}
          value={roles.tx_c != null ? roles.tx_c.toLocaleString() : "—"}
          hint="Phase-1 commitment pattern in raw hex."
        />
        <StatCard
          label="PQ reveal (TX_R)"
          loading={pq.isLoading}
          value={roles.tx_r != null ? roles.tx_r.toLocaleString() : "—"}
          hint="Reveal scriptSig / carrier linkage pattern."
        />
        <StatCard
          label="Invalid / noisy PQ markers"
          loading={pq.isLoading}
          value={agg?.invalid_quantum != null ? agg.invalid_quantum.toLocaleString() : "—"}
          hint="PQ-adjacent OP_RETURN that failed strict checks."
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
          hint="From your Dogecoin Core node."
        />
      </section>

      <section className="grid gap-8 lg:grid-cols-2">
        <div className="glass-card overflow-hidden">
          <div className="flex items-center justify-between border-b border-[#dad6cf] px-5 py-4 dark:border-[#1e2630]">
            <h2 className="text-base font-semibold text-[#1a1814] dark:text-[#f4f0e6]">Latest blocks</h2>
            <Link href="/blocks/" className="text-sm font-medium text-[#8a7020] hover:underline dark:text-[#e8c96a]">
              View all
            </Link>
          </div>
          <div className="overflow-x-auto px-3 py-2">
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
                        <Link className="text-[#8a7020] hover:underline dark:text-[#e8c96a]" href={`/block/?height=${b.height}`}>
                          {String(b.height)}
                        </Link>
                      </td>
                      <td className="text-[#5c574f] dark:text-[#9a9a8e]">{timeAgo(Number(b.time_unix))}</td>
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
          <div className="flex items-center justify-between border-b border-[#dad6cf] px-5 py-4 dark:border-[#1e2630]">
            <h2 className="text-base font-semibold text-[#1a1814] dark:text-[#f4f0e6]">Latest transactions</h2>
            <Link href="/transactions/" className="text-sm font-medium text-[#8a7020] hover:underline dark:text-[#e8c96a]">
              Stream
            </Link>
          </div>
          <div className="overflow-x-auto px-3 py-2">
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
                          <Link href={`/tx/?txid=${t.txid}`} className="text-[#8a7020] hover:underline dark:text-[#e8c96a]">
                            {shortenHash(String(t.txid), 12, 10)}
                          </Link>
                        </td>
                        <td className="text-[#5c574f] dark:text-[#9a9a8e]">{timeAgo(Number(t.time_unix))}</td>
                        <td>{satsToDoge(Number(t.value_out_sats))}</td>
                        <td>
                          {role === "tx_c" && (
                            <Badge variant="pq_carrier" title="Phase-1 PQ carrier / commitment">
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
