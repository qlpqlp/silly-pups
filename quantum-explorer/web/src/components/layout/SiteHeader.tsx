"use client";

import Link from "next/link";
import { useTheme } from "next-themes";
import { Menu, Moon, Sun } from "lucide-react";
import { useState } from "react";
import { GlobalSearch } from "@/components/search/GlobalSearch";
import { BrandLogo } from "@/components/layout/BrandLogo";
import clsx from "clsx";

const nav = [
  { href: "/blocks/", label: "Blocks" },
  { href: "/transactions/", label: "Transactions" },
  { href: "/post-quantum/", label: "PQ (TX_C / TX_R)" },
  { href: "/addresses/", label: "Addresses" },
  { href: "/mining/", label: "Mining" },
  { href: "/charts/", label: "Charts" },
  { href: "/api-docs/", label: "API" },
];

export function SiteHeader() {
  const { theme, setTheme } = useTheme();
  const [open, setOpen] = useState(false);

  return (
    <header className="sticky top-0 z-40 border-b border-[#dad6cf] bg-[#f4f2ee]/95 backdrop-blur-md dark:border-[#1e2630] dark:bg-[#0b0f14]/90">
      <div className="mx-auto flex max-w-6xl flex-col gap-4 px-4 py-4 lg:flex-row lg:items-center lg:gap-6 lg:px-8">
        <div className="flex items-center justify-between gap-4">
          <Link href="/" className="flex items-center gap-3">
            <BrandLogo />
            <div>
              <div className="flex flex-wrap items-center gap-2 text-base font-semibold leading-tight tracking-tight text-[#1a1814] dark:text-[#f4f0e6]">
                Quantum Explorer
                <span className="inline-flex items-center rounded-md border border-[#c4a035]/50 bg-[#f2e6c4] px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-[#5c4810] dark:border-[#8a7020]/60 dark:bg-[#2a2310] dark:text-[#e8d48a]">
                  PQ
                </span>
              </div>
              <div className="text-xs text-[#5c574f] dark:text-[#9a9a8e]">Dogecoin · commitments &amp; reveals</div>
            </div>
          </Link>
          <div className="flex items-center gap-2 lg:hidden">
            <button
              type="button"
              aria-label="Toggle theme"
              className="rounded-lg border border-[#dad6cf] bg-white p-2 dark:border-[#1e2630] dark:bg-[#111820]"
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            >
              {theme === "dark" ? <Sun className="h-5 w-5" /> : <Moon className="h-5 w-5" />}
            </button>
            <button
              type="button"
              className="rounded-lg border border-[#dad6cf] bg-white p-2 dark:border-[#1e2630] dark:bg-[#111820]"
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
              "flex flex-wrap gap-1 text-sm font-medium lg:flex-nowrap",
              open ? "flex" : "hidden lg:flex",
            )}
          >
            {nav.map((item) => (
              <Link
                key={item.href}
                href={item.href}
                className="rounded-lg px-3 py-2 text-[#3d3a34] transition hover:bg-[#e8e2d8] dark:text-[#d4d0c4] dark:hover:bg-[#151c26]"
              >
                {item.label}
              </Link>
            ))}
          </nav>
          <button
            type="button"
            className="hidden rounded-lg border border-[#dad6cf] bg-white p-2 lg:block dark:border-[#1e2630] dark:bg-[#111820]"
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
