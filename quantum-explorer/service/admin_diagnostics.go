package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

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
		"QE_POSTGRES_URL", "QE_EXPLORER_TX_API",
		"QE_CORE_RPC_URL", "QE_CORE_RPC_HOST", "QE_CORE_RPC_PORT", "QE_CORE_RPC_USER",
		"QE_CORE_ZMQ_RAWTX", "QE_CORE_ZMQ_HASHBLOCK", "QE_CORE_RPC_TIMEOUT_MS",
		"QE_CORE_INDEXER_AUTO_START", "QE_CORE_BACKFILL_BATCH", "QE_CORE_RAW_BACKFILL_BATCH", "QE_CORE_START_HEIGHT",
		"QE_PUBLIC_API_ALLOWLIST", "QE_PUBLIC_PROTECTED_PATHS",
		"QE_PUBLIC_RATE_LIMIT", "QE_PUBLIC_RATE_WINDOW_SEC",
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
		case "QE_CORE_RPC_URL":
			out[k] = redactDatabaseURL(v)
		case "QE_CORE_RPC_USER":
			out[k] = "(set)"
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
	if strings.TrimSpace(os.Getenv("QE_PUBLIC_API_TOKEN")) == "" {
		out["QE_PUBLIC_API_TOKEN"] = "(empty)"
	} else {
		out["QE_PUBLIC_API_TOKEN"] = "(set)"
	}
	return out
}

func (a *app) adminDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}

	chainDiag := a.chain.AdminDiagnostics()
	coreIndexerDiag := a.coreIndexerStatus()
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		coreIndexerDiag = a.cidx.summary(ctx)
		cancel()
	}

	pqtxStat := map[string]any{"path": a.storePath}
	if st, err := os.Stat(a.storePath); err != nil {
		pqtxStat["stat_error"] = err.Error()
	} else {
		pqtxStat["size_bytes"] = st.Size()
		pqtxStat["mod_time"] = st.ModTime().UTC().Format(time.RFC3339)
	}

	writeJSON(w, 200, map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"storage_dir":  a.storageDir,
		"config_path":  a.cfgPath,
		"pqtx_store":   pqtxStat,
		"env":          adminEnvSnapshot(),
		"chain_index":  chainDiag,
		"core_rpc":     a.core.snapshot(),
		"core_indexer": coreIndexerDiag,
	})
}
