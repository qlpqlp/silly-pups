// PQ Wallet — educational Dogecoin wallet UI with post-quantum proof context (libdogecoin PR #294).
//
// Standard Dogecoin P2PKH + WIF come from libdogecoin (`such -c generate_private_key` / `generate_public_key`). Post-quantum signing material
// (Falcon-512 / Dilithium2 via liboqs) using the bundled `such` CLI. `sendtx` broadcasts signed txs;
// `spvnode` runs SPV+SMPV in the background. Optional Dogecoin Core RPC for send/confirm.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	mu           sync.Mutex
	storageDir   string
	walletPath   string
	memetracker  string
	explorer     string
	explorerAddr string
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func (s *Server) generateDogecoinWallet(testnet bool) (*WalletFile, error) {
	wif, pubHex, addr, err := s.runSuchP2PKHWallet(testnet)
	if err != nil {
		return nil, err
	}
	network := "mainnet"
	if testnet {
		network = "testnet"
	}
	now := time.Now().UTC()
	w := &WalletFile{
		Version:       2,
		CreatedAt:     now,
		Network:       network,
		P2PKHAddress:  addr,
		WIFPrivateKey: wif,
		PublicKeyHex:  pubHex,
		PQScheme:      "Falcon-512 / Dilithium2 / Raccoon-G (liboqs via libdogecoin, experimental)",
		PQSource:      "none",
		PQNotes: "Post-quantum proofs attach to ordinary Dogecoin transactions: TX_C adds an OP_RETURN " +
			"commitment; an optional 1-DOGE carrier output can be spent in TX_R to reveal the full PQ " +
			"public key and signature on-chain. Standard P2PKH keys above fund and control DOGE; PQ " +
			"material is additional attestation per Dogecoin Foundation experiments.",
		LibdogecoinSPV: "Bundled spvnode (headers + BIP37 + SMPV) starts after wallet creation when SPVNODE_ENABLE=1. " +
			"Check GET /api/spv/status. Optional Dogecoin Core RPC (DOGE_RPC_URL) for sendtoaddress / confirmations.",
		ExperimentalDiscl: "Experimental research software. You may lose funds. Back up your WIF. " +
			"PQ proofs on mainnet are early-phase; verify any third-party tooling.",
		Addresses: []WalletAddress{{
			ID:        newAddressID(),
			Label:     "Primary",
			P2PKH:     addr,
			WIF:       wif,
			PubHex:    pubHex,
			CreatedAt: now,
			Primary:   true,
		}},
	}
	w.syncLegacyFromPrimary()
	return w, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleEducation(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, educationPayload())
}

func (s *Server) handleWalletGet(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"wallet": nil})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"wallet": wf})
}

type createBody struct {
	Network string `json:"network"`
	PQKeys  bool   `json:"pq_keys"`
}

func (s *Server) handleWalletCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body createBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	testnet := strings.EqualFold(strings.TrimSpace(body.Network), "testnet")

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadWallet(); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "wallet already exists; delete wallet.json on disk to reset"})
		return
	}
	wf, err := s.generateDogecoinWallet(testnet)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if body.PQKeys {
		pub, priv, err := s.runSuchFalconKeygen(testnet)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		wf.PQPublicHex = pub
		wf.PQPrivateHex = priv
		wf.PQSource = "such_falcon_keygen"
	}
	if err := s.saveWallet(wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.startSPVNode(wf)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "wallet": wf})
}

func (s *Server) handleMemetrackerPing(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(strings.TrimSpace(s.memetracker), "/")
	if base == "" {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "hint": "Set MEMETRACKER_BASE_URL to your MemeTracker pup (e.g. http://127.0.0.1:33555)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "reachable": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "reachable": resp.StatusCode < 500, "status": resp.StatusCode})
}

func (s *Server) handleTrackAddress(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(strings.TrimSpace(s.memetracker), "/")
	if base == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "MEMETRACKER_BASE_URL not set"})
		return
	}
	s.mu.Lock()
	wf, err := s.loadWallet()
	s.mu.Unlock()
	if err != nil || wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	addr := strings.TrimSpace(wf.P2PKHAddress)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/track/"+addr, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed any
	_ = json.Unmarshal(b, &parsed)
	writeJSON(w, http.StatusOK, map[string]any{"upstream_status": resp.StatusCode, "memetracker": parsed})
}

func (s *Server) handleExplorerTx(w http.ResponseWriter, r *http.Request) {
	txid := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/explorer/tx/"))
	if txid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing txid"})
		return
	}
	base := strings.TrimRight(strings.TrimSpace(s.explorer), "/")
	if base == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "EXPLORER_TX_API not set (e.g. https://api.blockchair.com/dogecoin/dashboards/transaction/)"})
		return
	}
	url := strings.ReplaceAll(base, "{txid}", txid)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var parsed any
	if err := json.Unmarshal(b, &parsed); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"raw": string(b), "upstream_status": resp.StatusCode})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstream_status": resp.StatusCode, "data": parsed})
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

func main() {
	port := env("PUBLIC_PORT", "33880")
	storage := env("PQ_STORAGE_DIR", "/storage/pq-wallet")
	_ = os.MkdirAll(storage, 0700)
	walletPath := filepath.Join(storage, "wallet.json")

	srv := &Server{
		storageDir:   storage,
		walletPath:   walletPath,
		memetracker:  env("MEMETRACKER_BASE_URL", ""),
		explorer:     env("EXPLORER_TX_API", ""),
		explorerAddr: env("EXPLORER_ADDRESS_API", ""),
	}

	go srv.backgroundMetricsLoop()

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", staticHandler()))
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, r *http.Request) {
		b, err := staticFS.ReadFile("static/logo.png")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/education", srv.handleEducation)
	mux.HandleFunc("/api/wallet", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			srv.handleWalletGet(w, r)
		case http.MethodPost:
			srv.handleWalletCreate(w, r)
		case http.MethodDelete:
			srv.handleWalletDelete(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
	mux.HandleFunc("/api/wallet/import", srv.handleWalletImport)
	mux.HandleFunc("/api/wallet/addresses", srv.handleWalletNewAddress)
	mux.HandleFunc("/api/wallet/primary", srv.handleWalletSetPrimary)
	mux.HandleFunc("/api/dashboard", srv.handleDashboard)
	mux.HandleFunc("/api/metrics", srv.handleMetrics)
	mux.HandleFunc("/api/transactions", srv.handleTransactions)
	mux.HandleFunc("/api/mempool/status", srv.handleMemetrackerPing)
	mux.HandleFunc("/api/mempool/track", srv.handleTrackAddress)
	mux.HandleFunc("/api/explorer/tx/", srv.handleExplorerTx)
	mux.HandleFunc("/api/spv/status", srv.handleSPVStatus)
	mux.HandleFunc("/api/tx/sign", srv.handleTxSign)
	mux.HandleFunc("/api/tx/broadcast", srv.handleTxBroadcast)
	mux.HandleFunc("/api/rpc/send", srv.handleRPCSend)
	mux.HandleFunc("/api/rpc/tx/", srv.handleRPCGetTx)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "index missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})

	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 {
		log.Fatalf("bad PUBLIC_PORT: %q", port)
	}
	addr := ":" + port
	log.Printf("[pq-wallet] storage=%s listen=%s", storage, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
