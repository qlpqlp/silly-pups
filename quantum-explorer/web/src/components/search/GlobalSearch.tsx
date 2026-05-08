"use client";

import { useRouter } from "next/navigation";
import { useCallback, useState } from "react";
import { Search } from "lucide-react";
import clsx from "clsx";
import { classifySearchInput } from "@/lib/search-detect";

export function GlobalSearch({ className }: { className?: string }) {
  const router = useRouter();
  const [q, setQ] = useState("");

  const onSubmit = useCallback(
    (e: React.FormEvent) => {
      e.preventDefault();
      const raw = q.trim();
      if (!raw) return;
      const hit = classifySearchInput(raw);
      if (!hit) return;
      if (hit.kind === "block_height") router.push(`/block/?height=${encodeURIComponent(hit.value)}`);
      else if (hit.kind === "tx") router.push(`/tx/?txid=${encodeURIComponent(hit.value)}`);
      else router.push(`/search/?q=${encodeURIComponent(hit.value)}`);
    },
    [q, router],
  );

  return (
    <form onSubmit={onSubmit} className={clsx("relative", className)}>
      <Search className="pointer-events-none absolute left-4 top-1/2 h-5 w-5 -translate-y-1/2 text-slate-400" />
      <input
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder="Search blocks, transactions, addresses…"
        className="w-full rounded-2xl border border-slate-200 bg-white/90 py-3 pl-12 pr-4 text-base shadow-inner outline-none ring-amber-400/30 placeholder:text-slate-400 focus:border-amber-400 focus:ring-4 dark:border-white/10 dark:bg-slate-900/80 dark:text-white"
      />
    </form>
  );
}
