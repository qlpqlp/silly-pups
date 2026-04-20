package main

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	bip32 "github.com/tyler-smith/go-bip32"
)

const (
	hdModeBIP44Doge = "bip44-dogecoin-v1"
	hardOffset      = 0x80000000
)

func base58CheckEncode(version byte, payload []byte) string {
	data := append([]byte{version}, payload...)
	h1 := sha256.Sum256(data)
	h2 := sha256.Sum256(h1[:])
	full := append(data, h2[:4]...)
	return base58.Encode(full)
}

func dogeWIFFromPriv(priv32 []byte, testnet bool) string {
	version := byte(0x9e)
	if testnet {
		version = 0xf1
	}
	b := append([]byte{}, priv32...)
	// Use compressed key form so libdogecoin emits compressed pubkey/address.
	b = append(b, 0x01)
	return base58CheckEncode(version, b)
}

func deriveBIP44ChildKey(master *bip32.Key, index uint32) (*bip32.Key, string, error) {
	purpose, err := master.NewChildKey(uint32(44) + hardOffset)
	if err != nil {
		return nil, "", err
	}
	coin, err := purpose.NewChildKey(uint32(3) + hardOffset) // Dogecoin SLIP-44 coin type
	if err != nil {
		return nil, "", err
	}
	acct, err := coin.NewChildKey(0 + hardOffset)
	if err != nil {
		return nil, "", err
	}
	chg, err := acct.NewChildKey(0)
	if err != nil {
		return nil, "", err
	}
	child, err := chg.NewChildKey(index)
	if err != nil {
		return nil, "", err
	}
	path := fmt.Sprintf("m/44'/3'/0'/0/%d", index)
	return child, path, nil
}

func (s *Server) deriveHDWalletAddress(wf *WalletFile, index int) (WalletAddress, error) {
	if wf == nil || !wf.isHDWallet() {
		return WalletAddress{}, fmt.Errorf("wallet is not HD-enabled")
	}
	if index < 0 {
		return WalletAddress{}, fmt.Errorf("invalid derive index")
	}
	master, err := bip32.B58Deserialize(strings.TrimSpace(wf.HDMasterXPrv))
	if err != nil {
		return WalletAddress{}, fmt.Errorf("decode hd master: %w", err)
	}
	child, path, err := deriveBIP44ChildKey(master, uint32(index))
	if err != nil {
		return WalletAddress{}, fmt.Errorf("derive child key: %w", err)
	}
	testnet := strings.EqualFold(wf.Network, "testnet")
	wif := dogeWIFFromPriv(child.Key, testnet)
	pubHex, addr, err := s.runSuchPubKeyFromWIF(wif, testnet)
	if err != nil {
		return WalletAddress{}, err
	}
	return WalletAddress{
		ID:          newAddressID(),
		Label:       fmt.Sprintf("Receive #%d", index+1),
		P2PKH:       strings.TrimSpace(addr),
		WIF:         strings.TrimSpace(wif),
		PubHex:      strings.TrimSpace(pubHex),
		CreatedAt:   time.Now().UTC(),
		Primary:     false,
		HDPath:      path,
		DeriveIndex: index,
	}, nil
}

func (s *Server) initHDWallet(testnet bool) (*WalletFile, error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	master, err := bip32.NewMasterKey(seed)
	if err != nil {
		return nil, err
	}
	network := "mainnet"
	if testnet {
		network = "testnet"
	}
	now := time.Now().UTC()
	w := &WalletFile{
		Version:   2,
		CreatedAt: now,
		Network:   network,
		PQScheme:  "Falcon-512 / Dilithium2 / Raccoon-G (liboqs via libdogecoin, experimental)",
		PQSource:  "none",
		PQNotes: "Post-quantum proofs attach to ordinary Dogecoin transactions: TX_C adds an OP_RETURN " +
			"commitment; an optional 1-DOGE carrier output can be spent in TX_R to reveal the full PQ " +
			"public key and signature on-chain. Standard P2PKH keys above fund and control DOGE; PQ " +
			"material is additional attestation per Dogecoin Foundation experiments.",
		LibdogecoinSPV: "Bundled spvnode: headers + BIP37 watch for every address in the wallet. Starts after wallet creation when SPVNODE_ENABLE=1. " +
			"Check GET /api/spv/status and GET /api/logs/spv.",
		ExperimentalDiscl: "Experimental research software. You may lose funds. Back up your WIF. " +
			"PQ proofs on mainnet are early-phase; verify any third-party tooling.",
		HDMode:       hdModeBIP44Doge,
		HDMasterXPrv: master.B58Serialize(),
		HDMasterXPub: master.PublicKey().B58Serialize(),
		HDNextIndex:  0,
	}
	a0, err := s.deriveHDWalletAddress(w, 0)
	if err != nil {
		return nil, err
	}
	a0.Primary = true
	a0.Label = "Primary"
	w.Addresses = []WalletAddress{a0}
	w.HDNextIndex = 1
	w.syncLegacyFromPrimary()
	return w, nil
}

func (s *Server) appendNextHDDerivedAddress(wf *WalletFile, makePrimary bool) (WalletAddress, error) {
	if wf == nil || !wf.isHDWallet() {
		return WalletAddress{}, fmt.Errorf("wallet is not HD-enabled")
	}
	if wf.HDNextIndex < 0 {
		wf.HDNextIndex = len(wf.Addresses)
	}
	a, err := s.deriveHDWalletAddress(wf, wf.HDNextIndex)
	if err != nil {
		return WalletAddress{}, err
	}
	wf.HDNextIndex++
	wf.Addresses = append(wf.Addresses, a)
	if makePrimary {
		for i := range wf.Addresses {
			wf.Addresses[i].Primary = wf.Addresses[i].ID == a.ID
		}
		wf.syncLegacyFromPrimary()
	}
	return a, nil
}

func (s *Server) maybeRotateHDReceiveAddress(wf *WalletFile, st *WalletState) (bool, error) {
	if wf == nil || st == nil || !wf.isHDWallet() {
		return false, nil
	}
	p := wf.PrimaryAddress()
	if p == nil {
		return false, nil
	}
	currentAddr := strings.TrimSpace(p.P2PKH)
	if currentAddr == "" {
		return false, nil
	}
	latestIncomingTxID := ""
	for _, tx := range st.Transactions {
		if !strings.EqualFold(strings.TrimSpace(tx.Direction), "in") {
			continue
		}
		if strings.TrimSpace(tx.Address) != currentAddr {
			continue
		}
		if normalizeTxid(tx.Txid) == "" {
			continue
		}
		if latestIncomingTxID == "" || tx.SeenAt.After(stampOfTx(st.Transactions, latestIncomingTxID)) {
			latestIncomingTxID = normalizeTxid(tx.Txid)
		}
	}
	if latestIncomingTxID == "" || latestIncomingTxID == strings.TrimSpace(wf.HDLastRotateTxID) {
		return false, nil
	}
	if _, err := s.appendNextHDDerivedAddress(wf, true); err != nil {
		return false, err
	}
	wf.HDLastRotateTxID = latestIncomingTxID
	return true, nil
}

func stampOfTx(rows []TxRecord, txid string) time.Time {
	id := normalizeTxid(txid)
	for _, tx := range rows {
		if normalizeTxid(tx.Txid) == id {
			return tx.SeenAt
		}
	}
	return time.Time{}
}
