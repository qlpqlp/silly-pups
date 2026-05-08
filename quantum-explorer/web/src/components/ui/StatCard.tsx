import { Skeleton } from "./Skeleton";

export function StatCard({
  label,
  value,
  hint,
  loading,
}: {
  label: string;
  value: React.ReactNode;
  hint?: string;
  loading?: boolean;
}) {
  return (
    <div className="glass-card p-5 transition hover:-translate-y-0.5 hover:shadow-xl">
      <div className="text-xs font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">{label}</div>
      <div className="mt-2 font-comic text-2xl font-bold text-doge-ink dark:text-amber-50">
        {loading ? <Skeleton className="h-8 w-32" /> : value}
      </div>
      {hint && <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">{hint}</p>}
    </div>
  );
}
