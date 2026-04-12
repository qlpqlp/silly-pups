package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// WalletAddress is one P2PKH key/address row (multi-address wallet).
type WalletAddress struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	P2PKH       string    `json:"p2pkh_address"`
	WIF         string    `json:"wif_private_key"`
	PubHex      string    `json:"public_key_hex_compressed"`
	CreatedAt   time.Time `json:"created_at"`
	Primary     bool      `json:"primary"`
}

// WalletFile is persisted as wallet.json (v1 legacy + v2 addresses).
type WalletFile struct {
	Version           int       `json:"version"`
	CreatedAt         time.Time `json:"created_at"`
	Network           string    `json:"network"`
	P2PKHAddress      string    `json:"p2pkh_address"`
	WIFPrivateKey     string    `json:"wif_private_key"`
	PublicKeyHex      string    `json:"public_key_hex_compressed"`
	PQScheme          string    `json:"pq_scheme"`
	PQPublicHex       string    `json:"pq_public_key_hex,omitempty"`
	PQPrivateHex      string    `json:"pq_private_key_hex,omitempty"`
	PQSource          string    `json:"pq_key_source"`
	PQNotes           string    `json:"pq_notes,omitempty"`
	LibdogecoinSPV    string    `json:"libdogecoin_spv_note"`
	ExperimentalDiscl string    `json:"experimental_disclaimer"`
	Addresses         []WalletAddress `json:"addresses,omitempty"`
}

func newAddressID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (w *WalletFile) syncLegacyFromPrimary() {
	p := w.PrimaryAddress()
	if p == nil {
		return
	}
	w.P2PKHAddress = p.P2PKH
	w.WIFPrivateKey = p.WIF
	w.PublicKeyHex = p.PubHex
}

// PrimaryAddress returns the marked primary row, or the first address, or nil.
func (w *WalletFile) PrimaryAddress() *WalletAddress {
	for i := range w.Addresses {
		if w.Addresses[i].Primary {
			return &w.Addresses[i]
		}
	}
	if len(w.Addresses) > 0 {
		return &w.Addresses[0]
	}
	if strings.TrimSpace(w.P2PKHAddress) != "" {
		// Legacy-only
		return &WalletAddress{
			ID:        "legacy",
			Label:     "Primary",
			P2PKH:     w.P2PKHAddress,
			WIF:       w.WIFPrivateKey,
			PubHex:    w.PublicKeyHex,
			CreatedAt: w.CreatedAt,
			Primary:   true,
		}
	}
	return nil
}

func (w *WalletFile) migrateAddresses() {
	if len(w.Addresses) > 0 {
		return
	}
	if strings.TrimSpace(w.P2PKHAddress) == "" {
		return
	}
	w.Addresses = []WalletAddress{{
		ID:        newAddressID(),
		Label:     "Primary",
		P2PKH:     strings.TrimSpace(w.P2PKHAddress),
		WIF:       w.WIFPrivateKey,
		PubHex:    w.PublicKeyHex,
		CreatedAt: w.CreatedAt,
		Primary:   true,
	}}
	if w.Version < 2 {
		w.Version = 2
	}
}

func (w *WalletFile) ensurePrimaryUnique() {
	var seen bool
	for i := range w.Addresses {
		if w.Addresses[i].Primary {
			if seen {
				w.Addresses[i].Primary = false
			} else {
				seen = true
			}
		}
	}
	if !seen && len(w.Addresses) > 0 {
		w.Addresses[0].Primary = true
	}
}

// AddressByID returns a pointer to the address with id, or nil.
func (w *WalletFile) AddressByID(id string) *WalletAddress {
	id = strings.TrimSpace(id)
	for i := range w.Addresses {
		if w.Addresses[i].ID == id {
			return &w.Addresses[i]
		}
	}
	return nil
}

func (w *WalletFile) setPrimary(id string) error {
	id = strings.TrimSpace(id)
	found := false
	for i := range w.Addresses {
		match := w.Addresses[i].ID == id
		w.Addresses[i].Primary = match
		if match {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("address id not found")
	}
	w.syncLegacyFromPrimary()
	return nil
}
