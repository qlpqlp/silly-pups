"use client";

import { useEffect, useState } from "react";

const LINES = [
  "Phase 1 (TX_C) publishes a commitment in OP_RETURN — tags like FLC1, DIL2, and RCG4 mark the same 32-byte digest shape across Falcon, Dilithium, and Raccoon-style carriers.",
  "The carrier transaction locks DOGE in a P2SH path so the heavy post-quantum signature material can follow safely in a second step.",
  "Phase 2 (TX_R) spends that carrier output and reveals the lattice proof material, linking back to the commitment your wallet already broadcast.",
  "Validators re-hash public key bytes and signature bytes to check commitment32 = SHA256(pk || sig), then bind that bundle to the transaction sighash — not just pretty OP_RETURN text.",
  "This explorer reads raw Dogecoin transactions from your Core node, classifies carrier vs reveal flows, and surfaces them beside ordinary sends and receives.",
  "Much like the playground at Such Quantum, the goal is clarity: see quantum-secured traffic on-chain, understand the two-step dance, and dig into each tx.",
];

export function RotatingPQCaption() {
  const [i, setI] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setI((n) => (n + 1) % LINES.length), 9000);
    return () => clearInterval(t);
  }, []);
  return (
    <p
      key={i}
      className="max-w-3xl animate-[qeFadeIn_0.7s_ease-out] text-base leading-relaxed text-[#3d3a34] dark:text-[#c8c4b8] md:text-lg"
    >
      {LINES[i]}
    </p>
  );
}
