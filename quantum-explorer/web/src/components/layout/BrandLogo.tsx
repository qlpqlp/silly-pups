"use client";

export function BrandLogo() {
  return (
    <div className="brand-logo shrink-0" aria-hidden>
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img src="/logo.png" alt="" className="h-11 w-11 rounded-xl object-cover shadow-sm ring-1 ring-black/5 dark:ring-white/10" />
    </div>
  );
}
