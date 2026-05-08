"use client";

import { useId } from "react";

/** Inline quantum / lattice mark for header & hero (matches explorer PQ theme). */
export function QuantumGlyph({ className = "h-8 w-8" }: { className?: string }) {
  const gid = useId().replace(/:/g, "");
  const grad = `url(#qg-${gid})`;
  return (
    <svg className={className} viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg" aria-hidden>
      <defs>
        <linearGradient id={`qg-${gid}`} x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stopColor="#c4b5fd" />
          <stop offset="100%" stopColor="#f2c94c" />
        </linearGradient>
      </defs>
      <circle cx="32" cy="32" r="28" fill={grad} opacity="0.25" />
      <ellipse cx="32" cy="32" rx="16" ry="26" stroke={grad} strokeWidth="2.5" fill="none" transform="rotate(-28 32 32)" />
      <ellipse cx="32" cy="32" rx="16" ry="26" stroke={grad} strokeWidth="2.5" fill="none" transform="rotate(28 32 32)" />
      <circle cx="32" cy="32" r="5" fill="#7c3aed" />
    </svg>
  );
}
