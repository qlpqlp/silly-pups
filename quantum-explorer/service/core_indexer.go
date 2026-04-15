package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

type coreIndexer struct {
	mu          sync.RWMutex
	db          *sql.DB
	rpc         *coreRPCClient
	network     string
	running     bool
	lastErr     string
	lastHeight  int64
	tipHeight   int64
	batch       int
	startHeight int64
}

func newCoreIndexer(db *sql.DB, rpc *coreRPCClient, network string) *coreIndexer {
	batch := 20
	if n, err := strconv.Atoi(strings.TrimSpace(env("QE_CORE_BACKFILL_BATCH", "20"))); err == nil && n > 0 && n <= 500 {
		batch = n
	}
	start := int64(6147099)
	if n, err := strconv.ParseInt(strings.TrimSpace(env("QE_CORE_START_HEIGHT", "6147099")), 10, 64); err == nil && n >= 0 {
		start = n
	}
	return &coreIndexer{
		db:          db,
		rpc:         rpc,
		network:     strings.ToLower(strings.TrimSpace(network)),
		batch:       batch,
		startHeight: start,
	}
}

func (ix *coreIndexer) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS qe_core_blocks (
			height BIGINT PRIMARY KEY,
			hash TEXT NOT NULL UNIQUE,
			time_unix BIGINT NOT NULL DEFAULT 0,
			tx_count INTEGER NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS qe_core_txs (
			txid TEXT PRIMARY KEY,
			block_height BIGINT NOT NULL,
			block_hash TEXT NOT NULL DEFAULT '',
			time_unix BIGINT NOT NULL DEFAULT 0,
			quantum_state TEXT NOT NULL DEFAULT 'non-quantum',
			pq_reason TEXT NOT NULL DEFAULT '',
			value_out_sats BIGINT NOT NULL DEFAULT 0,
			raw_hex TEXT NOT NULL DEFAULT '',
			created_at BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS qe_core_txs_block_height ON qe_core_txs (block_height DESC)`,
		`CREATE INDEX IF NOT EXISTS qe_core_txs_quantum_state ON qe_core_txs (quantum_state)`,
		`CREATE INDEX IF NOT EXISTS qe_core_txs_time_unix ON qe_core_txs (time_unix DESC)`,
		`CREATE INDEX IF NOT EXISTS qe_core_txs_missing_raw ON qe_core_txs (block_height ASC) WHERE LENGTH(TRIM(COALESCE(raw_hex,'')))=0`,
		`CREATE TABLE IF NOT EXISTS qe_core_addresses (
			address TEXT NOT NULL,
			txid TEXT NOT NULL,
			vout_n INTEGER NOT NULL DEFAULT 0,
			value_sats BIGINT NOT NULL DEFAULT 0,
			block_height BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (address, txid, vout_n)
		)`,
		`CREATE INDEX IF NOT EXISTS qe_core_addresses_txid ON qe_core_addresses (txid)`,
		`CREATE INDEX IF NOT EXISTS qe_core_addresses_height ON qe_core_addresses (block_height DESC)`,
		`CREATE INDEX IF NOT EXISTS qe_core_addresses_address_height ON qe_core_addresses (address, block_height DESC)`,
		`CREATE TABLE IF NOT EXISTS qe_core_indexer_state (
			id SMALLINT PRIMARY KEY,
			last_height BIGINT NOT NULL DEFAULT 0,
			tip_height BIGINT NOT NULL DEFAULT 0,
			updated_at BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS qe_core_hourly_metrics (
			hour_unix BIGINT PRIMARY KEY,
			all_count BIGINT NOT NULL DEFAULT 0,
			quantum_count BIGINT NOT NULL DEFAULT 0,
			non_quantum_count BIGINT NOT NULL DEFAULT 0,
			invalid_quantum_count BIGINT NOT NULL DEFAULT 0,
			updated_at BIGINT NOT NULL DEFAULT 0
		)`,
		`INSERT INTO qe_core_indexer_state (id, last_height, tip_height, updated_at)
		 VALUES (1, 0, 0, 0) ON CONFLICT (id) DO NOTHING`,
	}
	for _, s := range stmts {
		if _, err := ix.db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (ix *coreIndexer) loadState(ctx context.Context) error {
	return ix.db.QueryRowContext(ctx, `SELECT last_height, tip_height FROM qe_core_indexer_state WHERE id=1`).Scan(&ix.lastHeight, &ix.tipHeight)
}

func (ix *coreIndexer) saveState(ctx context.Context) error {
	_, err := ix.db.ExecContext(ctx, `UPDATE qe_core_indexer_state SET last_height=$1, tip_height=$2, updated_at=$3 WHERE id=1`,
		ix.lastHeight, ix.tipHeight, time.Now().Unix())
	return err
}

func (ix *coreIndexer) start() error {
	ix.mu.Lock()
	if ix.running {
		ix.mu.Unlock()
		return nil
	}
	if ix.db == nil {
		ix.lastErr = "core indexer requires postgres backend"
		ix.mu.Unlock()
		return fmt.Errorf(ix.lastErr)
	}
	if ix.rpc == nil || !ix.rpc.enabled() {
		ix.lastErr = "core rpc is not configured"
		ix.mu.Unlock()
		return fmt.Errorf(ix.lastErr)
	}
	ix.running = true
	ix.mu.Unlock()
	go ix.loop()
	return nil
}

func (ix *coreIndexer) stop() {
	ix.mu.Lock()
	ix.running = false
	ix.mu.Unlock()
}

func (ix *coreIndexer) isRunning() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.running
}

func (ix *coreIndexer) loop() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := ix.ensureSchema(ctx); err != nil {
		ix.mu.Lock()
		ix.lastErr = err.Error()
		ix.running = false
		ix.mu.Unlock()
		cancel()
		return
	}
	_ = ix.loadState(ctx)
	cancel()
	if ix.lastHeight < ix.startHeight {
		ix.lastHeight = ix.startHeight
	}
	hourlySeeded := false

	for {
		ix.mu.RLock()
		running := ix.running
		ix.mu.RUnlock()
		if !running {
			return
		}
		if !hourlySeeded {
			if err := ix.refreshHourlyMetrics(72); err == nil {
				hourlySeeded = true
			}
		}

		ctxTip, cancelTip := context.WithTimeout(context.Background(), 12*time.Second)
		var tip int64
		err := ix.rpc.call(ctxTip, "getblockcount", []any{}, &tip)
		cancelTip()
		if err != nil {
			ix.mu.Lock()
			ix.lastErr = err.Error()
			ix.mu.Unlock()
			time.Sleep(4 * time.Second)
			continue
		}

		ix.mu.Lock()
		ix.tipHeight = tip
		cur := ix.lastHeight
		batch := ix.batch
		ix.mu.Unlock()
		if cur > 0 {
			if err := ix.handlePossibleReorg(cur); err != nil {
				ix.mu.Lock()
				ix.lastErr = "reorg check: " + err.Error()
				ix.mu.Unlock()
				time.Sleep(2 * time.Second)
				continue
			}
			ix.mu.RLock()
			cur = ix.lastHeight
			ix.mu.RUnlock()
		}

		if cur >= tip {
			_ = ix.persistState()
			bfCtx, bfCancel := context.WithTimeout(context.Background(), 90*time.Second)
			filled := ix.backfillMissingRawHexBatch(bfCtx)
			bfCancel()
			if filled > 0 {
				_ = ix.refreshHourlyMetrics(72)
				log.Printf("[quantum-explorer] stored raw tx backfill: %d txs (postgres)", filled)
			}
			time.Sleep(3 * time.Second)
			continue
		}
		end := cur + int64(batch)
		if end > tip {
			end = tip
		}
		for h := cur + 1; h <= end; h++ {
			if err := ix.indexHeight(h); err != nil {
				ix.mu.Lock()
				ix.lastErr = fmt.Sprintf("height %d: %v", h, err)
				ix.mu.Unlock()
				time.Sleep(2 * time.Second)
				break
			}
			ix.mu.Lock()
			ix.lastHeight = h
			ix.lastErr = ""
			ix.mu.Unlock()
			_ = ix.refreshHourlyMetrics(72)
		}
		_ = ix.persistState()
	}
}

func (ix *coreIndexer) persistState() error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return ix.saveState(ctx)
}

func (ix *coreIndexer) indexHeight(height int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var hash string
	if err := ix.rpc.call(ctx, "getblockhash", []any{height}, &hash); err != nil {
		return err
	}
	var block map[string]any
	if err := ix.rpc.call(ctx, "getblock", []any{hash, 2}, &block); err != nil {
		return err
	}
	bhash := strings.ToLower(strings.TrimSpace(anyString(block["hash"])))
	btime := anyInt64(block["time"])
	txs, _ := block["tx"].([]any)
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `INSERT INTO qe_core_blocks (height, hash, time_unix, tx_count, created_at)
	VALUES ($1,$2,$3,$4,$5)
	ON CONFLICT (height) DO UPDATE SET hash=EXCLUDED.hash, time_unix=EXCLUDED.time_unix, tx_count=EXCLUDED.tx_count`,
		height, bhash, btime, len(txs), time.Now().Unix())
	if err != nil {
		return err
	}
	for _, tv := range txs {
		tm, ok := tv.(map[string]any)
		if !ok {
			continue
		}
		txid := strings.ToLower(strings.TrimSpace(anyString(tm["txid"])))
		if len(txid) != 64 || !isHex64String(txid) {
			continue
		}
		rawHex := strings.TrimSpace(anyString(tm["hex"]))
		if rawHex == "" && ix.rpc != nil && ix.rpc.enabled() {
			for attempt := 0; attempt < 2; attempt++ {
				if attempt > 0 {
					time.Sleep(100 * time.Millisecond)
				}
				if h, err := ix.rpc.getRawTransactionHex(ctx, txid, bhash); err == nil {
					rawHex = strings.TrimSpace(h)
					if rawHex != "" {
						break
					}
				}
			}
		}
		state := "non-quantum"
		reason := ""
		if rawHex != "" {
			ok, why, _, _ := verifyPQStrict(rawHex)
			if ok {
				state = "quantum"
			} else {
				state = "invalid-quantum"
				reason = why
			}
		}
		totalOut := int64(0)
		vouts := anySlice(tm["vout"])
		for _, vv := range vouts {
			vm, ok := vv.(map[string]any)
			if !ok {
				continue
			}
			sats := dogeToSats(anyFloat64(vm["value"]))
			totalOut += sats
			n := int(anyInt64(vm["n"]))
			spk, _ := vm["scriptPubKey"].(map[string]any)
			addrs := scriptAddresses(spk)
			for _, ad := range addrs {
				ad = strings.TrimSpace(ad)
				if ad == "" {
					continue
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO qe_core_addresses (address, txid, vout_n, value_sats, block_height)
				VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, ad, txid, n, sats, height)
				if err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO qe_core_txs (txid, block_height, block_hash, time_unix, quantum_state, pq_reason, value_out_sats, raw_hex, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (txid) DO UPDATE SET block_height=EXCLUDED.block_height, block_hash=EXCLUDED.block_hash, time_unix=EXCLUDED.time_unix,
			value_out_sats=EXCLUDED.value_out_sats,
			quantum_state=(CASE WHEN LENGTH(TRIM(COALESCE(EXCLUDED.raw_hex,'')))>0 THEN EXCLUDED.quantum_state ELSE qe_core_txs.quantum_state END),
			pq_reason=(CASE WHEN LENGTH(TRIM(COALESCE(EXCLUDED.raw_hex,'')))>0 THEN EXCLUDED.pq_reason ELSE qe_core_txs.pq_reason END),
			raw_hex=(CASE WHEN LENGTH(TRIM(COALESCE(EXCLUDED.raw_hex,'')))>0 THEN EXCLUDED.raw_hex ELSE qe_core_txs.raw_hex END)`,
			txid, height, bhash, btime, state, reason, totalOut, rawHex, time.Now().Unix())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (ix *coreIndexer) handlePossibleReorg(curHeight int64) error {
	if curHeight <= 0 {
		return nil
	}
	dbHash, err := ix.blockHashAtHeight(curHeight)
	if err != nil {
		return err
	}
	if dbHash == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var coreHash string
	if err := ix.rpc.call(ctx, "getblockhash", []any{curHeight}, &coreHash); err != nil {
		return err
	}
	coreHash = strings.ToLower(strings.TrimSpace(coreHash))
	if coreHash == dbHash {
		return nil
	}
	rollbackTo := curHeight
	for h := curHeight - 1; h >= 0; h-- {
		dbh, err := ix.blockHashAtHeight(h)
		if err != nil {
			return err
		}
		if dbh == "" {
			rollbackTo = h + 1
			break
		}
		var ch string
		if err := ix.rpc.call(ctx, "getblockhash", []any{h}, &ch); err != nil {
			return err
		}
		ch = strings.ToLower(strings.TrimSpace(ch))
		if ch == dbh {
			rollbackTo = h + 1
			break
		}
		if h == 0 {
			rollbackTo = 0
		}
	}
	if err := ix.rollbackFrom(rollbackTo); err != nil {
		return err
	}
	ix.mu.Lock()
	if ix.lastHeight >= rollbackTo {
		ix.lastHeight = rollbackTo - 1
		if ix.lastHeight < 0 {
			ix.lastHeight = 0
		}
	}
	ix.lastErr = fmt.Sprintf("reorg handled: rollback from height %d", rollbackTo)
	ix.mu.Unlock()
	return ix.persistState()
}

func (ix *coreIndexer) blockHashAtHeight(height int64) (string, error) {
	var h string
	err := ix.db.QueryRow(`SELECT hash FROM qe_core_blocks WHERE height=$1`, height).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return strings.ToLower(strings.TrimSpace(h)), err
}

func (ix *coreIndexer) rollbackFrom(height int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM qe_core_addresses WHERE block_height >= $1`, height); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM qe_core_txs WHERE block_height >= $1`, height); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM qe_core_blocks WHERE height >= $1`, height); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM qe_core_hourly_metrics`); err != nil {
		return err
	}
	return tx.Commit()
}

func (ix *coreIndexer) refreshHourlyMetrics(hours int) error {
	if hours < 1 {
		hours = 24
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Unix()
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM qe_core_hourly_metrics WHERE hour_unix >= $1`, cutoff-(cutoff%3600)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO qe_core_hourly_metrics (hour_unix, all_count, quantum_count, non_quantum_count, invalid_quantum_count, updated_at)
		SELECT (time_unix / 3600) * 3600 AS hour_unix,
		       COUNT(*)::bigint AS all_count,
		       SUM(CASE WHEN quantum_state='quantum' THEN 1 ELSE 0 END)::bigint AS quantum_count,
		       SUM(CASE WHEN quantum_state='non-quantum' THEN 1 ELSE 0 END)::bigint AS non_quantum_count,
		       SUM(CASE WHEN quantum_state='invalid-quantum' THEN 1 ELSE 0 END)::bigint AS invalid_quantum_count,
		       $2::bigint AS updated_at
		FROM qe_core_txs
		WHERE time_unix >= $1
		GROUP BY (time_unix / 3600)
		ON CONFLICT (hour_unix) DO UPDATE SET
		  all_count=EXCLUDED.all_count,
		  quantum_count=EXCLUDED.quantum_count,
		  non_quantum_count=EXCLUDED.non_quantum_count,
		  invalid_quantum_count=EXCLUDED.invalid_quantum_count,
		  updated_at=EXCLUDED.updated_at
	`, cutoff, time.Now().Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		return ""
	}
}

func anyInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	default:
		return 0
	}
}

func anyFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		n, _ := x.Float64()
		return n
	default:
		return 0
	}
}

func anySlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func dogeToSats(d float64) int64 {
	return int64(d * 100000000)
}

func scriptAddresses(spk map[string]any) []string {
	if spk == nil {
		return nil
	}
	out := []string{}
	if a := strings.TrimSpace(anyString(spk["address"])); a != "" {
		out = append(out, a)
	}
	if arr, ok := spk["addresses"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

func (ix *coreIndexer) status() map[string]any {
	if ix == nil {
		return map[string]any{"enabled": false}
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return map[string]any{
		"enabled":      true,
		"running":      ix.running,
		"last_error":   ix.lastErr,
		"last_height":  ix.lastHeight,
		"tip_height":   ix.tipHeight,
		"batch":        ix.batch,
		"start_height": ix.startHeight,
	}
}

func (ix *coreIndexer) postgresStatus(ctx context.Context) map[string]any {
	out := map[string]any{
		"configured": ix != nil && ix.db != nil,
		"running":    false,
	}
	if ix == nil || ix.db == nil {
		return out
	}
	if err := ix.db.PingContext(ctx); err != nil {
		out["error"] = err.Error()
		return out
	}
	out["running"] = true
	var dbName string
	_ = ix.db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&dbName)
	var sizeBytes int64
	if dbName != "" {
		_ = ix.db.QueryRowContext(ctx, `SELECT pg_database_size($1)`, dbName).Scan(&sizeBytes)
	}
	out["database"] = dbName
	out["size_bytes"] = sizeBytes
	if sizeBytes > 0 {
		out["size_mb"] = float64(sizeBytes) / (1024.0 * 1024.0)
	}
	return out
}

func (ix *coreIndexer) setStartHeightIfNeeded(ctx context.Context, height int64) (map[string]any, error) {
	if ix == nil || ix.db == nil {
		return nil, fmt.Errorf("core indexer unavailable")
	}
	if height < 0 {
		return nil, fmt.Errorf("height must be >= 0")
	}
	existingHash, err := ix.blockHashAtHeight(height)
	if err != nil {
		return nil, err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.startHeight = height
	// If height already exists in DB, do not rewind state; continue from current indexed progress.
	if strings.TrimSpace(existingHash) != "" {
		return map[string]any{
			"updated":            true,
			"height":             height,
			"already_in_db":      true,
			"existing_block_hash": strings.ToLower(strings.TrimSpace(existingHash)),
			"note":               "block already indexed; no rewind performed",
		}, nil
	}
	// If missing, start from just before requested height so next loop adds new rows from requested block onward.
	targetLast := height - 1
	if targetLast < 0 {
		targetLast = 0
	}
	if ix.lastHeight < targetLast {
		ix.lastHeight = targetLast
	}
	if err := ix.saveState(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"updated":       true,
		"height":        height,
		"already_in_db": false,
		"next_from":     targetLast + 1,
		"note":          "start height set; indexer will only append missing/new blocks",
	}, nil
}

func (ix *coreIndexer) summary(ctx context.Context) map[string]any {
	out := ix.status()
	if ix == nil || ix.db == nil {
		return out
	}
	var blocks, txs, addrs int64
	_ = ix.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qe_core_blocks`).Scan(&blocks)
	_ = ix.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qe_core_txs`).Scan(&txs)
	_ = ix.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qe_core_addresses`).Scan(&addrs)
	var hourly int64
	_ = ix.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qe_core_hourly_metrics`).Scan(&hourly)
	out["rows"] = map[string]int64{"blocks": blocks, "txs": txs, "addresses": addrs, "hourly_metrics": hourly}
	return out
}

func (ix *coreIndexer) metricBuckets(ctx context.Context, hours int) ([]map[string]any, error) {
	if hours < 1 {
		hours = 24
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Unix()
	rows, err := ix.db.QueryContext(ctx, `SELECT hour_unix, all_count, quantum_count, non_quantum_count, invalid_quantum_count
		FROM qe_core_hourly_metrics WHERE hour_unix >= $1 ORDER BY hour_unix`, cutoff-(cutoff%3600))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0, hours)
	for rows.Next() {
		var h, allc, q, nq, iq int64
		if err := rows.Scan(&h, &allc, &q, &nq, &iq); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"hour_unix":       h,
			"hour":            time.Unix(h, 0).UTC().Format("2006-01-02T15:00:00Z"),
			"all":             allc,
			"quantum":         q,
			"non_quantum":     nq,
			"invalid_quantum": iq,
		})
	}
	return out, rows.Err()
}

func (ix *coreIndexer) search(ctx context.Context, q string, limit int) (map[string]any, error) {
	if ix == nil || ix.db == nil {
		return map[string]any{"error": "core indexer unavailable"}, nil
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return map[string]any{"error": "missing q"}, nil
	}
	if limit < 1 || limit > 200 {
		limit = 25
	}
	if n, err := strconv.ParseInt(q, 10, 64); err == nil && n >= 0 {
		var row map[string]any
		var h, t int64
		var hash string
		if err := ix.db.QueryRowContext(ctx, `SELECT height, hash, time_unix FROM qe_core_blocks WHERE height=$1`, n).Scan(&h, &hash, &t); err == nil {
			row = map[string]any{"height": h, "hash": hash, "time_unix": t}
			return map[string]any{
				"kind":        "block_height",
				"query":       q,
				"block":       row,
				"block_query": "/api/public/block?height=" + strconv.FormatInt(h, 10),
			}, nil
		}
		return map[string]any{"kind": "block_height", "query": q, "block": row}, nil
	}
	if len(q) == 64 && isHex64String(strings.ToLower(q)) {
		q = strings.ToLower(q)
		var txid, state, reason string
		var h, tm, outSats int64
		err := ix.db.QueryRowContext(ctx, `SELECT txid, block_height, time_unix, quantum_state, pq_reason, value_out_sats FROM qe_core_txs WHERE txid=$1`, q).
			Scan(&txid, &h, &tm, &state, &reason, &outSats)
		if err == nil {
			return map[string]any{
				"kind":      "txid",
				"query":     q,
				"tx_detail": "/api/public/tx?txid=" + q,
				"tx": map[string]any{
					"txid": txid, "block_height": h, "time_unix": tm, "quantum_state": state, "pq_reason": reason, "value_out_sats": outSats,
				},
			}, nil
		}
		var bh int64
		var bHash string
		var bTime int64
		if err := ix.db.QueryRowContext(ctx, `SELECT height, hash, time_unix FROM qe_core_blocks WHERE hash=$1`, q).Scan(&bh, &bHash, &bTime); err == nil {
			return map[string]any{
				"kind":        "block_hash",
				"query":       q,
				"block":       map[string]any{"height": bh, "hash": bHash, "time_unix": bTime},
				"block_query": "/api/public/block?hash=" + q,
			}, nil
		}
		// Not a known txid or block hash; do not treat as a Dogecoin address.
		return map[string]any{
			"kind":  "unknown_hex64",
			"query": q,
			"note":  "No indexed block or transaction with this hash yet (Core indexer may still be catching up).",
		}, nil
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT address, txid, vout_n, value_sats, block_height
		FROM qe_core_addresses WHERE address=$1 ORDER BY block_height DESC LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var ad, txid string
		var n int
		var sats, h int64
		if err := rows.Scan(&ad, &txid, &n, &sats, &h); err != nil {
			return nil, err
		}
		list = append(list, map[string]any{"address": ad, "txid": txid, "vout_n": n, "value_sats": sats, "block_height": h})
	}
	return map[string]any{"kind": "address", "query": q, "results": list}, nil
}

func (ix *coreIndexer) recentBlocks(ctx context.Context, limit int) ([]map[string]any, error) {
	if ix == nil || ix.db == nil {
		return nil, nil
	}
	if limit < 1 || limit > 200 {
		limit = 15
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT height, hash, time_unix, tx_count FROM qe_core_blocks ORDER BY height DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var h, t int64
		var hash string
		var tc int
		if err := rows.Scan(&h, &hash, &t, &tc); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"height": h, "hash": hash, "time_unix": t, "tx_count": tc})
	}
	return out, rows.Err()
}

func (ix *coreIndexer) recentTransactions(ctx context.Context, limit int, mode string) ([]map[string]any, error) {
	if ix == nil || ix.db == nil {
		return nil, nil
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	query := `SELECT txid, block_height, time_unix, quantum_state, pq_reason, value_out_sats
		FROM qe_core_txs ORDER BY time_unix DESC, txid DESC LIMIT $1`
	args := []any{limit}
	if mode == "quantum" {
		query = `SELECT txid, block_height, time_unix, quantum_state, pq_reason, value_out_sats
			FROM qe_core_txs WHERE quantum_state='quantum' ORDER BY time_unix DESC, txid DESC LIMIT $1`
	}
	rows, err := ix.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0, limit)
	for rows.Next() {
		var txid, state, reason string
		var height, tm, valueOut int64
		if err := rows.Scan(&txid, &height, &tm, &state, &reason, &valueOut); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"txid":           txid,
			"block_height":   height,
			"time_unix":      tm,
			"quantum_state":  state,
			"pq_reason":      reason,
			"value_out_sats": valueOut,
		})
	}
	return out, rows.Err()
}

func (ix *coreIndexer) pqAggregates(ctx context.Context) map[string]int64 {
	out := map[string]int64{"quantum": 0, "non_quantum": 0, "invalid_quantum": 0, "all": 0}
	if ix == nil || ix.db == nil {
		return out
	}
	var all, q, nq, iq int64
	_ = ix.db.QueryRowContext(ctx, `SELECT COUNT(*)::bigint FROM qe_core_txs`).Scan(&all)
	_ = ix.db.QueryRowContext(ctx, `SELECT COUNT(*)::bigint FROM qe_core_txs WHERE quantum_state='quantum'`).Scan(&q)
	_ = ix.db.QueryRowContext(ctx, `SELECT COUNT(*)::bigint FROM qe_core_txs WHERE quantum_state='non-quantum'`).Scan(&nq)
	_ = ix.db.QueryRowContext(ctx, `SELECT COUNT(*)::bigint FROM qe_core_txs WHERE quantum_state='invalid-quantum'`).Scan(&iq)
	out["all"], out["quantum"], out["non_quantum"], out["invalid_quantum"] = all, q, nq, iq
	return out
}

// blockDetail returns indexed block metadata and txs from qe_core_txs (decode for first decodeLimit txs).
func (ix *coreIndexer) blockDetail(ctx context.Context, height *int64, hash *string, decodeLimit int) (map[string]any, error) {
	if ix == nil || ix.db == nil {
		return nil, fmt.Errorf("core indexer unavailable")
	}
	if decodeLimit < 1 || decodeLimit > 200 {
		decodeLimit = 50
	}
	var bH, bT int64
	var bHash string
	var txc int
	var err error
	if height != nil && *height >= 0 {
		err = ix.db.QueryRowContext(ctx, `SELECT height, hash, time_unix, tx_count FROM qe_core_blocks WHERE height=$1`, *height).
			Scan(&bH, &bHash, &bT, &txc)
	} else if hash != nil && strings.TrimSpace(*hash) != "" {
		h := strings.ToLower(strings.TrimSpace(*hash))
		err = ix.db.QueryRowContext(ctx, `SELECT height, hash, time_unix, tx_count FROM qe_core_blocks WHERE hash=$1`, h).
			Scan(&bH, &bHash, &bT, &txc)
	} else {
		return nil, fmt.Errorf("height or hash required")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"found": false}, nil
	}
	if err != nil {
		return nil, err
	}
	net := ix.network
	rows, err := ix.db.QueryContext(ctx, `SELECT txid, quantum_state, pq_reason, value_out_sats, raw_hex FROM qe_core_txs WHERE block_height=$1 ORDER BY txid`, bH)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	txs := []map[string]any{}
	n := 0
	for rows.Next() {
		var txid, state, reason, raw string
		var vs int64
		if err := rows.Scan(&txid, &state, &reason, &vs, &raw); err != nil {
			return nil, err
		}
		row := map[string]any{
			"txid": txid, "quantum_state": state, "pq_reason": reason, "value_out_sats": vs,
		}
		if n < decodeLimit && strings.TrimSpace(raw) != "" {
			row["decode"] = DecodeTxJSON(raw, net)
		}
		n++
		txs = append(txs, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"found":        true,
		"block":        map[string]any{"height": bH, "hash": bHash, "time_unix": bT, "tx_count": txc},
		"transactions": txs,
		"decode_limit": decodeLimit,
		"decode_rows":  min(n, decodeLimit),
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// backfillMissingRawHexBatch fetches getrawtransaction for rows with empty raw_hex while Core RPC is up,
// so /api/public/tx and block decodes work from Postgres alone when Core is offline later.
func (ix *coreIndexer) backfillMissingRawHexBatch(ctx context.Context) int {
	if ix == nil || ix.db == nil || ix.rpc == nil || !ix.rpc.enabled() {
		return 0
	}
	limit := envIntBounded("QE_CORE_RAW_BACKFILL_BATCH", 40, 1, 200)
	rows, err := ix.db.QueryContext(ctx, `
		SELECT txid, block_hash FROM qe_core_txs
		WHERE LENGTH(TRIM(COALESCE(raw_hex,'')))=0
		ORDER BY block_height ASC
		LIMIT $1`, limit)
	if err != nil {
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var txid, bhash string
		if err := rows.Scan(&txid, &bhash); err != nil {
			continue
		}
		txid = strings.ToLower(strings.TrimSpace(txid))
		bhash = strings.ToLower(strings.TrimSpace(bhash))
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		h, err := ix.rpc.getRawTransactionHex(rctx, txid, bhash)
		cancel()
		if err != nil || strings.TrimSpace(h) == "" {
			continue
		}
		bfCtx, bfDone := context.WithTimeout(context.Background(), 12*time.Second)
		err = ix.backfillRawHex(bfCtx, txid, strings.TrimSpace(h))
		bfDone()
		if err == nil {
			n++
		}
	}
	return n
}

func (ix *coreIndexer) txRowByID(ctx context.Context, txid string) (rawHex, quantumState, pqReason string, blockH, timeUnix, valueOut int64, blockHash string, ok bool, err error) {
	if ix == nil || ix.db == nil {
		return "", "", "", 0, 0, 0, "", false, nil
	}
	txid = strings.ToLower(strings.TrimSpace(txid))
	err = ix.db.QueryRowContext(ctx, `SELECT raw_hex, quantum_state, pq_reason, block_height, time_unix, value_out_sats, block_hash FROM qe_core_txs WHERE txid=$1`, txid).
		Scan(&rawHex, &quantumState, &pqReason, &blockH, &timeUnix, &valueOut, &blockHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", 0, 0, 0, "", false, nil
	}
	if err != nil {
		return "", "", "", 0, 0, 0, "", false, err
	}
	return rawHex, quantumState, pqReason, blockH, timeUnix, valueOut, strings.ToLower(strings.TrimSpace(blockHash)), true, nil
}

func (ix *coreIndexer) blockTxRows(ctx context.Context, blockHeight int64) ([]map[string]any, error) {
	if ix == nil || ix.db == nil || blockHeight < 0 {
		return nil, nil
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT txid, raw_hex FROM qe_core_txs WHERE block_height=$1 ORDER BY txid`, blockHeight)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0, 64)
	for rows.Next() {
		var txid, raw string
		if err := rows.Scan(&txid, &raw); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"txid":    strings.ToLower(strings.TrimSpace(txid)),
			"raw_hex": strings.TrimSpace(raw),
		})
	}
	return out, rows.Err()
}

func (ix *coreIndexer) backfillRawHex(ctx context.Context, txid, rawHex string) error {
	if ix == nil || ix.db == nil || strings.TrimSpace(rawHex) == "" {
		return nil
	}
	txid = strings.ToLower(strings.TrimSpace(txid))
	state := "non-quantum"
	reason := ""
	if ok, why, _, _ := verifyPQStrict(rawHex); ok {
		state = "quantum"
	} else {
		state = "invalid-quantum"
		reason = why
	}
	_, err := ix.db.ExecContext(ctx, `UPDATE qe_core_txs SET raw_hex=$1, quantum_state=$2, pq_reason=$3 WHERE txid=$4 AND (raw_hex='' OR LENGTH(TRIM(COALESCE(raw_hex,'')))=0)`,
		rawHex, state, reason, txid)
	return err
}

func (ix *coreIndexer) prevoutByTxVout(ctx context.Context, txid string, vout int64) (map[string]any, bool, error) {
	if ix == nil || ix.db == nil {
		return nil, false, nil
	}
	txid = strings.ToLower(strings.TrimSpace(txid))
	if len(txid) != 64 || vout < 0 {
		return nil, false, nil
	}
	var addr string
	var valueSats, blockHeight int64
	err := ix.db.QueryRowContext(ctx, `SELECT address, value_sats, block_height
		FROM qe_core_addresses WHERE txid=$1 AND vout_n=$2 LIMIT 1`, txid, int(vout)).
		Scan(&addr, &valueSats, &blockHeight)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return map[string]any{
		"address":      addr,
		"value_sats":   valueSats,
		"value_doge":   float64(valueSats) / 1e8,
		"block_height": blockHeight,
		"txid":         txid,
		"vout":         vout,
	}, true, nil
}

func (ix *coreIndexer) blockRawTransactions(ctx context.Context, height int64) ([]map[string]string, error) {
	if ix == nil || ix.db == nil || height < 0 {
		return nil, nil
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT txid, raw_hex
		FROM qe_core_txs
		WHERE block_height=$1 AND LENGTH(TRIM(COALESCE(raw_hex,'')))>0
		ORDER BY txid`, height)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]string, 0, 64)
	for rows.Next() {
		var txid, raw string
		if err := rows.Scan(&txid, &raw); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{
			"txid": strings.ToLower(strings.TrimSpace(txid)),
			"raw":  strings.ToLower(strings.TrimSpace(raw)),
		})
	}
	return out, rows.Err()
}

func (ix *coreIndexer) autoStartIfEnabled() {
	if ix == nil {
		return
	}
	if strings.TrimSpace(env("QE_CORE_INDEXER_AUTO_START", "1")) == "0" {
		log.Printf("[quantum-explorer] core indexer autostart disabled")
		return
	}
	if err := ix.start(); err != nil {
		log.Printf("[quantum-explorer] core indexer autostart failed: %v", err)
		return
	}
	log.Printf("[quantum-explorer] core indexer started")
}
