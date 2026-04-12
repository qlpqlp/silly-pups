package main

import (
	"encoding/json"
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

func (s *Server) loadWalletMigrate() (*WalletFile, error) {
	wf, err := s.loadWalletRaw()
	if err != nil {
		return nil, err
	}
	needsSave := len(wf.Addresses) == 0 && strings.TrimSpace(wf.P2PKHAddress) != ""
	wf.migrateAddresses()
	wf.ensurePrimaryUnique()
	wf.syncLegacyFromPrimary()
	if needsSave && len(wf.Addresses) > 0 {
		_ = s.saveWallet(wf)
	}
	return wf, nil
}

func (s *Server) loadWalletRaw() (*WalletFile, error) {
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

// loadWallet is used by handlers after migration.
func (s *Server) loadWallet() (*WalletFile, error) {
	return s.loadWalletMigrate()
}

func (s *Server) saveWallet(w *WalletFile) error {
	w.ensurePrimaryUnique()
	w.syncLegacyFromPrimary()
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
	_ = os.Remove(s.walletPath)
	_ = os.Remove(s.statePath())
	_ = os.Remove(filepath.Join(s.storageDir, "spv.log"))
	_ = os.Remove(s.spvPidPath())
	_ = os.Remove(filepath.Join(s.storageDir, "spv_wallet.db"))
	_ = os.Remove(filepath.Join(s.storageDir, "headers.db"))
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
	if wf.PrimaryAddress() == nil && strings.TrimSpace(wf.P2PKHAddress) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid wallet json"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadWalletRaw(); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "wallet already exists — delete first"})
		return
	}
	wf.migrateAddresses()
	wf.ensurePrimaryUnique()
	wf.syncLegacyFromPrimary()
	if wf.Version < 2 {
		wf.Version = 2
	}
	if err := s.saveWallet(&wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = s.saveState(&WalletState{Version: 1})
	s.startSPVNode(&wf)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "wallet": wf})
}

func (s *Server) handleWalletNewAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil || wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
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
	if err := s.saveWallet(wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
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
	if err != nil || wf == nil {
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
