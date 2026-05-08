import type { Metadata } from "next";
import { Comic_Neue } from "next/font/google";
import "./globals.css";
import { Providers } from "./providers";
import { SiteHeader } from "@/components/layout/SiteHeader";
import { SiteFooter } from "@/components/layout/SiteFooter";

const comic = Comic_Neue({
  subsets: ["latin"],
  weight: ["300", "400", "700"],
  variable: "--font-comic",
});

export const metadata: Metadata = {
  title: "Dogecoin Explorer — Quantum Explorer",
  description:
    "Dogecoin block explorer with post-quantum carrier and reveal detection — commitments, OP_RETURN markers, and on-chain PQ traffic.",
  icons: {
    icon: "/icon.svg",
    shortcut: "/icon.svg",
  },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className={`${comic.variable} font-comic`}>
        <Providers>
          <SiteHeader />
          <main className="mx-auto max-w-7xl px-4 py-10 lg:px-8">{children}</main>
          <SiteFooter />
        </Providers>
      </body>
    </html>
  );
}
