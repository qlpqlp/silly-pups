package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type signBody struct {
	RawHex      string `json:"raw_hex"`
	InputIndex  int    `json:"input_index"`
	SighashType int    `json:"sighash_type"`
}

type broadcastBody struct {
	RawHex string `json:"raw_hex"`
	Peers  string `json:"peers"`
}

func (s *Server) handleTxSign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body signBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.RawHex) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "raw_hex required"})
		return
	}
	if body.SighashType == 0 {
		body.SighashType = 1
	}
	s.mu.Lock()
	wf, err := s.loadWallet()
	s.mu.Unlock()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "locked", "need_unlock": true})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	if wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	testnet := strings.EqualFold(wf.Network, "testnet")
	scriptHex, err := s.p2pkhScriptPubKeyHexForSign(wf)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	pa := wf.PrimaryAddress()
	if pa == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no primary address"})
		return
	}
	signed, err := s.runSuchSign(strings.TrimSpace(body.RawHex), scriptHex, pa.WIF, body.InputIndex, body.SighashType, testnet)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"signed_raw_hex": signed,
		"signing_kind":   "ecdsa_secp256k1_p2pkh",
		"signing_tool":   "such -c sign",
		"pq_note":        "This step signs the Dogecoin transaction with your P2PKH key (ECDSA). Falcon/Dilithium PQ material is separate and used in commitment / experimental flows — not as a replacement for this chain signature.",
	})
}

func (s *Server) handleTxBroadcast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body broadcastBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.RawHex) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "raw_hex required"})
		return
	}
	s.mu.Lock()
	wf, _ := s.loadWallet()
	s.mu.Unlock()
	testnet := wf != nil && strings.EqualFold(wf.Network, "testnet")
	raw := strings.TrimSpace(body.RawHex)
	out, err := s.runSendtx(raw, testnet, strings.TrimSpace(body.Peers))
	if s.storageDir != "" {
		if err != nil {
			s.appendBroadcastLogLine(fmt.Sprintf("broadcast FAIL err=%q", err.Error()))
		} else {
			s.appendBroadcastLogLine(fmt.Sprintf("broadcast OK sendtx_output=%q", truncateStr(out, 500)))
		}
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	sum := summarizeSendtxOutput(out)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"txid":           sum.BroadcastTxID,
		"sendtx_output":  out,
		"sendtx_summary": sum,
		"transport":      "libdogecoin_sendtx_p2p",
		"transport_note": "Relayed via libdogecoin sendtx to Dogecoin peers (P2P). JSON-RPC sendrawtransaction is not used.",
	})
}

func (s *Server) handleSPVStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	wf, err := s.loadWallet()
	s.mu.Unlock()
	if errors.Is(err, ErrWalletLocked) {
		s.startSPVNodeFromWatchState()
	} else if err == nil && wf != nil {
		s.startSPVNode(wf)
	}
	writeJSON(w, http.StatusOK, s.readSPVStatus())
}

func (s *Server) handleTxLocalDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	txid := normalizeTxid(strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/tx/local/")))
	if txid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid txid required"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.loadState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var found *TxRecord
	for i := range st.Transactions {
		if normalizeTxid(st.Transactions[i].Txid) == txid {
			cp := st.Transactions[i]
			cp.Txid = txid
			found = &cp
			break
		}
	}

	if wf, _ := s.loadWallet(); wf != nil {
		if eng, eerr := s.ensureMempoolEngine(wf); eerr == nil && eng != nil {
			_, mtrLive, _, _ := eng.DashboardSnapshot()
			for _, m := range mtrLive {
				if normalizeTxid(jsonStringAny(m["txid"])) != txid {
					continue
				}
				if found == nil {
					found = &TxRecord{
						Txid:       txid,
						Direction:  "unknown",
						Source:     "memetracker",
						RawHex:     strings.TrimSpace(jsonStringAny(m["raw_hex"])),
						AmountDOGE: floatFromAny(m["amount_doge"]),
						Address:    strings.TrimSpace(jsonStringAny(m["address"])),
					}
				} else {
					if found.RawHex == "" {
						found.RawHex = strings.TrimSpace(jsonStringAny(m["raw_hex"]))
					}
					if found.AmountDOGE == 0 {
						found.AmountDOGE = floatFromAny(m["amount_doge"])
					}
					if found.Address == "" {
						found.Address = strings.TrimSpace(jsonStringAny(m["address"]))
					}
				}
				break
			}
		}
	}
	// Fallback: if state/live mempool don't have raw hex, scan a larger SPV log window
	// for structured lines emitted by patched spvnode (PQ_SPV_TX_RAW ...).
	if found != nil && strings.TrimSpace(found.RawHex) == "" {
		if lb, err := readFileTail(s.spvLogPath(), 4*1024*1024); err == nil && strings.TrimSpace(lb) != "" {
			if byTxid := parseSPVRawTxHexByTxid(lb); len(byTxid) > 0 {
				if raw := strings.TrimSpace(byTxid[txid]); raw != "" {
					found.RawHex = raw
					// Persist for future reads so UI can show full detail consistently.
					for i := range st.Transactions {
						if normalizeTxid(st.Transactions[i].Txid) == txid && strings.TrimSpace(st.Transactions[i].RawHex) == "" {
							st.Transactions[i].RawHex = raw
							_ = s.saveState(st)
							break
						}
					}
				}
			}
		}
	}

	if found == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tx not found in local state"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"source":            "local_p2p_spv",
		"tx":                found,
		"local_raw_hex":     found.RawHex,
		"raw_hex_available": strings.TrimSpace(found.RawHex) != "",
	})
}
