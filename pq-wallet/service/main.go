// PQ Wallet — educational Dogecoin wallet UI with post-quantum proof context (libdogecoin PR #294).
//
// Standard Dogecoin P2PKH + WIF come from libdogecoin (`such -c generate_private_key` / `generate_public_key`). Post-quantum signing material
// (Falcon-512 / Dilithium2 via liboqs) using the bundled `such` CLI. `sendtx` broadcasts signed txs;
// `spvnode` runs SPV headers + BIP37 watch; broadcasting uses `sendtx` (P2P), not Core RPC.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
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

	"github.com/inevitable360/silly-pups/pq-wallet/service/mempooltracker"
)

//go:embed static/*
var staticFS embed.FS

// pqWalletAppVersion is shown in /api/health, education JSON, and the UI footer (keep in sync with manifest.json).
const pqWalletAppVersion = "0.0.29"

// pqWalletBuildHash is a release fingerprint (SHA-256 hex of "pq-wallet-<version>"); bump when cutting a release.
const pqWalletBuildHash = "9f8466376b12886bf3a6dc7cb946948d399504ea2600e6e97b0e68e0fd898282"

type Server struct {
	mu            sync.Mutex
	spvStartMu    sync.Mutex
	storageDir    string
	walletPath    string
	watchPath     string
	explorer      string
	explorerAddr  string
	mempoolMu     sync.Mutex
	mempoolEngine *mempooltracker.Engine
	walletKey     []byte
	sealSalt      []byte
	memWallet     *WalletFile
	unlockUntil   time.Time
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func (s *Server) generateDogecoinWallet(testnet bool) (*WalletFile, error) {
	return s.initHDWallet(testnet)
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
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusOK, map[string]any{"wallet": nil, "locked": true, "sealed": true})
			return
		}
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
	if s.hasSealedWallet() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "wallet already exists (sealed on disk)"})
		return
	}
	if _, err := os.Stat(s.walletPath); err == nil {
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

func (s *Server) handleExplorerTx(w http.ResponseWriter, r *http.Request) {
	txid := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/explorer/tx/"))
	if txid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing txid"})
		return
	}
	base := strings.TrimRight(strings.TrimSpace(s.explorer), "/")
	if base == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "EXPLORER_TX_API not set (HTTP GET template with {txid} placeholder for optional tx lookup)"})
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
		watchPath:    filepath.Join(storage, "spv_watch_state.json"),
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
		writeJSON(w, http.StatusOK, map[string]string{
			"status":      "ok",
			"app_version": pqWalletAppVersion,
			"build_hash":  pqWalletBuildHash,
		})
	})
	mux.HandleFunc("/api/security/status", srv.handleSecurityStatus)
	mux.HandleFunc("/api/security/unlock", srv.handleSecurityUnlock)
	mux.HandleFunc("/api/security/lock", srv.handleSecurityLock)
	mux.HandleFunc("/api/security/seal", srv.handleSecuritySeal)
	mux.HandleFunc("/api/security/unseal", srv.handleSecurityUnseal)
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
	mux.HandleFunc("/api/wallet/addresses/", srv.handleWalletDeleteAddress)
	mux.HandleFunc("/api/wallet/primary", srv.handleWalletSetPrimary)
	mux.HandleFunc("/api/dashboard", srv.handleDashboard)
	mux.HandleFunc("/api/metrics", srv.handleMetrics)
	mux.HandleFunc("/api/transactions", srv.handleTransactions)
	mux.HandleFunc("/api/tx/local/", srv.handleTxLocalDetail)
	mux.HandleFunc("/api/explorer/tx/", srv.handleExplorerTx)
	mux.HandleFunc("/api/spv/status", srv.handleSPVStatus)
	mux.HandleFunc("/api/spv/rescan", srv.handleSPVRescan)
	mux.HandleFunc("/api/services/control", srv.handleServicesControl)
	mux.HandleFunc("/api/logs/spv", srv.handleLogsSPV)
	mux.HandleFunc("/api/logs/mempooltracker", srv.handleLogsMempoolTracker)
	mux.HandleFunc("/api/logs/broadcast", srv.handleLogsBroadcast)
	mux.HandleFunc("/api/tx/sign", srv.handleTxSign)
	mux.HandleFunc("/api/tx/broadcast", srv.handleTxBroadcast)
	mux.HandleFunc("/api/send/pq-safe", srv.handleSendPQSafe)

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
