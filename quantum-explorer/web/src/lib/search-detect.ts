export type SearchKind = "tx" | "block_height" | "block_hash" | "address";

/** Heuristic routing for omnibox-style search (Dogecoin-style addresses + 64-hex ids). */
export function classifySearchInput(raw: string): { kind: SearchKind; value: string } | null {
  const q = raw.trim();
  if (!q) return null;
  if (/^\d{1,12}$/.test(q)) {
    return { kind: "block_height", value: q };
  }
  if (/^[a-fA-F0-9]{64}$/.test(q)) {
    return { kind: "tx", value: q.toLowerCase() };
  }
  if (/^[Dd9][a-km-zA-HJ-NP-Z1-9]{25,34}$/.test(q)) {
    return { kind: "address", value: q };
  }
  return { kind: "address", value: q };
}
