package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	sealedWalletFile = "wallet.sealed"
	saltLen          = 16
	nonceLen         = 12
	argonTime        = 3
	argonMemory      = 64 * 1024
	argonThreads     = 4
	argonKeyLen      = 32
	unlockTTL        = 45 * time.Minute
)

// ErrWalletLocked is returned when wallet.sealed exists and the session has not unlocked.
var ErrWalletLocked = errors.New("wallet is sealed; unlock with PIN")

func (s *Server) sealedPath() string {
	return filepath.Join(s.storageDir, sealedWalletFile)
}

func (s *Server) hasSealedWallet() bool {
	_, err := os.Stat(s.sealedPath())
	return err == nil
}

func deriveWalletKey(pin string, salt []byte) []byte {
	return argon2.IDKey([]byte(pin), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}

func (s *Server) persistSealedWallet(wf *WalletFile) error {
	if s.walletKey == nil || len(s.sealSalt) != saltLen {
		return errors.New("wallet encryption key not loaded")
	}
	plain, err := json.Marshal(wf)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(s.walletKey)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	out := gcm.Seal(nil, nonce, plain, nil)
	blob := append(append(append([]byte{}, s.sealSalt...), nonce...), out...)
	tmp := s.sealedPath() + ".tmp"
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.sealedPath())
}

// sealWalletFromPlaintext reads wallet.json, encrypts to wallet.sealed, clears session material.
func (s *Server) sealWalletFromPlaintext(pin string) error {
	pin = strings.TrimSpace(pin)
	if len(pin) < 6 {
		return errors.New("PIN must be at least 6 characters")
	}
	wf, err := s.loadWalletRawPlaintext()
	if err != nil {
		return err
	}
	wf.migrateAddresses()
	wf.ensurePrimaryUnique()
	wf.syncLegacyFromPrimary()
	_ = s.saveSPVWatchState(wf)
	plain, err := json.Marshal(wf)
	if err != nil {
		return err
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return err
	}
	key := deriveWalletKey(pin, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	out := gcm.Seal(nil, nonce, plain, nil)
	blob := append(append(salt, nonce...), out...)
	tmp := s.sealedPath() + ".tmp"
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.sealedPath()); err != nil {
		return err
	}
	_ = os.Remove(s.walletPath)
	s.walletKey = key
	s.sealSalt = append([]byte(nil), salt...)
	s.memWallet = wf
	s.unlockUntil = time.Now().Add(unlockTTL)
	return nil
}

func (s *Server) unlockSealedWallet(pin string) error {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return errors.New("missing PIN")
	}
	b, err := os.ReadFile(s.sealedPath())
	if err != nil {
		return err
	}
	if len(b) < saltLen+nonceLen+16 {
		return errors.New("corrupt sealed wallet")
	}
	salt := append([]byte(nil), b[:saltLen]...)
	nonce := b[saltLen : saltLen+nonceLen]
	ct := b[saltLen+nonceLen:]
	key := deriveWalletKey(pin, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return errors.New("wrong PIN or corrupt wallet")
	}
	var wf WalletFile
	if err := json.Unmarshal(plain, &wf); err != nil {
		return err
	}
	wf.migrateAddresses()
	wf.ensurePrimaryUnique()
	wf.syncLegacyFromPrimary()
	_ = s.saveSPVWatchState(&wf)
	s.walletKey = key
	s.sealSalt = salt
	s.memWallet = &wf
	s.unlockUntil = time.Now().Add(unlockTTL)
	return nil
}

func (s *Server) lockWalletSession() {
	s.walletKey = nil
	s.sealSalt = nil
	s.memWallet = nil
	s.unlockUntil = time.Time{}
}

func (s *Server) touchUnlockSession() {
	if s.memWallet != nil && s.walletKey != nil && s.hasSealedWallet() {
		s.unlockUntil = time.Now().Add(unlockTTL)
	}
}

func (s *Server) loadWalletRawPlaintext() (*WalletFile, error) {
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
