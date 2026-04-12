/* global Chart, QRCode, Html5Qrcode */

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
  view: "dashboard",
  charts: { height: null, smpv: null },
  pollFast: null,
  pollTx: null,
  pollLogs: null,
  qrScanner: null,
  receiveQrAddr: null,
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
    logs: ["Logs", "SPV, SMPV/mempool filter, and broadcast tails (~420 lines)"],
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
}

async function refreshLogs() {
  try {
    const [spv, smpv, bc] = await Promise.all([
      fetch("/api/logs/spv?lines=420").then((r) => r.text()),
      fetch("/api/logs/smpv?lines=420").then((r) => r.text()),
      fetch("/api/logs/broadcast?lines=420").then((r) => r.text()),
    ]);
    const elS = $("log-spv");
    const elM = $("log-smpv");
    const elB = $("log-bc");
    if (elS) elS.textContent = spv;
    if (elM) elM.textContent = smpv;
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
      card.className = "card learn-section";
      card.innerHTML = "<h3>Quick flow</h3><ol class=\"steps\"></ol>";
      const ol = card.querySelector("ol");
      data.flow.forEach((f) => {
        const li = document.createElement("li");
        li.innerHTML = "<strong>" + escapeHtml(f.name || "") + "</strong> — " + escapeHtml(f.detail || "");
        ol.appendChild(li);
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
  const has = !!w;
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
    $("net-badge").textContent = (w.network || "mainnet").toUpperCase();
    showView(state.view || "dashboard");
    updateReceiveView();
    startPollers();
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
      y: { ticks: { color: "#8a9bb3" }, grid: { color: "rgba(255,255,255,0.06)" } },
    },
  };
  const ctxH = $("chart-height");
  const ctxSm = $("chart-smpv");
  if (ctxH && !state.charts.height) {
    state.charts.height = new Chart(ctxH, {
      type: "line",
      data: { labels: [], datasets: [{ label: "height", data: [], borderColor: "#f2cb2c", tension: 0.25, fill: false }] },
      options: common,
    });
  }
  if (ctxSm && !state.charts.smpv) {
    state.charts.smpv = new Chart(ctxSm, {
      type: "line",
      data: { labels: [], datasets: [{ label: "mempool txs", data: [], borderColor: "#c48cff", tension: 0.2, fill: false }] },
      options: common,
    });
  }
}

function updateCharts(metrics) {
  if (!metrics || !metrics.length) return;
  const labels = metrics.map((m) => fmtTime(m.t));
  const heights = metrics.map((m) => Number(m.header_height) || 0);
  const mp = metrics.map((m) => Number(m.mempool_tx_count) || 0);
  if (state.charts.height) {
    state.charts.height.data.labels = labels;
    state.charts.height.data.datasets[0].data = heights;
    state.charts.height.update("none");
  }
  if (state.charts.smpv) {
    state.charts.smpv.data.labels = labels;
    state.charts.smpv.data.datasets[0].data = mp;
    state.charts.smpv.update("none");
  }
}

async function refreshWallet() {
  const data = await api("/api/wallet");
  state.wallet = data.wallet;
  setOnboarding(data.wallet);
  if (data.wallet) {
    renderAddresses(data.wallet);
    updateReceiveView();
    await refreshDashboard();
    await refreshTxList(false);
  }
}

async function refreshDashboard() {
  const data = await api("/api/dashboard");
  $("conn-pill").innerHTML = '<span class="material-symbols-outlined icon-inline">verified</span> live';
  if (!data.dashboard) return;
  const t = data.dashboard.totals || {};
  const spendStr = t.spendable_hint_doge != null ? String(t.spendable_hint_doge) : "—";
  $("stat-spend").textContent = spendStr;
  const balBig = $("wallet-balance-big");
  if (balBig) balBig.textContent = spendStr;
  $("stat-in").textContent = t.received_doge != null ? String(t.received_doge) : "—";
  $("stat-out").textContent = t.sent_doge != null ? String(t.sent_doge) : "—";
  const spv = data.dashboard.spv || {};
  $("stat-spv").textContent = spv.running ? "running" : "stopped";
  const mtc = spv.mempool_tx_count;
  const elMp = $("stat-mempool");
  if (elMp) elMp.textContent = mtc != null && Number(mtc) >= 0 ? String(mtc) : "—";
  const cp = spv.current_peer;
  const setPeer = (id, v) => {
    const el = $(id);
    if (el) el.textContent = v != null && String(v) !== "" ? String(v) : "—";
  };
  if (cp && typeof cp === "object") {
    setPeer("peer-c-addr", cp.address);
    setPeer("peer-c-node", cp.node_id != null ? String(cp.node_id) : "");
    setPeer("peer-c-ua", cp.sub_version);
    setPeer("peer-c-h", cp.remote_start_height != null ? String(cp.remote_start_height) : "");
  } else {
    setPeer("peer-c-addr", "");
    setPeer("peer-c-node", "");
    setPeer("peer-c-ua", "");
    setPeer("peer-c-h", "");
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
  $("tx-sync-hint").textContent = refresh ? "Synced" : "";
  const tb = $("tx-table").querySelector("tbody");
  tb.innerHTML = "";
  txs.forEach((tx) => {
    const tr = document.createElement("tr");
    const short = (tx.txid || "").slice(0, 18) + (tx.txid && tx.txid.length > 18 ? "…" : "");
    const pqBadge = tx.pq_hint
      ? '<span class="badge-pq"><span class="material-symbols-outlined" style="font-size:15px">verified</span> PQ</span>'
      : '<span class="badge-pq off">—</span>';
    tr.innerHTML = `<td class="mono">${escapeHtml(short)}</td><td>${escapeHtml(tx.direction || "")}</td><td>${tx.amount_doge != null ? escapeHtml(String(tx.amount_doge)) : ""}</td><td>${pqBadge}</td><td>${escapeHtml(tx.source || "")}</td>`;
    tb.appendChild(tr);
  });
}

function renderAddresses(w) {
  const addrs = w.addresses && w.addresses.length ? w.addresses : [];
  const tb = $("addr-table").querySelector("tbody");
  tb.innerHTML = "";
  if (!addrs.length) {
    const tr = document.createElement("tr");
    tr.innerHTML = `<td colspan="4" class="muted">No address rows — restore or recreate wallet.</td>`;
    tb.appendChild(tr);
    return;
  }
  addrs.forEach((a) => {
    const tr = document.createElement("tr");
    const pri = a.primary ? "★" : "";
    tr.innerHTML = `<td>${escapeHtml(a.label || "")}</td><td class="mono">${escapeHtml(a.p2pkh_address || "")}</td><td>${pri}</td><td class="btn-row"></td>`;
    const td = tr.querySelector("td:last-child");
    const bCopy = document.createElement("button");
    bCopy.type = "button";
    bCopy.className = "btn";
    bCopy.textContent = "Copy";
    bCopy.addEventListener("click", () => navigator.clipboard.writeText(a.p2pkh_address || ""));
    td.appendChild(bCopy);
    if (!a.primary) {
      const bPrim = document.createElement("button");
      bPrim.type = "button";
      bPrim.className = "btn";
      bPrim.textContent = "Set primary";
      bPrim.addEventListener("click", async () => {
        const r = await api("/api/wallet/primary", {
          method: "POST",
          body: JSON.stringify({ id: a.id }),
        });
        if (r.error) alert(r.error);
        await refreshWallet();
      });
      td.appendChild(bPrim);
    }
    tb.appendChild(tr);
  });
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
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
    return;
  }
  if (state.receiveQrAddr === addr && qrEl.querySelector("img, canvas")) return;
  state.receiveQrAddr = addr;
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
      width: 220,
      height: 220,
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

async function openQrScanner() {
  const modal = $("qr-scan-modal");
  if (!modal) return;
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
  sb.classList.remove("collapsed");
  $("btn-sidebar-toggle").setAttribute("aria-expanded", "true");
  const icon = $("btn-sidebar-toggle").querySelector(".material-symbols-outlined");
  if (icon) icon.textContent = "menu_open";
}

$("btn-sidebar-toggle").addEventListener("click", () => {
  const sb = $("sidebar");
  const collapsed = sb.classList.toggle("collapsed");
  $("btn-sidebar-toggle").setAttribute("aria-expanded", (!collapsed).toString());
  const icon = $("btn-sidebar-toggle").querySelector(".material-symbols-outlined");
  if (icon) icon.textContent = collapsed ? "menu" : "menu_open";
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
document.getElementById("btn-spv-status").addEventListener("click", async () => {
  $("spv-out").textContent = JSON.stringify(await api("/api/spv/status"), null, 2);
});

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

function startPollers() {
  if (state.pollFast) clearInterval(state.pollFast);
  if (state.pollTx) clearInterval(state.pollTx);
  state.pollFast = setInterval(async () => {
    if (!state.wallet) return;
    try {
      await refreshDashboard();
    } catch {
      $("conn-pill").innerHTML = '<span class="material-symbols-outlined icon-inline">warning</span> stale';
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

refreshWallet();
