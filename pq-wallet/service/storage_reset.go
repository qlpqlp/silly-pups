package main

import (
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) stopMempoolEngineIfRunning() {
	s.mempoolMu.Lock()
	defer s.mempoolMu.Unlock()
	if s.mempoolEngine != nil {
		s.mempoolEngine.Stop()
		s.mempoolEngine = nil
	}
}

func removeLegacyHeaderBackups(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "headers.db.legacy.") {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// removeHeadersDBAndLegacy removes the main headers chain file and pq-wallet legacy rename backups.
func (s *Server) removeHeadersDBAndLegacy() {
	dir := strings.TrimSpace(s.storageDir)
	if dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, "headers.db"))
	removeLegacyHeaderBackups(dir)
}

// wipeAuxiliaryWalletRuntimeState clears libdogecoin SPV wallet DB, watch files, UI tx/metrics cache,
// SPV logs, broadcast log, and embedded mempooltracker storage. Does not remove wallet.json / sealed
// wallet, spv_sync_prefs.json, or service_prefs.json. Caller should stop spvnode (and usually headers)
// before calling so files are not held open.
func (s *Server) wipeAuxiliaryWalletRuntimeState() {
	s.stopMempoolEngineIfRunning()
	dir := strings.TrimSpace(s.storageDir)
	if dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, "spv_wallet.db"))
	_ = os.Remove(s.spvWatchAddrPath())
	if s.watchPath != "" {
		_ = os.Remove(s.watchPath)
	}
	_ = os.Remove(s.statePath())
	_ = os.Remove(s.statePath() + ".tmp")
	_ = os.Remove(filepath.Join(dir, "spv.log"))
	_ = os.Remove(filepath.Join(dir, "spv.pid"))
	_ = os.Remove(filepath.Join(dir, "broadcast.log"))
	_ = os.RemoveAll(filepath.Join(dir, "mempooltracker"))
}

// resetLocalChainForWalletImport stops SPV/mempool, removes headers + legacy backups, and wipes auxiliary state
// so a restored wallet starts with a clean chain + cache surface.
func (s *Server) resetLocalChainForWalletImport() {
	s.stopSPVNode()
	s.removeHeadersDBAndLegacy()
	s.wipeAuxiliaryWalletRuntimeState()
}
