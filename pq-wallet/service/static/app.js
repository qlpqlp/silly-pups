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
    : (isMutation ? 30000 : 35000);
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

function promptPinModal(title, subtitle) {
  return new Promise((resolve) => {
    const modal = $("pin-modal");
    const titleEl = $("pin-modal-title");
    const subEl = $("pin-modal-sub");
    const input = $("pin-modal-input");
    const msgEl = $("pin-modal-msg");
    const btnOk = $("btn-pin-modal-confirm");
    const btnCancel = $("btn-pin-modal-cancel");
    const backdrop = $("pin-modal-backdrop");
    if (!modal || !input || !btnOk || !btnCancel) {
      resolve(null);
      return;
    }
    if (titleEl) titleEl.textContent = title || "Enter PIN";
    if (subEl) subEl.textContent = subtitle || "Authorize action";
    if (msgEl) msgEl.textContent = "";
    input.value = "";
    renderPinDisplay(input);
    modal.classList.remove("hidden");
    let done = false;
    const cleanup = () => {
      btnOk.removeEventListener("click", onOk);
      btnCancel.removeEventListener("click", onCancel);
      input.removeEventListener("keydown", onKey);
      if (backdrop) backdrop.removeEventListener("click", onCancel);
    };
    const finish = (val) => {
      if (done) return;
      done = true;
      cleanup();
      modal.classList.add("hidden");
      resolve(val);
    };
    const onOk = () => {
      const cleaned = normalizePin4(input.value);
      if (!isPin4(cleaned)) {
        if (msgEl) msgEl.textContent = "PIN must be exactly 4 numbers.";
        input.focus();
        return;
      }
      finish(cleaned);
    };
    const onCancel = () => finish(null);
    const onKey = (ev) => {
      if (ev.key === "Enter") {
        ev.preventDefault();
        onOk();
      } else if (ev.key === "Escape") {
        ev.preventDefault();
        onCancel();
      }
    };
    btnOk.addEventListener("click", onOk);
    btnCancel.addEventListener("click", onCancel);
    input.addEventListener("keydown", onKey);
    if (backdrop) backdrop.addEventListener("click", onCancel);
    setTimeout(() => input.focus(), 0);
  });
}

function normalizePin4(v) {
  return String(v || "").replace(/\D/g, "").slice(0, 4);
}

function isPin4(v) {
  return /^\d{4}$/.test(String(v || ""));
}

function renderPinDisplay(input) {
  if (!input || !input.id) return;
  const display = document.querySelector(`[data-pin-display-for="${input.id}"]`);
  if (!display) return;
  const cells = display.querySelectorAll(".pin4-cell");
  const pin = normalizePin4(input.value);
  const masked = input.dataset.pinMasked !== "0";
  for (let i = 0; i < cells.length; i += 1) {
    const d = pin[i] || "";
    cells[i].textContent = d ? (masked ? "•" : d) : "";
    cells[i].classList.toggle("pin4-cell--filled", !!d);
  }
}

function initPin4Inputs() {
  document.querySelectorAll(".pin4-input").forEach((input) => {
    const display = document.querySelector(`[data-pin-display-for="${input.id}"]`);
    if (display) {
      display.addEventListener("click", () => input.focus());
    }
    input.addEventListener("input", () => {
      const cleaned = normalizePin4(input.value);
      if (input.value !== cleaned) input.value = cleaned;
      renderPinDisplay(input);
    });
    input.addEventListener("focus", () => {
      if (display) display.classList.add("pin4-display--focus");
    });
    input.addEventListener("blur", () => {
      if (display) display.classList.remove("pin4-display--focus");
    });
    renderPinDisplay(input);
  });
}

async function ensurePinForSensitiveAction(actionLabel) {
  try {
    const sec = await api("/api/security/status");
    const sealed = !!(sec && sec.sealed);
    if (!sealed) return true;
    const promptLabel = actionLabel || "this action";
    const pin = await promptPinModal("Wallet PIN required", `Enter wallet PIN to authorize ${promptLabel}.`);
    if (pin == null) return false;
    const res = await api("/api/security/unlock", {
      method: "POST",
      body: JSON.stringify({ pin }),
    });
    if (!res || res.error) {
      alert((res && res.error) ? String(res.error) : "Could not verify PIN.");
      return false;
    }
    return true;
  } catch (e) {
    alert(e && e.message ? e.message : "Could not verify PIN.");
    return false;
  }
}

/** Non-secure origins (HTTP): Async Clipboard is unavailable; use legacy copy. */
function unsecuredCopyToClipboard(text) {
  const s = String(text ?? "");
  if (!s) return false;
  const ta = document.createElement("textarea");
  ta.value = s;
  ta.setAttribute("readonly", "");
  ta.style.cssText = "position:fixed;left:-9999px;top:0;width:1px;height:1px;opacity:0;";
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  try {
    ta.setSelectionRange(0, s.length);
  } catch {
    /* ignore */
  }
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  try {
    document.body.removeChild(ta);
  } catch {
    /* ignore */
  }
  return ok;
}

/**
 * Clipboard write for both HTTPS and HTTP: secure context uses
 * `navigator.clipboard.writeText`; otherwise falls back to `execCommand('copy')`.
 */
async function copyTextToClipboard(text) {
  const s = String(text ?? "");
  if (!s) return false;
  if (
    typeof navigator !== "undefined" &&
    navigator.clipboard &&
    typeof navigator.clipboard.writeText === "function" &&
    window.isSecureContext
  ) {
    try {
      await navigator.clipboard.writeText(s);
      return true;
    } catch {
      /* fall through */
    }
  }
  return unsecuredCopyToClipboard(s);
}

const state = {
  wallet: null,
  walletLocked: false,
  lastPendingDoge: 0,
  lastTxs: [],
  txHasMore: false,
  txTotal: 0,
  txPageSize: 40,
  txScrollIO: null,
  txScrollDashIO: null,
  view: "dashboard",
  charts: { mempool: null },
  pollFast: null,
  pollTx: null,
  pollLogs: null,
  pollSpvDeep: null,
  /** 0 = Send Doge, 1 = Manual PQ TX */
  sendTabIndex: 0,
  qrScanner: null,
  receiveQrAddr: null,
  receiveQrBucket: "",
  txDetailTxid: "",
  txDetailHex: "",
  inFlightDashboard: false,
  inFlightTx: false,
  inFlightLogs: false,
  inFlightSpvDeep: false,
  inFlightSpvLedger: false,
  lastDashboard: null,
  spvHeaderFeed: [],
  pqSendMode: "txc_txr",
};

const CACHE_DB_NAME = "pq-wallet-ui-cache";
const CACHE_DB_VERSION = 1;
const CACHE_STORE = "snapshots";
const CACHE_KEY_DASHBOARD = "dashboard_v1";
const CACHE_KEY_TXS = "txs_v1";

function sortTxRowsDesc(a, b) {
  const ta = new Date(a && a.seen_at ? a.seen_at : 0).getTime() || 0;
  const tb = new Date(b && b.seen_at ? b.seen_at : 0).getTime() || 0;
  if (tb !== ta) return tb - ta;
  const ha = Number((a && a.block_height) || 0);
  const hb = Number((b && b.block_height) || 0);
  if (hb !== ha) return hb - ha;
  const ca = Number((a && a.confirmations) || 0);
  const cb = Number((b && b.confirmations) || 0);
  if (cb !== ca) return cb - ca;
  return String((b && b.txid) || "").localeCompare(String((a && a.txid) || ""));
}

function mergeTxRowsDedupe(existing, more) {
  const by = new Map();
  for (const t of existing || []) {
    const id = String((t && t.txid) || "")
      .trim()
      .toLowerCase();
    if (id) by.set(id, t);
  }
  for (const t of more || []) {
    const id = String((t && t.txid) || "")
      .trim()
      .toLowerCase();
    if (!id) continue;
    if (!by.has(id)) by.set(id, t);
  }
  return Array.from(by.values()).sort(sortTxRowsDesc);
}

function teardownTxListScrollObserver() {
  if (state.txScrollIO) {
    try {
      state.txScrollIO.disconnect();
    } catch {
      /* ignore */
    }
    state.txScrollIO = null;
  }
  if (state.txScrollDashIO) {
    try {
      state.txScrollDashIO.disconnect();
    } catch {
      /* ignore */
    }
    state.txScrollDashIO = null;
  }
}

function initTxListScrollObserver() {
  const onNear = (entries) => {
    for (const e of entries) {
      if (e.isIntersecting) loadMoreTxListPage().catch(() => {});
    }
  };
  const tRoot = $("tx-list-scroll");
  const tSent = $("tx-list-sentinel");
  if (tRoot && tSent && !state.txScrollIO) {
    state.txScrollIO = new IntersectionObserver(onNear, { root: tRoot, rootMargin: "140px", threshold: 0.01 });
    state.txScrollIO.observe(tSent);
  }
  const dRoot = $("dash-tx-list-scroll");
  const dSent = $("dash-tx-list-sentinel");
  if (dRoot && dSent && !state.txScrollDashIO) {
    state.txScrollDashIO = new IntersectionObserver(onNear, { root: dRoot, rootMargin: "140px", threshold: 0.01 });
    state.txScrollDashIO.observe(dSent);
  }
}
const LOCAL_KEY_PQ_SEND_MODE = "pq_send_mode_v1";
const LOCAL_KEY_SEND_FEE_DOGE_PER_KB = "pq_send_fee_doge_per_kb_v1";
const DEFAULT_FEE_PER_KB_DOGE = "0.01";
const reFeePerKbDoge = /^\d+(\.\d+)?$/;

function cacheOpenDB() {
  return new Promise((resolve, reject) => {
    if (typeof indexedDB === "undefined") {
      reject(new Error("indexeddb unavailable"));
      return;
    }
    const req = indexedDB.open(CACHE_DB_NAME, CACHE_DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(CACHE_STORE)) {
        db.createObjectStore(CACHE_STORE);
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error || new Error("indexeddb open failed"));
  });
}

async function cacheSet(key, value) {
  try {
    const db = await cacheOpenDB();
    await new Promise((resolve, reject) => {
      const tx = db.transaction(CACHE_STORE, "readwrite");
      const store = tx.objectStore(CACHE_STORE);
      const req = store.put({ key, value, saved_at: Date.now() }, key);
      req.onsuccess = () => resolve();
      req.onerror = () => reject(req.error || new Error("indexeddb put failed"));
    });
    db.close();
  } catch {
    /* cache best effort only */
  }
}

async function cacheGet(key) {
  try {
    const db = await cacheOpenDB();
    const row = await new Promise((resolve, reject) => {
      const tx = db.transaction(CACHE_STORE, "readonly");
      const store = tx.objectStore(CACHE_STORE);
      const req = store.get(key);
      req.onsuccess = () => resolve(req.result || null);
      req.onerror = () => reject(req.error || new Error("indexeddb get failed"));
    });
    db.close();
    if (!row || typeof row !== "object") return null;
    return row.value != null ? row.value : null;
  } catch {
    return null;
  }
}

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

function resetImportSpvRestoreSelectPlaceholder() {
  const sel = $("import-spv-restore-select");
  if (!sel) return;
  sel.innerHTML = "";
  const ph = document.createElement("option");
  ph.value = "";
  ph.textContent = "Choose a backup JSON file first…";
  ph.disabled = true;
  ph.selected = true;
  sel.appendChild(ph);
  sel.disabled = true;
}

/** After a backup file is chosen, populate SPV restore options (genesis default + bundled heights). */
async function refreshImportSpvRestoreOptionsFromFile(file) {
  const sel = $("import-spv-restore-select");
  if (!sel) return;
  if (!file) {
    resetImportSpvRestoreSelectPlaceholder();
    return;
  }
  try {
    const text = await file.text();
    const parsed = JSON.parse(text);
    const w = parsed.wallet && typeof parsed.wallet === "object" ? parsed.wallet : parsed;
    const net =
      w && w.network && String(w.network).toLowerCase() === "testnet" ? "testnet" : "mainnet";
    const st = await api("/api/spv/status");
    const rows =
      st && st.spv_checkpoints && Array.isArray(st.spv_checkpoints[net]) ? st.spv_checkpoints[net] : [];
    sel.innerHTML = "";
    const gen = document.createElement("option");
    gen.value = "genesis";
    gen.textContent = "Genesis — block 0 — full header chain (no -p)";
    gen.selected = true;
    sel.appendChild(gen);
    for (let i = 0; i < rows.length; i++) {
      const row = rows[i] || {};
      const h = row.height != null ? Number(row.height) : NaN;
      const hash = row.hash != null ? String(row.hash) : "";
      const ts = row.timestamp != null ? Number(row.timestamp) : 0;
      if (!Number.isFinite(h) || hash.length < 32) continue;
      if (h === 0) continue;
      const o = document.createElement("option");
      o.value = "h-" + h;
      o.textContent = "Block " + h + " — " + spvShortHashHex(hash) + " — " + spvFmtCheckpointUtc(ts);
      sel.appendChild(o);
    }
    sel.disabled = false;
  } catch {
    sel.innerHTML = "";
    const err = document.createElement("option");
    err.value = "";
    err.textContent = "Invalid JSON — fix backup file";
    err.disabled = true;
    err.selected = true;
    sel.appendChild(err);
    sel.disabled = true;
  }
}

function buildSpvOnRestoreFromImportSelect() {
  const sel = $("import-spv-restore-select");
  const v = sel && sel.value ? sel.value : "genesis";
  if (v === "genesis" || v === "") return { sync: "genesis", height: 0 };
  if (v.startsWith("h-")) {
    const h = parseInt(v.slice(2), 10);
    if (Number.isFinite(h) && h > 0) return { sync: "bundled_checkpoints", height: h };
  }
  return { sync: "genesis", height: 0 };
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
      const t = `Storage: ${dir} — headers.db ${hp ? "present (" + path + ")" + fmt : "not present yet (expected " + path + ")"}`;
      line.textContent = t;
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
    tools: ["Send Doge", "PQ-safe send or expand Manual PQ TX for raw hex broadcast"],
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
  if (state.pollSpvDeep) {
    clearInterval(state.pollSpvDeep);
    state.pollSpvDeep = null;
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
    refreshSpvDeepLog();
    state.pollSpvDeep = setInterval(refreshSpvDeepLog, 8000);
    refreshPQCarrierStatus().catch(() => {});
    refreshDashboard().catch(() => {});
  }
  if (name === "transactions") {
    initTxListScrollObserver();
    refreshTxList(false, { full: false }).catch(() => {});
  }
  if (name === "dashboard") {
    initTxListScrollObserver();
    refreshTxList(false, { full: false }).catch(() => {});
  }
  if (name === "tools") {
    setSendTab(typeof state.sendTabIndex === "number" ? state.sendTabIndex : 0);
  }
  if (isNarrowViewport()) {
    const sb = $("sidebar");
    if (sb) {
      sb.classList.add("sidebar-drawer-closed");
      syncSidebarDrawerToggleIcon();
      syncMobileMenuIcon();
    }
  }
}

async function refreshLogs() {
  if (state.inFlightLogs) return;
  state.inFlightLogs = true;
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
      fetch("/api/logs/mempooltracker", { signal: mkSignal(15000) }).then((r) => r.text()),
      fetch("/api/logs/broadcast?lines=200", { signal: mkSignal(15000) }).then((r) => r.text()),
    ]);
    const elM = $("log-mtr");
    const elB = $("log-bc");
    if (elM) elM.textContent = mtr;
    if (elB) elB.textContent = bc;
  } catch {
    /* ignore */
  } finally {
    state.inFlightLogs = false;
  }
}

async function refreshPQCarrierStatus() {
  if (state.view !== "settings") return;
  const out = $("pq-carrier-status-out");
  try {
    const res = await api("/api/pq/carrier/status", { timeout_ms: 25000 });
    if (res && res.error) {
      if (out) out.textContent = JSON.stringify(res, null, 2);
      return;
    }
    if (out) out.textContent = JSON.stringify(res, null, 2);
  } catch (e) {
    if (out) out.textContent = `(carrier status failed: ${e && e.message ? e.message : e})`;
  }
}

function spvDeepDebugEnabled() {
  const merkle = $("spv-deep-merkle");
  const hex = $("spv-deep-hex");
  const addr = $("spv-deep-addr");
  const raw = $("spv-deep-rawhdr");
  const tail = $("spv-deep-tail");
  return Boolean(
    (merkle && merkle.checked) ||
      (hex && hex.checked) ||
      (addr && addr.checked) ||
      (raw && raw.checked) ||
      (tail && tail.checked)
  );
}

function buildSpvDeepQuery() {
  const params = new URLSearchParams();
  params.set("lines", "1200");
  const merkle = $("spv-deep-merkle");
  const hex = $("spv-deep-hex");
  const addr = $("spv-deep-addr");
  const raw = $("spv-deep-rawhdr");
  const tail = $("spv-deep-tail");
  const hInp = $("spv-deep-height");
  if (merkle && merkle.checked) params.set("merkle", "1");
  if (hex && hex.checked) params.set("hex", "1");
  if (addr && addr.checked) params.set("addr", "1");
  if (raw && raw.checked) params.set("raw_header", "1");
  if (tail && tail.checked) params.set("tail", "1");
  if (hInp && String(hInp.value || "").trim()) {
    params.set("height", String(hInp.value || "").trim());
  }
  return params.toString();
}

async function refreshSpvLedgerSnapshot() {
  if (state.view !== "settings") return;
  const el = $("log-spv-ledger");
  if (state.inFlightSpvLedger) return;
  state.inFlightSpvLedger = true;
  try {
    const mkSignal = (ms) => {
      if (typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout === "function") {
        return AbortSignal.timeout(ms);
      }
      if (typeof AbortController === "undefined") return undefined;
      const c = new AbortController();
      setTimeout(() => {
        try {
          c.abort();
        } catch {
          /* ignore */
        }
      }, ms);
      return c.signal;
    };
    const res = await fetch("/api/debug/spv-ledger", { signal: mkSignal(45000) });
    const body = await res.text();
    let pretty = body;
    try {
      pretty = JSON.stringify(JSON.parse(body), null, 2);
    } catch {
      /* keep raw */
    }
    if (el) el.textContent = res.ok ? pretty : `${res.status} ${res.statusText}\n${pretty}`;
  } catch (e) {
    if (el) el.textContent = `(ledger snapshot failed: ${e && e.message ? e.message : e})`;
  } finally {
    state.inFlightSpvLedger = false;
  }
}

async function refreshSpvDeepLog() {
  if (state.view !== "settings") return;
  if (!spvDeepDebugEnabled()) return;
  if (state.inFlightSpvDeep) return;
  state.inFlightSpvDeep = true;
  const el = $("log-spv-deep");
  try {
    const mkSignal = (ms) => {
      if (typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout === "function") {
        return AbortSignal.timeout(ms);
      }
      if (typeof AbortController === "undefined") return undefined;
      const c = new AbortController();
      setTimeout(() => {
        try {
          c.abort();
        } catch {
          /* ignore */
        }
      }, ms);
      return c.signal;
    };
    const qs = buildSpvDeepQuery();
    const txt = await fetch(`/api/logs/spv-deep?${qs}`, { signal: mkSignal(20000) }).then((r) => r.text());
    if (el) el.textContent = txt;
  } catch {
    if (el) el.textContent = "(spv deep digest unavailable — try Refresh deep)";
  } finally {
    state.inFlightSpvDeep = false;
  }
}

function rememberSpvHeaderSample(spv) {
  const h = Number(spv && spv.header_height);
  const ts = Number(spv && spv.header_unix_time);
  const hash = spv && spv.best_block_hash ? String(spv.best_block_hash) : "";
  if (!Number.isFinite(h) || h <= 0) return;
  const key = `${h}|${Number.isFinite(ts) && ts > 0 ? ts : 0}|${hash}`;
  const feed = Array.isArray(state.spvHeaderFeed) ? state.spvHeaderFeed.slice() : [];
  if (feed.some((row) => row.key === key)) return;
  feed.unshift({
    key,
    height: h,
    ts: Number.isFinite(ts) && ts > 0 ? ts : 0,
    hash,
  });
  state.spvHeaderFeed = feed.slice(0, 12);
}

function syncToolsTopBarFromSendTab(n) {
  const titleText = $("page-title-text");
  const titleIco = $("page-title-ico");
  const subEl = $("page-sub");
  const titleEl = $("page-title");
  if (n === 0) {
    if (titleText) titleText.textContent = "Send Doge";
    if (titleIco) titleIco.textContent = "send";
    if (subEl) subEl.textContent = "PQ-safe send — tap Manual PQ TX below for raw hex.";
    if (titleEl) titleEl.setAttribute("title", subEl ? subEl.textContent : "");
  } else {
    if (titleText) titleText.textContent = "Manual PQ TX";
    if (titleIco) titleIco.textContent = "terminal";
    if (subEl) subEl.textContent = "Paste signed raw hex and broadcast — tap Send Doge above to return.";
    if (titleEl) titleEl.setAttribute("title", subEl ? subEl.textContent : "");
  }
}

function setSendTab(n) {
  if (n !== 0 && n !== 1) n = 0;
  state.sendTabIndex = n;
  const secNorm = $("send-acc-section-normal");
  const secMan = $("send-acc-section-manual");
  const bodyNorm = $("send-acc-body-normal");
  const bodyMan = $("send-acc-body-manual");
  const trNorm = $("send-acc-trigger-normal");
  const trMan = $("send-acc-trigger-manual");
  if (n === 0) {
    secNorm?.classList.add("send-acc-section--open");
    secMan?.classList.remove("send-acc-section--open");
    bodyNorm?.classList.remove("hidden");
    bodyMan?.classList.add("hidden");
    trNorm?.setAttribute("aria-expanded", "true");
    trMan?.setAttribute("aria-expanded", "false");
  } else {
    secNorm?.classList.remove("send-acc-section--open");
    secMan?.classList.add("send-acc-section--open");
    bodyNorm?.classList.add("hidden");
    bodyMan?.classList.remove("hidden");
    trNorm?.setAttribute("aria-expanded", "false");
    trMan?.setAttribute("aria-expanded", "true");
  }
  if (state.view === "tools") {
    syncToolsTopBarFromSendTab(n);
  }
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
    teardownTxListScrollObserver();
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
    showView(state.view || "dashboard");
    ensureMobileSidebarLayout();
    updateReceiveView();
    startPollers();
  } else {
    ensureOnboardingMobileSidebar();
  }
  syncMobileMenuIcon();
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
  syncMobileMenuIcon();
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
  syncMobileMenuIcon();
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

function syncMobileMenuIcon() {
  const sb = $("sidebar");
  const btn = $("btn-mobile-menu");
  if (!sb || !btn) return;
  const icon = btn.querySelector(".material-symbols-outlined");
  if (!icon) return;
  if (isNarrowViewport()) {
    const shut = sb.classList.contains("sidebar-drawer-closed");
    icon.textContent = shut ? "menu" : "close";
    btn.setAttribute("aria-expanded", shut ? "false" : "true");
    btn.setAttribute("aria-label", shut ? "Open navigation menu" : "Close navigation menu");
    btn.title = shut ? "Menu" : "Close";
    return;
  }
  icon.textContent = "menu";
  btn.setAttribute("aria-expanded", "false");
  btn.setAttribute("aria-label", "Open navigation menu");
  btn.title = "Menu";
}

function updateSyncChipVisual(label) {
  const chip = $("wallet-sync-chip-top");
  if (!chip) return;
  const txt = String(label || "Sync");
  chip.textContent = txt;
  chip.classList.remove("syncing", "synced");
  if (txt === "Synced") chip.classList.add("synced");
  else chip.classList.add("syncing");
  // On mobile, hide the sync badge once fully synced.
  if (isNarrowViewport() && txt === "Synced") {
    chip.classList.add("hidden");
  } else {
    chip.classList.remove("hidden");
  }
}

function triggerDogeWowWords() {
  const layer = $("doge-wow-layer");
  if (!layer) return;
  const words = [
    "Much Wow", "Such Quantum", "Very Doge", "So Secure", "Many Commitments",
    "Wow Ledger", "Such Reveal", "Very Shibe", "So Fast", "Much Chain",
  ];
  const colors = ["#ef4444", "#f59e0b", "#3b82f6", "#10b981", "#eab308", "#f97316", "#a855f7"];
  const vw = Math.max(window.innerWidth || 0, 320);
  const vh = Math.max(window.innerHeight || 0, 480);
  const count = 12;
  for (let i = 0; i < count; i++) {
    const el = document.createElement("span");
    el.className = "doge-wow-word";
    el.textContent = words[Math.floor(Math.random() * words.length)];
    el.style.color = colors[Math.floor(Math.random() * colors.length)];
    const x = Math.round(8 + Math.random() * 84);
    const y = Math.round(16 + Math.random() * 70);
    const rot = Math.round(-22 + Math.random() * 44);
    const sizeRem = (0.95 + Math.random() * 1.5).toFixed(2);
    const s0 = (0.82 + Math.random() * 0.28).toFixed(2);
    const s1 = (1.02 + Math.random() * 0.35).toFixed(2);
    const delay = Math.round(Math.random() * 280);
    el.style.left = `${(vw * x) / 100}px`;
    el.style.top = `${(vh * y) / 100}px`;
    el.style.setProperty("--wow-rot", `${rot}deg`);
    el.style.setProperty("--wow-size", `${sizeRem}rem`);
    el.style.setProperty("--wow-scale-start", s0);
    el.style.setProperty("--wow-scale-end", s1);
    el.style.animationDelay = `${delay}ms`;
    layer.appendChild(el);
    setTimeout(() => {
      try { el.remove(); } catch { /* ignore */ }
    }, 1500 + delay);
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
  if (!Number.isFinite(n) || n < 0) return "";
  if (n === 0) return "Synced";
  if (n < 60) return "";
  if (n < 3600) return `Sync ${Math.ceil(n / 60)} minutes`;
  if (n < 86400) return `Sync ${Math.ceil(n / 3600)} hours`;
  if (n < 86400 * 30) return `Sync ${Math.ceil(n / 86400)} days`;
  return `Sync ${Math.ceil(n / (86400 * 30))} months`;
}

function fitWalletHeroBalance() {
  const el = $("wallet-balance-big");
  if (!el) return;
  const row = el.closest(".wallet-hero-balance-row") || el.parentElement;
  if (!row) return;

  // Reset inline font-size to allow the CSS clamp to re-apply.
  el.style.fontSize = "";

  const maxW = row.clientWidth;
  if (!Number.isFinite(maxW) || maxW <= 0) return;

  const cs = window.getComputedStyle(el);
  let fs = parseFloat(cs.fontSize);
  if (!Number.isFinite(fs) || fs <= 0) return;

  // Reduce font size until the value fits. Keep a reasonable floor.
  const floorPx = 14;
  const maxIter = 18;
  for (let i = 0; i < maxIter; i++) {
    // scrollWidth includes content, independent of clipping from overflow.
    if (el.scrollWidth <= maxW - 10) break;
    fs = Math.max(floorPx, fs * 0.93);
    el.style.fontSize = fs.toFixed(2) + "px";
    if (fs <= floorPx + 0.01) break;
  }
}

function firstPrevTxidFromRawHex(rawHex) {
  const s = String(rawHex || "").trim().toLowerCase();
  if (!/^[0-9a-f]+$/.test(s) || s.length < 90) return "";
  try {
    let off = 8;
    if (s.slice(off, off + 4) === "0001") off += 4;
    const inCount = parseInt(s.slice(off, off + 2), 16);
    if (!Number.isFinite(inCount) || inCount < 1) return "";
    off += 2;
    const prevLe = s.slice(off, off + 64);
    if (!/^[0-9a-f]{64}$/.test(prevLe)) return "";
    const bytes = prevLe.match(/../g) || [];
    return bytes.reverse().join("");
  } catch {
    return "";
  }
}

function quantumMetaFromTx(tx, localRawHex) {
  const raw = String(localRawHex || tx.raw_hex || "").toLowerCase();
  const hint = !!tx.pq_hint || raw.includes("6a24464c4331") || raw.includes("6a2444494c32") || raw.includes("6a2452434734");
  const isReveal = raw.includes("464c433146554c4c") || raw.includes("44494c3246554c4c") || raw.includes("5243473446554c4c");
  const linkedTxC = isReveal ? firstPrevTxidFromRawHex(raw) : "";
  if (!hint && !isReveal) {
    return { label: "Classic", detail: "Classic transaction", pairTxid: "" };
  }
  if (isReveal) {
    return { label: "Quantum Reveal", detail: linkedTxC ? `Quantum reveal (linked TX_C: ${linkedTxC})` : "Quantum reveal", pairTxid: linkedTxC };
  }
  return { label: "Quantum Commit", detail: "Quantum commitment transaction", pairTxid: "" };
}

function buildTxExpandableCard(tx, includeSource) {
  const txidFull = String(tx.txid || "").trim();
  const conf = Number(tx.confirmations || 0);
  const pending = !!tx.pending || conf <= 0 || String(tx.source || "").toLowerCase() === "memetracker";
  const dir = String(tx.direction || "unknown").toLowerCase();
  const nAmt = tx.amount_doge != null && !Number.isNaN(Number(tx.amount_doge)) ? Number(tx.amount_doge) : null;
  let sign = "";
  if (dir === "out") sign = "-";
  else if (dir === "in") sign = "+";
  else if (nAmt != null && nAmt > 0) sign = "+";
  const amount = nAmt != null ? `${sign}${nAmt.toFixed(2)}` : "—";
  const seen = tx.seen_at ? fmtTime(tx.seen_at) : "—";
  const qm = quantumMetaFromTx(tx, null);
  const pqHint = qm.label !== "Classic";
  const isConfirmed = conf > 0;
  const short = txidFull ? txidFull.slice(0, 18) + (txidFull.length > 18 ? "…" : "") : "—";
  const addrLine = String(tx.address || "").trim() || "—";
  const card = document.createElement("details");
  card.className = "tx-card tx-card-modern";
  card.setAttribute("role", "listitem");
  if (txidFull) card.dataset.txid = txidFull;
  const summary = document.createElement("summary");
  const left = document.createElement("div");
  left.className = "tx-main";
  const row1 = document.createElement("div");
  row1.className = "tx-main-top tx-main-row-wallet";
  const statusDot = document.createElement("span");
  statusDot.className = "tx-status-dot";
  statusDot.classList.toggle("pending", pending);
  statusDot.classList.toggle("confirmed", !pending && isConfirmed);
  statusDot.title = pending ? "Mempool / unconfirmed" : `Confirmed (${conf} confirmation${conf === 1 ? "" : "s"})`;
  statusDot.setAttribute("aria-label", statusDot.title);
  const timeEl = document.createElement("span");
  timeEl.className = "tx-time-inline";
  timeEl.textContent = seen;
  const addrEl = document.createElement("span");
  addrEl.className = "tx-addr-inline mono";
  addrEl.textContent = addrLine;
  addrEl.title = addrLine;
  const pqBadge = document.createElement("span");
  pqBadge.className = "tx-quantum-badge" + (pqHint ? "" : " off");
  pqBadge.textContent = qm.label;
  row1.appendChild(statusDot);
  row1.appendChild(timeEl);
  row1.appendChild(pqBadge);
  row1.appendChild(addrEl);
  left.appendChild(row1);
  const meta = document.createElement("div");
  meta.className = "tx-meta-line mono";
  meta.textContent = short;
  left.appendChild(meta);
  const right = document.createElement("div");
  right.style.display = "flex";
  right.style.alignItems = "center";
  right.style.gap = "0.5rem";
  const amt = document.createElement("span");
  amt.className = "tx-card-amt mono";
  if (sign === "+") amt.classList.add("tx-amt-in");
  else if (sign === "-") amt.classList.add("tx-amt-out");
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
  addRow(isConfirmed ? "Block time" : "Seen", seen, false);
  addRow("Address", addrLine, true);
  const feeN = Number(tx.fee_doge);
  if (dir === "out") {
    addRow("Network fee", Number.isFinite(feeN) && feeN > 0 ? `${feeN.toFixed(4)} DOGE` : "—", false);
  }
  addRow("Security", qm.detail, false);
  if (qm.pairTxid) addRow("Linked TX_C", qm.pairTxid, true);
  if (includeSource) addRow("Source", tx.source || "—", false);
  if (txidFull) {
    const row = document.createElement("div");
    row.className = "tx-card-row tx-card-row-actions";
    const k = document.createElement("span");
    k.className = "tx-card-k";
    k.textContent = "Actions";
    const wrap = document.createElement("div");
    wrap.className = "tx-actions-wrap";
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn btn-sm";
    btn.innerHTML =
      '<span class="material-symbols-outlined btn-ico" aria-hidden="true">info</span>Open details';
    btn.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      openTxDetailModal(tx);
    });
    const a = document.createElement("a");
    a.className = "btn btn-sm btn-icon-sochain";
    a.href = sochainTxUrl(txidFull);
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.title = "View on SoChain";
    a.setAttribute("aria-label", "View on SoChain");
    a.addEventListener("click", (e) => e.stopPropagation());
    const ic = document.createElement("span");
    ic.className = "material-symbols-outlined";
    ic.textContent = "open_in_new";
    a.appendChild(ic);
    wrap.appendChild(btn);
    wrap.appendChild(a);
    row.appendChild(k);
    row.appendChild(wrap);
    body.appendChild(row);
  }
  card.appendChild(body);
  return card;
}

function upsertTxList(container, txs, includeSource, emptyEl) {
  if (!container) return;
  const arr = Array.isArray(txs) ? txs : [];
  // Rebuild the list to keep DOM order consistent with tx sort order.
  // Otherwise, the existing cards keep their old positions and the user needs a full page refresh.
  const open = collectOpenTxids(container);
  container.innerHTML = "";
  arr.forEach((tx) => {
    const txid = String((tx && tx.txid) || "").trim();
    if (!txid) return;
    const next = buildTxExpandableCard(tx, includeSource);
    if (open.has(txid)) next.open = true;
    container.appendChild(next);
  });
  if (emptyEl) emptyEl.classList.toggle("hidden", arr.length > 0);
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
  const [data, sec] = await Promise.all([
    api("/api/wallet"),
    api("/api/security/status").catch(() => ({})),
  ]);
  // If /api/wallet returns a transient non-JSON or error payload during startup,
  // do not flip the UI into onboarding; keep prior state until a stable read.
  if (!data || data.error || data._status >= 500) {
    return;
  }
  const sealed = !!((data && data.sealed) || (sec && sec.sealed));
  const locked = !!((data && data.locked) || (sec && sec.sealed && !sec.unlocked));
  state.walletLocked = !!(locked && sealed);
  state.wallet = data.wallet || null;
  // Defensive fallback: on some force-refresh races wallet payload can be null briefly
  // while the service is still warming up. Security status tells us whether a wallet
  // exists on disk (sealed or plaintext) so onboarding should remain hidden.
  if (!state.wallet && !state.walletLocked) {
    try {
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
  updateEncryptionButtons(sealed, locked);
  setOnboarding(data.wallet);
  if (data.wallet) {
    renderAddresses(data.wallet);
    updateReceiveView();
    refreshDashboard().catch(() => {});
    refreshTxList(false, { full: true }).catch(() => {});
  }
}

function updateEncryptionButtons(isSealed, isLocked) {
  const btnSeal = $("btn-seal-wallet");
  const btnUnseal = $("btn-unseal-wallet");
  const btnLock = $("btn-lock-session");
  if (btnSeal) btnSeal.classList.toggle("hidden", !!isSealed);
  if (btnUnseal) btnUnseal.classList.toggle("hidden", !isSealed);
  if (btnLock) btnLock.classList.toggle("hidden", !isSealed || isLocked);
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

function recalcBalanceFromTransactions() {
  const txs = Array.isArray(state.lastTxs) ? state.lastTxs : [];
  if (!txs.length) return null;
  let total = 0;
  let any = false;
  for (const tx of txs) {
    const amt = Number(tx && tx.amount_doge);
    if (!Number.isFinite(amt)) continue;
    const dir = String(tx && tx.direction || "").toLowerCase();
    if (dir === "in") total += amt;
    else if (dir === "out") total -= amt;
    any = true;
  }
  if (!any) return null;
  return Math.max(0, total);
}

function updateServicesControlUI(svc) {
  const card = $("svc-control-card");
  const spvLine = $("svc-spv-status");
  const mtrLine = $("svc-mtr-status");
  const bSpvStop = [$("btn-svc-spv-stop")].filter(Boolean);
  const bSpvStart = [$("btn-svc-spv-start")].filter(Boolean);
  const bMtrStop = [$("btn-svc-mtr-stop")].filter(Boolean);
  const bMtrStart = [$("btn-svc-mtr-start")].filter(Boolean);
  if (!svc) return;
  if (card) card.classList.remove("hidden");
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
  bSpvStop.forEach((el) => { el.disabled = !spvOn; });
  bSpvStart.forEach((el) => { el.disabled = spvOn && spvRun; });
  bMtrStop.forEach((el) => { el.disabled = !mtrOn; });
  bMtrStart.forEach((el) => { el.disabled = mtrOn && mtrEng; });
  updatePqCommitmentSwitchLabel();
}

const PQ_COMMIT_LABEL_DEFAULT =
  "Post-quantum payload mode";

function safeLocalStorageGet(key) {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function safeLocalStorageSet(key, val) {
  try {
    window.localStorage.setItem(key, val);
  } catch {
    /* ignore storage failures */
  }
}

function getPqSendMode() {
  const raw = (safeLocalStorageGet(LOCAL_KEY_PQ_SEND_MODE) || "").trim();
  if (raw === "txc_only" || raw === "txc_txr") return raw;
  return "txc_txr";
}

function setPqSendMode(mode) {
  const m = mode === "txc_only" ? "txc_only" : "txc_txr";
  state.pqSendMode = m;
  safeLocalStorageSet(LOCAL_KEY_PQ_SEND_MODE, m);
  updatePqCommitmentSwitchLabel();
}

function getSendFeeDogePerKb() {
  const raw = (safeLocalStorageGet(LOCAL_KEY_SEND_FEE_DOGE_PER_KB) || "").trim();
  if (!raw) return DEFAULT_FEE_PER_KB_DOGE;
  if (!reFeePerKbDoge.test(raw)) return DEFAULT_FEE_PER_KB_DOGE;
  return raw;
}

function updateSendFeeHint() {
  const hint = $("send-fee-hint");
  const inp = $("settings-fee-doge-per-kb");
  const v = getSendFeeDogePerKb();
  if (inp && document.activeElement !== inp) {
    inp.value = v;
  }
  if (hint) {
    hint.textContent = `fee: ${v} DOGE/kB`;
  }
}

function updatePqCommitmentSwitchLabel() {
  const el = $("send-pq-commitment-label");
  const badge = $("send-pq-mode-badge");
  const note = $("pq-mode-settings-note");
  const bOnly = $("btn-pq-mode-txc-only");
  const bBoth = $("btn-pq-mode-txc-txr");
  const mode = state.pqSendMode || getPqSendMode();
  if (el) el.textContent = PQ_COMMIT_LABEL_DEFAULT;
  if (badge) {
    if (mode === "txc_only") {
      badge.textContent = "Will send: commitment only";
      badge.className = "pq-plan-badge txc-only";
    } else {
      badge.textContent = "Will send: commitment + reveal";
      badge.className = "pq-plan-badge txc-txr";
    }
  }
  if (note) {
    note.textContent = mode === "txc_only" ? "Current mode: commitment only" : "Current mode: commitment + reveal";
  }
  if (bOnly) {
    bOnly.classList.toggle("primary", mode === "txc_only");
  }
  if (bBoth) {
    bBoth.classList.toggle("primary", mode === "txc_txr");
  }
  updateSendFeeHint();
}

const SERVICE_CTRL_BTN_IDS = [
  "btn-svc-spv-stop", "btn-svc-spv-start", "btn-svc-mtr-stop", "btn-svc-mtr-start"
];

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

function updatePendingNavBadge(txs) {
  let pendingNav = 0;
  (Array.isArray(txs) ? txs : []).forEach((tx) => {
    const isMTR = String(tx && tx.source ? tx.source : "").toLowerCase() === "memetracker";
    if (isMTR || (tx && tx.pending)) pendingNav += 1;
  });
  const navB = $("nav-tx-pending-badge");
  if (!navB) return;
  if (pendingNav > 0) {
    navB.textContent = pendingNav > 99 ? "99+" : String(pendingNav);
    navB.classList.remove("hidden");
  } else {
    navB.classList.add("hidden");
  }
}

function applyDashboardSnapshot(dashboard) {
  if (!dashboard || typeof dashboard !== "object") return;
  state.lastDashboard = dashboard;
  const t = dashboard.totals || {};
  const spendStr =
    t.spendable_hint_doge != null && !Number.isNaN(Number(t.spendable_hint_doge))
      ? Number(t.spendable_hint_doge).toFixed(2)
      : "—";
  const balBig = $("wallet-balance-big");
  const balUnit = $("wallet-balance-unit");
  const txDerived = recalcBalanceFromTransactions();
  const shown = spendStr !== "—" ? spendStr : (txDerived != null ? txDerived.toFixed(2) : "—");
  if (balBig) balBig.textContent = `Ð${shown}`;
  if (balBig) requestAnimationFrame(() => fitWalletHeroBalance());
  if (balUnit) balUnit.textContent = "";
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
  const spv = dashboard.spv || {};
  rememberSpvHeaderSample(spv);
  const mtr = dashboard.memetracker || {};
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
    const rows = mtr.mempool_transactions || [];
    mtrList.innerHTML = "";
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
        `<div class="mtr-tx-main"><span class="material-symbols-outlined mtr-tx-row-ico" aria-hidden="true">open_in_new</span><div class="mtr-tx-id" title="${escapeHtml(tx)}">${escapeHtml(tx.length > 36 ? tx.slice(0, 34) + "…" : tx)}</div></div>` +
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
        mtrExpand.innerHTML =
          `<span class="material-symbols-outlined btn-ico" aria-hidden="true">unfold_more</span>Show all (${extra} more)`;
        mtrExpand.setAttribute("aria-expanded", "false");
        mtrCard.classList.remove("mtr-expanded");
      } else {
        mtrExpand.classList.add("hidden");
        mtrCard.classList.remove("mtr-expanded");
      }
    }
  }
  const syncLabel = humanizeSyncEta(spv.sync_lag_seconds) || "Sync";
  updateSyncChipVisual(syncLabel);
  const chip = $("wallet-sync-chip-top");
  if (chip) chip.title = spv.sync_lag_label || "";
  const spvDbg = $("log-spv-headers");
  if (spvDbg) {
    const h = Number(spv.header_height || 0);
    const hh = Number.isFinite(h) && h > 0 ? String(h) : "—";
    const bestHash = String(spv.best_block_hash || "").trim();
    const ts = Number(spv.header_unix_time || 0);
    const tsText = Number.isFinite(ts) && ts > 0 ? new Date(ts * 1000).toISOString() : "—";
    const lag = spv.sync_lag_label || "Unknown";
    const running = spv.running ? "yes" : "no";
    const lines = [
      `running: ${running}`,
      `header_height: ${hh}`,
      `best_block_hash: ${bestHash || "—"}`,
      `sync: ${lag}`,
      `header_unix_time: ${ts > 0 ? String(ts) : "—"}`,
      `header_time_iso: ${tsText}`
    ];
    const feed = Array.isArray(state.spvHeaderFeed) ? state.spvHeaderFeed : [];
    if (feed.length) {
      lines.push("", "header_feed_live:");
      for (const row of feed) {
        const iso = row.ts > 0 ? new Date(row.ts * 1000).toISOString() : "—";
        const hash = row.hash ? String(row.hash).trim() : "";
        lines.push(`  h=${row.height} t=${iso}${hash ? " hash=" + hash : ""}`);
      }
    }
    spvDbg.textContent = lines.join("\n");
  }
  const sample = dashboard.metrics_sample || [];
  initCharts();
  updateCharts(sample);
  if (dashboard.services) updateServicesControlUI(dashboard.services);
  renderDashboardTxPreview();
}

function applyTxSnapshot(txs, refresh) {
  const rows = Array.isArray(txs) ? txs : [];
  state.lastTxs = rows;
  const balBig = $("wallet-balance-big");
  const dashTotals = state.lastDashboard && state.lastDashboard.totals ? state.lastDashboard.totals : null;
  const spendHint = dashTotals && dashTotals.spendable_hint_doge != null ? Number(dashTotals.spendable_hint_doge) : NaN;
  if ((!Number.isFinite(spendHint) || spendHint <= 0) && balBig) {
    const derived = recalcBalanceFromTransactions();
    if (derived != null && derived > 0) balBig.textContent = `Ð${derived.toFixed(2)}`;
  }
  if (balBig) requestAnimationFrame(() => fitWalletHeroBalance());
  const hint = $("tx-sync-hint");
  if (hint) hint.textContent = refresh ? "Refreshed" : "";
  const list = $("tx-list");
  const emptyEl = $("tx-list-empty");
  if (list) upsertTxList(list, rows, true, emptyEl);
  updatePendingNavBadge(rows);
  renderDashboardTxPreview();
}

async function hydrateUiFromCache() {
  const [cachedDashboard, cachedTxs] = await Promise.all([
    cacheGet(CACHE_KEY_DASHBOARD),
    cacheGet(CACHE_KEY_TXS),
  ]);
  if (cachedTxs && Array.isArray(cachedTxs.transactions)) {
    applyTxSnapshot(cachedTxs.transactions, false);
  }
  if (cachedDashboard && cachedDashboard.dashboard && typeof cachedDashboard.dashboard === "object") {
    applyDashboardSnapshot(cachedDashboard.dashboard);
  }
}

async function refreshDashboard() {
  if (state.inFlightDashboard) return;
  state.inFlightDashboard = true;
  try {
  const data = await api("/api/dashboard", { timeout_ms: 18000 });
  if (!data || data.error || !data.dashboard || typeof data.dashboard !== "object") return;
  applyDashboardSnapshot(data.dashboard);
  cacheSet(CACHE_KEY_DASHBOARD, { dashboard: data.dashboard });
  } finally {
    state.inFlightDashboard = false;
  }
}

function renderDashboardTxPreview() {
  const list = $("dash-tx-list");
  if (!list) return;
  const txs = Array.isArray(state.lastTxs) ? state.lastTxs : [];
  if (!txs.length) {
    if (!list.querySelector(".dash-empty")) {
      list.innerHTML = "";
      const p = document.createElement("p");
      p.className = "small muted dash-empty";
      p.textContent = "No transactions yet.";
      list.appendChild(p);
    }
    return;
  }
  const oldEmpty = list.querySelector(".dash-empty");
  if (oldEmpty) oldEmpty.remove();
  upsertTxList(list, txs, false, null);
}

["btn-svc-spv-stop"].forEach((id) => $(id)?.addEventListener("click", () => postServiceControl({ spv_enabled: false })));
["btn-svc-spv-start"].forEach((id) => $(id)?.addEventListener("click", () => postServiceControl({ spv_enabled: true })));
["btn-svc-mtr-stop"].forEach((id) => $(id)?.addEventListener("click", () => postServiceControl({ memetracker_enabled: false })));
["btn-svc-mtr-start"].forEach((id) => $(id)?.addEventListener("click", () => postServiceControl({ memetracker_enabled: true })));

async function loadMoreTxListPage() {
  if (state.inFlightTx || !state.txHasMore) return;
  const off = state.lastTxs.length;
  const lim = state.txPageSize || 40;
  state.inFlightTx = true;
  try {
    const data = await api(`/api/transactions?limit=${lim}&offset=${off}`, { timeout_ms: 18000 });
    if (!data || data.error || !Array.isArray(data.transactions)) return;
    const batch = (data.transactions || []).slice().sort(sortTxRowsDesc);
    state.txHasMore = !!data.has_more;
    state.txTotal = typeof data.total === "number" ? data.total : off + batch.length;
    const merged = mergeTxRowsDedupe(state.lastTxs, batch);
    applyTxSnapshot(merged, false);
    cacheSet(CACHE_KEY_TXS, { transactions: merged });
  } finally {
    state.inFlightTx = false;
  }
}

async function refreshTxList(refresh, opts) {
  const o = opts || {};
  const full = o.full === true;
  if (state.inFlightTx) return;
  state.inFlightTx = true;
  try {
    let data;
    if (full) {
      data = await api("/api/transactions", { timeout_ms: 18000 });
    } else {
      const lim = state.txPageSize || 40;
      data = await api(`/api/transactions?limit=${lim}&offset=0`, { timeout_ms: 18000 });
    }
    if (!data || data.error || !Array.isArray(data.transactions)) return;
    const txs = (data.transactions || []).slice().sort(sortTxRowsDesc);
    if (full) {
      state.txHasMore = false;
      state.txTotal = typeof data.total === "number" ? data.total : txs.length;
    } else {
      state.txHasMore = !!data.has_more;
      state.txTotal = typeof data.total === "number" ? data.total : txs.length;
    }
    applyTxSnapshot(txs, !!refresh);
    cacheSet(CACHE_KEY_TXS, { transactions: txs });
  } finally {
    state.inFlightTx = false;
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
    bCopy.innerHTML =
      '<span class="material-symbols-outlined btn-ico" aria-hidden="true">content_copy</span>Copy';
    bCopy.addEventListener("click", async (e) => {
      e.stopPropagation();
      await copyTextToClipboard(a.p2pkh_address || "");
    });
    actions.appendChild(bCopy);
    if (!a.primary) {
      const bPrim = document.createElement("button");
      bPrim.type = "button";
      bPrim.className = "btn";
      bPrim.innerHTML =
        '<span class="material-symbols-outlined btn-ico" aria-hidden="true">star</span>Set primary';
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
      bDel.innerHTML =
        '<span class="material-symbols-outlined btn-ico" aria-hidden="true">delete</span>Remove';
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

function txDetailPrettyText(localSummary, localDetail) {
  const directionRaw = String(localSummary.direction || "").toLowerCase();
  const direction = directionRaw === "in" ? "Incoming" : directionRaw === "out" ? "Outgoing" : "Unknown";
  const amount =
    localSummary.amount_doge != null && Number.isFinite(localSummary.amount_doge)
      ? `${Number(localSummary.amount_doge).toFixed(8)} DOGE`
      : "—";
  const conf = Number(localSummary.confirmations || 0);
  const qm = quantumMetaFromTx(localSummary, localDetail && localDetail.local_raw_hex ? localDetail.local_raw_hex : "");
  const lines = [
    "Overview",
    "--------",
    `Txid: ${localSummary.txid || "—"}`,
    `Direction: ${direction}`,
    `Amount: ${amount}`,
    `Confirmations: ${conf > 0 ? conf : 0}`,
    `Source: ${localSummary.source || "spv"}`,
    `Security: ${qm.detail}`,
  ];
  if (localSummary.pq_verified) {
    lines.push("Reveal verification: PASSED");
  }
  if (qm.pairTxid) {
    lines.push(`Linked TX_C: ${qm.pairTxid}`);
  }
  if (localDetail && localDetail.local_raw_hex) {
    lines.push(`Raw hex: captured (${String(localDetail.local_raw_hex).length} chars)`);
  } else {
    lines.push("Raw hex: not captured yet");
  }
  if (localDetail) {
    lines.push("", "Local JSON", "----------", JSON.stringify(localDetail, null, 2));
  }
  return lines.join("\n");
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
  const pairBtn = $("btn-tx-pair");
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
  let pairTxid = "";
  body.textContent = txDetailPrettyText(localSummary, null);
  if (sub) sub.textContent = "Clean summary first, then local JSON details.";
  if (ext) {
    ext.href = sochainTxUrl(txid);
    ext.textContent = "Open on SoChain";
  }
  if (pairBtn) {
    pairBtn.classList.add("hidden");
    pairBtn.href = "#";
  }
  try {
    const localRes = await api("/api/tx/local/" + encodeURIComponent(txid));
    if (!localRes.error) {
      localDetail = localRes;
      if (localRes.local_raw_hex && /^[0-9a-f]+$/i.test(String(localRes.local_raw_hex))) {
        state.txDetailHex = String(localRes.local_raw_hex).trim();
      }
      body.textContent = txDetailPrettyText(localSummary, localRes).slice(0, 500000);
      const qm = quantumMetaFromTx(localSummary, state.txDetailHex);
      pairTxid = qm.pairTxid || "";
      if (sub) {
        sub.textContent = state.txDetailHex
          ? "Modern summary with local SPV/P2P data (raw hex captured)."
          : "Modern summary with local SPV/P2P data (raw hex not captured yet).";
      }
    }
  } catch {
    /* ignore local detail fetch errors */
  }
  const copyRawBtn = $("btn-tx-copy-raw");
  if (copyRawBtn) copyRawBtn.disabled = !state.txDetailHex;
  if (pairBtn && pairTxid) {
    pairBtn.href = sochainTxUrl(pairTxid);
    pairBtn.classList.remove("hidden");
  }
}

function closeTxDetailModal() {
  const modal = $("tx-detail-modal");
  if (modal) modal.classList.add("hidden");
}

function initLogCopyButtons() {
  const buttons = document.querySelectorAll(".log-copy-btn[data-copy-target]");
  buttons.forEach((btn) => {
    btn.addEventListener("click", async () => {
      const targetId = btn.getAttribute("data-copy-target");
      if (!targetId) return;
      const el = $(targetId);
      const text = el ? String(el.textContent || "").trim() : "";
      if (!text) {
        alert("No logs to copy yet.");
        return;
      }
      try {
        await copyTextToClipboard(text);
        const prev = btn.innerHTML;
        btn.innerHTML = '<span class="material-symbols-outlined btn-ico">check</span> Copied';
        setTimeout(() => {
          btn.innerHTML = prev;
        }, 1200);
      } catch {
        alert("Could not copy logs to clipboard.");
      }
    });
  });
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
const wowBtn = $("tabbar-doge-wow");
if (wowBtn) {
  wowBtn.addEventListener("click", () => {
    triggerDogeWowWords();
    showView("dashboard");
  });
}

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
    const ok = await copyTextToClipboard(addr);
    const lbl = $("copy-receive-label");
    const prev = lbl ? lbl.textContent : "";
    if (ok) {
      if (lbl) lbl.textContent = "Copied";
      setTimeout(() => {
        if (lbl) lbl.textContent = prev;
      }, 1600);
    } else {
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
  syncMobileMenuIcon();
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
  syncMobileMenuIcon();
});

const btnMob = $("btn-mobile-menu");
if (btnMob) {
  btnMob.addEventListener("click", () => {
    const sb = $("sidebar");
    if (!sb) return;
    if (isNarrowViewport()) {
      sb.classList.toggle("sidebar-drawer-closed");
      syncSidebarDrawerToggleIcon();
      syncMobileMenuIcon();
      return;
    }
    openSidebarNav();
  });
}

const sendAccTriggerNormal = $("send-acc-trigger-normal");
const sendAccTriggerManual = $("send-acc-trigger-manual");
if (sendAccTriggerNormal) {
  sendAccTriggerNormal.addEventListener("click", () => setSendTab(0));
}
if (sendAccTriggerManual) {
  sendAccTriggerManual.addEventListener("click", () => setSendTab(1));
}
setSendTab(0);

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
  let parsed;
  try {
    parsed = JSON.parse(text);
  } catch {
    alert("Invalid JSON in backup file.");
    return;
  }
  const isWrappedTopLevelWallet =
    Object.prototype.hasOwnProperty.call(parsed, "wallet") &&
    parsed.wallet != null &&
    typeof parsed.wallet === "object";
  const walletOnly = isWrappedTopLevelWallet ? parsed.wallet : parsed;
  const importSel = $("import-spv-restore-select");
  if (importSel && importSel.disabled) {
    alert("Could not read SPV restore options — fix the JSON file or pick another backup.");
    return;
  }
  const postBody = JSON.stringify({
    wallet: walletOnly,
    spv_on_restore: buildSpvOnRestoreFromImportSelect(),
  });
  const data = await api("/api/wallet/import", {
    method: "POST",
    body: postBody,
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
  await refreshTxList(true, { full: true });
});

const btnSpvRb = $("btn-spv-rollback");
if (btnSpvRb) {
  btnSpvRb.addEventListener("click", async () => {
    const ckSel = $("spv-rollback-checkpoint-select");
    const mode = ckSel && ckSel.value ? ckSel.value : "";
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
      alert("Choose a bundled checkpoint from the list.");
      return;
    }
    const rbLabel = btnSpvRb.querySelector(".btn-spv-rollback-label");
    const prevLabel = rbLabel ? rbLabel.textContent : "Rollback";
    btnSpvRb.disabled = true;
    btnSpvRb.setAttribute("aria-busy", "true");
    if (rbLabel) rbLabel.textContent = "Working…";
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
      if (rbLabel) rbLabel.textContent = prevLabel;
    }
  });
}

const btnOpenSpvRepair = $("btn-open-spv-repair");
if (btnOpenSpvRepair) btnOpenSpvRepair.addEventListener("click", () => openSpvRepairModal());
const spvRepairBackdrop = $("spv-repair-backdrop");
if (spvRepairBackdrop) spvRepairBackdrop.addEventListener("click", closeSpvRepairModal);
const btnSpvRepairClose = $("btn-spv-repair-close");
if (btnSpvRepairClose) btnSpvRepairClose.addEventListener("click", closeSpvRepairModal);


document.getElementById("btn-backup").addEventListener("click", async () => {
  if (!(await ensurePinForSensitiveAction("backup"))) return;
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
  if (!(await ensurePinForSensitiveAction("wallet deletion"))) return;
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
    btnMtrExpand.innerHTML = on
      ? '<span class="material-symbols-outlined btn-ico" aria-hidden="true">unfold_less</span>Show less'
      : `<span class="material-symbols-outlined btn-ico" aria-hidden="true">unfold_more</span>Show all (${extra} more)`;
  });
}

const logoHome = $("btn-logo-home");
if (logoHome) {
  logoHome.addEventListener("click", () => {
    triggerDogeWowWords();
    showView("dashboard");
  });
}

state.pqSendMode = getPqSendMode();
updatePqCommitmentSwitchLabel();
const btnSaveFeePerKb = $("btn-save-fee-per-kb");
if (btnSaveFeePerKb) {
  btnSaveFeePerKb.addEventListener("click", () => {
    const inp = $("settings-fee-doge-per-kb");
    const msg = $("settings-fee-msg");
    const v = (inp && inp.value ? inp.value : "").trim();
    if (!reFeePerKbDoge.test(v)) {
      if (msg) msg.textContent = "Enter a numeric DOGE rate only, e.g. 0.01";
      return;
    }
    safeLocalStorageSet(LOCAL_KEY_SEND_FEE_DOGE_PER_KB, v);
    if (msg) msg.textContent = "Saved. Used on the next send (server clamps 0.001–1.0 DOGE/kB).";
    updateSendFeeHint();
  });
}
const btnCarrierRefresh = $("btn-pq-carrier-refresh");
if (btnCarrierRefresh) {
  btnCarrierRefresh.addEventListener("click", () => {
    refreshPQCarrierStatus();
  });
}
const btnCarrierRecover = $("btn-pq-carrier-recover");
if (btnCarrierRecover) {
  btnCarrierRecover.addEventListener("click", async () => {
    const inp = $("pq-carrier-recover-txid");
    const rawEl = $("pq-carrier-recover-raw");
    const msg = $("pq-carrier-recover-msg");
    const out = $("pq-carrier-status-out");
    const txid = String((inp && inp.value) || "").trim().replace(/\s+/g, "");
    const rawHex = String((rawEl && rawEl.value) || "").trim().replace(/\s+/g, "");
    if (!txid && !rawHex) {
      if (msg) msg.textContent = "Enter a TX_C txid and/or paste raw transaction hex.";
      return;
    }
    if (txid && !/^[0-9a-fA-F]{64}$/.test(txid)) {
      if (msg) msg.textContent = "TX_C txid must be 64 hex characters (or leave empty if you paste raw hex only).";
      return;
    }
    if (rawHex && (!/^[0-9a-fA-F]+$/i.test(rawHex) || rawHex.length % 2 !== 0)) {
      if (msg) msg.textContent = "Raw hex must be an even-length hex string.";
      return;
    }
    if (!confirm("Broadcast recovery spend for this TX_C carrier output back to your wallet?")) return;
    if (msg) msg.textContent = "Recovering carrier funds...";
    try {
      const payload = { tx_c_txid: txid };
      if (rawHex) payload.tx_c_raw_hex = rawHex;
      const res = await api("/api/pq/carrier/recover", {
        method: "POST",
        body: JSON.stringify(payload),
        timeout_ms: 120000,
      });
      if (out) out.textContent = JSON.stringify(res, null, 2);
      if (msg) msg.textContent = res && res.error ? String(res.error) : "Recovery request sent.";
      await refreshPQCarrierStatus();
      await refreshTxList(true, { full: true });
      await refreshDashboard();
    } catch (e) {
      if (msg) msg.textContent = e && e.message ? e.message : String(e);
    }
  });
}
const btnPqModeOnly = $("btn-pq-mode-txc-only");
if (btnPqModeOnly) {
  btnPqModeOnly.addEventListener("click", () => setPqSendMode("txc_only"));
}
const btnPqModeBoth = $("btn-pq-mode-txc-txr");
if (btnPqModeBoth) {
  btnPqModeBoth.addEventListener("click", () => setPqSendMode("txc_txr"));
}

const btnSpvDeep = $("btn-spv-deep-refresh");
if (btnSpvDeep) btnSpvDeep.addEventListener("click", () => refreshSpvDeepLog());
const btnSpvLedger = $("btn-spv-ledger-refresh");
if (btnSpvLedger) btnSpvLedger.addEventListener("click", () => refreshSpvLedgerSnapshot());
const btnSpvRestProbe = $("btn-spv-rest-probe");
if (btnSpvRestProbe) {
  btnSpvRestProbe.addEventListener("click", async () => {
    const sel = $("spv-rest-probe-select");
    const out = $("spv-rest-probe-out");
    const msg = $("spv-rest-probe-msg");
    const path = sel && sel.value ? String(sel.value).trim() : "/getChaintip";
    if (msg) msg.textContent = "";
    if (out) out.textContent = "…";
    try {
      const j = await api(`/api/debug/spv-rest?path=${encodeURIComponent(path)}`, { timeout_ms: 25000 });
      if (j && j.error && !j.path) {
        if (msg) msg.textContent = String(j.error);
        if (out) out.textContent = JSON.stringify(j, null, 2);
        return;
      }
      if (msg && j.spv_http_base) msg.textContent = `Base: ${j.spv_http_base} — HTTP ${j.status}`;
      if (out) out.textContent = JSON.stringify(j, null, 2);
    } catch (e) {
      if (msg) msg.textContent = e && e.message ? e.message : String(e);
      if (out) out.textContent = "";
    }
  });
}
["spv-deep-merkle", "spv-deep-hex", "spv-deep-addr", "spv-deep-rawhdr", "spv-deep-tail"].forEach((id) => {
  const el = $(id);
  if (!el) return;
  el.addEventListener("change", () => {
    if (state.view !== "settings") return;
    refreshSpvDeepLog();
  });
});

document.getElementById("btn-send-pq-safe").addEventListener("click", async () => {
  if (!(await ensurePinForSensitiveAction("transaction send"))) return;
  const btn = document.getElementById("btn-send-pq-safe");
  const out = $("send-pq-out");
  const to_address = document.getElementById("send-to").value.trim();
  const amount_doge = document.getElementById("send-amt").value.trim();
  const mode = state.pqSendMode || getPqSendMode();
  const include_pq_commitment = true;
  const include_pq_reveal = mode !== "txc_only";
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
    const fee_doge_per_kb = getSendFeeDogePerKb();
    const res = await api("/api/send/pq-safe", {
      method: "POST",
      body: JSON.stringify({ to_address, amount_doge, include_pq_commitment, include_pq_reveal, fee_doge_per_kb }),
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
    const pin = normalizePin4(($("unlock-pin-input") && $("unlock-pin-input").value) || "");
    const errEl = $("unlock-err");
    if (errEl) errEl.textContent = "";
    if (!isPin4(pin)) {
      if (errEl) errEl.textContent = "PIN must be exactly 4 numbers.";
      return;
    }
    const res = await api("/api/security/unlock", {
      method: "POST",
      body: JSON.stringify({ pin }),
    });
    if (res.error) {
      if (errEl) errEl.textContent = res.error;
      return;
    }
    const inp = $("unlock-pin-input");
    if (inp) {
      inp.value = "";
      renderPinDisplay(inp);
    }
    await refreshWallet();
  });
}

const btnSeal = $("btn-seal-wallet");
if (btnSeal) {
  btnSeal.addEventListener("click", async () => {
    const pin = normalizePin4(($("seal-pin-input") && $("seal-pin-input").value) || "");
    const msg = $("seal-msg");
    const pin2 = await promptPinModal("Confirm wallet PIN", "Re-enter your PIN to enable encryption.");
    if (pin2 == null) {
      if (msg) msg.textContent = "Encryption cancelled.";
      return;
    }
    if (!isPin4(pin) || !isPin4(pin2)) {
      if (msg) msg.textContent = "PIN must be exactly 4 numbers.";
      return;
    }
    if (pin !== pin2) {
      if (msg) msg.textContent = "PIN mismatch. Enter the same PIN twice.";
      return;
    }
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
    if (sealInp) {
      sealInp.value = "";
      renderPinDisplay(sealInp);
    }
    window.alert(
      "Wallet encrypted successfully.\n\nYour wallet is now sealed on disk (Argon2id + AES-GCM). Use your PIN on the lock screen to unlock."
    );
    await refreshWallet();
  });
}

const btnUnseal = $("btn-unseal-wallet");
if (btnUnseal) {
  btnUnseal.addEventListener("click", async () => {
    const pin = normalizePin4(($("seal-pin-input") && $("seal-pin-input").value) || "");
    const msg = $("seal-msg");
    if (!isPin4(pin)) {
      if (msg) msg.textContent = "PIN must be exactly 4 numbers.";
      return;
    }
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
    if (sealInp) {
      sealInp.value = "";
      renderPinDisplay(sealInp);
    }
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
    try {
      await refreshTxList(false, { full: true });
    } catch {
      /* tx poll failed */
    }
  }, 12000);
}

window.addEventListener("resize", () => {
  // Let layout settle.
  setTimeout(() => fitWalletHeroBalance(), 0);
});

function wireImportFileUI() {
  const input = document.getElementById("import-file");
  const nameEl = document.getElementById("import-file-name");
  const wrap = document.querySelector(".file-upload");
  if (!input || !nameEl) return;
  resetImportSpvRestoreSelectPlaceholder();
  function showName() {
    const f = input.files && input.files[0];
    nameEl.textContent = f ? f.name : "No file selected";
    nameEl.classList.toggle("has-file", !!f);
    void refreshImportSpvRestoreOptionsFromFile(f || null);
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

let deferredInstallPrompt = null;
window.addEventListener("beforeinstallprompt", (e) => {
  e.preventDefault();
  deferredInstallPrompt = e;
  const btn = $("btn-install-app");
  const hint = $("install-app-hint");
  if (btn) btn.classList.remove("hidden");
  if (hint) hint.classList.add("hidden");
});
window.addEventListener("appinstalled", () => {
  deferredInstallPrompt = null;
  const btn = $("btn-install-app");
  if (btn) btn.classList.add("hidden");
});
const btnInstall = $("btn-install-app");
if (btnInstall) {
  btnInstall.addEventListener("click", async () => {
    if (!deferredInstallPrompt) return;
    deferredInstallPrompt.prompt();
    try { await deferredInstallPrompt.userChoice; } catch { /* ignore */ }
  });
}
const installHint = $("install-app-hint");
if (installHint) {
  const isiOS = /iphone|ipad|ipod/i.test(navigator.userAgent || "");
  const isStandalone = window.matchMedia && window.matchMedia("(display-mode: standalone)").matches;
  if (isiOS && !isStandalone) installHint.classList.remove("hidden");
}
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/static/sw.js").catch(() => {});
  });
}

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
    syncMobileMenuIcon();
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
    if (!id) return;
    await copyTextToClipboard(id);
  });
}
if (btnTxCopyRaw) {
  btnTxCopyRaw.addEventListener("click", async () => {
    const h = state.txDetailHex || "";
    if (!h) return;
    await copyTextToClipboard(h);
  });
}
initLogCopyButtons();
initPin4Inputs();

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

updateSyncChipVisual("Sync");
(async () => {
  await hydrateUiFromCache();
  await refreshWallet();
})();
