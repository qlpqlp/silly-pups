package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type primaryBody struct {
	ID string `json:"id"`
}

type spvWatchState struct {
	Network   string   `json:"network"`
	Addresses []string `json:"addresses"`
}

func cloneWallet(w *WalletFile) (*WalletFile, error) {
	b, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	var out WalletFile
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Server) loadWalletRaw() (*WalletFile, error) {
	if s.hasSealedWallet() {
		if s.memWallet != nil && time.Now().Before(s.unlockUntil) {
			return cloneWallet(s.memWallet)
		}
		return nil, ErrWalletLocked
	}
	b, err := os.ReadFile(s.walletPath)
	if err != nil {
		return nil, err
	}
	var w WalletFile
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *Server) loadWalletMigrate() (*WalletFile, error) {
	wf, err := s.loadWalletRaw()
	if err != nil {
		return nil, err
	}
	needsSave := len(wf.Addresses) == 0 && strings.TrimSpace(wf.P2PKHAddress) != ""
	wf.migrateAddresses()
	wf.ensurePrimaryUnique()
	if wf.isHDWallet() && wf.HDNextIndex <= 0 {
		maxIdx := -1
		for _, a := range wf.Addresses {
			if a.DeriveIndex > maxIdx {
				maxIdx = a.DeriveIndex
			}
		}
		wf.HDNextIndex = maxIdx + 1
	}
	wf.syncLegacyFromPrimary()
	if needsSave && len(wf.Addresses) > 0 {
		_ = s.saveWallet(wf)
	}
	s.touchUnlockSession()
	return wf, nil
}

// loadWallet is used by handlers after migration.
func (s *Server) loadWallet() (*WalletFile, error) {
	return s.loadWalletMigrate()
}

func (s *Server) saveWallet(w *WalletFile) error {
	w.ensurePrimaryUnique()
	w.syncLegacyFromPrimary()
	_ = s.saveSPVWatchState(w)
	if s.walletKey != nil {
		s.memWallet = w
		return s.persistSealedWallet(w)
	}
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.walletPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.walletPath)
}

func (s *Server) saveSPVWatchState(w *WalletFile) error {
	if w == nil {
		return fmt.Errorf("nil wallet")
	}
	st := spvWatchState{
		Network:   strings.TrimSpace(w.Network),
		Addresses: w.AllDistinctP2PKHAddresses(),
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.watchPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.watchPath)
}

func (s *Server) loadSPVWatchState() (*spvWatchState, error) {
	b, err := os.ReadFile(s.watchPath)
	if err != nil {
		return nil, err
	}
	var st spvWatchState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Server) handleWalletDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "DELETE only"})
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Confirm) != "DELETE" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"confirm":"DELETE"}`})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopSPVNode()
	s.lockWalletSession()
	_ = os.Remove(s.walletPath)
	_ = os.Remove(s.sealedPath())
	_ = os.Remove(s.statePath())
	_ = os.Remove(filepath.Join(s.storageDir, "spv.log"))
	_ = os.Remove(s.spvPidPath())
	_ = os.Remove(filepath.Join(s.storageDir, "spv_wallet.db"))
	_ = os.Remove(filepath.Join(s.storageDir, "headers.db"))
	_ = os.Remove(s.watchPath)
	s.removeServicePrefsFile()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleWalletImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var wf WalletFile
	if err := json.Unmarshal(b, &wf); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	wf.HDMode = strings.TrimSpace(wf.HDMode)
	wf.HDMasterXPrv = strings.TrimSpace(wf.HDMasterXPrv)
	wf.HDMasterXPub = strings.TrimSpace(wf.HDMasterXPub)
	if wf.HDMasterXPrv != "" && wf.HDMode == "" {
		// Backward compatibility for early HD exports that only stored xprv/xpub.
		wf.HDMode = hdModeBIP44Doge
	}
	isHDImport := wf.HDMasterXPrv != ""
	if wf.PrimaryAddress() == nil && strings.TrimSpace(wf.P2PKHAddress) == "" && !isHDImport {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid wallet json"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasSealedWallet() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "sealed wallet exists — delete or unlock first"})
		return
	}
	if _, err := s.loadWalletRawPlaintext(); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "wallet already exists — delete first"})
		return
	}
	wf.migrateAddresses()
	if isHDImport && len(wf.Addresses) == 0 {
		// Allow restore from HD-only backups (master keys + metadata) by deriving the first receive address.
		wf.HDNextIndex = 0
		if _, err := s.appendNextHDDerivedAddress(&wf, true); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid HD wallet backup: " + err.Error()})
			return
		}
	}
	if isHDImport && wf.HDNextIndex <= 0 {
		maxIdx := -1
		for _, a := range wf.Addresses {
			if a.DeriveIndex > maxIdx {
				maxIdx = a.DeriveIndex
			}
		}
		wf.HDNextIndex = maxIdx + 1
	}
	wf.ensurePrimaryUnique()
	wf.syncLegacyFromPrimary()
	if wf.Version < 2 {
		wf.Version = 2
	}
	if err := s.saveWallet(&wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Import/restore is typically an existing wallet history, not a new wallet.
	// Clear tx cache + SPV wallet DB so libdogecoin can rebuild full history for
	// the restored addresses from current headers.
	s.stopSPVNode()
	_ = os.Remove(filepath.Join(s.storageDir, "spv_wallet.db"))
	_ = os.Remove(s.spvWatchAddrPath())
	_ = s.saveState(&WalletState{Version: 1})
	s.startSPVNode(&wf)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"wallet":             wf,
		"import_mode":        "restore_resync",
		"removed_spv_wallet": filepath.Join(s.storageDir, "spv_wallet.db"),
		"note":               "Wallet restore started. SPV wallet DB was reset so transaction history can resync for restored addresses.",
	})
}

func (s *Server) handleWalletNewAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "locked", "need_unlock": true})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	if wf.isHDWallet() {
		if _, err := s.appendNextHDDerivedAddress(wf, false); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	} else {
		testnet := strings.EqualFold(wf.Network, "testnet")
		wif, pubHex, addr, err := s.runSuchP2PKHWallet(testnet)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		wf.Addresses = append(wf.Addresses, WalletAddress{
			ID:        newAddressID(),
			Label:     time.Now().UTC().Format("2006-01-02 15:04"),
			P2PKH:     addr,
			WIF:       wif,
			PubHex:    pubHex,
			CreatedAt: time.Now().UTC(),
			Primary:   false,
		})
	}
	if err := s.saveWallet(wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.startSPVNode(wf)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "wallet": wf})
}

func (s *Server) handleWalletSetPrimary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body primaryBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "locked", "need_unlock": true})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	if err := wf.setPrimary(body.ID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.saveWallet(wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.stopSPVNode()
	s.startSPVNode(wf)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "wallet": wf})
}

func (s *Server) handleWalletDeleteAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "DELETE only"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/wallet/addresses/")
	id = strings.TrimSpace(id)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing address id"})
		return
	}
	var body struct {
		ConfirmAddress string `json:"confirm_address"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "locked", "need_unlock": true})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	wf.migrateAddresses()
	var (
		idx  = -1
		addr WalletAddress
	)
	for i := range wf.Addresses {
		if wf.Addresses[i].ID == id {
			idx = i
			addr = wf.Addresses[i]
			break
		}
	}
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "address not found"})
		return
	}
	if addr.Primary {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot remove primary address"})
		return
	}
	if strings.TrimSpace(body.ConfirmAddress) != strings.TrimSpace(addr.P2PKH) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirmation address mismatch"})
		return
	}
	if st, err := s.loadState(); err == nil {
		for _, tx := range st.Transactions {
			if strings.EqualFold(strings.TrimSpace(tx.Address), strings.TrimSpace(addr.P2PKH)) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "address has associated transactions and cannot be removed"})
				return
			}
		}
	}
	wf.Addresses = append(wf.Addresses[:idx], wf.Addresses[idx+1:]...)
	if err := s.saveWallet(wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.stopSPVNode()
	s.startSPVNode(wf)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "wallet": wf})
}
