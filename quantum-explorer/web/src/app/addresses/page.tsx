import Link from "next/link";

export default function AddressesPage() {
  return (
    <div className="glass-card space-y-4 p-10">
      <h1 className="font-comic text-4xl font-bold text-doge-ink dark:text-amber-50">Addresses</h1>
      <p className="text-lg text-slate-600 dark:text-slate-300">
        Use the omnibar to jump straight to <code className="rounded bg-black/5 px-2 py-1 dark:bg-white/10">/address/?a=…</code> or start from{" "}
        <Link href="/search/?q=D" className="font-semibold text-amber-700 hover:underline dark:text-amber-300">
          search
        </Link>
        .
      </p>
    </div>
  );
}
