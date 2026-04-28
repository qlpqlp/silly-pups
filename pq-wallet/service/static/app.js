/* global Chart, QRCode, Html5Qrcode, jsQR */

let activeMutations = 0;

function setGlobalActionBusy(on) {
  const badge = $("global-action-badge");
  if (!badge) return;
  badge.classList.toggle("hidden", !on);
}

async function api(path, opts) {
  opts = opts || {};
  const method = String(opts.method || "GET").toUpperCase();
  const isMutation = method !== "GET";
  const { signal, ...fetchOpts } = opts;
  const reqTimeoutMs = Number(fetchOpts.timeout_ms) > 0
    ? Number(fetchOpts.timeout_ms)
    : (isMutation ? 30000 : 12000);
  delete fetchOpts.timeout_ms;
  let timeoutHandle = null;
  let effectiveSignal = signal;
  let timeoutController = null;
  if (!effectiveSignal && typeof AbortController !== "undefined") {
    timeoutController = new AbortController();
    effectiveSignal = timeoutController.signal;
    timeoutHandle = setTimeout(() => {
      try { timeoutController.abort(); } catch { /* ignore */ }
    }, reqTimeoutMs);
  }
  if (isMutation) {
    activeMutations += 1;
    setGlobalActionBusy(true);
  }
  try {
    const r = await fetch(path, {
      headers: { "Content-Type": "application/json" },
      ...fetchOpts,
      signal: effectiveSignal,
    });
    const text = await r.text();
    try {
      return JSON.parse(text);
    } catch {
      return { _raw: text, _status: r.status };
    }
  } catch (e) {
    if (e && e.name === "AbortError") {
      return { error: "request timeout", _timeout: true };
    }
    throw e;
  } finally {
    if (timeoutHandle) clearTimeout(timeoutHandle);
    if (isMutation) {
      activeMutations = Math.max(0, activeMutations - 1);
      setGlobalActionBusy(activeMutations > 0);
    }
  }
}

const state = {
  wallet: null,
  walletLocked: false,
  lastPendingDoge: 0,
  lastTxs: [],
  view: "dashboard",
  charts: { mempool: null },
  pollFast: null,
  pollTx: null,
  pollLogs: null,
  qrScanner: null,
  receiveQrAddr: null,
  receiveQrBucket: "",
  txDetailTxid: "",
  txDetailHex: "",
};

function flashButtonFeedback(el) {
  if (!el) return;
  el.classList.add("btn-flash");
  setTimeout(() => el.classList.remove("btn-flash"), 180);
}

function $(id) {
  return document.getElementById(id);
}

function closeSpvRepairModal() {
  const m = $("spv-repair-modal");
  if (m) m.classList.add("hidden");
}

function spvFmtCheckpointUtc(ts) {
  const n = Number(ts);
  if (!Number.isFinite(n) || n <= 0) return "—";
  try {
    return new Date(n * 1000).toISOString().slice(0, 19).replace("T", " ") + " UTC";
  } catch {
    return String(ts);
  }
}

function spvShortHashHex(hex) {
  const h = String(hex || "");
  if (h.length <= 14) return h;
  return h.slice(0, 8) + "…" + h.slice(-6);
}

function fillSpvRollbackCheckpointSelect(st) {
  const sel = $("spv-rollback-checkpoint-select");
  if (!sel) return;
  const net =
    state.wallet && state.wallet.network && String(state.wallet.network).toLowerCase() === "testnet"
      ? "testnet"
      : "mainnet";
  const rows =
    st && st.spv_checkpoints && Array.isArray(st.spv_checkpoints[net]) ? st.spv_checkpoints[net] : [];
  sel.innerHTML = "";
  const custom = document.createElement("option");
  custom.value = "custom";
  custom.textContent = "Custom — use hash / height fields below";
  sel.appendChild(custom);
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i] || {};
    const h = row.height != null ? Number(row.height) : NaN;
    const hash = row.hash != null ? String(row.hash) : "";
    const ts = row.timestamp != null ? Number(row.timestamp) : 0;
    if (!Number.isFinite(h) || hash.length < 32) continue;
    const o = document.createElement("option");
    o.value = "h-" + h;
    const head = h === 0 ? "Genesis — block 0 — " : "Block " + h + " — ";
    o.textContent = head + spvShortHashHex(hash) + " — " + spvFmtCheckpointUtc(ts);
    sel.appendChild(o);
  }
}

async function openSpvRepairModal() {
  const m = $("spv-repair-modal");
  const line = $("spv-repair-storage-line");
  const syncSel = $("spv-rescan-sync-mode");
  const ckSel = $("spv-rollback-checkpoint-select");
  if (!m) return;
  m.classList.remove("hidden");
  if (line) line.textContent = "Loading storage path…";
  if (ckSel) ckSel.innerHTML = '<option value="custom">Loading checkpoints…</option>';
  try {
    const st = await api("/api/spv/status");
    if (syncSel) {
      const cp = st.use_checkpoint !== false;
      syncSel.value = cp ? "checkpoints" : "genesis";
    }
    fillSpvRollbackCheckpointSelect(st);
    if (line) {
      const dir = st.storage_dir != null ? String(st.storage_dir) : "—";
      const hp = st.headers_db_present === true;
      const path = st.headers_db ? String(st.headers_db) : `${dir}/headers.db`;
      const fmt = st.headers_db_format ? ` [${String(st.headers_db_format)}]` : "";
      line.textContent = `Storage: ${dir} — headers.db ${hp ? "present (" + path + ")" + fmt : "not present yet (expected " + path + ")"}`;
    }
  } catch {
    if (line) line.textContent = "Could not load /api/spv/status.";
    fillSpvRollbackCheckpointSelect(null);
  }
}

function showView(name) {
  closeSpvRepairModal();
  state.view = name;
  document.querySelectorAll(".nav-item").forEach((b) => {
    b.classList.toggle("active", b.dataset.view === name);
  });
  const titles = {
    dashboard: ["Dashboard", "Balances, sync ETA, and post-quantum transaction hints"],
    receive: ["Receive Dogecoin", "QR and address for your primary receiving address"],
    addresses: ["Addresses", "Generate keys and choose which address SPV watches"],
    transactions: ["Transactions", "Local SPV/P2P transaction history"],
    tools: ["Send Doge", "Destination & amount, or paste a signed raw hex for P2P broadcast"],
    learn: ["Help", "ECDSA vs PQ · send · verify · broadcast"],
    settings: ["Settings", "Encryption, logs, backup and wallet controls"],
  };
  const icons = {
    dashboard: "space_dashboard",
    receive: "qr_code_2",
    addresses: "account_balance_wallet",
    transactions: "swap_horiz",
    tools: "send",
    learn: "help",
    settings: "settings",
  };
  const [t, s] = titles[name] || [name, ""];
  const ico = icons[name] || "pets";
  const titleText = $("page-title-text");
  const titleIco = $("page-title-ico");
  const titleEl = $("page-title");
  const subEl = $("page-sub");
  if (titleText) titleText.textContent = t;
  if (titleIco) titleIco.textContent = ico;
  if (subEl) subEl.textContent = s;
  if (titleEl) {
    titleEl.setAttribute("title", s || "");
    if (s) titleEl.setAttribute("aria-describedby", "page-sub");
    else titleEl.removeAttribute("aria-describedby");
  }

  if (state.pollLogs) {
    clearInterval(state.pollLogs);
    state.pollLogs = null;
  }

  document.querySelectorAll(".content .view").forEach((v) => v.classList.add("hidden"));
  const el = $("view-" + name);
  if (el) el.classList.remove("hidden");
  syncMobileTabbar(name);
  if (name === "receive") updateReceiveView();
  if (name === "learn") loadEducation();
  if (name === "settings") {
    refreshLogs();
    state.pollLogs = setInterval(refreshLogs, 4000);
    refreshDashboard().catch(() => {});
  }
  if (isNarrowViewport()) {
    const sb = $("sidebar");
    if (sb) {
      sb.classList.add("sidebar-drawer-closed");
      syncSidebarDrawerToggleIcon();
    }
  }
}

async function refreshLogs() {
  try {
    const mkSignal = (ms) => {
      if (typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout === "function") {
        return AbortSignal.timeout(ms);
      }
      if (typeof AbortController === "undefined") return undefined;
      const c = new AbortController();
      setTimeout(() => {
        try { c.abort(); } catch { /* ignore */ }
      }, ms);
      return c.signal;
    };
    const [mtr, bc] = await Promise.all([
      fetch("/api/logs/mempooltracker", { signal: mkSignal(7000) }).then((r) => r.text()),
      fetch("/api/logs/broadcast?lines=200", { signal: mkSignal(7000) }).then((r) => r.text()),
    ]);
    const elM = $("log-mtr");
    const elB = $("log-bc");
    if (elM) elM.textContent = mtr;
    if (elB) elB.textContent = bc;
  } catch {
    /* ignore */
  }
}

function setSendTab(n) {
  document.querySelectorAll("[data-send-tab]").forEach((t) => {
    const on = parseInt(t.dataset.sendTab, 10) === n;
    t.classList.toggle("active", on);
    t.setAttribute("aria-selected", on ? "true" : "false");
  });
  document.querySelectorAll("[data-send-panel]").forEach((p) => {
    p.classList.toggle("hidden", parseInt(p.getAttribute("data-send-panel"), 10) !== n);
  });
}

async function loadEducation() {
  const root = $("learn-root");
  const loading = $("learn-loading");
  if (!root || root.dataset.loaded === "1") return;
  try {
    const data = await api("/api/education");
    loading.classList.add("hidden");
    root.classList.remove("hidden");
    root.innerHTML = "";
    const h = document.createElement("h2");
    h.textContent = data.title || "Education";
    root.appendChild(h);
    if (data.summary) {
      const p = document.createElement("p");
      p.className = "small muted";
      p.textContent = data.summary;
      root.appendChild(p);
    }
    (data.sections || []).forEach((sec) => {
      const card = document.createElement("div");
      card.className = "card learn-section";
      const th = document.createElement("h3");
      th.textContent = sec.title || "";
      card.appendChild(th);
      (sec.body || []).forEach((line) => {
        const p = document.createElement("p");
        p.className = "small";
        p.innerHTML = mdBold(line);
        card.appendChild(p);
      });
      root.appendChild(card);
    });
    if (data.flow && data.flow.length) {
      const card = document.createElement("div");
      card.className = "card learn-section learn-flow-card";
      const lead = data.flow_lead || "From a normal Dogecoin spend to optional post-quantum commitments.";
      card.innerHTML =
        "<h3>Quick flow</h3>" +
        '<p class="learn-flow-lead small muted">' +
        escapeHtml(lead) +
        "</p>" +
        '<div class="learn-flow-steps" role="list"></div>';
      const wrap = card.querySelector(".learn-flow-steps");
      data.flow.forEach((f) => {
        const step = document.createElement("div");
        step.className = "learn-flow-step";
        step.setAttribute("role", "listitem");
        const num = document.createElement("span");
        num.className = "learn-flow-num";
        num.textContent = f.step || "";
        const body = document.createElement("div");
        body.className = "learn-flow-body";
        const h4 = document.createElement("h4");
        h4.className = "learn-flow-name";
        h4.textContent = f.name || "";
        const p = document.createElement("p");
        p.className = "learn-flow-detail small";
        p.innerHTML = mdBold(f.detail || "");
        body.appendChild(h4);
        body.appendChild(p);
        step.appendChild(num);
        step.appendChild(body);
        wrap.appendChild(step);
      });
      root.appendChild(card);
    }
    if (data.references) {
      const card = document.createElement("div");
      card.className = "card learn-links-card";
      const h3 = document.createElement("h3");
      h3.textContent = "Links";
      card.appendChild(h3);
      const linksWrap = document.createElement("div");
      linksWrap.className = "learn-ref-links";
      data.references.forEach((url) => {
        const a = document.createElement("a");
        a.href = url;
        a.target = "_blank";
        a.rel = "noopener";
        a.textContent = url;
        linksWrap.appendChild(a);
      });
      card.appendChild(linksWrap);
      root.appendChild(card);
    }
    if (data.libdogecoin_build) {
      const p = document.createElement("p");
      p.className = "small muted";
      p.textContent = data.libdogecoin_build;
      root.appendChild(p);
    }
    root.dataset.loaded = "1";
  } catch (e) {
    root.dataset.loaded = "";
    if (loading) {
      loading.classList.remove("hidden");
      loading.textContent = "Could not load Help content. Open Help again to retry.";
    }
  }
}

function mdBold(s) {
  const parts = String(s).split(/\*\*(.+?)\*\*/g);
  let out = "";
  for (let i = 0; i < parts.length; i++) {
    out += i % 2 === 1 ? "<strong>" + escapeHtml(parts[i]) + "</strong>" : escapeHtml(parts[i]);
  }
  return out;
}

function setOnboarding(w) {
  const has = !!w || state.walletLocked;
  if (!has) {
    if (state.pollFast) clearInterval(state.pollFast);
    if (state.pollTx) clearInterval(state.pollTx);
    if (state.pollLogs) clearInterval(state.pollLogs);
    state.pollFast = null;
    state.pollTx = null;
    state.pollLogs = null;
  }
  $("view-onboarding").classList.toggle("hidden", has);
  const appEl = $("app");
  if (appEl) appEl.classList.toggle("has-wallet", has);
  const mt = $("mobile-tabbar");
  if (mt) mt.classList.toggle("hidden", !has);
  ["view-dashboard", "view-receive", "view-addresses", "view-transactions", "view-tools", "view-learn", "view-settings"].forEach((id) => {
    $(id).classList.toggle("hidden", !has);
  });
  if (has) {
    if (w && w.network) $("net-badge").textContent = (w.network || "mainnet").toUpperCase();
    showView(state.view || "dashboard");
    ensureMobileSidebarLayout();
    updateReceiveView();
    startPollers();
  } else {
    ensureOnboardingMobileSidebar();
  }
}

function isNarrowViewport() {
  if (typeof window === "undefined") return false;
  const iw = window.innerWidth;
  const cw = document.documentElement ? document.documentElement.clientWidth : iw;
  const x = Math.min(typeof iw === "number" ? iw : 9999, typeof cw === "number" ? cw : 9999);
  if (x <= 900) return true;
  try {
    if (window.matchMedia("(max-width: 900px)").matches) return true;
  } catch {
    /* ignore */
  }
  return false;
}

/** Keep desktop “collapsed” rail and mobile drawer separate; default mobile drawer shut when wallet UI is active. */
function ensureMobileSidebarLayout() {
  const sb = $("sidebar");
  const app = $("app");
  if (!sb || !app || !app.classList.contains("has-wallet")) return;
  if (isNarrowViewport()) {
    sb.classList.remove("collapsed");
    sb.classList.add("sidebar-drawer-closed");
  } else {
    sb.classList.remove("sidebar-drawer-closed");
  }
  syncSidebarDrawerToggleIcon();
}

/** On narrow viewports, keep the nav drawer closed on create/restore until the user opens it. */
function ensureOnboardingMobileSidebar() {
  const sb = $("sidebar");
  if (!sb) return;
  if (isNarrowViewport()) {
    sb.classList.remove("collapsed");
    sb.classList.add("sidebar-drawer-closed");
  } else {
    sb.classList.remove("sidebar-drawer-closed");
  }
  syncSidebarDrawerToggleIcon();
}

function syncSidebarDrawerToggleIcon() {
  const sb = $("sidebar");
  const btn = $("btn-sidebar-toggle");
  if (!sb || !btn) return;
  const icon = btn.querySelector(".material-symbols-outlined");
  if (!icon) return;
  if (isNarrowViewport()) {
    const shut = sb.classList.contains("sidebar-drawer-closed");
    icon.textContent = shut ? "menu" : "menu_open";
    btn.setAttribute("aria-expanded", shut ? "false" : "true");
  } else {
    const collapsed = sb.classList.contains("collapsed");
    icon.textContent = collapsed ? "menu" : "menu_open";
    btn.setAttribute("aria-expanded", (!collapsed).toString());
  }
}

function fmtTime(iso) {
  if (!iso) return "";
  try {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime()) || d.getUTCFullYear() < 2009) return "—";
    return d.toLocaleString(undefined, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
  } catch {
    return "—";
  }
}

function humanizeSyncEta(sec) {
  const n = Number(sec);
  if (!Number.isFinite(n) || n < 0) return "Sync ETA unknown";
  if (n === 0) return "Synced";
  if (n < 60) return "Tip ETA < 1 minute";
  if (n < 3600) return `Tip ETA ${Math.ceil(n / 60)} minutes`;
  if (n < 86400) return `Tip ETA ${Math.ceil(n / 3600)} hours`;
  if (n < 86400 * 30) return `Tip ETA ${Math.ceil(n / 86400)} days`;
  return `Tip ETA ${Math.ceil(n / (86400 * 30))} months`;
}

function buildTxExpandableCard(tx, includeSource) {
  const txidFull = String(tx.txid || "").trim();
  const conf = Number(tx.confirmations || 0);
  const pending = !!tx.pending || conf <= 0 || String(tx.source || "").toLowerCase() === "memetracker";
  const dir = String(tx.direction || "unknown").toLowerCase();
  const dirLabel = dir === "in" ? "In" : dir === "out" ? "Out" : "Unknown";
  const sign = dir === "out" ? "-" : dir === "in" ? "+" : "";
  const amount = tx.amount_doge != null && !Number.isNaN(Number(tx.amount_doge))
    ? `${sign}${Number(tx.amount_doge).toFixed(2)} DOGE`
    : "—";
  const seen = tx.seen_at ? fmtTime(tx.seen_at) : "—";
  const isConfirmed = conf > 0;
  const short = txidFull ? txidFull.slice(0, 18) + (txidFull.length > 18 ? "…" : "") : "—";
  const card = document.createElement("details");
  card.className = "tx-card tx-card-modern";
  card.setAttribute("role", "listitem");
  if (txidFull) card.dataset.txid = txidFull;
  const summary = document.createElement("summary");
  const left = document.createElement("div");
  left.className = "tx-main";
  const top = document.createElement("div");
  top.className = "tx-main-top";
  const confPie = document.createElement("span");
  confPie.className = "tx-conf-pie";
  const percent = Math.max(0, Math.min(100, Math.floor((conf / 4) * 100))); // 4 conf = 100%
  confPie.style.setProperty("--pct", `${percent}%`);
  confPie.classList.toggle("pending", pending);
  confPie.title = pending ? "Seen in mempool" : `${conf} confirmations`;
  confPie.setAttribute("aria-label", confPie.title);
  const dirPill = document.createElement("span");
  dirPill.className = `tx-dir-pill ${dir === "in" ? "in" : dir === "out" ? "out" : ""}`;
  dirPill.textContent = dirLabel;
  top.appendChild(confPie);
  top.appendChild(dirPill);
  left.appendChild(top);
  const meta = document.createElement("div");
  meta.className = "tx-meta-line mono";
  meta.textContent = `${short} · ${seen}`;
  left.appendChild(meta);
  const right = document.createElement("div");
  right.style.display = "flex";
  right.style.alignItems = "center";
  right.style.gap = "0.5rem";
  const amt = document.createElement("span");
  amt.className = "tx-card-amt mono";
  amt.textContent = amount;
  const caret = document.createElement("span");
  caret.className = "material-symbols-outlined tx-expand-caret";
  caret.textContent = "expand_more";
  right.appendChild(amt);
  right.appendChild(caret);
  summary.appendChild(left);
  summary.appendChild(right);
  card.appendChild(summary);
  const body = document.createElement("div");
  body.className = "tx-expand-body tx-card-body";
  function addRow(label, value, mono) {
    const row = document.createElement("div");
    row.className = "tx-card-row";
    const k = document.createElement("span");
    k.className = "tx-card-k";
    k.textContent = label;
    const v = document.createElement("span");
    if (mono) v.className = "mono";
    v.textContent = value;
    row.appendChild(k);
    row.appendChild(v);
    body.appendChild(row);
  }
  addRow("Txid", txidFull || "—", true);
  addRow("Direction", dirLabel, false);
  addRow(isConfirmed ? "Block time" : "Seen", seen, false);
  addRow("Address", tx.address || "—", true);
  addRow("PQ", tx.pq_hint ? "Yes" : "No", false);
  if (includeSource) addRow("Source", tx.source || "—", false);
  if (txidFull) {
    const row = document.createElement("div");
    row.className = "tx-card-row";
    const k = document.createElement("span");
    k.className = "tx-card-k";
    k.textContent = "Actions";
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn btn-sm";
    btn.textContent = "Open details";
    btn.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      openTxDetailModal(tx);
    });
    row.appendChild(k);
    row.appendChild(btn);
    body.appendChild(row);
  }
  card.appendChild(body);
  return card;
}

function collectOpenTxids(container) {
  const set = new Set();
  if (!container) return set;
  container.querySelectorAll("details.tx-card-modern[open][data-txid]").forEach((el) => {
    const id = String(el.dataset.txid || "").trim();
    if (id) set.add(id);
  });
  return set;
}

function initCharts() {
  if (typeof Chart === "undefined") {
    setTimeout(initCharts, 50);
    return;
  }
  const common = {
    responsive: true,
    maintainAspectRatio: false,
    plugins: { legend: { display: false } },
    scales: {
      x: { ticks: { maxTicksLimit: 8, color: "#8a9bb3" }, grid: { color: "rgba(255,255,255,0.06)" } },
      y: { ticks: { color: "#8a9bb3", beginAtZero: true }, grid: { color: "rgba(255,255,255,0.06)" } },
    },
  };
  const ctxM = $("chart-mempool");
  if (ctxM && !state.charts.mempool) {
    state.charts.mempool = new Chart(ctxM, {
      type: "line",
      data: {
        labels: [],
        datasets: [{ label: "Mempool", data: [], borderColor: "#4ade80", tension: 0.25, fill: false }],
      },
      options: common,
    });
  }
}

function filterMetrics24h(metrics) {
  if (!metrics || !metrics.length) return [];
  const cutoff = Date.now() - 24 * 60 * 60 * 1000;
  return metrics.filter((m) => {
    if (!m.t) return false;
    const ms = new Date(m.t).getTime();
    return !Number.isNaN(ms) && ms >= cutoff;
  });
}

function updateCharts(metrics) {
  const m = filterMetrics24h(metrics || []);
  if (!m.length) return;
  const labels = m.map((x) => fmtTime(x.t));
  const mem = m.map((x) => Number(x.mempool_relay_count) || 0);
  if (state.charts.mempool) {
    state.charts.mempool.data.labels = labels;
    state.charts.mempool.data.datasets[0].data = mem;
    state.charts.mempool.update("none");
  }
}

async function refreshWallet() {
  const data = await api("/api/wallet");
  // If /api/wallet returns a transient non-JSON or error payload during startup,
  // do not flip the UI into onboarding; keep prior state until a stable read.
  if (!data || data.error || data._status >= 500) {
    return;
  }
  state.walletLocked = !!(data.locked && data.sealed);
  state.wallet = data.wallet || null;
  // Defensive fallback: on some force-refresh races wallet payload can be null briefly
  // while the service is still warming up. Security status tells us whether a wallet
  // exists on disk (sealed or plaintext) so onboarding should remain hidden.
  if (!state.wallet && !state.walletLocked) {
    try {
      const sec = await api("/api/security/status");
      const hasWalletOnDisk = !!(sec && (sec.sealed || sec.has_plaintext_wallet));
      if (hasWalletOnDisk) {
        state.walletLocked = !!(sec.sealed && !sec.unlocked);
      }
    } catch {
      /* ignore fallback failures */
    }
  }
  const lockEl = $("wallet-lock-screen");
  if (lockEl) {
    lockEl.classList.toggle("hidden", !state.walletLocked);
    lockEl.setAttribute("aria-hidden", state.walletLocked ? "false" : "true");
  }
  setOnboarding(data.wallet);
  if (data.wallet) {
    renderAddresses(data.wallet);
    updateReceiveView();
    refreshDashboard().catch(() => {});
    refreshTxList(false).catch(() => {});
  }
}

function maybeNotifyPending(pending) {
  const p = Number(pending) || 0;
  if (p > 0 && p > state.lastPendingDoge && "Notification" in window && Notification.permission === "granted") {
    try {
      new Notification("PQ Wallet — pending", {
        body: `≈ ${p.toFixed(2)} DOGE unconfirmed`,
      });
    } catch {
      /* ignore */
    }
  }
  state.lastPendingDoge = p;
}

function updateServicesControlUI(svc) {
  const card = $("svc-control-card");
  const spvLine = $("svc-spv-status");
  const mtrLine = $("svc-mtr-status");
  const bSpvStop = $("btn-svc-spv-stop");
  const bSpvStart = $("btn-svc-spv-start");
  const bMtrStop = $("btn-svc-mtr-stop");
  const bMtrStart = $("btn-svc-mtr-start");
  if (!svc || !card) return;
  card.classList.remove("hidden");
  const spvOn = !!svc.spv_enabled;
  const spvRun = !!svc.spv_running;
  const mtrOn = !!svc.memetracker_enabled;
  const mtrEng = !!svc.memetracker_engine_alive;
  const mtrP2p = !!svc.memetracker_p2p_active;
  const wk = svc.memetracker_workers_connected != null ? Number(svc.memetracker_workers_connected) : 0;
  const mcnt = svc.memetracker_mempool_tx_count != null ? Number(svc.memetracker_mempool_tx_count) : 0;
  if (spvLine) {
    spvLine.textContent = `Preference: ${spvOn ? "on" : "off"} · Process: ${spvRun ? "running" : "stopped"}`;
  }
  if (mtrLine) {
    mtrLine.textContent = `Preference: ${mtrOn ? "on" : "off"} · Engine: ${mtrEng ? "up" : "down"} · P2P: ${mtrP2p ? "active" : "idle"} · workers ${wk} · relay txs ${mcnt}`;
  }
  if (bSpvStop) bSpvStop.disabled = !spvOn;
  if (bSpvStart) bSpvStart.disabled = spvOn && spvRun;
  if (bMtrStop) bMtrStop.disabled = !mtrOn;
  if (bMtrStart) bMtrStart.disabled = mtrOn && mtrEng;
}

const SERVICE_CTRL_BTN_IDS = ["btn-svc-spv-stop", "btn-svc-spv-start", "btn-svc-mtr-stop", "btn-svc-mtr-start"];

function setServiceControlBusy(busy) {
  const els = SERVICE_CTRL_BTN_IDS.map((id) => document.getElementById(id)).filter(Boolean);
  const spvLine = $("svc-spv-status");
  const mtrLine = $("svc-mtr-status");
  if (busy) {
    els.forEach((el) => {
      el.setAttribute("aria-busy", "true");
      el.disabled = true;
    });
    if (spvLine) {
      spvLine.dataset.prevText = spvLine.textContent || "";
      spvLine.textContent = "Applying…";
    }
    if (mtrLine) {
      mtrLine.dataset.prevText = mtrLine.textContent || "";
      mtrLine.textContent = "Applying…";
    }
  } else {
    els.forEach((el) => el.removeAttribute("aria-busy"));
    if (spvLine && spvLine.dataset.prevText != null) {
      spvLine.textContent = spvLine.dataset.prevText;
      delete spvLine.dataset.prevText;
    }
    if (mtrLine && mtrLine.dataset.prevText != null) {
      mtrLine.textContent = mtrLine.dataset.prevText;
      delete mtrLine.dataset.prevText;
    }
  }
}

async function postServiceControl(patch) {
  setServiceControlBusy(true);
  try {
    const res = await api("/api/services/control", { method: "POST", body: JSON.stringify(patch) });
    if (res.error) {
      let msg = res.error;
      if (res.need_unlock === true) msg = "Unlock the wallet first.";
      alert(msg);
      return;
    }
    if (res.memetracker_err) {
      alert("MemeTracker: " + res.memetracker_err);
    }
    await refreshDashboard();
  } finally {
    setServiceControlBusy(false);
  }
}

async function refreshDashboard() {
  const data = await api("/api/dashboard");
  const svcCard = $("svc-control-card");
  if (!data.dashboard) {
    if (svcCard) svcCard.classList.add("hidden");
    return;
  }
  if (svcCard) svcCard.classList.remove("hidden");
  const t = data.dashboard.totals || {};
  const spendStr =
    t.spendable_hint_doge != null && !Number.isNaN(Number(t.spendable_hint_doge))
      ? Number(t.spendable_hint_doge).toFixed(2)
      : "—";
  const balBig = $("wallet-balance-big");
  const balUnit = $("wallet-balance-unit");
  if (balBig) balBig.textContent = spendStr;
  if (balUnit) {
    const net = (state.wallet && state.wallet.network && String(state.wallet.network).toLowerCase()) || "mainnet";
    balUnit.textContent = net === "testnet" ? "DOGE (testnet)" : "DOGE";
  }
  const pendRaw = t.pending_mempool_doge;
  const pendNum = Number(pendRaw);
  const hasPending = pendRaw != null && Number.isFinite(pendNum) && pendNum > 0;
  const pendRow = $("wallet-pending-row");
  const pendEl = $("wallet-pending-line");
  if (pendRow && pendEl) {
    if (hasPending) {
      pendEl.textContent = `${pendNum.toFixed(2)} DOGE`;
      pendRow.hidden = false;
    } else {
      pendEl.textContent = "—";
      pendRow.hidden = true;
    }
  }
  maybeNotifyPending(hasPending ? pendNum : 0);
  const spv = data.dashboard.spv || {};
  const mtr = data.dashboard.memetracker || {};
  const mtrMeta = $("mtr-mempool-meta");
  const mtrList = $("mtr-mempool-list");
  const mtrExpand = $("btn-mtr-mempool-expand");
  const mtrCard = $("mtr-mempool-card");
  const MTR_VISIBLE = 3;
  if (mtrMeta && mtrList) {
    if (mtr.engine_ok) {
      mtrMeta.textContent = `P2P workers connected: ${mtr.workers_connected ?? 0} · unique tx ids on relay: ${mtr.mempool_tx_count ?? 0}`;
    } else {
      mtrMeta.textContent = mtr.engine_error || "Mempool engine not available.";
    }
    mtrList.innerHTML = "";
    const rows = mtr.mempool_transactions || [];
    rows.forEach((row, i) => {
      const tx = row.txid != null ? String(row.txid) : "";
      if (!tx) return;
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "mtr-tx-item";
      if (i >= MTR_VISIBLE) btn.classList.add("mtr-extra-row");
      btn.setAttribute("role", "listitem");
      const tracked = !!row.tracked_match;
      const amt = row.amount_doge != null ? String(row.amount_doge) : "";
      btn.innerHTML =
        `<div class="mtr-tx-main"><div class="mtr-tx-id" title="${escapeHtml(tx)}">${escapeHtml(tx.length > 36 ? tx.slice(0, 34) + "…" : tx)}</div></div>` +
        `<div class="mtr-tx-badges">` +
        `<span class="badge-mtr-track ${tracked ? "" : "off"}">${tracked ? "Tracked" : "Scanning"}</span>` +
        (amt ? `<span class="badge-mtr-amt">${escapeHtml(amt)} DOGE</span>` : "") +
        `</div>`;
      btn.addEventListener("click", () => window.open(sochainTxUrl(tx), "_blank", "noopener,noreferrer"));
      mtrList.appendChild(btn);
    });
    if (mtrExpand && mtrCard) {
      const extra = rows.length - MTR_VISIBLE;
      if (extra > 0) {
        mtrExpand.classList.remove("hidden");
        mtrExpand.textContent = `Show all (${extra} more)`;
        mtrExpand.setAttribute("aria-expanded", "false");
        mtrCard.classList.remove("mtr-expanded");
      } else {
        mtrExpand.classList.add("hidden");
        mtrCard.classList.remove("mtr-expanded");
      }
    }
  }
  const chip = $("wallet-sync-chip");
  if (chip) {
    if (!Number.isFinite(Number(spv.header_height)) || Number(spv.header_height) <= 0) {
      chip.textContent = "Sync ETA unknown";
    } else {
      chip.textContent = humanizeSyncEta(spv.sync_lag_seconds);
    }
    chip.title = spv.sync_lag_label || "";
  }
  const spvDbg = $("log-spv-headers");
  if (spvDbg) {
    const h = Number(spv.header_height || 0);
    const hh = Number.isFinite(h) && h > 0 ? String(h) : "—";
    const ts = Number(spv.header_unix_time || 0);
    const tsText = Number.isFinite(ts) && ts > 0 ? new Date(ts * 1000).toISOString() : "—";
    const bh = spv.best_block_hash ? String(spv.best_block_hash) : "—";
    const lag = spv.sync_lag_label || "Unknown";
    const running = spv.running ? "yes" : "no";
    spvDbg.textContent = [
      `running: ${running}`,
      `header_height: ${hh}`,
      `sync: ${lag}`,
      `header_unix_time: ${ts > 0 ? String(ts) : "—"}`,
      `header_time_iso: ${tsText}`,
      `best_block_hash: ${bh}`,
      `spv_http_url: ${spv.spv_http_url || "—"}`
    ].join("\n");
  }
  const sample = data.dashboard.metrics_sample || [];
  initCharts();
  updateCharts(sample);
  if (data.dashboard.services) updateServicesControlUI(data.dashboard.services);
  renderDashboardTxPreview();
}

function renderDashboardTxPreview() {
  const list = $("dash-tx-list");
  if (!list) return;
  const openTxids = collectOpenTxids(list);
  list.innerHTML = "";
  const txs = Array.isArray(state.lastTxs) ? state.lastTxs : [];
  if (!txs.length) {
    const p = document.createElement("p");
    p.className = "small muted";
    p.textContent = "No transactions yet.";
    list.appendChild(p);
    return;
  }
  const max = Math.min(6, txs.length);
  for (let i = 0; i < max; i++) {
    const tx = txs[i] || {};
    const card = buildTxExpandableCard(tx, false);
    const txid = String((tx && tx.txid) || "").trim();
    if (txid && openTxids.has(txid)) card.open = true;
    list.appendChild(card);
  }
}

$("btn-svc-spv-stop")?.addEventListener("click", () => postServiceControl({ spv_enabled: false }));
$("btn-svc-spv-start")?.addEventListener("click", () => postServiceControl({ spv_enabled: true }));
$("btn-svc-mtr-stop")?.addEventListener("click", () => postServiceControl({ memetracker_enabled: false }));
$("btn-svc-mtr-start")?.addEventListener("click", () => postServiceControl({ memetracker_enabled: true }));

async function refreshTxList(refresh) {
  const q = "";
  const data = await api("/api/transactions" + q);
  const txs = (data.transactions || []).slice().sort((a, b) => {
    const ta = new Date(a && a.seen_at ? a.seen_at : 0).getTime() || 0;
    const tb = new Date(b && b.seen_at ? b.seen_at : 0).getTime() || 0;
    if (tb !== ta) return tb - ta;
    return String((b && b.txid) || "").localeCompare(String((a && a.txid) || ""));
  });
  state.lastTxs = txs;
  const hint = $("tx-sync-hint");
  if (hint) hint.textContent = refresh ? "Refreshed" : "";
  const list = $("tx-list");
  const emptyEl = $("tx-list-empty");
  if (!list) return;
  const openTxids = collectOpenTxids(list);
  list.innerHTML = "";
  if (!txs.length) {
    if (emptyEl) emptyEl.classList.remove("hidden");
  } else if (emptyEl) {
    emptyEl.classList.add("hidden");
  }
  let pendingNav = 0;
  txs.forEach((tx) => {
    const isMTR = String(tx.source || "").toLowerCase() === "memetracker";
    const showPending = isMTR || tx.pending;
    if (showPending) pendingNav += 1;
    const card = buildTxExpandableCard(tx, true);
    const txid = String((tx && tx.txid) || "").trim();
    if (txid && openTxids.has(txid)) card.open = true;
    list.appendChild(card);
  });
  const navB = $("nav-tx-pending-badge");
  if (navB) {
    if (pendingNav > 0) {
      navB.textContent = pendingNav > 99 ? "99+" : String(pendingNav);
      navB.classList.remove("hidden");
    } else {
      navB.classList.add("hidden");
    }
  }
  renderDashboardTxPreview();
}

function renderAddresses(w) {
  const addrs = w.addresses && w.addresses.length ? w.addresses : [];
  const list = $("addr-list");
  if (!list) return;
  list.innerHTML = "";
  if (!addrs.length) {
    const p = document.createElement("p");
    p.className = "muted small";
    p.style.padding = "0.5rem 0";
    p.textContent = "No address rows — restore or recreate wallet.";
    list.appendChild(p);
    return;
  }
  addrs.forEach((a) => {
    const card = document.createElement("div");
    card.className = "addr-card";
    card.setAttribute("role", "listitem");
    const head = document.createElement("div");
    head.className = "addr-card-head";
    const lab = document.createElement("span");
    lab.className = "addr-card-label";
    lab.textContent = a.label || "Address";
    head.appendChild(lab);
    if (a.primary) {
      const b = document.createElement("span");
      b.className = "badge-primary-addr";
      b.textContent = "Primary";
      head.appendChild(b);
    }
    const addrEl = document.createElement("div");
    addrEl.className = "addr-card-addr";
    addrEl.textContent = a.p2pkh_address || "";
    const actions = document.createElement("div");
    actions.className = "addr-card-actions";
    const bCopy = document.createElement("button");
    bCopy.type = "button";
    bCopy.className = "btn";
    bCopy.textContent = "Copy";
    bCopy.addEventListener("click", (e) => {
      e.stopPropagation();
      navigator.clipboard.writeText(a.p2pkh_address || "");
    });
    actions.appendChild(bCopy);
    if (!a.primary) {
      const bPrim = document.createElement("button");
      bPrim.type = "button";
      bPrim.className = "btn";
      bPrim.textContent = "Set primary";
      bPrim.addEventListener("click", async (e) => {
        e.stopPropagation();
        const r = await api("/api/wallet/primary", {
          method: "POST",
          body: JSON.stringify({ id: a.id }),
        });
        if (r.error) alert(r.error);
        await refreshWallet();
      });
      actions.appendChild(bPrim);
      const bDel = document.createElement("button");
      bDel.type = "button";
      bDel.className = "btn danger";
      bDel.textContent = "Remove";
      bDel.addEventListener("click", async (e) => {
        e.stopPropagation();
        const typed = prompt(`Type this address to confirm removal:\n\n${a.p2pkh_address || ""}`);
        if (typed == null) return;
        const r = await api("/api/wallet/addresses/" + encodeURIComponent(a.id), {
          method: "DELETE",
          body: JSON.stringify({ confirm_address: typed.trim() }),
        });
        if (r.error) {
          alert(r.error);
          return;
        }
        await refreshWallet();
      });
      actions.appendChild(bDel);
    }
    card.appendChild(head);
    card.appendChild(addrEl);
    card.appendChild(actions);
    list.appendChild(card);
  });
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

/** SoChain explorer URL for current wallet network */
function sochainTxUrl(txid) {
  const raw = String(txid || "").trim();
  if (!raw) return "#";
  const net = (state.wallet && state.wallet.network && String(state.wallet.network).toLowerCase()) || "mainnet";
  const coin = net === "testnet" ? "DOGETEST" : "DOGE";
  return "https://sochain.com/tx/" + coin + "/" + encodeURIComponent(raw);
}

async function openTxDetailModal(tx) {
  const modal = $("tx-detail-modal");
  const body = $("tx-detail-body");
  const sub = $("tx-detail-sub");
  const ext = $("btn-tx-external");
  const txid = String((tx && tx.txid) || "").trim();
  if (!modal || !body) return;
  state.txDetailTxid = txid;
  state.txDetailHex = "";
  modal.classList.remove("hidden");
  const localSummary = {
    txid: txid || "",
    source: tx && tx.source ? String(tx.source) : "",
    direction: tx && tx.direction ? String(tx.direction) : "",
    amount_doge: tx && tx.amount_doge != null ? Number(tx.amount_doge) : null,
    confirmations: tx && tx.confirmations != null ? Number(tx.confirmations) : 0,
    pending: !!(tx && tx.pending),
    pq_hint: !!(tx && tx.pq_hint),
    pq_verified: !!(tx && tx.pq_verified),
  };
  let localDetail = null;
  body.textContent = JSON.stringify({ local_tx: localSummary }, null, 2);
  if (sub) sub.textContent = "Local SPV/P2P wallet data only.";
  if (ext) {
    ext.href = sochainTxUrl(txid);
    ext.textContent = "Open on SoChain";
  }
  try {
    const localRes = await api("/api/tx/local/" + encodeURIComponent(txid));
    if (!localRes.error) {
      localDetail = localRes;
      if (localRes.local_raw_hex && /^[0-9a-f]+$/i.test(String(localRes.local_raw_hex))) {
        state.txDetailHex = String(localRes.local_raw_hex).trim();
      }
      body.textContent = JSON.stringify(
        {
          local_tx: localSummary,
          local_detail: localRes,
        },
        null,
        2
      ).slice(0, 500000);
      if (sub) {
        sub.textContent = state.txDetailHex
          ? "Local SPV/P2P details with raw hex captured from P2P tx relay."
          : "Local SPV/P2P details (raw hex not captured yet for this tx).";
      }
    }
  } catch {
    /* ignore local detail fetch errors */
  }
  const copyRawBtn = $("btn-tx-copy-raw");
  if (copyRawBtn) copyRawBtn.disabled = !state.txDetailHex;
}

function closeTxDetailModal() {
  const modal = $("tx-detail-modal");
  if (modal) modal.classList.add("hidden");
}

function getPrimaryAddress(w) {
  if (!w || !w.addresses || !w.addresses.length) return "";
  const p = w.addresses.find((a) => a.primary);
  return (p && p.p2pkh_address) || "";
}

function updateReceiveView() {
  const addr = getPrimaryAddress(state.wallet);
  const textEl = $("receive-addr-text");
  const qrEl = $("receive-qr");
  if (textEl) textEl.textContent = addr || "—";
  if (!qrEl) return;
  if (!addr) {
    qrEl.innerHTML = "";
    state.receiveQrAddr = null;
    state.receiveQrBucket = "";
    return;
  }
  const bucket = window.matchMedia("(max-width: 900px)").matches ? "sm" : "lg";
  const qrSize = bucket === "sm" ? 168 : 220;
  if (state.receiveQrAddr === addr && qrEl.querySelector("img, canvas") && state.receiveQrBucket === bucket) return;
  state.receiveQrAddr = addr;
  state.receiveQrBucket = bucket;
  qrEl.innerHTML = "";
  if (typeof QRCode === "undefined") {
    const p = document.createElement("p");
    p.className = "small muted";
    p.textContent = "QR library loading…";
    qrEl.appendChild(p);
    return;
  }
  try {
    new QRCode(qrEl, {
      text: addr,
      width: qrSize,
      height: qrSize,
      colorDark: "#0b0f14",
      colorLight: "#ffffff",
      correctLevel: QRCode.CorrectLevel.H,
    });
  } catch {
    qrEl.textContent = "Could not build QR code.";
  }
}

function syncMobileTabbar(name) {
  document.querySelectorAll(".mobile-tabbar-btn").forEach((b) => {
    const v = b.dataset.tabbar;
    b.classList.toggle("active", v === name);
  });
}

function parseQrAddress(raw) {
  let t = String(raw || "").trim();
  const uri = /^dogecoin:([^?]+)/i.exec(t);
  if (uri) t = uri[1];
  return t.trim();
}

async function closeQrScanner() {
  const modal = $("qr-scan-modal");
  if (modal) modal.classList.add("hidden");
  const h5 = state.qrScanner;
  state.qrScanner = null;
  if (h5) {
    try {
      await h5.stop();
    } catch {
      /* ignore */
    }
    try {
      h5.clear();
    } catch {
      /* ignore */
    }
  }
  const el = $("qr-reader");
  if (el) el.innerHTML = "";
}

function decodeQrFromImageFile(file) {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      const canvas = document.createElement("canvas");
      canvas.width = img.naturalWidth;
      canvas.height = img.naturalHeight;
      const ctx = canvas.getContext("2d");
      if (!ctx) {
        reject(new Error("canvas"));
        return;
      }
      ctx.drawImage(img, 0, 0);
      const imgData = ctx.getImageData(0, 0, canvas.width, canvas.height);
      if (typeof jsQR === "undefined") {
        reject(new Error("jsQR"));
        return;
      }
      const code = jsQR(imgData.data, imgData.width, imgData.height);
      if (code && code.data) resolve(code.data);
      else reject(new Error("no QR in image"));
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error("image"));
    };
    img.src = url;
  });
}

async function openQrScanner() {
  const modal = $("qr-scan-modal");
  if (!modal) return;
  if (!window.isSecureContext) {
    alert("Camera QR needs HTTPS. Pick a screenshot or photo of the QR code instead.");
    const fin = $("qr-file-input");
    if (fin) fin.click();
    return;
  }
  if (typeof Html5Qrcode === "undefined") {
    alert("QR scanner is not available. Check your network or use HTTPS.");
    return;
  }
  modal.classList.remove("hidden");
  const readerId = "qr-reader";
  const host = $(readerId);
  if (host) host.innerHTML = "";
  const h5 = new Html5Qrcode(readerId);
  state.qrScanner = h5;
  const cfg = { fps: 10, qrbox: { width: 240, height: 240 } };
  const onOk = (decodedText) => {
    const t = parseQrAddress(decodedText);
    const send = $("send-to");
    if (send) send.value = t;
    closeQrScanner();
  };
  try {
    await h5.start({ facingMode: "environment" }, cfg, onOk, () => {});
  } catch {
    try {
      const cams = await Html5Qrcode.getCameras();
      if (!cams || !cams.length) throw new Error("no cameras");
      await h5.start(cams[0].id, cfg, onOk, () => {});
    } catch {
      alert("Could not start camera. Use HTTPS, allow camera access, or paste the address.");
      await closeQrScanner();
    }
  }
}

document.querySelectorAll(".nav-item").forEach((btn) => {
  btn.addEventListener("click", () => showView(btn.dataset.view));
});

document.addEventListener("click", (e) => {
  const btn = e.target && e.target.closest ? e.target.closest("button") : null;
  if (!btn || btn.disabled) return;
  flashButtonFeedback(btn);
});

document.querySelectorAll(".mobile-tabbar-btn").forEach((btn) => {
  btn.addEventListener("click", () => {
    const v = btn.dataset.tabbar;
    if (v) showView(v);
  });
});

const btnScan = $("btn-scan-qr");
if (btnScan) btnScan.addEventListener("click", () => openQrScanner());
const btnQrCancel = $("btn-qr-scan-cancel");
if (btnQrCancel) btnQrCancel.addEventListener("click", () => closeQrScanner());
const qrBackdrop = $("qr-scan-backdrop");
if (qrBackdrop) qrBackdrop.addEventListener("click", () => closeQrScanner());

document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  const modal = $("qr-scan-modal");
  if (modal && !modal.classList.contains("hidden")) closeQrScanner();
});

const btnCopyRecv = $("btn-copy-receive");
if (btnCopyRecv) {
  btnCopyRecv.addEventListener("click", async () => {
    const addr = getPrimaryAddress(state.wallet);
    if (!addr) return;
    try {
      await navigator.clipboard.writeText(addr);
      const lbl = $("copy-receive-label");
      const prev = lbl ? lbl.textContent : "";
      if (lbl) lbl.textContent = "Copied";
      setTimeout(() => {
        if (lbl) lbl.textContent = prev;
      }, 1600);
    } catch {
      alert("Could not copy. Copy the address manually.");
    }
  });
}

function openSidebarNav() {
  const sb = $("sidebar");
  if (!sb) return;
  sb.classList.remove("collapsed");
  sb.classList.remove("sidebar-drawer-closed");
  syncSidebarDrawerToggleIcon();
}

$("btn-sidebar-toggle").addEventListener("click", () => {
  const sb = $("sidebar");
  if (!sb) return;
  if (isNarrowViewport()) {
    sb.classList.toggle("sidebar-drawer-closed");
  } else {
    sb.classList.toggle("collapsed");
  }
  syncSidebarDrawerToggleIcon();
});

const btnMob = $("btn-mobile-menu");
if (btnMob) {
  btnMob.addEventListener("click", () => openSidebarNav());
}

document.querySelectorAll("[data-send-tab]").forEach((tab) => {
  tab.addEventListener("click", () => setSendTab(parseInt(tab.dataset.sendTab, 10)));
});

/** Keep only digits and at most one '.' for DOGE amount fields. */
function sanitizeDecimalDogeString(raw) {
  let t = String(raw).replace(/[^\d.]/g, "");
  const d = t.indexOf(".");
  if (d !== -1) {
    t = t.slice(0, d + 1) + t.slice(d + 1).replace(/\./g, "");
  }
  return t;
}

function wireDecimalDogeInput(el) {
  if (!el) return;
  const apply = () => {
    const next = sanitizeDecimalDogeString(el.value);
    if (el.value !== next) el.value = next;
  };
  el.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const k = e.key;
    if (k.length !== 1) return;
    if ((k >= "0" && k <= "9") || k === ".") return;
    e.preventDefault();
  });
  el.addEventListener("input", apply);
  el.addEventListener("blur", apply);
}

wireDecimalDogeInput(document.getElementById("send-amt"));

document.getElementById("btn-create").addEventListener("click", async () => {
  const network = document.getElementById("net-select").value;
  const pqKeys = document.getElementById("pq-keys").checked;
  const data = await api("/api/wallet", {
    method: "POST",
    body: JSON.stringify({ network, pq_keys: pqKeys }),
  });
  if (data.error) {
    alert(data.error);
    return;
  }
  state.wallet = data.wallet;
  setOnboarding(data.wallet);
  renderAddresses(data.wallet);
  await refreshDashboard();
});

document.getElementById("btn-import").addEventListener("click", async () => {
  const f = document.getElementById("import-file").files[0];
  if (!f) {
    alert("Choose a JSON file");
    return;
  }
  const text = await f.text();
  const data = await api("/api/wallet/import", {
    method: "POST",
    body: text,
  });
  if (data.error) {
    alert(data.error);
    return;
  }
  await refreshWallet();
});

document.getElementById("btn-new-addr").addEventListener("click", async () => {
  const data = await api("/api/wallet/addresses", { method: "POST", body: "{}" });
  if (data.error) {
    alert(data.error);
    return;
  }
  state.wallet = data.wallet;
  renderAddresses(data.wallet);
  await refreshDashboard();
});

document.getElementById("btn-sync-tx").addEventListener("click", async () => {
  await refreshTxList(true);
});

const btnSpvFull = $("btn-spv-full-rescan");
const btnSpvRb = $("btn-spv-rollback");
if (btnSpvFull) {
  btnSpvFull.addEventListener("click", async () => {
    const syncSel = $("spv-rescan-sync-mode");
    const useCp = !syncSel || syncSel.value !== "genesis";
    const modeLabel = useCp ? "assisted header sync (spvnode -p)" : "from genesis (no -p)";
    if (!confirm(`Delete SPV headers.db and spv_wallet.db on this pup and restart spvnode with ${modeLabel}?`)) return;
    const prev = btnSpvFull.textContent;
    btnSpvFull.disabled = true;
    btnSpvFull.setAttribute("aria-busy", "true");
    btnSpvFull.textContent = "Working…";
    try {
      const out = $("spv-rescan-out");
      const res = await api("/api/spv/rescan", {
        method: "POST",
        body: JSON.stringify({ confirm: "RESCAN", mode: "full", use_checkpoint: useCp }),
      });
      if (out) out.textContent = JSON.stringify(res, null, 2);
      if (res.error) {
        let msg = res.error;
        if (res.hint) msg += "\n\n" + res.hint;
        alert(msg);
      }
      await refreshDashboard();
    } finally {
      btnSpvFull.disabled = false;
      btnSpvFull.removeAttribute("aria-busy");
      btnSpvFull.textContent = prev;
    }
  });
}
if (btnSpvRb) {
  btnSpvRb.addEventListener("click", async () => {
    const ckSel = $("spv-rollback-checkpoint-select");
    const mode = ckSel && ckSel.value ? ckSel.value : "custom";
    let body = { confirm: "ROLLBACK" };
    if (mode.startsWith("h-")) {
      const h = parseInt(mode.slice(2), 10);
      if (!Number.isFinite(h) || h < 0) {
        alert("Invalid checkpoint selection.");
        return;
      }
      if (!confirm(`Stop SPV, remove header rows above height ${h}, delete spv_wallet.db, and restart? (checkpoint rollback)`)) return;
      body.rollback_height = h;
    } else {
      const hash = ($("spv-rescan-hash") && $("spv-rescan-hash").value.trim()) || "";
      const hRaw = ($("spv-rescan-height") && $("spv-rescan-height").value.trim()) || "";
      const rollback_height = hRaw ? parseInt(hRaw, 10) : NaN;
      const hasHeight = Number.isFinite(rollback_height) && rollback_height >= 0;
      if (!hash && !hasHeight) {
        alert("Choose a checkpoint above, or pick Custom and enter a 64-character header hash or a rollback height (0+).");
        return;
      }
      if (!confirm("Stop SPV, truncate headers newer than the chosen block/height, delete spv_wallet.db, and restart?")) return;
      if (hash) body.rollback_block_hash = hash;
      if (hasHeight) body.rollback_height = rollback_height;
    }
    const prev = btnSpvRb.textContent;
    btnSpvRb.disabled = true;
    btnSpvRb.setAttribute("aria-busy", "true");
    btnSpvRb.textContent = "Working…";
    try {
      const out = $("spv-rescan-out");
      const res = await api("/api/spv/rescan", { method: "POST", body: JSON.stringify(body) });
      if (out) out.textContent = JSON.stringify(res, null, 2);
      if (res.error) {
        let msg = res.error;
        if (res.hint) msg += "\n\n" + res.hint;
        alert(msg);
      }
      await refreshDashboard();
    } finally {
      btnSpvRb.disabled = false;
      btnSpvRb.removeAttribute("aria-busy");
      btnSpvRb.textContent = prev;
    }
  });
}

const btnOpenSpvRepair = $("btn-open-spv-repair");
if (btnOpenSpvRepair) btnOpenSpvRepair.addEventListener("click", () => openSpvRepairModal());
const spvRepairBackdrop = $("spv-repair-backdrop");
if (spvRepairBackdrop) spvRepairBackdrop.addEventListener("click", closeSpvRepairModal);
const btnSpvRepairClose = $("btn-spv-repair-close");
if (btnSpvRepairClose) btnSpvRepairClose.addEventListener("click", closeSpvRepairModal);

function spvDbDebugShow(obj) {
  const out = $("spv-db-debug-out");
  const meta = $("spv-db-debug-meta");
  if (out) out.textContent = JSON.stringify(obj, null, 2);
  if (meta && obj) {
    const parts = [];
    if (obj.path) parts.push(String(obj.path));
    if (obj.format) parts.push(String(obj.format));
    if (obj.tables && Array.isArray(obj.tables)) parts.push(obj.tables.length + " tables");
    if (obj.row_count != null) parts.push(String(obj.row_count) + " rows" + (obj.truncated ? " (truncated)" : ""));
    meta.textContent = parts.length ? parts.join(" · ") : "—";
  }
}

const btnSpvDbProbe = $("btn-spv-db-probe");
if (btnSpvDbProbe) {
  btnSpvDbProbe.addEventListener("click", async () => {
    const res = await api("/api/debug/spv-wallet-db?op=meta");
    if (res.error) alert(res.error);
    spvDbDebugShow(res);
  });
}
const btnSpvDbTables = $("btn-spv-db-tables");
if (btnSpvDbTables) {
  btnSpvDbTables.addEventListener("click", async () => {
    const res = await api("/api/debug/spv-wallet-db?op=tables");
    if (res.error) alert(res.error);
    spvDbDebugShow(res);
  });
}
const btnSpvDbRun = $("btn-spv-db-run");
if (btnSpvDbRun) {
  btnSpvDbRun.addEventListener("click", async () => {
    const q = ($("spv-db-debug-query") && $("spv-db-debug-query").value.trim()) || "";
    if (!q) {
      alert("Enter a query");
      return;
    }
    const res = await api("/api/debug/spv-wallet-db?op=query&q=" + encodeURIComponent(q));
    if (res.error) alert(res.error);
    spvDbDebugShow(res);
  });
}

document.getElementById("btn-backup").addEventListener("click", async () => {
  const data = await api("/api/wallet");
  if (!data.wallet) return;
  const blob = new Blob([JSON.stringify(data.wallet, null, 2)], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "pq-doge-wallet-backup.json";
  a.click();
  URL.revokeObjectURL(a.href);
});

document.getElementById("delete-confirm").addEventListener("input", (e) => {
  document.getElementById("btn-delete").disabled = e.target.value.trim() !== "DELETE";
});

document.getElementById("btn-delete").addEventListener("click", async () => {
  if (!confirm("Permanently delete wallet and SPV data on this pup?")) return;
  const data = await api("/api/wallet", {
    method: "DELETE",
    body: JSON.stringify({ confirm: "DELETE" }),
  });
  if (data.error) {
    alert(data.error);
    return;
  }
  state.wallet = null;
  document.getElementById("delete-confirm").value = "";
  document.getElementById("btn-delete").disabled = true;
  await refreshWallet();
});

const btnMtrExpand = $("btn-mtr-mempool-expand");
if (btnMtrExpand) {
  btnMtrExpand.addEventListener("click", () => {
    const card = $("mtr-mempool-card");
    if (!card) return;
    const on = card.classList.toggle("mtr-expanded");
    btnMtrExpand.setAttribute("aria-expanded", on ? "true" : "false");
    const extra = card.querySelectorAll(".mtr-tx-item.mtr-extra-row").length;
    btnMtrExpand.textContent = on ? "Show less" : `Show all (${extra} more)`;
  });
}

const logoHome = $("btn-logo-home");
if (logoHome) {
  logoHome.addEventListener("click", () => showView("dashboard"));
}

document.getElementById("btn-send-pq-safe").addEventListener("click", async () => {
  const btn = document.getElementById("btn-send-pq-safe");
  const out = $("send-pq-out");
  const to_address = document.getElementById("send-to").value.trim();
  const amount_doge = document.getElementById("send-amt").value.trim();
  let tick = 0;
  const prevLabel = btn ? btn.innerHTML : "";
  if (btn) {
    btn.disabled = true;
    btn.innerHTML = '<span class="material-symbols-outlined btn-ico">hourglass_top</span> Broadcasting...';
  }
  const timer = setInterval(() => {
    tick = (tick + 1) % 4;
    if (out) out.textContent = `Sending transaction over libdogecoin P2P${".".repeat(tick)}\nWaiting for peer feedback...`;
  }, 350);
  try {
    const sendSignal =
      typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout === "function"
        ? AbortSignal.timeout(120000)
        : undefined;
    const res = await api("/api/send/pq-safe", {
      method: "POST",
      body: JSON.stringify({ to_address, amount_doge }),
      signal: sendSignal,
    });
    const sum = res && res.sendtx_summary ? res.sendtx_summary : null;
    if (out && sum) {
      const diag = Array.isArray(sum.sendtx_diagnostic_lines) ? sum.sendtx_diagnostic_lines : [];
      const lines = [
        `status: ${sum.status || "unknown"}`,
        `note: ${sum.human_note || "—"}`,
        `txid: ${res.txid || sum.broadcast_txid || "—"}`,
        `connected_nodes: ${sum.connected_nodes ?? 0}`,
        `informed_nodes: ${sum.informed_nodes ?? 0}`,
        `requested_from_nodes: ${sum.requested_from_nodes ?? 0}`,
        `seen_on_other_nodes: ${sum.seen_on_other_nodes ?? 0}`,
        sum.relay_heuristic_error ? `relay_heuristic_error (from sendtx tool, not Core RPC): ${sum.relay_heuristic_error}` : "",
        diag.length ? `sendtx_diagnostic_lines:\n${diag.map((d) => "  " + d).join("\n")}` : "",
        res.signed_raw_hex ? `signed_raw_hex (first 200 chars): ${String(res.signed_raw_hex).slice(0, 200)}${String(res.signed_raw_hex).length > 200 ? "…" : ""}` : "",
        "",
        JSON.stringify(res, null, 2)
      ].filter(Boolean);
      out.textContent = lines.join("\n");
    } else if (out) {
      out.textContent = JSON.stringify(res, null, 2);
    }
  } finally {
    clearInterval(timer);
    if (btn) {
      btn.disabled = false;
      btn.innerHTML = prevLabel;
    }
  }
});

document.getElementById("btn-sign").addEventListener("click", async () => {
  const raw = document.getElementById("raw-hex").value.trim();
  const inputIndex = parseInt(document.getElementById("vin-idx").value, 10) || 0;
  const res = await api("/api/tx/sign", {
    method: "POST",
    body: JSON.stringify({ raw_hex: raw, input_index: inputIndex, sighash_type: 1 }),
  });
  $("sign-out").textContent = JSON.stringify(res, null, 2);
  if (res.signed_raw_hex) {
    const manual = $("manual-signed-hex");
    if (manual) manual.value = res.signed_raw_hex;
  }
});

document.getElementById("btn-manual-broadcast").addEventListener("click", async () => {
  const hex = document.getElementById("manual-signed-hex").value.trim();
  const peers = document.getElementById("manual-peers").value.trim();
  const bc = await api("/api/tx/broadcast", {
    method: "POST",
    body: JSON.stringify({ raw_hex: hex, peers }),
  });
  $("manual-bc-out").textContent = JSON.stringify(bc, null, 2);
});

const qrFileInp = $("qr-file-input");
if (qrFileInp) {
  qrFileInp.addEventListener("change", async () => {
    const f = qrFileInp.files && qrFileInp.files[0];
    qrFileInp.value = "";
    if (!f) return;
    try {
      const text = await decodeQrFromImageFile(f);
      const t = parseQrAddress(text);
      const send = $("send-to");
      if (send) send.value = t;
      showView("tools");
    } catch {
      alert("Could not read a QR code from that image. Try another photo or paste the address.");
    }
  });
}

const btnUnlock = $("btn-unlock-wallet");
if (btnUnlock) {
  btnUnlock.addEventListener("click", async () => {
    const pin = ($("unlock-pin-input") && $("unlock-pin-input").value) || "";
    const errEl = $("unlock-err");
    if (errEl) errEl.textContent = "";
    const res = await api("/api/security/unlock", {
      method: "POST",
      body: JSON.stringify({ pin }),
    });
    if (res.error) {
      if (errEl) errEl.textContent = res.error;
      return;
    }
    const inp = $("unlock-pin-input");
    if (inp) inp.value = "";
    await refreshWallet();
  });
}

const btnSeal = $("btn-seal-wallet");
if (btnSeal) {
  btnSeal.addEventListener("click", async () => {
    const pin = ($("seal-pin-input") && $("seal-pin-input").value) || "";
    const msg = $("seal-msg");
    const res = await api("/api/security/seal", {
      method: "POST",
      body: JSON.stringify({ pin }),
    });
    if (res.error) {
      if (msg) msg.textContent = res.error;
      return;
    }
    if (msg) msg.textContent = "Encryption enabled.";
    const sealInp = $("seal-pin-input");
    if (sealInp) sealInp.value = "";
    window.alert(
      "Wallet encrypted successfully.\n\nYour wallet is now sealed on disk (Argon2id + AES-GCM). Use your PIN on the lock screen to unlock."
    );
    await refreshWallet();
  });
}

const btnUnseal = $("btn-unseal-wallet");
if (btnUnseal) {
  btnUnseal.addEventListener("click", async () => {
    const pin = ($("seal-pin-input") && $("seal-pin-input").value) || "";
    const msg = $("seal-msg");
    const res = await api("/api/security/unseal", {
      method: "POST",
      body: JSON.stringify({ pin }),
    });
    if (res.error) {
      if (msg) msg.textContent = res.error;
      return;
    }
    if (msg) msg.textContent = "Encryption disabled.";
    const sealInp = $("seal-pin-input");
    if (sealInp) sealInp.value = "";
    await refreshWallet();
  });
}

const btnLockSess = $("btn-lock-session");
if (btnLockSess) {
  btnLockSess.addEventListener("click", async () => {
    await api("/api/security/lock", { method: "POST", body: "{}" });
    await refreshWallet();
  });
}

function startPollers() {
  if (state.pollFast) clearInterval(state.pollFast);
  if (state.pollTx) clearInterval(state.pollTx);
  state.pollFast = setInterval(async () => {
    if (!state.wallet) return;
    try {
      await refreshDashboard();
    } catch {
      /* dashboard poll failed */
    }
  }, 4000);
  state.pollTx = setInterval(async () => {
    if (!state.wallet) return;
    await refreshTxList(false);
  }, 12000);
}

function wireImportFileUI() {
  const input = document.getElementById("import-file");
  const nameEl = document.getElementById("import-file-name");
  const wrap = document.querySelector(".file-upload");
  if (!input || !nameEl) return;
  function showName() {
    const f = input.files && input.files[0];
    nameEl.textContent = f ? f.name : "No file selected";
    nameEl.classList.toggle("has-file", !!f);
  }
  input.addEventListener("change", showName);
  if (!wrap) return;
  ["dragenter", "dragover", "dragleave", "drop"].forEach((ev) => {
    wrap.addEventListener(ev, (e) => {
      e.preventDefault();
      e.stopPropagation();
    });
  });
  ["dragenter", "dragover"].forEach((ev) => {
    wrap.addEventListener(ev, () => wrap.classList.add("file-upload--drag"));
  });
  ["dragleave", "drop"].forEach((ev) => {
    wrap.addEventListener(ev, () => wrap.classList.remove("file-upload--drag"));
  });
  wrap.addEventListener("drop", (e) => {
    const files = e.dataTransfer && e.dataTransfer.files;
    if (!files || !files.length) return;
    const file = files[0];
    const ok =
      !file.type ||
      file.type === "application/json" ||
      /\.json$/i.test(file.name);
    if (!ok) return;
    const dt = new DataTransfer();
    dt.items.add(file);
    input.files = dt.files;
    showName();
  });
}

wireImportFileUI();

let qrResizeTimer;
window.addEventListener("resize", () => {
  clearTimeout(qrResizeTimer);
  qrResizeTimer = setTimeout(() => {
    const sb = $("sidebar");
    const app = $("app");
    if (sb && app) {
      if (app.classList.contains("has-wallet")) {
        ensureMobileSidebarLayout();
      } else {
        ensureOnboardingMobileSidebar();
      }
    }
    if (state.view !== "receive") return;
    const bucket = window.matchMedia("(max-width: 900px)").matches ? "sm" : "lg";
    if (bucket === state.receiveQrBucket) return;
    state.receiveQrAddr = null;
    updateReceiveView();
  }, 200);
});

const txDetailBack = $("tx-detail-backdrop");
const txDetailClose = $("btn-tx-detail-close");
const btnTxCopyTxid = $("btn-tx-copy-txid");
const btnTxCopyRaw = $("btn-tx-copy-raw");
if (txDetailBack) txDetailBack.addEventListener("click", closeTxDetailModal);
if (txDetailClose) txDetailClose.addEventListener("click", closeTxDetailModal);
if (btnTxCopyTxid) {
  btnTxCopyTxid.addEventListener("click", async () => {
    const id = state.txDetailTxid || "";
    if (!id || !navigator.clipboard) return;
    try {
      await navigator.clipboard.writeText(id);
    } catch {
      /* ignore */
    }
  });
}
if (btnTxCopyRaw) {
  btnTxCopyRaw.addEventListener("click", async () => {
    const h = state.txDetailHex || "";
    if (!h || !navigator.clipboard) return;
    try {
      await navigator.clipboard.writeText(h);
    } catch {
      /* ignore */
    }
  });
}

if (typeof Notification !== "undefined" && Notification.permission === "default") {
  Notification.requestPermission().catch(() => {});
}

(async function initFooterVersion() {
  try {
    const r = await fetch("/api/health");
    const d = await r.json();
    const el = $("pq-footer-version");
    if (el && d.app_version) {
      const bh = d.build_hash != null ? String(d.build_hash) : "";
      const short = bh.length >= 12 ? bh.slice(0, 12) + "…" : bh;
      el.textContent = "v" + d.app_version + (short ? " · " + short : "");
      el.title = "PQ Wallet v" + d.app_version + (bh ? " — " + bh : "");
    }
  } catch {
    /* ignore */
  }
})();

refreshWallet();
