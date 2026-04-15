package main

// educationPayload returns long-form copy for the web UI (How it works).
func educationPayload() map[string]any {
	return map[string]any{
		"title": "How Dogecoin + post-quantum proofs fit together",
		"summary": "Normal Dogecoin spends still use ECDSA (secp256k1) P2PKH. Experimental post-quantum material (e.g. Falcon/Dilithium via liboqs) " +
			"can be used to add commitments or attestations around a transaction, it does not replace the chain’s ECDSA signatures today.",
		"sections": []map[string]any{
			{
				"id":    "layers",
				"title": "Two layers: chain signatures vs PQ attestations",
				"body": []string{
					"**On-chain spend authorization** is still the usual Dogecoin-style ECDSA signature over the sighash, using your P2PKH key. The `such -c sign` command in this wallet performs that ECDSA signing step.",
					"**Post-quantum (PQ) keys** (e.g. Falcon-512 generated with `such -c falcon_keygen`) are separate material. Phase-1 commitment format is canonical tagged OP_RETURN: `6a24 + TAG4 + 32-byte commitment` where TAG4 is `FLC1`, `DIL2`, or `RCG4`.",
					"So: **spending DOGE** = ECDSA. **PQ** = additional proof or attestation layer described in libdogecoin experiments, not a drop-in replacement for ECDSA on the base layer.",
				},
			},
			{
				"id":    "send_flow",
				"title": "How a payment is created and sent (conceptually)",
				"body": []string{
					"1. You choose inputs (UTXOs) and outputs (recipient + change). That yields an **unsigned** raw transaction hex.",
					"2. For each input you must sign with the private key that locks that input, here that is **`such -c sign`** with your WIF and the correct scriptPubKey for your address.",
					"3. The result is a **signed** raw hex, valid under Dogecoin’s consensus rules.",
					"4. **Broadcast** propagates the signed tx to the peer-to-peer network so miners can include it. This pup uses **`sendtx`** (libdogecoin) to talk to Dogecoin peers, **not** `sendrawtransaction` over JSON-RPC.",
				},
			},
			{
				"id":    "verify",
				"title": "What “verification” means here",
				"body": []string{
					"**Consensus verification** happens on every node: ECDSA signatures must validate, scripts must succeed, balances must add up.",
					"**PQ verification in this UI** checks canonical Phase-1 OP_RETURN commitment layout and tags (`FLC1`/`DIL2`/`RCG4`). Full cryptographic proof still requires verifier material (`pubkey || signature`) and signature validation flow.",
					"A serious audit would decode the transaction, parse outputs, and verify commitments against published PQ keys and schemes, beyond what this lightweight wallet does automatically.",
				},
			},
			{
				"id":    "tx_c_tx_r",
				"title": "TX_C and TX_R (high level)",
				"body": []string{
					"**TX_C** carries canonical Phase-1 commitment output: `OP_RETURN 0x24 <TAG4><32-byte-commitment>`.",
					"An optional **carrier** output can hold value while a follow-up **TX_R** reveals more PQ data on-chain and returns funds minus fees, depending on the exact experiment.",
					"For many tests, the commitment alone is enough; the carrier/reveal path is optional.",
				},
			},
			{
				"id":    "this_pup",
				"title": "What this pup actually runs",
				"body": []string{
					"Binaries on PATH: **`such`**, **`sendtx`**, **`spvnode`**, built with **USE_LIBOQS** for Falcon/Dilithium support in libdogecoin.",
					"**Broadcast** uses **`sendtx`** only (P2P), not JSON-RPC `sendrawtransaction`.",
					"**SPV** (`spvnode`) follows headers and BIP37-watches **all addresses** in your wallet. **Pending** amounts use the embedded **Memepool Tracker** (data under `mempooltracker/`). The dashboard charts mempool relay activity over the last 24 hours.",
				},
			},
		},
		"references": []string{
			"https://github.com/dogecoinfoundation/libdogecoin/pull/294",
			"https://github.com/dogecoinfoundation/libdogecoin",
		},
		"flow_lead": "You start with a normal Dogecoin payment (ECDSA), then optionally attach PQ-related data. Each step builds on the last.",
		"flow": []map[string]string{
			{
				"step":   "1",
				"name":   "Build and sign like any Dogecoin tx",
				"detail": "Pick UTXOs, set outputs, then sign inputs with **ECDSA** (`such -c sign`). Until this step succeeds, nothing is valid on-chain.",
			},
			{
				"step":   "2",
				"name":   "Canonical commitment output (TX_C)",
				"detail": "Add canonical Phase-1 OP_RETURN commitment: **`6a24 + TAG4 + commitment32`** (`FLC1`, `DIL2`, `RCG4`).",
			},
			{
				"step":   "3",
				"name":   "Carrier output and reveal (TX_R), if needed",
				"detail": "Some flows lock a little value in a **carrier** output, then spend it in **TX_R** to reveal more PQ data and recover funds minus fees.",
			},
			{
				"step":   "4",
				"name":   "Broadcast with P2P sendtx",
				"detail": "Submit the **signed** hex with **`sendtx`** so Dogecoin peers relay it. Miners include it when it pays enough fee.",
			},
		},
		"libdogecoin_build": "This pup ships such/sendtx/spvnode built with -DUSE_LIBOQS=ON (Falcon-512, Dilithium2).",
	}
}
