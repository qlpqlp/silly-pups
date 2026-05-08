"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { Skeleton } from "@/components/ui/Skeleton";
import { shortenHash } from "@/lib/format";

export default function SearchPage() {
  return (
    <Suspense fallback={<Skeleton className="h-40 w-full" />}>
      <SearchBody />
    </Suspense>
  );
}

function SearchBody() {
  const sp = useSearchParams();
  const q = sp.get("q") || "";
  const query = useQuery({
    queryKey: ["core-search", q],
    enabled: q.length > 2,
    queryFn: () => fetchJSON<Record<string, unknown>>(`/api/public/core/search/?q=${encodeURIComponent(q)}&limit=40`),
  });

  if (!q) {
    return <p className="text-slate-600 dark:text-slate-300">Enter a query from the header search.</p>;
  }

  return (
    <div className="space-y-6">
      <h1 className="font-comic text-3xl font-bold text-doge-ink dark:text-amber-50">Search results</h1>
      <p className="text-slate-600 dark:text-slate-400">Query: {q}</p>
      {query.isLoading && <Skeleton className="h-40 w-full" />}
      {query.data && <Results payload={query.data} />}
      {query.error && <p className="text-red-600">{String((query.error as Error).message)}</p>}
    </div>
  );
}

function Results({ payload }: { payload: Record<string, unknown> }) {
  const kind = String(payload.kind || "");
  const results = payload.results as unknown[] | undefined;
  if (!results) return <pre className="overflow-auto rounded-xl bg-black/5 p-4 text-xs dark:bg-white/5">{JSON.stringify(payload, null, 2)}</pre>;

  return (
    <div className="glass-card divide-y divide-white/40 dark:divide-white/10">
      {results.map((row, i) => {
        if (kind === "address") {
          const m = row as { txid?: string; address?: string };
          return (
            <div key={i} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <Link href={`/tx/?txid=${m.txid}`} className="font-mono text-sm text-amber-700 hover:underline dark:text-amber-300">
                {shortenHash(String(m.txid), 14, 12)}
              </Link>
              <span className="text-xs text-slate-500">{m.address}</span>
            </div>
          );
        }
        return (
          <div key={i} className="px-4 py-3 font-mono text-sm">
            {JSON.stringify(row)}
          </div>
        );
      })}
    </div>
  );
}
