"use client";

/** Crossfades between the pup artwork and the widely recognised Dogecoin mark (Wikimedia Commons). */
const DOGE_COMMONS = "https://upload.wikimedia.org/wikipedia/en/d/d0/Dogecoin_Logo.png";

export function BrandLogo() {
  return (
    <div className="brand-logo-crossfade shrink-0" aria-hidden>
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img src="/logo.png" alt="" className="brand-logo-a" />
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img src={DOGE_COMMONS} alt="" className="brand-logo-b" />
    </div>
  );
}
