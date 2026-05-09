import Link from "next/link";

export function SiteFooter() {
  return (
    <footer className="mt-16 border-t border-[#dad6cf] bg-[#ebe7e0] py-10 dark:border-[#1e2630] dark:bg-[#0b0f14]">
      <div className="mx-auto flex max-w-6xl flex-col gap-6 px-4 text-center lg:px-8">
        <a
          href="https://foundation.dogecoin.com/"
          target="_blank"
          rel="noopener noreferrer"
          className="text-sm font-semibold text-[#5c4810] underline-offset-4 hover:underline dark:text-[#d4b85c]"
        >
          Dogecoin Foundation
        </a>
        <p className="text-sm font-medium text-[#3d3a34] dark:text-[#d4d0c4]">Built for the Dogecoin community.</p>
        <p className="mx-auto max-w-2xl text-sm text-[#5c574f] dark:text-[#9a9a8e]">
          Post-quantum sends use a <strong>carrier</strong> (TX_C) and a <strong>reveal</strong> (TX_R). This explorer classifies both from indexed chain data. Learn more at{" "}
          <a
            href="https://suchquantum.com/"
            target="_blank"
            rel="noopener noreferrer"
            className="font-semibold text-[#8a7020] underline-offset-2 hover:underline dark:text-[#e8c96a]"
          >
            Such Quantum
          </a>
          .
        </p>
        <div className="flex flex-wrap justify-center gap-4 text-sm">
          <Link href="/post-quantum/" className="text-[#8a7020] hover:underline dark:text-[#e8c96a]">
            PQ metrics
          </Link>
          <Link href="/api-docs/" className="text-[#8a7020] hover:underline dark:text-[#e8c96a]">
            API
          </Link>
        </div>
      </div>
    </footer>
  );
}
