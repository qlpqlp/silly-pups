import type { Metadata } from "next";
import { Inter } from "next/font/google";
import "./globals.css";
import { Providers } from "./providers";
import { SiteHeader } from "@/components/layout/SiteHeader";
import { SiteFooter } from "@/components/layout/SiteFooter";

const inter = Inter({
  subsets: ["latin"],
  variable: "--font-sans",
});

export const metadata: Metadata = {
  title: "Quantum Explorer — Dogecoin",
  description:
    "Blocks, transactions, and post-quantum carrier (TX_C) and reveal (TX_R) detection on Dogecoin.",
  icons: {
    icon: "/logo.png",
    shortcut: "/logo.png",
    apple: "/logo.png",
  },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className={`${inter.variable} font-sans`}>
        <Providers>
          <SiteHeader />
          <main className="mx-auto max-w-6xl px-4 py-10 lg:px-8">{children}</main>
          <SiteFooter />
        </Providers>
      </body>
    </html>
  );
}
