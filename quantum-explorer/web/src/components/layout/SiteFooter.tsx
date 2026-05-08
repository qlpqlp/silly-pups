import Link from "next/link";

export function SiteFooter() {
  return (
    <footer className="border-t border-white/30 bg-gradient-to-b from-white/50 to-amber-50/40 py-12 text-center backdrop-blur dark:border-white/10 dark:from-slate-950/80 dark:to-slate-900/90">
      <div className="mx-auto flex max-w-3xl flex-col items-center gap-6 px-4">
        <a
          href="https://foundation.dogecoin.com/"
          target="_blank"
          rel="noopener noreferrer"
          className="block transition hover:opacity-90"
        >
          {/* Local SVG wordmark — swap for official artwork if you prefer */}
          <img src="/foundation-wordmark.svg" alt="Dogecoin Foundation" className="mx-auto h-10 w-auto max-w-[260px]" />
        </a>
        <p className="font-comic text-base font-semibold text-doge-ink dark:text-amber-100">Coded with love to all Dogecoin community</p>
        <p className="text-sm text-slate-600 dark:text-slate-400">
          Learn more about post-quantum commitments and the in-browser digest playground at{" "}
          <a
            href="https://suchquantum.com/"
            target="_blank"
            rel="noopener noreferrer"
            className="font-semibold text-amber-700 underline-offset-2 hover:underline dark:text-amber-300"
          >
            Such Quantum
          </a>
          . This DogeBox explorer focuses on on-chain detection and pairing of carrier and reveal transactions.
        </p>
        <div className="flex flex-wrap justify-center gap-4 text-sm">
          <Link href="/post-quantum/" className="text-amber-800 hover:underline dark:text-amber-200">
            Post-quantum analytics
          </Link>
          <Link href="/api-docs/" className="text-amber-800 hover:underline dark:text-amber-200">
            API
          </Link>
        </div>
      </div>
    </footer>
  );
}
