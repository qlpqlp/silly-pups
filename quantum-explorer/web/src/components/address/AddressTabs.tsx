"use client";

import Link from "next/link";
import { useState } from "react";
import { shortenHash } from "@/lib/format";

export default function AddressTabs({
  addr,
  rows,
  graph,
}: {
  addr: string;
  rows: { txid?: string }[];
  graph: React.ReactNode;
}) {
  const [tab, setTab] = useState<"txs" | "flow" | "pq">("txs");
  return (
    <div className="glass-card overflow-hidden">
      <div className="flex flex-wrap gap-2 border-b border-white/40 px-4 py-3 dark:border-white/10">
        {(
          [
            ["txs", "Transactions"],
            ["flow", "Flow"],
            ["pq", "PQ activity"],
          ] as const
        ).map(([k, label]) => (
          <button
            key={k}
            type="button"
            onClick={() => setTab(k)}
            className={`rounded-xl px-4 py-2 text-sm font-semibold transition ${
              tab === k
                ? "bg-amber-400 text-doge-ink shadow"
                : "bg-white/40 text-slate-700 hover:bg-amber-100/60 dark:bg-white/5 dark:text-slate-200"
            }`}
          >
            {label}
          </button>
        ))}
      </div>
      <div className="p-4">
        {tab === "txs" && (
          <ul className="space-y-2">
            {rows.map((r, i) => (
              <li key={i}>
                <Link href={`/tx/?txid=${r.txid}`} className="font-mono text-sm text-amber-700 hover:underline dark:text-amber-300">
                  {shortenHash(String(r.txid), 18, 14)}
                </Link>
              </li>
            ))}
          </ul>
        )}
        {tab === "flow" && <div className="space-y-4">{graph}</div>}
        {tab === "pq" && (
          <p className="text-sm text-slate-600 dark:text-slate-300">
            PQ involvement per address will summarize quantum_state joins — see <Link href="/post-quantum/">Post-quantum analytics</Link> for network-wide stats.
          </p>
        )}
      </div>
    </div>
  );
}
