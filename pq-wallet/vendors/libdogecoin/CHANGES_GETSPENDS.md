# Changes for `GET /getSpends` (upstream PR notes)

Target file: `src/rest.c` — handler branch `strcmp(path, "/getSpends") == 0`.

## Problems addressed

1. **False “spends”** — Transactions where the wallet only moved coins internally (e.g. consolidation or change-only patterns with no net value to an external address) were still listed. The handler now **skips** any wtx where the sum of non-wallet vout values (`ext_out`) is **≤ 0**, so only txs that actually pay **out** of the wallet are shown.

2. **Noise in per-output detail** — Previously every vout could appear in the detail section; change back to the user’s own addresses cluttered the output. The vout loop now **only emits `output:` blocks for vouts where `dogecoin_wallet_txout_is_mine` is false**, so each block is an external recipient. Each block includes `is_mine: 0` for parsers that key off the original shape.

3. **Duplicate lines** — If `vec_wtxes` ever contained the same transaction more than once (same `tx_hash_cache`), the REST output repeated it. A **dedupe pass** compares each wtx’s `tx_hash_cache` to all prior entries in the vector and **continues** on a match.

4. **Txid byte order** — Documented in-code: `tx_hash_cache` is printed with `utils_bin_to_hex` then `utils_reverse_hex` so the displayed `txid` matches explorer-style ordering (same pattern as elsewhere in the stack).

## API shape preserved

- Header fields remain **`sent:`** (not renamed) so existing consumers (e.g. pq-wallet `parseSPVRESTGetSpends`) keep working.
- Each external payment still uses a **`  output:`** section with **`address:`** and **`is_mine:`** as before.

## Doc update

- `doc/rest.md` — `/getSpends` section updated to describe dedupe, external-only filter, and external-only `output:` blocks.

## Files touched (for a single upstream commit)

| Path        | Change                                      |
|------------|-----------------------------------------------|
| `src/rest.c` | Logic above + brief comment on txid hex     |
| `doc/rest.md` | `/getSpends` behavior documentation        |

Optional: add this file as `CHANGES_GETSPENDS.md` only if the upstream project wants a PR checklist; it can be omitted from the final merge.
