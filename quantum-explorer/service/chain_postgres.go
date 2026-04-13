package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type postgresChainBackend struct {
	db *sql.DB
}

func newPostgresChainBackend(dsn, jsonImportPath string) (*postgresChainBackend, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migratePostgresChain(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := maybeImportChainJSON(ctx, db, jsonImportPath); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &postgresChainBackend{db: db}, nil
}

func migratePostgresChain(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS qe_chain_headers (
			height INTEGER PRIMARY KEY,
			hash TEXT NOT NULL DEFAULT '',
			timestamp TEXT NOT NULL DEFAULT '',
			raw_source TEXT NOT NULL DEFAULT '',
			updated_at BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS qe_chain_headers_hash_nonempty
			ON qe_chain_headers (hash) WHERE hash <> ''`,
		`CREATE TABLE IF NOT EXISTS qe_block_tx_links (
			height INTEGER NOT NULL,
			txid TEXT NOT NULL,
			PRIMARY KEY (height, txid)
		)`,
		`CREATE INDEX IF NOT EXISTS qe_block_tx_links_height ON qe_block_tx_links (height)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func maybeImportChainJSON(ctx context.Context, db *sql.DB, jsonPath string) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)::int FROM qe_chain_headers`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	raw, err := os.ReadFile(jsonPath)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var f chainIndexFile
	if json.Unmarshal(raw, &f) != nil || f.Headers == nil {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for hs, bh := range f.Headers {
		h, err := strconv.Atoi(hs)
		if err != nil || h <= 0 {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(bh.Hash))
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO qe_chain_headers (height, hash, timestamp, raw_source, updated_at) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (height) DO NOTHING`,
			h, hash, bh.Timestamp, bh.RawSource, nowUnix(),
		); err != nil {
			return err
		}
	}
	if f.BlockTxs != nil {
		for hs, txs := range f.BlockTxs {
			h, err := strconv.Atoi(hs)
			if err != nil || h <= 0 {
				continue
			}
			for _, txid := range txs {
				t := strings.ToLower(strings.TrimSpace(txid))
				if len(t) != 64 || !isHex64String(t) {
					continue
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO qe_block_tx_links (height, txid) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
					h, t,
				); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func (p *postgresChainBackend) Kind() string { return "postgres" }

func (p *postgresChainBackend) Close() error { return p.db.Close() }

func (p *postgresChainBackend) Persist() error { return nil }

func (p *postgresChainBackend) UpsertHeader(height int, hash, timestamp, rawSource string) (bool, error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	var curHash, curTS, curRaw string
	err := p.db.QueryRow(
		`SELECT hash, timestamp, raw_source FROM qe_chain_headers WHERE height = $1`, height,
	).Scan(&curHash, &curTS, &curRaw)
	noRow := errors.Is(err, sql.ErrNoRows)
	if err != nil && !noRow {
		return false, err
	}
	if noRow {
		_, err := p.db.Exec(
			`INSERT INTO qe_chain_headers (height, hash, timestamp, raw_source, updated_at) VALUES ($1,$2,$3,$4,$5)`,
			height, hash, timestamp, rawSource, nowUnix(),
		)
		return true, err
	}
	changed := false
	newHash := curHash
	if hash != "" && curHash != hash {
		newHash = hash
		changed = true
	}
	newTS := curTS
	if timestamp != "" && curTS != timestamp {
		newTS = timestamp
		changed = true
	}
	newRaw := curRaw
	if rawSource != "" && curRaw != rawSource {
		newRaw = rawSource
		changed = true
	}
	if !changed {
		return false, nil
	}
	_, err = p.db.Exec(
		`UPDATE qe_chain_headers SET hash = $1, timestamp = $2, raw_source = $3, updated_at = $4 WHERE height = $5`,
		newHash, newTS, newRaw, nowUnix(), height,
	)
	return true, err
}

func (p *postgresChainBackend) AppendTxLinkIfNew(height int, txid string) (bool, error) {
	if height <= 0 || len(txid) != 64 || !isHex64String(txid) {
		return false, nil
	}
	txid = strings.ToLower(txid)
	var blockHash string
	_ = p.db.QueryRow(`SELECT hash FROM qe_chain_headers WHERE height = $1`, height).Scan(&blockHash)
	if blockHash != "" && strings.EqualFold(blockHash, txid) {
		return false, nil
	}
	res, err := p.db.Exec(
		`INSERT INTO qe_block_tx_links (height, txid) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		height, txid,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (p *postgresChainBackend) GetByHash(hash string) (*IndexedBlockHeader, error) {
	h := strings.ToLower(strings.TrimSpace(hash))
	var bh IndexedBlockHeader
	err := p.db.QueryRow(
		`SELECT height, hash, timestamp, raw_source FROM qe_chain_headers WHERE hash = $1 LIMIT 1`, h,
	).Scan(&bh.Height, &bh.Hash, &bh.Timestamp, &bh.RawSource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &bh, nil
}

func (p *postgresChainBackend) GetByHeight(height int) (*IndexedBlockHeader, error) {
	var bh IndexedBlockHeader
	err := p.db.QueryRow(
		`SELECT height, hash, timestamp, raw_source FROM qe_chain_headers WHERE height = $1`, height,
	).Scan(&bh.Height, &bh.Hash, &bh.Timestamp, &bh.RawSource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &bh, nil
}

func (p *postgresChainBackend) ListTxidsForHeight(height int) ([]string, error) {
	rows, err := p.db.Query(`SELECT txid FROM qe_block_tx_links WHERE height = $1 ORDER BY txid`, height)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *postgresChainBackend) Summary() (headerCount, tipHeight, txLinkHeights int64, err error) {
	err = p.db.QueryRow(`SELECT COUNT(*)::bigint, COALESCE(MAX(height), 0)::bigint FROM qe_chain_headers`).Scan(&headerCount, &tipHeight)
	if err != nil {
		return 0, 0, 0, err
	}
	err = p.db.QueryRow(`SELECT COUNT(DISTINCT height)::bigint FROM qe_block_tx_links`).Scan(&txLinkHeights)
	if err != nil {
		return 0, 0, 0, err
	}
	return headerCount, tipHeight, txLinkHeights, nil
}

func (p *postgresChainBackend) RecentHeaders(limit int) ([]IndexedBlockHeader, error) {
	if limit <= 0 {
		limit = 12
	}
	rows, err := p.db.Query(
		`SELECT height, hash, timestamp, raw_source FROM qe_chain_headers ORDER BY height DESC LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexedBlockHeader
	for rows.Next() {
		var bh IndexedBlockHeader
		if err := rows.Scan(&bh.Height, &bh.Hash, &bh.Timestamp, &bh.RawSource); err != nil {
			return nil, err
		}
		out = append(out, bh)
	}
	return out, rows.Err()
}

func (p *postgresChainBackend) AdminDiagnostics() map[string]any {
	out := map[string]any{"backend": "postgres"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := p.db.PingContext(ctx); err != nil {
		out["ping_ok"] = false
		out["ping_error"] = err.Error()
		return out
	}
	out["ping_ok"] = true
	var ver string
	if err := p.db.QueryRowContext(ctx, `SELECT version()`).Scan(&ver); err != nil {
		out["version_query_error"] = err.Error()
	} else {
		out["server_version"] = ver
	}
	st := p.db.Stats()
	out["pool"] = map[string]any{
		"max_open_connections": st.MaxOpenConnections,
		"open_connections":     st.OpenConnections,
		"in_use":               st.InUse,
		"idle":                 st.Idle,
		"wait_count":           st.WaitCount,
		"wait_duration_ms":     st.WaitDuration.Milliseconds(),
	}
	var nH, nL int64
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*)::bigint FROM qe_chain_headers`).Scan(&nH)
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*)::bigint FROM qe_block_tx_links`).Scan(&nL)
	out["table_row_counts"] = map[string]any{
		"qe_chain_headers":  nH,
		"qe_block_tx_links": nL,
	}
	hc, tip, txh, err := p.Summary()
	if err == nil {
		out["summary"] = map[string]any{"header_count": hc, "tip_height": tip, "heights_with_tx_links": txh}
	} else {
		out["summary_error"] = err.Error()
	}
	return out
}
