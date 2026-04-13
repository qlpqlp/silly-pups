package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const adminLogTailMax = 96 << 10

func tailFileLastBytes(path string, max int) (content string, readErr string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err.Error()
	}
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return string(b), ""
}

func listDirBrief(dir string, maxFiles int) []map[string]any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []map[string]any{{"read_error": err.Error()}}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]map[string]any, 0, min(len(entries), maxFiles)+1)
	n := len(entries)
	for i, e := range entries {
		if i >= maxFiles {
			out = append(out, map[string]any{"note": "listing_truncated", "total_entries": n})
			break
		}
		fi, err := e.Info()
		row := map[string]any{"name": e.Name(), "is_dir": e.IsDir()}
		if err == nil {
			row["size"] = fi.Size()
			row["mod_time"] = fi.ModTime().UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}

func redactDatabaseURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	schemeEnd := strings.Index(s, "://")
	if schemeEnd < 0 {
		return "(non-url; hidden)"
	}
	rest := s[schemeEnd+3:]
	at := strings.LastIndex(rest, "@")
	if at <= 0 {
		return s
	}
	hostPart := rest[at+1:]
	cred := rest[:at]
	colon := strings.Index(cred, ":")
	if colon > 0 {
		user := cred[:colon]
		return s[:schemeEnd+3] + user + ":***@" + hostPart
	}
	return s[:schemeEnd+3] + "***@" + hostPart
}

func adminEnvSnapshot() map[string]any {
	keys := []string{
		"QE_STORAGE_DIR", "PUBLIC_PORT", "NETWORK",
		"QE_SPV_AUTO_START", "QE_SPV_USE_CHECKPOINT", "QE_SPV_WATCH_ADDRESS",
		"QE_POSTGRES_URL", "LIBDOGECOIN_SPVNODE", "QE_EXPLORER_TX_API",
	}
	out := make(map[string]any, len(keys)+1)
	for _, k := range keys {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			out[k] = ""
			continue
		}
		switch k {
		case "QE_POSTGRES_URL":
			out[k] = redactDatabaseURL(v)
		case "QE_EXPLORER_TX_API":
			if len(v) > 160 {
				out[k] = v[:160] + "…"
			} else {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	if strings.TrimSpace(os.Getenv("QE_ADMIN_TOKEN")) == "" {
		out["QE_ADMIN_TOKEN"] = "(empty; default may apply)"
	} else {
		out["QE_ADMIN_TOKEN"] = "(set)"
	}
	return out
}

func (a *app) adminDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	spvLogPath := filepath.Join(a.storageDir, "spv.log")
	tail, tailReadErr := tailFileLastBytes(spvLogPath, adminLogTailMax)
	memDir := filepath.Join(a.storageDir, "mempool")
	memFiles := listDirBrief(memDir, 50)

	a.mu.RLock()
	lastIngestErr := a.lastSPVLogErr
	memRun := a.engRunning
	memStartErr := a.mempoolStartErr
	spvRun := a.spvRunning
	spvStartErr := a.spvStartErr
	eng := a.eng
	a.mu.RUnlock()

	var engSnap map[string]any
	if eng != nil {
		n, live, _, wc := eng.DashboardSnapshot()
		engSnap = map[string]any{
			"live_mempool_map_size": n,
			"dashboard_rows":        len(live),
			"p2p_workers_connected": wc,
		}
	}

	chainDiag := a.chain.AdminDiagnostics()

	pqtxStat := map[string]any{"path": a.storePath}
	if st, err := os.Stat(a.storePath); err != nil {
		pqtxStat["stat_error"] = err.Error()
	} else {
		pqtxStat["size_bytes"] = st.Size()
		pqtxStat["mod_time"] = st.ModTime().UTC().Format(time.RFC3339)
	}

	spvPayload := map[string]any{
		"running":     spvRun,
		"log_path":    spvLogPath,
		"log_tail":    tail,
		"tail_max":    adminLogTailMax,
		"start_error": emptyOrString(spvStartErr),
	}
	if tailReadErr != "" {
		spvPayload["log_read_error"] = tailReadErr
	}

	writeJSON(w, 200, map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"storage_dir":  a.storageDir,
		"config_path":  a.cfgPath,
		"pqtx_store":   pqtxStat,
		"env":          adminEnvSnapshot(),
		"mempool_tracker": map[string]any{
			"running":         memRun,
			"start_error":     emptyOrString(memStartErr),
			"storage_dir":     memDir,
			"storage_listing": memFiles,
			"engine":          engSnap,
		},
		"spv": spvPayload,
		"spv_chain_refresh": map[string]any{
			"last_spv_log_read_error": emptyOrString(lastIngestErr),
		},
		"chain_index": chainDiag,
	})
}

func emptyOrString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func (a *app) adminRestartMempool(w http.ResponseWriter) {
	a.stopMempool()
	time.Sleep(400 * time.Millisecond)
	if err := a.startMempool(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "note": "mempool tracker restarted"})
}

func (a *app) adminRestartSPV(w http.ResponseWriter) {
	a.stopSPV()
	time.Sleep(400 * time.Millisecond)
	if err := a.startSPV(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "note": "spvnode restarted"})
}
