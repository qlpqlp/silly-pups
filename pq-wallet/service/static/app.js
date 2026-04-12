/* global Chart, QRCode, Html5Qrcode, jsQR */

async function api(path, opts) {
  const r = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  const text = await r.text();
  try {
    return JSON.parse(text);
  } catch {
    return { _raw: text, _status: r.status };
  }
}

const state = {
  wallet: null,
  walletLocked: false,
  lastPendingDoge: 0,
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

function $(id) {
  return document.getElementById(id);
}

function showView(name) {
  state.view = name;
  document.querySelectorAll(".nav-item").forEach((b) => {
    b.classList.toggle("active", b.dataset.view === name);
  });
  const titles = {
    dashboard: ["Dashboard", "SPV headers, balances, and post-quantum hints"],
    receive: ["Receive Dogecoin", "QR and address for your primary receiving address"],
    addresses: ["Addresses", "Generate keys and choose which address SPV watches"],
    transactions: ["Transactions", "PQ badges = explorer OP_RETURN hints (not a full audit)"],
    tools: ["Send Doge", "Destination & amount, or paste a signed raw hex for P2P broadcast"],
    logs: ["Logs", "SPV and broadcast log tails (~420 lines each)"],
    learn: ["How it works", "ECDSA vs PQ · send · verify · broadcast"],
    settings: ["Wallet file", "Backup or remove this pup’s wallet"],
  };
  const [t, s] = titles[name] || [name, ""];
  $("page-title").textContent = t;
  $("page-sub").textContent = s;

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
  if (name === "logs") {
    refreshLogs();
    state.pollLogs = setInterval(refreshLogs, 4000);
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
    const [spv, mtr, bc] = await Promise.all([
      fetch("/api/logs/spv?lines=200").then((r) => r.text()),
      fetch("/api/logs/mempooltracker").then((r) => r.text()),
      fetch("/api/logs/broadcast?lines=200").then((r) => r.text()),
    ]);
    const elS = $("log-spv");
    const elM = $("log-mtr");
    const elB = $("log-bc");
    if (elS) elS.textContent = spv;
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
    root.dataset.loaded = "1";
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
      card.className = "card";
      card.innerHTML = "<h3>Links</h3>";
      data.references.forEach((url) => {
        const a = document.createElement("a");
        a.href = url;
        a.target = "_blank";
        a.rel = "noopener";
        a.textContent = url;
        card.appendChild(document.createElement("br"));
        card.appendChild(a);
      });
      root.appendChild(card);
    }
    if (data.libdogecoin_build) {
      const p = document.createElement("p");
      p.className = "small muted";
      p.textContent = data.libdogecoin_build;
      root.appendChild(p);
    }
  } catch (e) {
    loading.textContent = "Could not load education.";
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
  ["view-dashboard", "view-receive", "view-addresses", "view-transactions", "view-tools", "view-logs", "view-learn", "view-settings"].forEach((id) => {
    $(id).classList.toggle("hidden", !has);
  });
  if (has) {
    if (w && w.network) $("net-badge").textContent = (w.network || "mainnet").toUpperCase();
    showView(state.view || "dashboard");
    ensureMobileSidebarLayout();
    updateReceiveView();
    startPollers();
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
    return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  } catch {
    return iso;
  }
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
  state.walletLocked = !!(data.locked && data.sealed);
  state.wallet = data.wallet;
  const lockEl = $("wallet-lock-screen");
  if (lockEl) {
    lockEl.classList.toggle("hidden", !state.walletLocked);
    lockEl.setAttribute("aria-hidden", state.walletLocked ? "false" : "true");
  }
  setOnboarding(data.wallet);
  if (data.wallet) {
    renderAddresses(data.wallet);
    updateReceiveView();
    await refreshDashboard();
    await refreshTxList(false);
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

async function refreshDashboard() {
  const data = await api("/api/dashboard");
  if (!data.dashboard) return;
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
  const pend = t.pending_mempool_doge;
  const pendRow = $("wallet-pending-row");
  const pendEl = $("wallet-pending-line");
  if (pendRow && pendEl) {
    if (pend != null && Number(pend) > 0) {
      pendEl.textContent = `${Number(pend).toFixed(2)} DOGE`;
      pendRow.hidden = false;
    } else {
      pendEl.textContent = "—";
      pendRow.hidden = true;
    }
  }
  maybeNotifyPending(pend);
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
  const h = spv.header_height;
  $("pill-height").textContent = h != null && Number(h) > 0 ? `height ${h}` : "height —";
  $("hdr-hash").textContent = spv.best_block_hash || "—";
  const sample = data.dashboard.metrics_sample || [];
  initCharts();
  updateCharts(sample);
}

async function refreshTxList(refresh) {
  const q = refresh ? "?refresh=1" : "";
  const data = await api("/api/transactions" + q);
  const txs = data.transactions || [];
  const hint = $("tx-sync-hint");
  if (hint) hint.textContent = refresh ? "Synced" : "";
  const list = $("tx-list");
  const emptyEl = $("tx-list-empty");
  if (!list) return;
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
    const card = document.createElement("article");
    card.className = "tx-card";
    card.setAttribute("role", "listitem");
    const txidFull = String(tx.txid || "").trim();
    if (txidFull) {
      card.style.cursor = "pointer";
      card.title = "Details (explorer + raw hex if available)";
      card.addEventListener("click", (e) => {
        if (e.target.closest("a, button")) return;
        openTxDetailModal(tx);
      });
    }
    const short = (tx.txid || "").slice(0, 22) + (tx.txid && tx.txid.length > 22 ? "…" : "");
    const head = document.createElement("div");
    head.className = "tx-card-head";
    const status = document.createElement("span");
    if (showPending) {
      status.className = "badge badge-tx-pending";
      status.textContent = "Pending";
    } else {
      status.className = "muted small";
      status.textContent = "Confirmed";
    }
    const amt = document.createElement("span");
    amt.className = "tx-card-amt mono";
    amt.textContent =
      tx.amount_doge != null && !Number.isNaN(Number(tx.amount_doge))
        ? `${Number(tx.amount_doge).toFixed(2)} DOGE`
        : "—";
    head.appendChild(status);
    head.appendChild(amt);
    const body = document.createElement("div");
    body.className = "tx-card-body";
    function addRow(label, valueEl) {
      const row = document.createElement("div");
      row.className = "tx-card-row";
      const k = document.createElement("span");
      k.className = "tx-card-k";
      k.textContent = label;
      row.appendChild(k);
      row.appendChild(valueEl);
      body.appendChild(row);
    }
    const txidEl = document.createElement("span");
    txidEl.className = "mono tx-card-txid";
    txidEl.title = tx.txid || "";
    txidEl.textContent = short;
    addRow("Txid", txidEl);
    const dirEl = document.createElement("span");
    dirEl.textContent = tx.direction || "";
    addRow("Dir", dirEl);
    const pqWrap = document.createElement("span");
    if (tx.pq_hint) {
      pqWrap.className = "badge-pq";
      pqWrap.innerHTML =
        '<span class="material-symbols-outlined" style="font-size:15px" aria-hidden="true">verified</span> PQ';
    } else {
      pqWrap.className = "badge-pq off";
      pqWrap.textContent = "—";
    }
    addRow("PQ", pqWrap);
    const srcEl = document.createElement("span");
    srcEl.textContent = tx.source || "";
    addRow("Source", srcEl);
    card.appendChild(head);
    card.appendChild(body);
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

/** Best-effort extract of a long transaction hex from explorer JSON (field names vary by API). */
function extractTxHexFromExplorerPayload(obj, depth) {
  if (depth === undefined) depth = 0;
  if (depth > 12 || obj == null) return "";
  if (typeof obj === "string") {
    const s = obj.trim();
    if (/^[0-9a-f]{64,}$/i.test(s) && s.length % 2 === 0) return s;
    return "";
  }
  if (typeof obj === "object" && !Array.isArray(obj)) {
    const keys = ["raw_hex", "hex", "transaction_hex", "tx_hex", "raw", "data_hex"];
    for (const k of keys) {
      if (obj[k] != null && typeof obj[k] === "string") {
        const h = extractTxHexFromExplorerPayload(obj[k], depth + 1);
        if (h) return h;
      }
    }
  }
  if (typeof obj === "object") {
    if (Array.isArray(obj)) {
      for (const it of obj) {
        const h = extractTxHexFromExplorerPayload(it, depth + 1);
        if (h) return h;
      }
    } else {
      for (const v of Object.values(obj)) {
        const h = extractTxHexFromExplorerPayload(v, depth + 1);
        if (h) return h;
      }
    }
  }
  return "";
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
  body.textContent = "Loading…";
  if (sub) sub.textContent = "Fetching explorer data when EXPLORER_TX_API is configured.";
  if (ext) {
    ext.href = sochainTxUrl(txid);
    ext.textContent = "Open on SoChain";
  }
  try {
    const res = await api("/api/explorer/tx/" + encodeURIComponent(txid));
    if (res.error) {
      body.textContent =
        String(res.error) +
        "\n\nYou can still copy the txid and use SoChain or another explorer. Configure EXPLORER_TX_API on the pup for JSON/raw from your indexer.";
      if (sub) sub.textContent = "Explorer API not configured or request failed.";
    } else if (res.raw != null && typeof res.raw === "string") {
      body.textContent = res.raw.slice(0, 500000);
      let parsed = null;
      try {
        parsed = JSON.parse(res.raw);
      } catch {
        parsed = null;
      }
      state.txDetailHex = parsed ? extractTxHexFromExplorerPayload(parsed) : "";
      if (!state.txDetailHex && /^[0-9a-f]+$/i.test(res.raw.trim()) && res.raw.trim().length >= 64) {
        state.txDetailHex = res.raw.trim();
      }
      if (sub) sub.textContent = "Upstream response (may include raw transaction fields).";
    } else {
      const payload = res.data != null ? res.data : res;
      const text = JSON.stringify(payload, null, 2);
      body.textContent = text.slice(0, 500000);
      state.txDetailHex = extractTxHexFromExplorerPayload(payload);
      if (sub) sub.textContent = "Parsed JSON — copy hex for signing/broadcast if your API exposes it.";
    }
  } catch (e) {
    body.textContent = String(e);
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
});

document.getElementById("btn-sync-tx").addEventListener("click", async () => {
  await refreshTxList(true);
});

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

document.getElementById("btn-explorer").addEventListener("click", async () => {
  const txid = document.getElementById("txid-input").value.trim();
  if (!txid) return;
  $("ex-out").textContent = JSON.stringify(await api("/api/explorer/tx/" + encodeURIComponent(txid)), null, 2);
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

document.getElementById("btn-send-pq-safe").addEventListener("click", async () => {
  const to_address = document.getElementById("send-to").value.trim();
  const amount_doge = document.getElementById("send-amt").value.trim();
  const res = await api("/api/send/pq-safe", {
    method: "POST",
    body: JSON.stringify({ to_address, amount_doge }),
  });
  $("send-pq-out").textContent = JSON.stringify(res, null, 2);
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
    if (msg) msg.textContent = "Wallet sealed. Unlock with PIN when you return.";
    const sealInp = $("seal-pin-input");
    if (sealInp) sealInp.value = "";
    window.alert(
      "Wallet encrypted successfully.\n\nYour wallet is now sealed on disk (Argon2id + AES-GCM). Use your PIN on the lock screen to unlock."
    );
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
    if (sb && app && app.classList.contains("has-wallet")) {
      if (isNarrowViewport()) {
        sb.classList.remove("collapsed");
      } else {
        sb.classList.remove("sidebar-drawer-closed");
      }
      syncSidebarDrawerToggleIcon();
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

refreshWallet();
