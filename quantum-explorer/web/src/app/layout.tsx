import type { Metadata } from "next";
import { Comic_Neue } from "next/font/google";
import "./globals.css";
import { Providers } from "./providers";
import { SiteHeader } from "@/components/layout/SiteHeader";

const comic = Comic_Neue({
  subsets: ["latin"],
  weight: ["300", "400", "700"],
  variable: "--font-comic",
});

export const metadata: Metadata = {
  title: "Dogecoin Explorer — Quantum Explorer",
  description: "Modern Dogecoin blockchain explorer powered by Core RPC and PostgreSQL indexing.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className={`${comic.variable} font-comic`}>
        <Providers>
          <SiteHeader />
          <main className="mx-auto max-w-7xl px-4 py-10 lg:px-8">{children}</main>
          <footer className="border-t border-white/30 bg-white/40 py-10 text-center text-sm text-slate-600 backdrop-blur dark:border-white/10 dark:bg-slate-950/60 dark:text-slate-400">
            Built for Dogecoin · PQ carrier detection from indexed raw transactions · Data from your node
          </footer>
        </Providers>
      </body>
    </html>
  );
}
