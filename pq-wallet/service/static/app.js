/* global Chart */

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
  charts: { height: null, spv: null },
  pollFast: null,
  pollTx: null,
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
    dashboard: ["Dashboard", "SPV headers, balances, and PQ hints"],
    addresses: ["Addresses", "Generate keys and choose which address SPV watches"],
    transactions: ["Transactions", "Explorer/RPC cache with OP_RETURN PQ hints"],
    tools: ["Sign & broadcast", "such + sendtx + optional Core RPC"],
    network: ["Network & SPV", "Logs, MemeTracker, explorer"],
    learn: ["How it works", "TX_C / TX_R overview"],
    settings: ["Wallet file", "Backup or remove this pup’s wallet"],
  };
  const [t, s] = titles[name] || [name, ""];
  $("page-title").textContent = t;
  $("page-sub").textContent = s;

  document.querySelectorAll(".content .view").forEach((v) => v.classList.add("hidden"));
  const el = $("view-" + name);
  if (el) el.classList.remove("hidden");
}

function setOnboarding(w) {
  const has = !!w;
  if (!has) {
    if (state.pollFast) clearInterval(state.pollFast);
    if (state.pollTx) clearInterval(state.pollTx);
    state.pollFast = null;
    state.pollTx = null;
  }
  $("view-onboarding").classList.toggle("hidden", has);
  ["view-dashboard", "view-addresses", "view-transactions", "view-tools", "view-network", "view-learn", "view-settings"].forEach((id) => {
    $(id).classList.toggle("hidden", !has);
  });
  if (has) {
    $("net-badge").textContent = (w.network || "mainnet").toUpperCase();
    showView(state.view || "dashboard");
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
  const ctxS = $("chart-spv");
  if (ctxH && !state.charts.height) {
    state.charts.height = new Chart(ctxH, {
      type: "line",
      data: { labels: [], datasets: [{ label: "height", data: [], borderColor: "#f2cb2c", tension: 0.25, fill: false }] },
      options: common,
    });
  }
  if (ctxS && !state.charts.spv) {
    state.charts.spv = new Chart(ctxS, {
      type: "line",
      data: { labels: [], datasets: [{ label: "spv", data: [], borderColor: "#3ecf8e", stepped: true, fill: false }] },
      options: common,
    });
  }
}

function updateCharts(metrics) {
  if (!metrics || !metrics.length) return;
  const labels = metrics.map((m) => fmtTime(m.t));
  const heights = metrics.map((m) => Number(m.header_height) || 0);
  const spv = metrics.map((m) => (m.spv_running ? 1 : 0));
  if (state.charts.height) {
    state.charts.height.data.labels = labels;
    state.charts.height.data.datasets[0].data = heights;
    state.charts.height.update("none");
  }
  if (state.charts.spv) {
    state.charts.spv.data.labels = labels;
    state.charts.spv.data.datasets[0].data = spv;
    state.charts.spv.update("none");
  }
}

async function refreshWallet() {
  const data = await api("/api/wallet");
  state.wallet = data.wallet;
  setOnboarding(data.wallet);
  if (data.wallet) {
    renderAddresses(data.wallet);
    await refreshDashboard();
    await refreshTxList(false);
  }
}

async function refreshDashboard() {
  const data = await api("/api/dashboard");
  $("conn-pill").textContent = "● live";
  if (!data.dashboard) return;
  const t = data.dashboard.totals || {};
  $("stat-spend").textContent = t.spendable_hint_doge != null ? String(t.spendable_hint_doge) : "—";
  $("stat-in").textContent = t.received_doge != null ? String(t.received_doge) : "—";
  $("stat-out").textContent = t.sent_doge != null ? String(t.sent_doge) : "—";
  const spv = data.dashboard.spv || {};
  $("stat-spv").textContent = spv.running ? "running" : "stopped";
  $("pill-height").textContent = spv.header_height ? `height ${spv.header_height}` : "height —";
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
    tr.innerHTML = `<td class="mono">${short}</td><td>${tx.direction || ""}</td><td>${tx.amount_doge != null ? tx.amount_doge : ""}</td><td>${tx.pq_hint ? "yes" : ""}</td><td>${tx.source || ""}</td>`;
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

document.querySelectorAll(".nav-item").forEach((btn) => {
  btn.addEventListener("click", () => showView(btn.dataset.view));
});

$("btn-sidebar-toggle").addEventListener("click", () => {
  const sb = $("sidebar");
  const collapsed = sb.classList.toggle("collapsed");
  $("btn-sidebar-toggle").setAttribute("aria-expanded", (!collapsed).toString());
  $("btn-sidebar-toggle").textContent = collapsed ? "⟩" : "⟨";
});

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

document.getElementById("btn-mem-status").addEventListener("click", async () => {
  $("mem-out").textContent = JSON.stringify(await api("/api/mempool/status"), null, 2);
});
document.getElementById("btn-mem-track").addEventListener("click", async () => {
  $("mem-out").textContent = JSON.stringify(await api("/api/mempool/track"), null, 2);
});
document.getElementById("btn-explorer").addEventListener("click", async () => {
  const txid = document.getElementById("txid-input").value.trim();
  if (!txid) return;
  $("ex-out").textContent = JSON.stringify(await api("/api/explorer/tx/" + encodeURIComponent(txid)), null, 2);
});
document.getElementById("btn-spv-status").addEventListener("click", async () => {
  $("spv-out").textContent = JSON.stringify(await api("/api/spv/status"), null, 2);
});

document.getElementById("btn-sign").addEventListener("click", async () => {
  const raw = document.getElementById("raw-hex").value.trim();
  const inputIndex = parseInt(document.getElementById("vin-idx").value, 10) || 0;
  $("sign-out").textContent = JSON.stringify(
    await api("/api/tx/sign", {
      method: "POST",
      body: JSON.stringify({ raw_hex: raw, input_index: inputIndex, sighash_type: 1 }),
    }),
    null,
    2
  );
});
document.getElementById("btn-broadcast").addEventListener("click", async () => {
  const hex = document.getElementById("signed-hex").value.trim();
  const peers = document.getElementById("peers-input").value.trim();
  $("bc-out").textContent = JSON.stringify(
    await api("/api/tx/broadcast", {
      method: "POST",
      body: JSON.stringify({ raw_hex: hex, peers }),
    }),
    null,
    2
  );
});
document.getElementById("btn-rpc-send").addEventListener("click", async () => {
  const to_address = document.getElementById("rpc-to").value.trim();
  const amount = parseFloat(document.getElementById("rpc-amt").value);
  $("rpc-out").textContent = JSON.stringify(
    await api("/api/rpc/send", {
      method: "POST",
      body: JSON.stringify({ to_address, amount_doge: amount }),
    }),
    null,
    2
  );
});

function startPollers() {
  if (state.pollFast) clearInterval(state.pollFast);
  if (state.pollTx) clearInterval(state.pollTx);
  state.pollFast = setInterval(async () => {
    if (!state.wallet) return;
    try {
      await refreshDashboard();
    } catch {
      $("conn-pill").textContent = "● stale";
    }
  }, 4000);
  state.pollTx = setInterval(async () => {
    if (!state.wallet) return;
    await refreshTxList(false);
  }, 12000);
}

refreshWallet();
