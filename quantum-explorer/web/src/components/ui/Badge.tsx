import clsx from "clsx";

const variants: Record<string, string> = {
  coinbase: "bg-sky-100 text-sky-900 dark:bg-sky-900/40 dark:text-sky-100",
  miner: "bg-amber-100 text-amber-950 dark:bg-amber-900/40 dark:text-amber-50",
  pq_carrier: "bg-[#e8e2d8] text-[#3d3020] ring-1 ring-[#c4b8a8] dark:bg-[#1a222c] dark:text-[#e8dcc8] dark:ring-[#2a3440]",
  pq_reveal: "bg-emerald-200 text-emerald-950 dark:bg-emerald-900/40 dark:text-emerald-50",
  exchange: "bg-fuchsia-100 text-fuchsia-950 dark:bg-fuchsia-900/40 dark:text-fuchsia-50",
  neutral: "bg-slate-100 text-slate-800 dark:bg-white/10 dark:text-slate-100",
};

export function Badge({
  children,
  variant = "neutral",
  title,
}: {
  children: React.ReactNode;
  variant?: keyof typeof variants;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={clsx(
        "inline-flex items-center rounded-full px-2.5 py-0.5 text-[11px] font-semibold tracking-wide",
        variants[variant] || variants.neutral,
      )}
    >
      {children}
    </span>
  );
}
