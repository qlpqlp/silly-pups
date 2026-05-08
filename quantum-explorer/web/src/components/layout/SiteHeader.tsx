"use client";

import Link from "next/link";
import { useTheme } from "next-themes";
import { Menu, Moon, Sparkles, Sun } from "lucide-react";
import { useState } from "react";
import { GlobalSearch } from "@/components/search/GlobalSearch";
import { QuantumGlyph } from "@/components/ui/QuantumGlyph";
import { BrandLogo } from "@/components/layout/BrandLogo";
import clsx from "clsx";

const nav = [
  { href: "/blocks/", label: "Blocks" },
  { href: "/transactions/", label: "Transactions" },
  { href: "/addresses/", label: "Addresses" },
  { href: "/mining/", label: "Mining analytics", star: true },
  { href: "/post-quantum/", label: "Post-quantum", star: true },
  { href: "/charts/", label: "Charts" },
  { href: "/api-docs/", label: "API" },
];

export function SiteHeader() {
  const { theme, setTheme } = useTheme();
  const [open, setOpen] = useState(false);

  return (
    <header className="sticky top-0 z-40 border-b border-white/30 bg-white/70 backdrop-blur-xl dark:border-white/10 dark:bg-slate-950/70">
      <div className="mx-auto flex max-w-7xl flex-col gap-4 px-4 py-4 lg:flex-row lg:items-center lg:gap-6 lg:px-8">
        <div className="flex items-center justify-between gap-4">
          <Link href="/" className="flex items-center gap-3 font-comic">
            <BrandLogo />
            <QuantumGlyph className="hidden h-9 w-9 sm:block" />
            <div>
              <div className="flex flex-wrap items-center gap-2 text-lg font-bold leading-tight tracking-tight text-doge-ink dark:text-amber-100">
                Dogecoin Explorer
                <span className="inline-flex items-center rounded-full bg-violet-100 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-violet-900 dark:bg-violet-900/50 dark:text-violet-100">
                  PQ
                </span>
              </div>
              <div className="text-xs font-medium text-slate-500 dark:text-slate-400">Post-quantum aware · Such Quantum spirit</div>
            </div>
          </Link>
          <div className="flex items-center gap-2 lg:hidden">
            <button
              type="button"
              aria-label="Toggle theme"
              className="rounded-xl border border-slate-200 bg-white p-2 dark:border-white/10 dark:bg-white/5"
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            >
              {theme === "dark" ? <Sun className="h-5 w-5" /> : <Moon className="h-5 w-5" />}
            </button>
            <button
              type="button"
              className="rounded-xl border border-slate-200 bg-white p-2 dark:border-white/10 dark:bg-white/5"
              onClick={() => setOpen((v) => !v)}
              aria-label="Menu"
            >
              <Menu className="h-5 w-5" />
            </button>
          </div>
        </div>

        <div className="flex flex-1 flex-col gap-4 lg:flex-row lg:items-center">
          <GlobalSearch className="w-full flex-1" />
          <nav
            className={clsx(
              "flex flex-wrap gap-2 text-sm font-medium lg:flex-nowrap",
              open ? "flex" : "hidden lg:flex",
            )}
          >
            {nav.map((item) => (
              <Link
                key={item.href}
                href={item.href}
                className="flex items-center gap-1 rounded-xl px-3 py-2 text-slate-700 transition hover:bg-amber-100/60 hover:text-doge-ink dark:text-slate-200 dark:hover:bg-white/10 dark:hover:text-white"
              >
                {item.star && <Sparkles className="h-3.5 w-3.5 text-amber-500" />}
                {item.label}
              </Link>
            ))}
          </nav>
          <button
            type="button"
            className="hidden rounded-xl border border-slate-200 bg-white p-2 lg:block dark:border-white/10 dark:bg-white/5"
            aria-label="Toggle theme"
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          >
            {theme === "dark" ? <Sun className="h-5 w-5" /> : <Moon className="h-5 w-5" />}
          </button>
        </div>
      </div>
    </header>
  );
}
