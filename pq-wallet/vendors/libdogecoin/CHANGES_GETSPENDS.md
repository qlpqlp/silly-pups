# Changes for `GET /getSpends` (upstream PR notes)

Target file: `src/rest.c` — handler branch `strcmp(path, "/getSpends") == 0`.

## Problems addressed

1. **False “spends”** — Transactions where the wallet only moved coins internally (e.g. consolidation or change-only patterns with no net value to an external address) were still listed. The handler now **skips** any wtx where the sum of non-wallet vout values (`ext_out`) is **≤ 0**, so only txs that actually pay **out** of the wallet are shown.

2. **Noise in per-output detail** — Previously every vout could appear in the detail section; change back to the user’s own addresses cluttered the output. The vout loop now **only emits `output:` blocks for vouts where `dogecoin_wallet_txout_is_mine` is false**, so each block is an external recipient. Each block includes `is_mine: 0` for parsers that key off the original shape.

3. **Duplicate lines** — If `vec_wtxes` ever contained the same transaction more than once (same `tx_hash_cache`), the REST output repeated it. A **dedupe pass** compares each wtx’s `tx_hash_cache` to all prior entries in the vector and **continues** on a match.

4. **Txid display endian** — Internally `tx_hash_cache` is **wire** byte order; explorers and `utxo->txid` use the **reversed** hex string. `/getSpends` uses `utils_bin_to_hex` + `utils_reverse_hex` on `tx_hash_cache`, and `dogecoin_wallet_sent_payment_hints_for_prevout` formats `spend_txid` the same way, so `/getSpends`, `/getTransactions` `spend_txid`, and `/getUTXOs` `txid` all agree on display encoding.

5. **Fake second “recipient” lines** — Non-change vouts with **value 0** (e.g. OP_RETURN / data carrier decoded as a bogus P2PKH) still printed as `output:` blocks. Those vouts are **skipped** in the listing; `sent` sums only non-wallet vouts with `value > 0` and not starting with `OP_RETURN` (`0x6a`).

6. **`pay_to` / `pay_amount` in hints** — `dogecoin_wallet_sent_payment_hints_for_prevout` (`wallet.c`) now skips zero-value and OP_RETURN vouts before picking the first external P2PKH payee, aligning hints with `/getSpends`.

## API shape preserved

- Header fields remain **`sent:`** (not renamed) so existing consumers (e.g. pq-wallet `parseSPVRESTGetSpends`) keep working.
- Each external payment still uses a **`  output:`** section with **`address:`** and **`is_mine:`** as before.

## Doc update

- `doc/rest.md` — `/getSpends` section updated to describe dedupe, external-only filter, and external-only `output:` blocks.

## Files touched (for a single upstream commit)

| Path        | Change                                      |
|------------|-----------------------------------------------|
| `src/rest.c` | Logic above + txid / ext_out / output filters |
| `src/wallet.c` | Payment hints: 0-value + OP_RETURN skip; `spend_txid` display endian |
| `doc/rest.md` | `/getSpends` behavior documentation        |

Optional: add this file as `CHANGES_GETSPENDS.md` only if the upstream project wants a PR checklist; it can be omitted from the final merge.
