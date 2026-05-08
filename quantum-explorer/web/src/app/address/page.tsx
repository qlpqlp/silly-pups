"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "@/lib/api";
import { Skeleton } from "@/components/ui/Skeleton";
import { AddressMiniGraph } from "@/components/graph/AddressMiniGraph";
import dynamic from "next/dynamic";

const Tabs = dynamic(() => import("@/components/address/AddressTabs"), { ssr: false });

export default function AddressPage() {
  return (
    <Suspense fallback={<Skeleton className="h-64 w-full" />}>
      <AddressBody />
    </Suspense>
  );
}

function AddressBody() {
  const sp = useSearchParams();
  const addr = sp.get("a") || "";
  if (!addr) {
    return <p className="text-slate-600 dark:text-slate-300">Pass <code className="rounded bg-black/5 px-2 py-1 dark:bg-white/10">?a=ADDRESS</code>.</p>;
  }

  const q = useQuery({
    queryKey: ["address-search", addr],
    queryFn: () => fetchJSON<Record<string, unknown>>(`/api/public/core/search/?q=${encodeURIComponent(addr)}&limit=120`),
  });

  if (q.isLoading) return <Skeleton className="h-64 w-full" />;

  const rows = (q.data?.results as { txid?: string; address?: string }[]) || [];
  const peers = Array.from(new Set(rows.map((r) => r.address).filter(Boolean))) as string[];

  return (
    <div className="space-y-8">
      <div className="glass-card space-y-3 p-8">
        <h1 className="font-comic text-3xl font-bold text-doge-ink dark:text-amber-50">Address</h1>
        <p className="break-all font-mono text-lg">{addr}</p>
        <p className="text-sm text-slate-600 dark:text-slate-400">{rows.length} matching movements (indexed)</p>
      </div>

      <Tabs addr={addr} rows={rows} graph={<AddressMiniGraph center={addr} peers={peers.filter((p) => p !== addr)} />} />
    </div>
  );
}
