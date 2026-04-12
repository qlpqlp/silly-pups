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
					"**Post-quantum (PQ) keys** (e.g. Falcon-512 generated with `such -c falcon_keygen`) are separate material. They are used in research flows to produce commitments or signatures that can be referenced in OP_RETURN or follow-up transactions (TX_C / TX_R style experiments).",
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
					"**PQ “hints” in this UI** use heuristics on explorer data (e.g. presence of OP_RETURN script patterns). That is **not** a full cryptographic proof that a specific Falcon/Dilithium signature matches a commitment, it is a **UX hint** that something in the tx might relate to experimental PQ data.",
					"A serious audit would decode the transaction, parse outputs, and verify commitments against published PQ keys and schemes, beyond what this lightweight wallet does automatically.",
				},
			},
			{
				"id":    "tx_c_tx_r",
				"title": "TX_C and TX_R (high level)",
				"body": []string{
					"**TX_C** often carries a **commitment** (fingerprint) tied to PQ signing material in an OP_RETURN (small footprint).",
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
					"**SPV** (`spvnode`) follows headers and watches your primary address. **Pending** amounts use the embedded **Memepool Tracker** (data under `mempooltracker/`). The dashboard charts mempool relay activity over the last 24 hours.",
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
				"name":   "Optional OP_RETURN commitment (TX_C)",
				"detail": "Add a small **OP_RETURN** carrying a **commitment** (fingerprint) to Falcon or other PQ material if you are running an experiment.",
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
