package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// publicNetworkOverview aggregates Dogecoin Core chain + mempool info for the dashboard.
func (a *app) publicNetworkOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := map[string]any{
		"app_version": qeAppVersion,
		"build_hash":  qeAppBuildHash,
		"network":     strings.ToLower(strings.TrimSpace(a.cfg.Network)),
	}
	if a.core == nil || !a.core.enabled() {
		out["core_rpc"] = map[string]any{"enabled": false}
		writeJSON(w, 200, out)
		return
	}
	var chain map[string]any
	if err := a.core.call(ctx, "getblockchaininfo", []any{}, &chain); err != nil {
		out["core_rpc"] = map[string]any{"enabled": true, "connected": false, "error": err.Error()}
		writeJSON(w, 200, out)
		return
	}
	var mempool map[string]any
	_ = a.core.call(ctx, "getmempoolinfo", []any{}, &mempool)
	var hashps float64
	_ = a.core.withTimeout(12*time.Second).call(ctx, "getnetworkhashps", []any{120, int64(-1)}, &hashps)
	out["core_rpc"] = map[string]any{"enabled": true, "connected": true}
	out["chain"] = chain
	out["mempool"] = mempool
	out["network_hashps_120"] = hashps
	if px := fetchDogeMarketSnapshot(ctx, 4*time.Second); len(px) > 0 {
		out["market"] = px
	}
	writeJSON(w, 200, out)
}

func fetchDogeMarketSnapshot(ctx context.Context, timeout time.Duration) map[string]any {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.coingecko.com/api/v3/simple/price?ids=dogecoin&vs_currencies=usd&include_market_cap=true&include_24hr_vol=true", nil)
	if err != nil {
		return nil
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var wrap map[string]map[string]float64
	if json.Unmarshal(b, &wrap) != nil {
		return nil
	}
	doge := wrap["dogecoin"]
	if doge == nil {
		return nil
	}
	return map[string]any{
		"price_usd": doge["usd"], "market_cap_usd": doge["usd_market_cap"], "volume_24h_usd": doge["usd_24h_vol"],
		"source": "coingecko-simple-price",
	}
}

func (a *app) publicMiningStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if !a.publicEndpointAllowed(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	lookback := int64(4320) // ~3 days @ ~1 min blocks
	if s := strings.TrimSpace(r.URL.Query().Get("lookback_blocks")); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 50 && n <= 250000 {
			lookback = n
		}
	}
	top := 40
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("top"))); err == nil && n > 0 && n <= 200 {
		top = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tip := a.cidx.dbTipHeight(ctx)
	minH := tip - lookback
	if minH < 0 {
		minH = 0
	}
	lb, err := a.cidx.miningLeaderboard(ctx, minH, top)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	recent, _ := a.cidx.recentBlocks(ctx, 40)
	writeJSON(w, 200, map[string]any{
		"tip_height":          tip,
		"lookback_blocks":     lookback,
		"window_min_height":   minH,
		"top_miners":          lb,
		"recent_blocks":       recent,
		"note":                "Miner addresses are inferred from the first decoded coinbase pay-to output in each indexed block.",
	})
}

func (a *app) publicPQAnalytics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if !a.publicEndpointAllowed(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	hours := 168
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("hours"))); err == nil && n > 12 && n <= 720 {
		hours = n
	}
	pairsLimit := 60
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("pairs_limit"))); err == nil && n > 5 && n <= 200 {
		pairsLimit = n
	}
	addrLimit := 60
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("addr_limit"))); err == nil && n > 5 && n <= 300 {
		addrLimit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	agg := a.cidx.pqAggregates(ctx)
	series, err := a.cidx.pqHourlySeries(ctx, hours)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	addrs, err := a.cidx.pqAddressLeaderboard(ctx, addrLimit)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	pairs, err := a.cidx.pqCarrierRevealPairs(ctx, pairsLimit)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	roleCounts := a.cidx.pqCarrierRoleCounts(ctx)
	adoption := 0.0
	if agg["all"] > 0 {
		adoption = float64(agg["quantum"]) * 100 / float64(agg["all"])
	}
	writeJSON(w, 200, map[string]any{
		"aggregates":               agg,
		"pq_adoption_percent":      adoption,
		"pq_carrier_role_counts":   roleCounts,
		"hourly":                   series,
		"pq_address_leaderboard":   addrs,
		"carrier_reveal_activity":  pairs,
		"protocol_note":            "Carrier (TX_C) and reveal (TX_R) rows are detected from indexed raw transaction hex using PQ carrier markers.",
	})
}
