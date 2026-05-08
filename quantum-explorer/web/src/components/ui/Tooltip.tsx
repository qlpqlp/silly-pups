"use client";

export function Tip({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <span className="group relative inline-flex cursor-help items-center gap-1">
      {children}
      <span className="pointer-events-none absolute bottom-full left-1/2 z-20 mb-2 hidden w-64 -translate-x-1/2 rounded-xl border border-slate-200 bg-white p-3 text-xs font-normal text-slate-700 shadow-lg group-hover:block dark:border-white/10 dark:bg-slate-900 dark:text-slate-200">
        {label}
      </span>
    </span>
  );
}
