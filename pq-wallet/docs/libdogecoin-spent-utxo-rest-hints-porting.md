# libdogecoin changes: spent-UTXO REST hints (Dogecoin Wallet–style)

This document describes **every change** made under PQ Wallet’s vendored libdogecoin (`pq-wallet/vendors/libdogecoin`) so you can **reproduce or port** the same edits onto the [official libdogecoin](https://github.com/dogecoinfoundation/libdogecoin) tree (or any fork).

## Goal

`/getTransactions` lists **spent** wallet UTXOs keyed by the **funding** txid (`txid` / `vout` / wallet `address`). That is not how **Dogecoin Wallet** shows **sends**: the UI wants the **spending transaction** id, the **payee address**, and the **payment line amount** (first external P2PKH output on that spend, same idea as bitcoinj’s “to address of sent”).

These changes add **optional** `key: value` lines to each spent-UTXO block so REST consumers (e.g. PQ Wallet) can build an OUT row for the **spend** without guessing from raw hex alone.

## Files touched (canonical list)

| File | Change |
|------|--------|
| `include/dogecoin/wallet.h` | Declare `dogecoin_wallet_sent_payment_hints_for_prevout(...)`. |
| `src/wallet.c` | Implement prevout → spending `wtx` lookup; spend txid hex; first non-mine P2PKH `pay_to` / `pay_amount`; optional height & confirmations. |
| `src/rest.c` | In `GET /getTransactions`, after each spent UTXO’s `solvable:` line, call the helper and print optional REST lines. |
| `doc/rest.md` | Document the new fields and extend the sample response. |

No CMake / new source files: `wallet.c` and `rest.c` are already in the build.

---

## 1. `include/dogecoin/wallet.h`

**Where:** Immediately after the declaration of `dogecoin_wallet_txout_is_mine` (and before `dogecoin_wallet_is_spent` or the next API block—match your tree’s ordering).

**Add:** the block comment + prototype:

```c
/**
 * For a spent prevout (UTXO txid + vout), find the wallet tx that spends it and derive Dogecoin-Wallet-style
 * payment metadata: spend txid (display hex), first non-wallet P2PKH recipient, and that output's amount.
 * Any of pay_to / pay_amount buffers may be NULL if the caller does not need them.
 * opt_spend_height / opt_spend_confirmations may be NULL.
 */
LIBDOGECOIN_API dogecoin_bool dogecoin_wallet_sent_payment_hints_for_prevout(dogecoin_wallet* wallet, const uint256_t prev_txid, uint32_t prev_vout, char spend_txid_hex65[65], char pay_to[P2PKHLEN], char pay_amount[KOINU_STRINGLEN], int* opt_spend_height, int* opt_spend_confirmations);
```

**Semantics:**

- `prev_txid` / `prev_vout`: same interpretation as `dogecoin_utxo` in the wallet (the **funding** outpoint that was later spent).
- `spend_txid_hex65`: 64 hex chars + NUL; same display style as other REST txids (`utils_uint8_to_hex` on `dogecoin_tx_hash` of the spending tx).
- `pay_to` / `pay_amount`: first **non–wallet-mine** output that decodes to P2PKH via `dogecoin_pubkey_hash_to_p2pkh_address`; amount via `koinu_to_coins_str`.
- Returns `true` if a spending transaction was found in `wallet->vec_wtxes` (even if no external P2PKH was found—then `pay_*` may stay empty).
- `opt_spend_height`: `wtx->height` of the spending tx.
- `opt_spend_confirmations`: `bestblockheight - height + 1` when both are set and positive.

---

## 2. `src/wallet.c`

**Where:** Implement the function **after** `dogecoin_wallet_txout_is_mine` and **before** `dogecoin_wallet_is_mine` (anchor names; line numbers differ per upstream revision).

**Algorithm (must stay consistent with wallet prevout handling):**

1. Require `wallet`, `spend_txid_hex65`, and `wallet->vec_wtxes`.
2. For each `dogecoin_wtx` in `vec_wtxes` (skip `ignore`, missing `tx` / `vin`).
3. For each input, resolve prevout bytes the **same way** as `dogecoin_wallet_scrape_utxos`: `utils_uint8_to_hex` on `tx_in->prevout.hash`, `utils_reverse_hex` on the 64-char buffer, then `utils_hex_to_uint8` into 32 bytes; compare to `prev_txid` and `tx_in->prevout.n` to `prev_vout`.
4. On match: `dogecoin_tx_hash(wtx->tx, spendh)` → copy hex into `spend_txid_hex65`.
5. Scan `vout` in order; skip `dogecoin_wallet_txout_is_mine`; for the first output where `dogecoin_pubkey_hash_to_p2pkh_address` succeeds and address non-empty, fill `pay_to` / `pay_amount` and break.
6. Fill optional height / confirmations; return `true`.
7. If no spend found, return `false`.

**Reference implementation:** copy the function `dogecoin_wallet_sent_payment_hints_for_prevout` verbatim from:

`silly-pups/pq-wallet/vendors/libdogecoin/src/wallet.c`

(search for that symbol). It is self-contained and uses only existing wallet / tx / utils APIs.

**Caveats for upstream review:**

- **P2PKH only** for `pay_to`: sends to P2SH / non-standard scripts will not get `pay_to` / `pay_amount` (spend_txid still appears).
- **“First external P2PKH”** matches a simple Dogecoin Wallet / bitcoinj-style heuristic, not full payment decomposition for complex txs.
- Depends on **`vec_wtxes`** containing the spending tx (same assumption as the rest of the wallet).

---

## 3. `src/rest.c`

**Where:** In `dogecoin_http_request_cb`, branch `strcmp(path, "/getTransactions") == 0`, inside `HASH_ITER` over `wallet->utxos` where `!utxo->spendable` (spent UTXO block).

**After** the line that prints `solvable:` (and **before** `wallet_total_u64 += coins_to_koinu_str(utxo->amount);`), insert a block that:

1. Allocates stack buffers: `char spend_hex[65]`, `char pay_to[P2PKHLEN]`, `char pay_amt[KOINU_STRINGLEN]`, `int sh = 0, sc = 0`.
2. Zeroes them with `dogecoin_mem_zero`.
3. Calls  
   `dogecoin_wallet_sent_payment_hints_for_prevout(wallet, utxo->txid, (uint32_t)utxo->vout, spend_hex, pay_to, pay_amt, &sh, &sc)`.
4. If it returns true, print only non-empty / positive fields:

| REST key | C condition | Notes |
|----------|-------------|--------|
| `spend_txid` | `spend_hex[0]` | Same hex style as `txid:` line |
| `pay_to` | `pay_to[0]` | Base58 P2PKH |
| `pay_amount` | `pay_amt[0]` | Coin string |
| `spend_height` | `sh > 0` | Spending tx block height |
| `spend_confirmations` | `sc > 0` | From `bestblockheight` |

**Exact labels in the reference tree** (PQ vendor; keep identical if you want PQ’s Go parser to match without changes):

- `spend_txid:     %s\n`
- `pay_to:         %s\n`
- `pay_amount:     %s\n`
- `spend_height:   %d\n`
- `spend_confirmations: %d\n`

`rest.c` already includes `dogecoin/wallet.h`; no new includes were required in the PQ vendor tree.

---

## 4. `doc/rest.md`

Under **GET /getTransactions**:

- Extend the response body template with the optional `spend_txid`, `pay_to`, `pay_amount`, `spend_height`, `spend_confirmations` lines.
- Add a short sentence that these mirror Dogecoin Wallet–style metadata for the **spending** transaction.
- Extend the **sample response** with a concrete example block showing those lines.

Copy from:

`silly-pups/pq-wallet/vendors/libdogecoin/doc/rest.md`

---

## How to apply on the official repo

1. **Clone** [dogecoinfoundation/libdogecoin](https://github.com/dogecoinfoundation/libdogecoin) and create a branch, e.g. `feature/rest-spent-utxo-payment-hints`.

2. **Apply edits** using either:
   - **Manual:** follow sections 1–4 above and use the vendor files as a side-by-side reference, or  
   - **Diff:** from a machine that has both trees, diff only those four paths between upstream and  
     `silly-pups/pq-wallet/vendors/libdogecoin/…`  
     then `git apply` or cherry-pick hunks (resolve conflicts if upstream moved `rest.c` / `wallet.c`).

3. **Build** your usual targets (`spvnode`, tools) and run SPV with a wallet that has spent P2PKH sends.

4. **Smoke test:**

   ```bash
   curl -sS "http://127.0.0.1:<REST_PORT>/getTransactions" | head -n 80
   ```

   Each spent UTXO block should optionally show `spend_txid` / `pay_to` / `pay_amount` when the wallet has the spending wtx and a decodable external P2PKH output.

5. **Upstream PR:** describe motivation (REST consumers / wallet UX parity with Dogecoin Wallet), document backward compatibility (all new lines are optional), and note limitations (P2PKH-first heuristic, `vec_wtxes` dependency).

---

## PQ Wallet (silly-pups) consumer

PQ Wallet parses these keys in `pq-wallet/service/spv_rest_sync.go` and merges a separate **OUT** `TxRecord` keyed by `spend_txid`. If you change key names or formats in official libdogecoin, update that parser (and `doc/rest.md` here) to stay aligned.

---

## Single reference copy in this repo

Canonical patched sources live at:

- `pq-wallet/vendors/libdogecoin/include/dogecoin/wallet.h`
- `pq-wallet/vendors/libdogecoin/src/wallet.c`
- `pq-wallet/vendors/libdogecoin/src/rest.c`
- `pq-wallet/vendors/libdogecoin/doc/rest.md`

Use those files as the **source of truth** when porting to another clone.
