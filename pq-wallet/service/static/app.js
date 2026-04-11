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

function showPanel(id) {
  document.querySelectorAll(".panel").forEach((p) => p.classList.remove("visible"));
  document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
  const panel = document.getElementById("panel-" + id);
  if (panel) panel.classList.add("visible");
  const tab = document.querySelector(`.tab[data-panel="${id}"]`);
  if (tab) tab.classList.add("active");
}

function renderWallet(w) {
  const empty = document.getElementById("wallet-empty");
  const loaded = document.getElementById("wallet-loaded");
  if (!w) {
    empty.classList.remove("hidden");
    loaded.classList.add("hidden");
    return;
  }
  empty.classList.add("hidden");
  loaded.classList.remove("hidden");
  document.getElementById("addr-display").textContent = w.p2pkh_address || "";
  document.getElementById("wif-display").textContent = w.wif_private_key || "";
  document.getElementById("pq-scheme").textContent = w.pq_scheme || "";
  const pqPub = document.getElementById("pq-pub");
  const pqPriv = document.getElementById("pq-priv");
  if (w.pq_public_key_hex) {
    pqPub.textContent = "PQ public (hex): " + w.pq_public_key_hex;
  } else {
    pqPub.textContent = "PQ public: (not set — enable Falcon keygen when creating the wallet)";
  }
  if (w.pq_private_key_hex) {
    pqPriv.textContent = "PQ private (hex): " + w.pq_private_key_hex;
  } else {
    pqPriv.textContent = "PQ private: (not set)";
  }
}

async function refreshWallet() {
  const data = await api("/api/wallet");
  renderWallet(data.wallet);
}

document.querySelectorAll(".tab").forEach((btn) => {
  btn.addEventListener("click", () => showPanel(btn.dataset.panel));
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
  renderWallet(data.wallet);
});

function copyText(t) {
  navigator.clipboard.writeText(t).then(
    () => {},
    () => alert("Copy failed")
  );
}

document.getElementById("btn-copy-addr").addEventListener("click", () => {
  const t = document.getElementById("addr-display").textContent;
  copyText(t);
});

document.getElementById("btn-copy-wif").addEventListener("click", () => {
  const t = document.getElementById("wif-display").textContent;
  copyText(t);
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

document.getElementById("btn-mem-status").addEventListener("click", async () => {
  const out = document.getElementById("mem-out");
  out.textContent = "…";
  const data = await api("/api/mempool/status");
  out.textContent = JSON.stringify(data, null, 2);
});

document.getElementById("btn-mem-track").addEventListener("click", async () => {
  const out = document.getElementById("mem-out");
  out.textContent = "…";
  const data = await api("/api/mempool/track");
  out.textContent = JSON.stringify(data, null, 2);
});

document.getElementById("btn-explorer").addEventListener("click", async () => {
  const txid = document.getElementById("txid-input").value.trim();
  const out = document.getElementById("ex-out");
  if (!txid) {
    out.textContent = "Enter a txid";
    return;
  }
  out.textContent = "…";
  const data = await api("/api/explorer/tx/" + encodeURIComponent(txid));
  out.textContent = JSON.stringify(data, null, 2);
});

document.getElementById("btn-spv-status").addEventListener("click", async () => {
  const out = document.getElementById("spv-out");
  out.textContent = "…";
  const data = await api("/api/spv/status");
  out.textContent = JSON.stringify(data, null, 2);
});

document.getElementById("btn-sign").addEventListener("click", async () => {
  const out = document.getElementById("sign-out");
  out.textContent = "…";
  const raw = document.getElementById("raw-hex").value.trim();
  const inputIndex = parseInt(document.getElementById("vin-idx").value, 10) || 0;
  const data = await api("/api/tx/sign", {
    method: "POST",
    body: JSON.stringify({ raw_hex: raw, input_index: inputIndex, sighash_type: 1 }),
  });
  out.textContent = JSON.stringify(data, null, 2);
});

document.getElementById("btn-broadcast").addEventListener("click", async () => {
  const out = document.getElementById("bc-out");
  out.textContent = "…";
  const hex = document.getElementById("signed-hex").value.trim();
  const peers = document.getElementById("peers-input").value.trim();
  const data = await api("/api/tx/broadcast", {
    method: "POST",
    body: JSON.stringify({ raw_hex: hex, peers }),
  });
  out.textContent = JSON.stringify(data, null, 2);
});

document.getElementById("btn-rpc-send").addEventListener("click", async () => {
  const out = document.getElementById("rpc-out");
  out.textContent = "…";
  const to_address = document.getElementById("rpc-to").value.trim();
  const amount = parseFloat(document.getElementById("rpc-amt").value);
  const data = await api("/api/rpc/send", {
    method: "POST",
    body: JSON.stringify({ to_address, amount_doge: amount }),
  });
  out.textContent = JSON.stringify(data, null, 2);
});

refreshWallet();
