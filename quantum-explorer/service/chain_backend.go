package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IndexedBlockHeader is a block header learned from spvnode logs (not a full block).
type IndexedBlockHeader struct {
	Height    int    `json:"height"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp,omitempty"`
	RawSource string `json:"raw_source,omitempty"`
}

// chainBackend stores SPV-derived headers and heuristic block→tx links.
// PostgreSQL implementations support concurrent HTTP readers + writer; JSON backend uses RWMutex.
type chainBackend interface {
	Kind() string
	Close() error
	UpsertHeader(height int, hash, timestamp, rawSource string) (changed bool, err error)
	AppendTxLinkIfNew(height int, txid string) (inserted bool, err error)
	GetByHash(hash string) (*IndexedBlockHeader, error)
	GetByHeight(height int) (*IndexedBlockHeader, error)
	ListTxidsForHeight(height int) ([]string, error)
	Summary() (headerCount, tipHeight, txLinkHeights int64, err error)
	RecentHeaders(limit int) ([]IndexedBlockHeader, error)
	Persist() error
}

// --- JSON file backend (single-writer friendly; mutex for concurrent reads) ---

type jsonChainBackend struct {
	mu         sync.RWMutex
	path       string
	byHeight   map[int]*IndexedBlockHeader
	byHash     map[string]*IndexedBlockHeader
	heightTxs  map[int][]string
	dirty      bool
}

type chainIndexFile struct {
	Headers  map[string]IndexedBlockHeader `json:"headers"`
	BlockTxs map[string][]string           `json:"block_txs,omitempty"`
}

func newJSONChainBackend(jsonPath string) *jsonChainBackend {
	b := &jsonChainBackend{
		path:      jsonPath,
		byHeight:  map[int]*IndexedBlockHeader{},
		byHash:    map[string]*IndexedBlockHeader{},
		heightTxs: map[int][]string{},
	}
	b.loadUnlocked()
	return b
}

func (b *jsonChainBackend) Kind() string { return "json-file" }

func (b *jsonChainBackend) Close() error { return nil }

func (b *jsonChainBackend) loadUnlocked() {
	raw, err := os.ReadFile(b.path)
	if err != nil || len(raw) == 0 {
		return
	}
	var f chainIndexFile
	if json.Unmarshal(raw, &f) != nil || f.Headers == nil {
		return
	}
	for hs, bh := range f.Headers {
		h, err := strconv.Atoi(hs)
		if err != nil || h <= 0 {
			continue
		}
		cp := bh
		cp.Hash = strings.ToLower(strings.TrimSpace(cp.Hash))
		b.byHeight[h] = &cp
		if cp.Hash != "" {
			b.byHash[cp.Hash] = &cp
		}
	}
	if f.BlockTxs != nil {
		for hs, txs := range f.BlockTxs {
			h, err := strconv.Atoi(hs)
			if err != nil || h <= 0 {
				continue
			}
			seen := map[string]struct{}{}
			for _, tx := range txs {
				t := strings.ToLower(strings.TrimSpace(tx))
				if len(t) != 64 || !isHex64String(t) {
					continue
				}
				if _, ok := seen[t]; ok {
					continue
				}
				seen[t] = struct{}{}
				b.heightTxs[h] = append(b.heightTxs[h], t)
			}
		}
	}
}

func (b *jsonChainBackend) Persist() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.dirty {
		return nil
	}
	f := chainIndexFile{Headers: map[string]IndexedBlockHeader{}, BlockTxs: map[string][]string{}}
	for h, bh := range b.byHeight {
		if bh == nil {
			continue
		}
		f.Headers[strconv.Itoa(h)] = *bh
	}
	for h, txs := range b.heightTxs {
		if len(txs) == 0 {
			continue
		}
		f.BlockTxs[strconv.Itoa(h)] = append([]string(nil), txs...)
	}
	if err := saveJSON(b.path, f); err != nil {
		return err
	}
	b.dirty = false
	return nil
}

func (b *jsonChainBackend) UpsertHeader(height int, hash, timestamp, rawSource string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	hash = strings.ToLower(strings.TrimSpace(hash))
	changed := false
	bp := b.byHeight[height]
	if bp == nil {
		bp = &IndexedBlockHeader{Height: height}
		b.byHeight[height] = bp
		changed = true
	}
	if hash != "" && bp.Hash != hash {
		if bp.Hash != "" {
			delete(b.byHash, bp.Hash)
		}
		bp.Hash = hash
		b.byHash[hash] = bp
		changed = true
	}
	if timestamp != "" && bp.Timestamp != timestamp {
		bp.Timestamp = timestamp
		changed = true
	}
	if rawSource != "" && bp.RawSource != rawSource {
		bp.RawSource = rawSource
		changed = true
	}
	if changed {
		b.dirty = true
	}
	return changed, nil
}

func (b *jsonChainBackend) AppendTxLinkIfNew(height int, txid string) (bool, error) {
	if height <= 0 || len(txid) != 64 || !isHex64String(txid) {
		return false, nil
	}
	txid = strings.ToLower(txid)
	b.mu.Lock()
	defer b.mu.Unlock()
	if bh := b.byHeight[height]; bh != nil && bh.Hash == txid {
		return false, nil
	}
	for _, x := range b.heightTxs[height] {
		if x == txid {
			return false, nil
		}
	}
	b.heightTxs[height] = append(b.heightTxs[height], txid)
	b.dirty = true
	return true, nil
}

func (b *jsonChainBackend) GetByHash(hash string) (*IndexedBlockHeader, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	h := strings.ToLower(strings.TrimSpace(hash))
	bp := b.byHash[h]
	if bp == nil {
		return nil, nil
	}
	cp := *bp
	return &cp, nil
}

func (b *jsonChainBackend) GetByHeight(height int) (*IndexedBlockHeader, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	bp := b.byHeight[height]
	if bp == nil {
		return nil, nil
	}
	cp := *bp
	return &cp, nil
}

func (b *jsonChainBackend) ListTxidsForHeight(height int) ([]string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]string(nil), b.heightTxs[height]...), nil
}

func (b *jsonChainBackend) Summary() (int64, int64, int64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var tip int
	for h := range b.byHeight {
		if h > tip {
			tip = h
		}
	}
	return int64(len(b.byHeight)), int64(tip), int64(len(b.heightTxs)), nil
}

func (b *jsonChainBackend) RecentHeaders(limit int) ([]IndexedBlockHeader, error) {
	if limit <= 0 {
		limit = 12
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	hs := make([]int, 0, len(b.byHeight))
	for h := range b.byHeight {
		hs = append(hs, h)
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i] > hs[j] })
	out := make([]IndexedBlockHeader, 0, limit)
	for _, h := range hs {
		if bh := b.byHeight[h]; bh != nil {
			out = append(out, *bh)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func openChainBackend(storageDir string) (chainBackend, error) {
	dsn := strings.TrimSpace(env("QE_POSTGRES_URL", ""))
	jsonPath := filepath.Join(storageDir, "qe-chain-index.json")
	if dsn != "" {
		return newPostgresChainBackend(dsn, jsonPath)
	}
	return newJSONChainBackend(jsonPath), nil
}

func nowUnix() int64 {
	return time.Now().Unix()
}
