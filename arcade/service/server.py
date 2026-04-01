from __future__ import annotations

import asyncio
import base64
import hashlib
import hmac
import json
import os
import random
import secrets
import socket
import sqlite3
import subprocess
import struct
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from http.server import ThreadingHTTPServer, SimpleHTTPRequestHandler
from pathlib import Path
from threading import Lock
from typing import Dict, List, Optional, Tuple

MAGIC = bytes.fromhex("c0c0c0c0")
COMMAND_LEN = 12
# Same as Bitcoin/Dogecoin protocol.h (BIP144)
MSG_WITNESS_FLAG = 1 << 30
MSG_TX = 1
MSG_WITNESS_TX = MSG_TX | MSG_WITNESS_FLAG
NODE_NETWORK = 1 << 0
NODE_WITNESS = 1 << 3
_MAX_TX_FETCH_PER_INV = 200
_GETDATA_BATCH = 48
_MEMPOOL_RESYNC_SEC = 90
_B58 = b"123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
# Same seed hostnames as memetracker/mainnet Dogecoin DNS (port 22556 for all).
_MAINNET_DNS_SEEDS = (
    "seed.dogecoin.org",
    "seed.dogecoin.net",
    "seed.multidoge.org",
    "seed2.multidoge.org",
    "seed.dogecoin.com",
)
_MAINNET_P2P_PORT = 22556


def game_id_to_payout_env_key(game_id: str) -> str:
    return "PAYOUT_GAME_" + game_id.upper().replace("-", "_")


# Built-in mainnet P2PKH per game (override with PAYOUT_GAME_<ID> env). No global fallback.
DEFAULT_GAME_PAYOUT_ADDRESSES: Dict[str, str] = {
    "doge-mine-quest": "D9ArHWcegdLwLq6wtDDhwsR6BHZNr6rkEu",
    "doge-indy-500": "DTqAFgNNUgiPEfGmc4HZUkqJ4sz5vADd1n",
    "fear-the-doge": "DAd3QzjJ2Yh1rwTxxVz7GEKxtNQh2s9zJe",
}


def game_id_to_gigawallet_account_env_key(game_id: str) -> str:
    return "ARCADE_GIGAWALLET_ACCOUNT_" + game_id.upper().replace("-", "_")


def _parse_csv_ids(raw: str) -> set:
    return {x.strip() for x in (raw or "").split(",") if x.strip()}


def http_json_admin(
    method: str,
    url: str,
    body: Optional[dict] = None,
    *,
    timeout: float = 45,
) -> dict:
    """POST/GET JSON to GigaWallet Admin API (no auth in default Dogebox setup)."""
    data: Optional[bytes] = None
    if method.upper() != "GET":
        data = json.dumps(body if body is not None else {}).encode("utf-8")
    req = urllib.request.Request(url, method=method.upper(), data=data)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8")
    except urllib.error.HTTPError as ex:
        hint = ex.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"gigawallet_http_{ex.code}:{hint[:900]}") from ex
    except urllib.error.URLError as ex:
        raise RuntimeError(f"gigawallet_unreachable:{ex!r}") from ex
    if not raw.strip():
        return {}
    try:
        return json.loads(raw)
    except json.JSONDecodeError as ex:
        raise RuntimeError(f"gigawallet_bad_json:{ex}:{raw[:200]}") from ex


def gigawallet_foreign_id_for_game(game_id: str) -> str:
    env_k = game_id_to_gigawallet_account_env_key(game_id)
    return (os.getenv(env_k, "").strip() or os.getenv("ARCADE_GIGAWALLET_ACCOUNT_ID", "").strip())


def discover_game_manifests(static_dir: Path) -> List[Tuple[str, dict]]:
    games_dir = static_dir / "games"
    out: List[Tuple[str, dict]] = []
    if not games_dir.is_dir():
        return out
    for sub in sorted(games_dir.iterdir()):
        if not sub.is_dir():
            continue
        gj = sub / "game.json"
        if not gj.is_file():
            continue
        try:
            meta = json.loads(gj.read_text(encoding="utf-8"))
        except Exception:
            continue
        gid = str(meta.get("id") or sub.name).strip()
        if gid:
            out.append((gid, meta))
    return out


def _p2p_log_level() -> int:
    """0=quiet (payments only), 1=P2P+timer summary, 2=verbose (inv noise, rejects)."""
    try:
        v = (os.getenv("ARCADE_P2P_LOG", "1") or "1").strip().lower()
        if v in ("0", "no", "off", "false"):
            return 0
        if v in ("2", "debug", "verbose"):
            return 2
        return 1
    except Exception:
        return 1


def _log_ts() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%S", time.localtime())


def log_pay_credit(msg: str) -> None:
    """Always logged when a payment is actually credited."""
    print(f"{_log_ts()} run-arcade.py[PAYMENT] {msg}", file=sys.stderr, flush=True)


def log_p2p(msg: str, level: int = 1) -> None:
    if _p2p_log_level() < level:
        return
    print(f"{_log_ts()} run-arcade.py[P2P] {msg}", file=sys.stderr, flush=True)


def log_timer(msg: str) -> None:
    if _p2p_log_level() < 1:
        return
    print(f"{_log_ts()} run-arcade.py[TIMER] {msg}", file=sys.stderr, flush=True)


def log_always(msg: str) -> None:
    """Startup / configuration (always stderr, independent of ARCADE_P2P_LOG)."""
    print(f"{_log_ts()} run-arcade.py[ARCADE] {msg}", file=sys.stderr, flush=True)


def sha256d(data: bytes) -> bytes:
    import hashlib
    return hashlib.sha256(hashlib.sha256(data).digest()).digest()


def build_message(command: str, payload: bytes) -> bytes:
    cmd = command.encode("ascii").ljust(COMMAND_LEN, b"\x00")
    checksum = sha256d(payload)[:4]
    return MAGIC + cmd + struct.pack("<I", len(payload)) + checksum + payload


def read_exact(sock: socket.socket, size: int) -> bytes:
    out = b""
    while len(out) < size:
        part = sock.recv(size - len(out))
        if not part:
            raise ConnectionError("socket closed")
        out += part
    return out


def b58decode_int(s: str) -> int:
    n = 0
    for c in s.encode("ascii"):
        n = n * 58 + _B58.index(c)
    return n


def b58check_decode(addr: str) -> bytes:
    """Decode Base58Check to payload (version + hash160 for P2PKH)."""
    raw_int = b58decode_int(addr)
    pad = 0
    for c in addr:
        if c == "1":
            pad += 1
        else:
            break
    if raw_int == 0:
        combined = b""
    else:
        bl = (raw_int.bit_length() + 7) // 8
        combined = raw_int.to_bytes(bl, "big")
    raw = b"\x00" * pad + combined
    if len(raw) < 5:
        raise ValueError("invalid address")
    payload, chk = raw[:-4], raw[-4:]
    if sha256d(payload)[:4] != chk:
        raise ValueError("bad checksum")
    return payload


def decode_payout_to_hash160(address: str, network: str) -> bytes:
    """Mainnet P2PKH 0x1e, testnet 0x71."""
    want_ver = 0x1E if network == "mainnet" else 0x71
    p = b58check_decode(address.strip())
    if len(p) != 21 or p[0] != want_ver:
        raise ValueError("need P2PKH base58 address for this network")
    return p[1:21]


def write_varint(n: int) -> bytes:
    if n < 0xFD:
        return bytes([n])
    if n <= 0xFFFF:
        return b"\xFD" + struct.pack("<H", n)
    if n <= 0xFFFFFFFF:
        return b"\xFE" + struct.pack("<I", n)
    return b"\xFF" + struct.pack("<Q", n)


def read_varint(data: bytes, off: int) -> Tuple[int, int]:
    if off >= len(data):
        raise ValueError("eof")
    b0 = data[off]
    off += 1
    if b0 < 0xFD:
        return b0, off
    if b0 == 0xFD:
        return struct.unpack_from("<H", data, off)[0], off + 2
    if b0 == 0xFE:
        return struct.unpack_from("<I", data, off)[0], off + 4
    return struct.unpack_from("<Q", data, off)[0], off + 8


def parse_inv_payload(payload: bytes) -> List[Tuple[int, bytes]]:
    n, off = read_varint(payload, 0)
    out: List[Tuple[int, bytes]] = []
    for _ in range(min(n, 100_000)):
        if off + 36 > len(payload):
            break
        inv_type = struct.unpack_from("<I", payload, off)[0]
        h = payload[off + 4 : off + 36]
        off += 36
        out.append((inv_type, h))
    return out


def build_getdata_payload(items: List[Tuple[int, bytes]]) -> bytes:
    parts = [write_varint(len(items))]
    for inv_type, h in items:
        parts.append(struct.pack("<I", inv_type) + h)
    return b"".join(parts)


def script_pubkey_hash160(script: bytes) -> Optional[bytes]:
    """P2PKH, P2SH, v0 P2WPKH (hash-only match)."""
    if (
        len(script) == 25
        and script[0] == 0x76
        and script[1] == 0xA9
        and script[2] == 0x14
        and script[23] == 0x88
        and script[24] == 0xAC
    ):
        return script[3:23]
    if len(script) == 23 and script[0] == 0xA9 and script[1] == 0x14 and script[22] == 0x87:
        return script[2:22]
    if len(script) == 22 and script[0] == 0x00 and script[1] == 0x14:
        return script[2:22]
    return None


def parse_tx_outputs(raw: bytes) -> List[Tuple[int, bytes]]:
    """Outputs as (value_koinu, scriptPubKey); skips witness after outputs."""
    off = 0
    if len(raw) < 8:
        return []
    off += 4  # version
    is_segwit = False
    if off + 2 <= len(raw) and raw[off] == 0 and raw[off + 1] == 1:
        is_segwit = True
        off += 2
    nin, off = read_varint(raw, off)
    for _ in range(nin):
        if off + 36 > len(raw):
            raise ValueError("truncated_txin")
        off += 32 + 4
        slen, off = read_varint(raw, off)
        if off + slen > len(raw):
            raise ValueError("truncated_scriptSig")
        off += slen
        if off + 4 > len(raw):
            raise ValueError("truncated_sequence")
        off += 4
    nout, off = read_varint(raw, off)
    outs: List[Tuple[int, bytes]] = []
    for _ in range(nout):
        if off + 8 > len(raw):
            raise ValueError("truncated_value")
        value = struct.unpack_from("<q", raw, off)[0]
        off += 8
        slen, off = read_varint(raw, off)
        if off + slen > len(raw):
            raise ValueError("truncated_pk_script")
        script = raw[off : off + slen]
        off += slen
        outs.append((value, script))
    if is_segwit:
        for _ in range(nin):
            nstk, off = read_varint(raw, off)
            for __ in range(nstk):
                elen, off = read_varint(raw, off)
                if off + elen > len(raw):
                    raise ValueError("truncated_witness")
                off += elen
    return outs


def inv_type_is_tx(t: int) -> bool:
    """MSG_TX or MSG_WITNESS_TX (BIP144)."""
    return (t & ~MSG_WITNESS_FLAG) == MSG_TX


def txid_hex(raw: bytes) -> str:
    """BIP141 transaction id = hash256(non-witness serialization), reversed hex."""
    if len(raw) < 8:
        return sha256d(raw)[::-1].hex()
    off = 4
    is_segwit = len(raw) >= off + 2 and raw[off] == 0 and raw[off + 1] == 1
    if is_segwit:
        off += 2
    body_start = off
    nin, off = read_varint(raw, off)
    for _ in range(nin):
        if off + 36 > len(raw):
            return sha256d(raw)[::-1].hex()
        off += 32 + 4
        slen, off = read_varint(raw, off)
        off += slen
        if off + 4 > len(raw):
            return sha256d(raw)[::-1].hex()
        off += 4
    nout, off = read_varint(raw, off)
    for _ in range(nout):
        if off + 8 > len(raw):
            return sha256d(raw)[::-1].hex()
        off += 8
        slen, off = read_varint(raw, off)
        off += slen
    end_outputs = off
    rest = end_outputs
    if is_segwit:
        try:
            nwi, rest = read_varint(raw, rest)
            for _ in range(nwi):
                ns, rest = read_varint(raw, rest)
                for __ in range(ns):
                    el, rest = read_varint(raw, rest)
                    rest += el
        except Exception:
            return sha256d(raw)[::-1].hex()
    locktime = raw[rest : rest + 4] if rest + 4 <= len(raw) else b"\x00\x00\x00\x00"
    if is_segwit:
        preimage = raw[0:4] + raw[body_start:end_outputs] + locktime
    else:
        preimage = raw[0:end_outputs] + locktime
    return sha256d(preimage)[::-1].hex()


def wtxid_hex(raw: bytes) -> str:
    """BIP141 wtxid = hash256(full wire serialization). Same as txid for non-segwit txs."""
    return sha256d(raw)[::-1].hex()


def process_raw_tx_hub(hub: "ArcadeHub", raw_tx: bytes, watch_to_games: Dict[bytes, str]) -> None:
    log_p2p(f"raw tx received {len(raw_tx)} bytes", level=2)
    try:
        outs = parse_tx_outputs(raw_tx)
    except Exception as ex:
        log_p2p(f"tx parse failed: {ex}", level=2)
        return
    try:
        txid = txid_hex(raw_tx)
    except Exception:
        txid = sha256d(raw_tx)[::-1].hex()
    wtx = wtxid_hex(raw_tx)
    by_game_k: Dict[str, int] = {}
    for value_k, spk in outs:
        h = script_pubkey_hash160(spk)
        if h is None or h not in watch_to_games:
            continue
        gid = watch_to_games[h]
        by_game_k[gid] = by_game_k.get(gid, 0) + value_k
    if not by_game_k:
        log_p2p(
            f"tx {txid[:16]}… (n={len(outs)} outputs) no payment to watched payout hash160",
            level=2,
        )
        return
    for gid, total_k in by_game_k.items():
        if total_k > 0:
            hub.add_payment(gid, txid, total_k / 1e8, "p2p", wtxid=wtx)


def p2p_seed_hosts(network: str) -> List[Tuple[str, int]]:
    """DNS seeds to try (see memetracker/Crypto DNS). Override with ARCADE_P2P_HOST + ARCADE_P2P_PORT."""
    host = os.getenv("ARCADE_P2P_HOST", "").strip()
    port_e = os.getenv("ARCADE_P2P_PORT", "").strip()
    if host and port_e:
        return [(host, int(port_e))]
    if network == "mainnet":
        return [(h, _MAINNET_P2P_PORT) for h in _MAINNET_DNS_SEEDS]
    return [("seed.testnet.dogecoin.org", 44556)]


@dataclass
class GameRuntime:
    game_id: str
    address: str
    credits_seconds: int = 0
    payments_seen: int = 0
    seen_txids: set = field(default_factory=set)
    recent: List[Dict] = field(default_factory=list)
    watch_h160: Optional[bytes] = None


class ArcadeHub:
    """Per-game credits, shared payment rules; P2P multi-watch or MemeTracker HTTP polling."""

    def __init__(
        self,
        storage: Path,
        static_dir: Path,
        network: str,
        amount: float,
        minutes: int,
        games: Dict[str, GameRuntime],
        use_memetracker: bool,
        memetracker_base: str,
        memetracker_callback_base: str,
    ) -> None:
        self.storage = storage
        self.static_dir = static_dir
        self.network = network
        self.amount = max(amount, 0.00000001)
        self.minutes = max(minutes, 1)
        self.games = games
        self.use_memetracker = use_memetracker
        self.memetracker_base = (memetracker_base or "").strip()
        self.memetracker_callback_base = (memetracker_callback_base or "").strip()
        self.last_peer = ""
        self.mempool_live = False
        self.memetracker_ok = False
        self.lock = Lock()
        self.state_file = self.storage / "state.json"
        self.last_touch: Dict[str, float] = {}
        self.touch_grace_sec = float(os.getenv("ARCADE_PLAY_TOUCH_GRACE_SEC", "4") or "4")
        self.memetracker_poll_sec = float(os.getenv("MEMETRACKER_POLL_SEC", "12") or "12")
        self.claim_secret = os.getenv("ARCADE_CLAIM_SECRET", "change-this-claim-secret")
        self.gigawallet_admin_url = os.getenv("ARCADE_GIGAWALLET_ADMIN_URL", "").strip().rstrip("/")
        self._gigawallet_invoice_games = _parse_csv_ids(os.getenv("ARCADE_GIGAWALLET_INVOICE_GAMES", ""))
        self._gigawallet_max_addrs = int(os.getenv("ARCADE_GIGAWALLET_MAX_ADDRESSES_PER_GAME", "500") or "500")
        self._deposit_lock = Lock()
        self._deposit_watch_extra: Dict[bytes, str] = {}
        self._deposit_addresses_by_game: Dict[str, List[str]] = {}
        self.load()

    def load(self) -> None:
        if not self.state_file.exists():
            return
        try:
            data = json.loads(self.state_file.read_text(encoding="utf-8"))
            if isinstance(data, dict) and "games" not in data and "credits_seconds" in data:
                legacy = (
                    os.getenv("ARCADE_LEGACY_GAME_ID", "doge-indy-500").strip()
                    or "doge-indy-500"
                )
                data = {
                    "version": 2,
                    "games": {
                        legacy: {
                            "credits_seconds": data.get("credits_seconds", 0),
                            "payments_seen": data.get("payments_seen", 0),
                            "seen_txids": data.get("seen_txids", []),
                            "recent": data.get("recent", []),
                        }
                    },
                }
            gmap = data.get("games") if isinstance(data, dict) else None
            if not isinstance(gmap, dict):
                return
            for gid, blob in gmap.items():
                if gid not in self.games or not isinstance(blob, dict):
                    continue
                g = self.games[gid]
                g.credits_seconds = int(blob.get("credits_seconds", 0))
                g.payments_seen = int(blob.get("payments_seen", 0))
                g.seen_txids = set(blob.get("seen_txids", []))
                g.recent = list(blob.get("recent", []))[:40]
        except Exception:
            pass

    def persist(self) -> None:
        gmap: Dict[str, dict] = {}
        for gid, g in self.games.items():
            gmap[gid] = {
                "credits_seconds": g.credits_seconds,
                "payments_seen": g.payments_seen,
                "seen_txids": list(g.seen_txids)[-1000:],
                "recent": g.recent[:40],
            }
        payload = {
            "version": 2,
            "games": gmap,
        }
        self.state_file.write_text(json.dumps(payload, indent=2), encoding="utf-8")

    def touch_game(self, game_id: str) -> None:
        if game_id not in self.games:
            return
        self.last_touch[game_id] = time.monotonic()

    def gigawallet_invoice_enabled(self, game_id: str) -> bool:
        return bool(self.gigawallet_admin_url) and game_id in self._gigawallet_invoice_games

    def register_gigawallet_deposit(self, game_id: str, pay_to_address: str) -> None:
        addr = (pay_to_address or "").strip()
        if not addr or game_id not in self.games:
            return
        try:
            h160 = decode_payout_to_hash160(addr, self.network)
        except Exception:
            log_always(f"GigaWallet deposit: skip bad address for game={game_id!r}")
            return
        with self._deposit_lock:
            self._deposit_watch_extra[h160] = game_id
            lst = self._deposit_addresses_by_game.setdefault(game_id, [])
            if addr not in lst:
                lst.append(addr)
            max_n = max(10, self._gigawallet_max_addrs)
            while len(lst) > max_n:
                old = lst.pop(0)
                try:
                    old_h = decode_payout_to_hash160(old, self.network)
                except Exception:
                    continue
                self._deposit_watch_extra.pop(old_h, None)

    def merged_watch_map(self) -> Dict[bytes, str]:
        with self._deposit_lock:
            extra = dict(self._deposit_watch_extra)
        m: Dict[bytes, str] = {}
        for gid, g in self.games.items():
            if g.watch_h160 is None:
                continue
            h = g.watch_h160
            if h in m and m[h] != gid:
                log_always(
                    f"WARNING: duplicate payout hash160 — P2P credits {gid!r} "
                    f"(same address as {m[h]!r}; use a unique payout per game)"
                )
            m[h] = gid
        for h, gid in extra.items():
            if h in m and m[h] != gid:
                log_always(
                    f"WARNING: invoice deposit hash160 — P2P credits {gid!r} "
                    f"(same address as {m[h]!r}; use a unique deposit per game)"
                )
            m[h] = gid
        return m

    def memetracker_poll_pairs(self) -> List[Tuple[str, str]]:
        """(game_id, payout_address) pairs to poll. Includes GigaWallet invoice addresses."""
        with self._deposit_lock:
            by_game: Dict[str, List[str]] = {k: list(v) for k, v in self._deposit_addresses_by_game.items()}
        out: List[Tuple[str, str]] = []
        for gid, g in self.games.items():
            addrs = by_game.get(gid) or []
            if addrs:
                for a in addrs:
                    if a.strip():
                        out.append((gid, a.strip()))
            elif g.address.strip():
                out.append((gid, g.address.strip()))
        return out

    def mark_mempool_live(self) -> None:
        with self.lock:
            if self.mempool_live:
                return
            self.mempool_live = True
        log_always("Mempool / P2P relay is live — payment QR enabled for P2P mode")

    def set_memetracker_ok(self, ok: bool) -> None:
        with self.lock:
            prev = self.memetracker_ok
            self.memetracker_ok = ok
        if ok and not prev:
            log_always("MemeTracker polling OK — payment QR enabled for MemeTracker mode")

    def add_payment(
        self,
        game_id: str,
        txid: str,
        value_doge: float,
        source: str,
        wtxid: Optional[str] = None,
    ) -> None:
        g = self.games.get(game_id)
        if not g:
            return
        id_keys = {txid}
        if wtxid:
            id_keys.add(wtxid)
        with self.lock:
            if g.seen_txids & id_keys:
                log_p2p(
                    f"skip duplicate mempool tx (already credited this txid/wtxid) txid={txid}",
                    level=2,
                )
                return
            if value_doge + 1e-12 < self.amount:
                log_p2p(
                    f"skip underpay txid={txid} got={value_doge:.8f} DOGE min={self.amount}",
                    level=2,
                )
                return
            multiplier = max(1, int(value_doge / self.amount))
            gained = multiplier * self.minutes * 60
            g.seen_txids.update(id_keys)
            g.credits_seconds += gained
            g.payments_seen += 1
            g.recent.insert(
                0,
                {
                    "txid": txid,
                    "value_doge": round(value_doge, 8),
                    "multiplier": multiplier,
                    "seconds_added": gained,
                    "source": source,
                    "ts": int(time.time()),
                },
            )
            g.recent = g.recent[:40]
            self.persist()
            credits_now = g.credits_seconds
            pays = g.payments_seen
        log_pay_credit(
            f"[{game_id}] +{gained}s play time (×{multiplier}) txid={txid} "
            f"paid={value_doge:.8f} DOGE via {source} — timer now {credits_now}s "
            f"({credits_now // 60}m {credits_now % 60}s) payments_total={pays}"
        )

    def tick(self) -> None:
        now = time.monotonic()
        with self.lock:
            poke = False
            for gid, g in self.games.items():
                if g.credits_seconds <= 0:
                    continue
                touched = self.last_touch.get(gid, 0.0)
                if now - touched > self.touch_grace_sec:
                    continue
                g.credits_seconds -= 1
                if g.credits_seconds > 0 and g.credits_seconds % 5 == 0:
                    poke = True
            if poke:
                self.persist()

    def snapshot_game(self, game_id: str) -> Optional[Dict]:
        g = self.games.get(game_id)
        if not g:
            return None
        with self.lock:
            gw_inv = self.gigawallet_invoice_enabled(game_id)
            addr_ok = bool(g.address.strip())
            has_p2p_watch = g.watch_h160 is not None
            payment_ready = False
            if self.use_memetracker:
                payment_ready = self.memetracker_ok and (addr_ok or gw_inv)
            else:
                payment_ready = self.mempool_live and (has_p2p_watch or gw_inv)
            amount_txt = f"{self.amount:.8f}".rstrip("0").rstrip(".")
            uri = f"dogecoin:{g.address}?amount={amount_txt}" if addr_ok else ""
            return {
                "game_id": game_id,
                "payout_address": g.address,
                "amount_doge": self.amount,
                "minutes_per_payment": self.minutes,
                "credits_seconds": g.credits_seconds,
                "payments_seen": g.payments_seen,
                "recent": list(g.recent),
                "insert_uri": uri,
                "insert_uri_encoded": urllib.parse.quote(uri, safe="") if uri else "",
                "last_peer": self.last_peer,
                "p2p_enabled": has_p2p_watch and not self.use_memetracker,
                "mempool_live": self.mempool_live,
                "use_memetracker": self.use_memetracker,
                "memetracker_ok": self.memetracker_ok,
                "payment_ready": payment_ready,
                "gigawallet_invoice_mode": gw_inv,
            }


class LeaderboardStore:
    """SQLite TOP-10 leaderboard persisted under the pup storage volume."""

    def __init__(self, db_path: Path) -> None:
        self.db_path = db_path
        self.lock = Lock()
        self._init_db()

    def _init_db(self) -> None:
        with self.lock:
            con = sqlite3.connect(self.db_path)
            try:
                con.execute(
                    """CREATE TABLE IF NOT EXISTS leaderboard (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        name TEXT NOT NULL,
                        score INTEGER NOT NULL,
                        time_sec INTEGER NOT NULL,
                        created_at INTEGER NOT NULL
                    )"""
                )
                con.commit()
            finally:
                con.close()

    def top10(self) -> List[Dict]:
        with self.lock:
            con = sqlite3.connect(self.db_path)
            try:
                cur = con.execute(
                    """SELECT name, score, time_sec FROM leaderboard
                       ORDER BY score DESC, time_sec ASC, id ASC
                       LIMIT 10"""
                )
                return [{"name": row[0], "score": int(row[1]), "timeSec": int(row[2])} for row in cur.fetchall()]
            finally:
                con.close()

    def try_add(self, name: str, score: int, time_sec: int) -> Tuple[bool, str]:
        clean = (name or "DOGE").strip()[:13] or "DOGE"
        if score <= 0 or score > 999_999_999:
            return False, "bad_score"
        if time_sec < 0 or time_sec > 864_000:
            return False, "bad_time"
        now = int(time.time())
        with self.lock:
            con = sqlite3.connect(self.db_path)
            try:
                cur = con.execute(
                    "INSERT INTO leaderboard (name, score, time_sec, created_at) VALUES (?, ?, ?, ?)",
                    (clean, score, time_sec, now),
                )
                new_id = int(cur.lastrowid)
                con.execute(
                    """DELETE FROM leaderboard WHERE id NOT IN (
                        SELECT id FROM (
                            SELECT id FROM leaderboard
                            ORDER BY score DESC, time_sec ASC, id ASC
                            LIMIT 10
                        )
                    )"""
                )
                row = con.execute("SELECT 1 FROM leaderboard WHERE id = ?", (new_id,)).fetchone()
                con.commit()
                if row:
                    return True, ""
                return False, "not_top_10"
            finally:
                con.close()


class RewardStore:
    """Stores payout claims and tracks paid totals per game."""

    def __init__(self, db_path: Path) -> None:
        self.db_path = db_path
        self.lock = Lock()
        self._init_db()

    def _init_db(self) -> None:
        with self.lock:
            con = sqlite3.connect(self.db_path)
            try:
                con.execute(
                    """CREATE TABLE IF NOT EXISTS reward_claims (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        game_id TEXT NOT NULL,
                        name TEXT NOT NULL,
                        score INTEGER NOT NULL,
                        coins INTEGER NOT NULL,
                        amount_doge REAL NOT NULL,
                        payout_address TEXT NOT NULL,
                        status TEXT NOT NULL,
                        payout_txid TEXT,
                        error TEXT,
                        created_at INTEGER NOT NULL
                    )"""
                )
                con.commit()
            finally:
                con.close()

    def add_claim(
        self,
        game_id: str,
        name: str,
        score: int,
        coins: int,
        amount_doge: float,
        payout_address: str,
        status: str,
        payout_txid: str = "",
        error: str = "",
    ) -> int:
        now = int(time.time())
        with self.lock:
            con = sqlite3.connect(self.db_path)
            try:
                cur = con.execute(
                    """INSERT INTO reward_claims
                       (game_id, name, score, coins, amount_doge, payout_address, status, payout_txid, error, created_at)
                       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
                    (game_id, name, score, coins, amount_doge, payout_address, status, payout_txid, error, now),
                )
                con.commit()
                return int(cur.lastrowid)
            finally:
                con.close()

    def total_paid_for_game(self, game_id: str) -> float:
        with self.lock:
            con = sqlite3.connect(self.db_path)
            try:
                cur = con.execute(
                    "SELECT COALESCE(SUM(amount_doge), 0) FROM reward_claims WHERE game_id = ? AND status = 'paid'",
                    (game_id,),
                )
                row = cur.fetchone()
                return float(row[0] or 0.0)
            finally:
                con.close()


class PayoutEngine:
    """Server-side Dogecoin payout (keys never leave the server).

    ARCADE_PAYOUT_BACKEND: rpc (default, Core sendtoaddress), libdogecoin
    (ARCADE_LIBDOGECOIN_HELPER executable; see https://lib.dogecoin.org/), or gigawallet
    (GigaWallet Admin API POST /account/:foreignID/pay — see https://gigawallet.dogecoin.org/docs/).
    """

    def __init__(self) -> None:
        self.enabled = (os.getenv("ARCADE_ENABLE_PAYOUTS", "").strip().lower() in ("1", "true", "yes"))
        self.backend = os.getenv("ARCADE_PAYOUT_BACKEND", "rpc").strip().lower()
        self.libdogecoin_helper = os.getenv("ARCADE_LIBDOGECOIN_HELPER", "").strip()
        self.gigawallet_admin_url = os.getenv("ARCADE_GIGAWALLET_ADMIN_URL", "").strip().rstrip("/")
        self.rpc_url = os.getenv("DOGE_RPC_URL", "http://127.0.0.1:22555").strip()
        self.rpc_user = os.getenv("DOGE_RPC_USER", "").strip()
        self.rpc_pass = os.getenv("DOGE_RPC_PASS", "").strip()

    def _rpc(self, method: str, params: List) -> dict:
        req_id = random.randint(1, 1_000_000)
        payload = json.dumps({"jsonrpc": "1.0", "id": req_id, "method": method, "params": params}).encode("utf-8")
        req = urllib.request.Request(self.rpc_url, method="POST", data=payload)
        req.add_header("Content-Type", "application/json")
        if self.rpc_user or self.rpc_pass:
            token = base64.b64encode(f"{self.rpc_user}:{self.rpc_pass}".encode("utf-8")).decode("ascii")
            req.add_header("Authorization", "Basic " + token)
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read().decode("utf-8")
        out = json.loads(raw)
        if out.get("error"):
            raise RuntimeError(str(out["error"]))
        return out

    def _send_reward_libdogecoin(self, to_address: str, amount_doge: float, from_wif: str) -> str:
        if not self.libdogecoin_helper:
            raise RuntimeError("libdogecoin_helper_unset")
        helper_path = Path(self.libdogecoin_helper)
        if not helper_path.is_file():
            raise RuntimeError("libdogecoin_helper_not_found")
        if not from_wif:
            raise RuntimeError("libdogecoin_missing_wif")
        payload = json.dumps(
            {
                "to_address": to_address,
                "amount_doge": round(float(amount_doge), 8),
                "wif": from_wif,
            },
            separators=(",", ":"),
        ).encode("utf-8")
        proc = subprocess.run(
            [str(helper_path.resolve())],
            input=payload,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=120,
            check=False,
        )
        err_tail = (proc.stderr or b"").decode("utf-8", errors="replace").strip()
        if proc.returncode != 0:
            msg = err_tail or (proc.stdout or b"").decode("utf-8", errors="replace").strip()
            raise RuntimeError(msg[:800] or "libdogecoin_helper_failed")

        raw_out = (proc.stdout or b"").decode("utf-8", errors="replace").strip()
        try:
            out = json.loads(raw_out) if raw_out else {}
        except json.JSONDecodeError as ex:
            raise RuntimeError(f"libdogecoin_bad_json:{ex}") from ex
        if not out.get("ok"):
            raise RuntimeError(str(out.get("error") or "libdogecoin_failed"))
        txid = str(out.get("txid") or "").strip()
        if not txid:
            raise RuntimeError("libdogecoin_missing_txid")
        return txid

    def _send_reward_gigawallet(self, foreign_id: str, to_address: str, amount_doge: float) -> str:
        if not self.gigawallet_admin_url:
            raise RuntimeError("gigawallet_admin_url_unset")
        fid = (foreign_id or "").strip()
        if not fid:
            raise RuntimeError("gigawallet_foreign_id_unset")
        amt = round(float(amount_doge), 8)
        url = (
            f"{self.gigawallet_admin_url}/account/"
            f"{urllib.parse.quote(fid, safe='')}/pay"
        )
        out = http_json_admin(
            "POST",
            url,
            {"amount": f"{amt:.8f}".rstrip("0").rstrip("."), "to": to_address},
            timeout=120,
        )
        txid = str(out.get("hex") or out.get("txid") or "").strip()
        if not txid:
            raise RuntimeError(str(out.get("error") or out.get("message") or "gigawallet_missing_txid"))
        return txid

    def send_reward(
        self,
        to_address: str,
        amount_doge: float,
        from_wif: str = "",
        *,
        gigawallet_foreign_id: str = "",
    ) -> str:
        if not self.enabled:
            raise RuntimeError("payouts_disabled")
        if amount_doge <= 0:
            raise RuntimeError("bad_amount")

        if self.backend in ("gigawallet", "gw", "giga"):
            return self._send_reward_gigawallet(gigawallet_foreign_id, to_address, amount_doge)

        if self.backend in ("libdogecoin", "libdoge", "ld"):
            return self._send_reward_libdogecoin(to_address, amount_doge, from_wif)

        # Default: Dogecoin Core wallet RPC.
        out = self._rpc("sendtoaddress", [to_address, round(float(amount_doge), 8)])
        txid = str(out.get("result") or "").strip()
        if not txid:
            raise RuntimeError("missing_txid")
        return txid


def build_version_payload(p2p_port: int) -> bytes:
    version = 70015
    services = NODE_NETWORK | NODE_WITNESS
    timestamp = int(time.time())
    addr_recv = struct.pack("<Q", 0) + b"\x00" * 16 + struct.pack(">H", p2p_port)
    addr_from = struct.pack("<Q", 0) + b"\x00" * 16 + struct.pack(">H", p2p_port)
    nonce = random.getrandbits(64)
    user_agent = b"\x14/arcade-pup:0.0.105/"
    start_height = 0
    relay = 1
    return (
        struct.pack("<iQQ", version, services, timestamp)
        + addr_recv
        + addr_from
        + struct.pack("<Q", nonce)
        + user_agent
        + struct.pack("<i?", start_height, bool(relay))
    )


def _union_seen_txids(hub: ArcadeHub) -> set:
    s: set = set()
    for g in hub.games.values():
        s |= g.seen_txids
    return s


def _p2p_peer_session_sync(hub: ArcadeHub, peer: str, p_port: int) -> None:
    s: Optional[socket.socket] = None
    try:
        log_p2p(f"connecting TCP {peer}:{p_port} …")
        s = socket.create_connection((peer, p_port), timeout=8)
        s.settimeout(25)
        hub.last_peer = f"{peer}:{p_port}"
        log_p2p(f"connected peer={peer}:{p_port} sending version (handshake)")
        s.sendall(build_message("version", build_version_payload(p_port)))
        got_verack = False
        mempool_sent = False
        last_mempool_resync = 0.0
        start = time.time()

        while time.time() - start < 300:
            header = read_exact(s, 24)
            if header[0:4] != MAGIC:
                continue
            cmd = header[4:16].rstrip(b"\x00").decode("ascii", errors="ignore")
            size = struct.unpack("<I", header[16:20])[0]
            payload = read_exact(s, size) if size else b""

            if cmd == "version":
                log_p2p("recv version -> sending verack", level=2)
                s.sendall(build_message("verack", b""))
            elif cmd == "verack":
                got_verack = True
                log_p2p("handshake complete (verack)")
                if not mempool_sent:
                    s.sendall(build_message("mempool", b""))
                    mempool_sent = True
                    last_mempool_resync = time.time()
                    log_p2p("sent mempool (request peer unconfirmed tx inv)")
            elif cmd == "ping":
                s.sendall(build_message("pong", payload))
            elif cmd == "tx":
                hub.mark_mempool_live()
                log_p2p(f"recv tx {len(payload)} B (parsing for payout match)")
                process_raw_tx_hub(hub, payload, hub.merged_watch_map())
            elif cmd == "notfound":
                log_p2p(f"recv notfound (peer missing some getdata object) payload={len(payload)} B", level=2)
            elif cmd == "inv" and got_verack:
                if not payload:
                    continue
                try:
                    invs = parse_inv_payload(payload)
                except Exception as iex:
                    log_p2p(f"inv parse error: {iex!r}", level=2)
                    continue
                if invs:
                    hub.mark_mempool_live()
                with hub.lock:
                    seen = _union_seen_txids(hub)
                fetch: List[Tuple[int, bytes]] = []
                inv_seen: set[str] = set()
                for inv_type, hb in invs:
                    if not inv_type_is_tx(inv_type):
                        continue
                    key = f"{inv_type:x}:{hb.hex()}"
                    if key in inv_seen:
                        continue
                    inv_seen.add(key)
                    inv_hash_hex = hb[::-1].hex()
                    if inv_hash_hex in seen:
                        continue
                    fetch.append((inv_type, hb))
                    if len(fetch) >= _MAX_TX_FETCH_PER_INV:
                        break
                n_tx_inv = sum(1 for t, _ in invs if inv_type_is_tx(t))
                log_p2p(
                    f"inv: {len(invs)} item(s), {n_tx_inv} tx-like, "
                    f"requesting {len(fetch)} via getdata (peer {hub.last_peer})"
                )
                for i in range(0, len(fetch), _GETDATA_BATCH):
                    batch = fetch[i : i + _GETDATA_BATCH]
                    pl = build_getdata_payload(batch)
                    s.sendall(build_message("getdata", pl))
                    log_p2p(f"sent getdata for {len(batch)} tx object(s)", level=2)
            if got_verack and mempool_sent and (time.time() - last_mempool_resync) >= _MEMPOOL_RESYNC_SEC:
                s.sendall(build_message("mempool", b""))
                last_mempool_resync = time.time()
                log_p2p(f"periodic mempool refresh ({_MEMPOOL_RESYNC_SEC}s)")
        log_p2p(f"session end peer={peer} (300s read loop or peer closed)")
    except Exception as ex:
        log_p2p(f"peer {peer}:{p_port} error: {ex!r}")
    finally:
        if s is not None:
            try:
                s.close()
            except Exception:
                pass


async def p2p_sniffer(hub: ArcadeHub, network: str) -> None:
    seeds = p2p_seed_hosts(network)
    log_p2p(f"starting network={network!r} seeds={len(seeds)} (set ARCADE_P2P_LOG=0 quiet, 2 verbose)")
    seed_round = 0
    while True:
        try:
            seeds = p2p_seed_hosts(network)
            seed_host, p_port = seeds[seed_round % len(seeds)]
            seed_round += 1
            try:
                infos = socket.getaddrinfo(seed_host, p_port, type=socket.SOCK_STREAM)
            except Exception as ex:
                log_p2p(f"DNS resolve failed seed={seed_host}:{p_port} err={ex!r}")
                await asyncio.sleep(3)
                continue
            peers = [x[4][0] for x in infos][:12]
            log_p2p(f"seed {seed_host} -> {len(peers)} peer IP(s) (using up to 12)")
            if not peers:
                log_p2p(f"no A/AAAA records for seed {seed_host}, retry later")
                await asyncio.sleep(10)
                continue

            for peer in peers:
                await asyncio.to_thread(_p2p_peer_session_sync, hub, peer, p_port)
        except Exception as ex:
            log_p2p(f"P2P outer error: {ex!r}")
        await asyncio.sleep(5)


def memetracker_reachable(hub: ArcadeHub) -> bool:
    """GET /healthz — used when no deposit addresses exist yet (GigaWallet invoice flow)."""
    base = hub.memetracker_base.rstrip("/")
    url = f"{base}/healthz"
    req = urllib.request.Request(url, method="GET", headers={"User-Agent": "arcade-pup/1"})
    try:
        with urllib.request.urlopen(req, timeout=8) as resp:
            code = resp.getcode()
            return 200 <= code < 300
    except Exception:
        return False


def memetracker_fetch_and_apply(hub: ArcadeHub, game_id: str, track_address: str) -> bool:
    g = hub.games.get(game_id)
    addr = (track_address or "").strip()
    if not g or not hub.memetracker_base or not addr:
        return False
    base = hub.memetracker_base.rstrip("/")
    callback_url = ""
    if hub.memetracker_callback_base:
        callback_url = (
            f"{hub.memetracker_callback_base.rstrip('/')}/"
            f"{urllib.parse.quote(addr, safe='')}/"
            f"?game={urllib.parse.quote(game_id, safe='')}"
        )
    url = f"{base}/track/{urllib.parse.quote(addr, safe='')}"
    if callback_url:
        url += f"?callback={urllib.parse.quote(callback_url, safe='')}"
    req = urllib.request.Request(url, method="GET", headers={"User-Agent": "arcade-pup/1"})
    try:
        with urllib.request.urlopen(req, timeout=18) as resp:
            raw = resp.read().decode("utf-8")
    except (urllib.error.URLError, TimeoutError, OSError) as ex:
        log_always(f"MemeTracker GET failed game={game_id} url={url!r} err={ex!r}")
        return False
    try:
        data = json.loads(raw)
    except Exception as ex:
        log_always(f"MemeTracker JSON error game={game_id}: {ex!r}")
        return False
    txs = data.get("transactions")
    if txs is None:
        txs = []
    if not isinstance(txs, list):
        log_always(f"MemeTracker bad transactions shape game={game_id}")
        return False
    for tx in txs:
        if not isinstance(tx, dict):
            continue
        txid = str(tx.get("txid") or "").strip()
        if not txid:
            continue
        try:
            amt = float(tx.get("amount_doge") or 0)
        except (TypeError, ValueError):
            amt = 0.0
        hub.add_payment(game_id, txid, amt, "memetracker")
    return True


async def memetracker_poller(hub: ArcadeHub) -> None:
    while True:
        if not hub.memetracker_base:
            hub.set_memetracker_ok(False)
            await asyncio.sleep(hub.memetracker_poll_sec)
            continue
        pairs = hub.memetracker_poll_pairs()
        if not pairs:
            if any(hub.gigawallet_invoice_enabled(gid) for gid in hub.games):
                ok = await asyncio.to_thread(memetracker_reachable, hub)
                hub.set_memetracker_ok(ok)
            else:
                hub.set_memetracker_ok(False)
            await asyncio.sleep(hub.memetracker_poll_sec)
            continue
        all_ok = True
        for gid, addr in pairs:
            ok = await asyncio.to_thread(memetracker_fetch_and_apply, hub, gid, addr)
            if not ok:
                all_ok = False
        hub.set_memetracker_ok(all_ok)
        await asyncio.sleep(hub.memetracker_poll_sec)


def games_list_payload(static_dir: Path) -> List[dict]:
    rows = []
    for gid, meta in discover_game_manifests(static_dir):
        rows.append(
            {
                "id": gid,
                "title": meta.get("title", gid),
                "description": meta.get("description", ""),
                "path": meta.get("path", f"/games/{gid}/index.html"),
                "demo_path": meta.get("demo_path", f"/games/{gid}/demo.html"),
            }
        )
    return rows


def sign_claim_token(secret: str, game_id: str, expires_at: int, payments_seen: int) -> str:
    msg = f"{game_id}|{expires_at}|{payments_seen}".encode("utf-8")
    return hmac.new(secret.encode("utf-8"), msg, hashlib.sha256).hexdigest()


def verify_claim_token(secret: str, game_id: str, token: str, payments_seen: int) -> bool:
    parts = token.split(".")
    if len(parts) != 2:
        return False
    try:
        exp = int(parts[0])
    except ValueError:
        return False
    if time.time() > exp:
        return False
    expected = sign_claim_token(secret, game_id, exp, payments_seen)
    return hmac.compare_digest(expected, parts[1])


def gigawallet_mint_play_invoice(hub: ArcadeHub, game_id: str) -> dict:
    """Create GigaWallet invoice (HD deposit address); MemeTracker / P2P detect payment as usual."""
    if not hub.gigawallet_invoice_enabled(game_id):
        raise ValueError("gigawallet_invoice_disabled")
    foreign_id = gigawallet_foreign_id_for_game(game_id)
    if not foreign_id:
        raise ValueError("missing_gigawallet_foreign_id")
    base = hub.gigawallet_admin_url
    acc_url = f"{base}/account/{urllib.parse.quote(foreign_id, safe='')}"
    http_json_admin("POST", acc_url, {})
    inv_url = f"{acc_url}/invoice"
    item_val = f"{hub.amount:.8f}".rstrip("0").rstrip(".")
    inv = http_json_admin(
        "POST",
        inv_url,
        {
            "items": [
                {
                    "type": "item",
                    "name": f"Arcade play — {game_id}",
                    "value": item_val,
                    "quantity": 1,
                }
            ],
            "required_confirmations": 1,
        },
    )
    pay_to = str(inv.get("pay_to_address") or inv.get("id") or "").strip()
    if not pay_to:
        raise ValueError("gigawallet_invoice_no_address")
    hub.register_gigawallet_deposit(game_id, pay_to)
    uri = f"dogecoin:{pay_to}?amount={item_val}"
    return {
        "ok": True,
        "game_id": game_id,
        "foreign_id": foreign_id,
        "invoice_id": str(inv.get("id") or pay_to),
        "pay_to_address": pay_to,
        "insert_uri": uri,
        "insert_uri_encoded": urllib.parse.quote(uri, safe=""),
        "total_doge": hub.amount,
    }


class ArcadeHandler(SimpleHTTPRequestHandler):
    def __init__(
        self,
        *args,
        static_dir: Path,
        hub: ArcadeHub,
        leaderboard: LeaderboardStore,
        rewards: RewardStore,
        payout_engine: PayoutEngine,
        **kwargs,
    ):
        self.hub = hub
        self.static_dir = static_dir
        self.leaderboard = leaderboard
        self.rewards = rewards
        self.payout_engine = payout_engine
        super().__init__(*args, directory=str(static_dir), **kwargs)

    def copyfile(self, source, outputfile):
        try:
            super().copyfile(source, outputfile)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def _json(self, code: int, obj: dict) -> None:
        body = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        path_only = parsed.path or "/"
        if path_only.startswith("/api/leaderboard"):
            rows = self.leaderboard.top10()
            body = json.dumps(rows).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if path_only == "/api/games":
            body = json.dumps(games_list_payload(self.static_dir)).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if path_only.startswith("/api/state"):
            qs = urllib.parse.parse_qs(parsed.query)
            game_id = (qs.get("game") or [None])[0]
            if not game_id:
                self._json(400, {"ok": False, "error": "missing_game", "hint": "Use ?game=doge-indy-500"})
                return
            snap = self.hub.snapshot_game(game_id)
            if not snap:
                self._json(404, {"ok": False, "error": "unknown_game"})
                return
            self.hub.touch_game(game_id)
            body = json.dumps(snap).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if path_only.startswith("/api/claim-token"):
            qs = urllib.parse.parse_qs(parsed.query)
            game_id = (qs.get("game") or [None])[0]
            if not game_id:
                self._json(400, {"ok": False, "error": "missing_game"})
                return
            g = self.hub.games.get(game_id)
            if not g:
                self._json(404, {"ok": False, "error": "unknown_game"})
                return
            exp = int(time.time()) + 120
            sig = sign_claim_token(self.hub.claim_secret, game_id, exp, g.payments_seen)
            self._json(200, {"ok": True, "token": f"{exp}.{sig}"})
            return
        if path_only.startswith("/api/mempool"):
            with self.hub.lock:
                gw_p2p = any(self.hub.gigawallet_invoice_enabled(gid) for gid in self.hub.games)
                gate = {
                    "p2p_enabled": (
                        any(g.watch_h160 is not None for g in self.hub.games.values()) or gw_p2p
                    )
                    and not self.hub.use_memetracker,
                    "mempool_live": self.hub.mempool_live,
                    "use_memetracker": self.hub.use_memetracker,
                    "memetracker_ok": self.hub.memetracker_ok,
                }
            body = json.dumps(gate).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        return super().do_GET()

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        path_only = parsed.path or "/"

        if path_only.startswith("/api/gigawallet/play-invoice"):
            try:
                length = min(int(self.headers.get("Content-Length", "0")), 2048)
            except ValueError:
                length = 0
            raw = self.rfile.read(length) if length > 0 else b"{}"
            try:
                data = json.loads(raw.decode("utf-8", errors="replace"))
            except Exception:
                self._json(400, {"ok": False, "error": "invalid_json"})
                return
            game_id = str(data.get("game_id") or "").strip()
            if not game_id or game_id not in self.hub.games:
                self._json(400, {"ok": False, "error": "unknown_game"})
                return
            try:
                out = gigawallet_mint_play_invoice(self.hub, game_id)
                self._json(200, out)
            except ValueError as ex:
                self._json(400, {"ok": False, "error": str(ex)})
            except RuntimeError as ex:
                self._json(502, {"ok": False, "error": str(ex)})
            return

        if path_only.startswith("/api/leaderboard"):
            try:
                length = min(int(self.headers.get("Content-Length", "0")), 4096)
            except ValueError:
                length = 0
            raw = self.rfile.read(length) if length > 0 else b"{}"
            try:
                data = json.loads(raw.decode("utf-8", errors="replace"))
            except Exception:
                self._json(400, {"ok": False, "error": "invalid_json"})
                return
            name = data.get("name", "DOGE")
            try:
                score = int(data.get("score", 0))
                time_sec = int(data.get("timeSec", data.get("time_sec", 0)))
            except (TypeError, ValueError):
                self._json(400, {"ok": False, "error": "bad_numbers"})
                return
            ok, err = self.leaderboard.try_add(str(name), score, time_sec)
            if ok:
                self._json(200, {"ok": True})
            elif err == "not_top_10":
                self._json(200, {"ok": False, "error": err})
            else:
                self._json(400, {"ok": False, "error": err})
            return

        if path_only.startswith("/api/claim-reward"):
            try:
                length = min(int(self.headers.get("Content-Length", "0")), 8192)
            except ValueError:
                length = 0
            raw = self.rfile.read(length) if length > 0 else b"{}"
            try:
                data = json.loads(raw.decode("utf-8", errors="replace"))
            except Exception:
                self._json(400, {"ok": False, "error": "invalid_json"})
                return
            if not isinstance(data, dict):
                self._json(400, {"ok": False, "error": "invalid_payload"})
                return
            game_id = str(data.get("game_id") or "").strip()
            token = str(data.get("claim_token") or "").strip()
            payout_address = str(data.get("payout_address") or "").strip()
            name = str(data.get("name") or "DOGE").strip()[:13] or "DOGE"
            try:
                coins = int(data.get("coins", 0))
                score = int(data.get("score", 0))
            except (TypeError, ValueError):
                self._json(400, {"ok": False, "error": "bad_numbers"})
                return
            if coins < 0 or coins > 5000 or score < 0:
                self._json(400, {"ok": False, "error": "out_of_range"})
                return
            g = self.hub.games.get(game_id)
            if not g:
                self._json(404, {"ok": False, "error": "unknown_game"})
                return
            if not verify_claim_token(self.hub.claim_secret, game_id, token, g.payments_seen):
                self._json(403, {"ok": False, "error": "bad_or_expired_claim_token"})
                return
            try:
                decode_payout_to_hash160(payout_address, self.hub.network)
            except Exception:
                self._json(400, {"ok": False, "error": "invalid_payout_address"})
                return
            priv_env = game_id_to_payout_env_key(game_id) + "_PRIVKEY_WIF"
            gw_backend = self.payout_engine.backend in ("gigawallet", "gw", "giga")
            gw_fid = gigawallet_foreign_id_for_game(game_id)
            if gw_backend:
                if not gw_fid:
                    self._json(
                        400,
                        {
                            "ok": False,
                            "error": "missing_gigawallet_account",
                            "hint": game_id_to_gigawallet_account_env_key(game_id),
                        },
                    )
                    return
            elif not os.getenv(priv_env, "").strip():
                self._json(400, {"ok": False, "error": f"missing_server_privkey_env:{priv_env}"})
                return

            amount_doge = round(coins * 0.10, 8)
            paid_total = self.rewards.total_paid_for_game(game_id)
            tipjar_estimate = max(0.0, g.payments_seen * self.hub.amount - paid_total)
            if amount_doge <= 0:
                self._json(400, {"ok": False, "error": "nothing_to_claim"})
                return
            if amount_doge > tipjar_estimate + 1e-9:
                self._json(400, {"ok": False, "error": "insufficient_tipjar", "tipjar_available": round(tipjar_estimate, 8)})
                return

            txid = ""
            err_txt = ""
            status = "paid"
            try:
                if gw_backend:
                    txid = self.payout_engine.send_reward(
                        payout_address,
                        amount_doge,
                        gigawallet_foreign_id=gw_fid,
                    )
                else:
                    wif = os.getenv(priv_env, "").strip()
                    txid = self.payout_engine.send_reward(payout_address, amount_doge, from_wif=wif)
            except Exception as ex:
                status = "failed"
                err_txt = str(ex)

            self.rewards.add_claim(
                game_id=game_id,
                name=name,
                score=score,
                coins=coins,
                amount_doge=amount_doge,
                payout_address=payout_address,
                status=status,
                payout_txid=txid,
                error=err_txt,
            )

            if status == "paid":
                self._json(200, {"ok": True, "status": status, "txid": txid, "amount_doge": amount_doge})
            else:
                self._json(502, {"ok": False, "status": status, "error": err_txt, "amount_doge": amount_doge})
            return

        # MemeTracker push callback endpoint:
        # - /api/memetracker/callback?game=<id>&address=<D...>
        # - /<D...>/ (address in path)
        if path_only.startswith("/api/memetracker/callback") or (path_only.count("/") <= 2 and path_only.strip("/").startswith("D")):
            try:
                length = min(int(self.headers.get("Content-Length", "0")), 8192)
            except ValueError:
                length = 0
            raw = self.rfile.read(length) if length > 0 else b"{}"
            try:
                data = json.loads(raw.decode("utf-8", errors="replace"))
            except Exception:
                self._json(400, {"ok": False, "error": "invalid_json"})
                return
            if not isinstance(data, dict):
                self._json(400, {"ok": False, "error": "invalid_payload"})
                return

            qs = urllib.parse.parse_qs(parsed.query)
            game_id = (qs.get("game") or [None])[0]
            addr = (qs.get("address") or [None])[0]
            if not addr and path_only.strip("/").startswith("D"):
                addr = path_only.strip("/")
            if not addr:
                addr = str(data.get("address") or "").strip()
            if not game_id and addr:
                for gid, g in self.hub.games.items():
                    if g.address.strip() == addr:
                        game_id = gid
                        break
            if not game_id:
                self._json(400, {"ok": False, "error": "unknown_game"})
                return

            txid = str(data.get("txid") or "").strip()
            if not txid:
                self._json(400, {"ok": False, "error": "missing_txid"})
                return
            try:
                amt = float(data.get("payment_amount", data.get("amount_doge", 0)) or 0.0)
            except (TypeError, ValueError):
                amt = 0.0
            self.hub.add_payment(game_id, txid, amt, "memetracker-callback")
            self._json(200, {"ok": True, "game_id": game_id})
            return

        else:
            self.send_error(404)
            return


def start_http(
    static_dir: Path,
    hub: ArcadeHub,
    leaderboard: LeaderboardStore,
    rewards: RewardStore,
    payout_engine: PayoutEngine,
    bind_ip: str,
    port: int,
) -> None:
    def factory(*args, **kwargs):
        return ArcadeHandler(
            *args,
            static_dir=static_dir,
            hub=hub,
            leaderboard=leaderboard,
            rewards=rewards,
            payout_engine=payout_engine,
            **kwargs,
        )

    server = ThreadingHTTPServer((bind_ip, port), factory)
    server.serve_forever()


async def ticker(hub: ArcadeHub) -> None:
    tick_i = 0
    while True:
        hub.tick()
        tick_i += 1
        if tick_i % 60 == 0:
            with hub.lock:
                totals = [g.credits_seconds for g in hub.games.values() if g.credits_seconds > 0]
            if totals:
                log_timer(f"countdown: active game timers {len(totals)} (1/s per open client)")
        await asyncio.sleep(1)


def main() -> None:
    storage = Path(os.getenv("ARCADE_STORAGE", "/storage/arcade"))
    _static_env = os.getenv("ARCADE_STATIC_DIR", "").strip()
    static_dir = Path(_static_env) if _static_env else (storage / "static")
    storage.mkdir(parents=True, exist_ok=True)

    amount = float(os.getenv("DOGE_AMOUNT_TO_PLAY", "1") or "1")
    minutes = int(float(os.getenv("MINUTES_PER_PAYMENT", "3") or "3"))
    network = os.getenv("DOGE_NETWORK", "mainnet").strip().lower()
    bind_ip = os.getenv("ARCADE_BIND_IP", "0.0.0.0")
    port = int(os.getenv("ARCADE_PORT", "8099"))

    use_meme = (os.getenv("USE_MEMETRACKER", "").strip().lower() in ("1", "true", "yes"))
    meme_base = os.getenv("MEMETRACKER_BASE_URL", "").strip()
    meme_callback_base = os.getenv("ARCADE_MEMETRACKER_CALLBACK_BASE_URL", "").strip()

    manifests = discover_game_manifests(static_dir)
    if not manifests:
        log_always(f"No games found under {static_dir / 'games'} — add games/*/game.json")

    games: Dict[str, GameRuntime] = {}
    for folder_id, meta in manifests:
        gid = str(meta.get("id") or folder_id).strip()
        env_key = game_id_to_payout_env_key(gid)
        addr = os.getenv(env_key, "").strip() or DEFAULT_GAME_PAYOUT_ADDRESSES.get(gid, "")
        wh: Optional[bytes] = None
        if addr:
            try:
                wh = decode_payout_to_hash160(addr, network)
            except Exception:
                wh = None
        games[gid] = GameRuntime(game_id=gid, address=addr, watch_h160=wh)

    if not games:
        log_always("No games configured — add folders under static/games/ with game.json")

    hub = ArcadeHub(
        storage,
        static_dir,
        network,
        amount,
        minutes,
        games,
        use_meme,
        meme_base,
        meme_callback_base,
    )
    leaderboard = LeaderboardStore(storage / "leaderboard.sqlite3")
    rewards = RewardStore(storage / "rewards.sqlite3")
    payout_engine = PayoutEngine()
    if payout_engine.enabled:
        log_always(
            f"Payouts enabled backend={payout_engine.backend!r} "
            f"libdogecoin_helper={'set' if payout_engine.libdogecoin_helper else 'unset'} "
            f"gigawallet_admin={'set' if payout_engine.gigawallet_admin_url else 'unset'}"
        )

    watch_to_games: Dict[bytes, str] = {}
    for gid, g in hub.games.items():
        if g.watch_h160 is not None:
            if g.watch_h160 in watch_to_games:
                log_always(
                    f"WARNING: duplicate payout hash160 — P2P credits {gid!r} "
                    f"(same address as {watch_to_games[g.watch_h160]!r}; "
                    f"use a unique payout per game)"
                )
            watch_to_games[g.watch_h160] = gid

    invoice_p2p = any(hub.gigawallet_invoice_enabled(gid) for gid in hub.games)
    if hub.gigawallet_admin_url and hub._gigawallet_invoice_games:
        log_always(
            f"GigaWallet invoice deposit games={sorted(hub._gigawallet_invoice_games)!r} "
            f"admin={hub.gigawallet_admin_url!r}"
        )

    log_always(
        f"Arcade HTTP :{port} network={network!r} games={list(hub.games.keys())!r} "
        f"min_payment={amount} DOGE +{minutes}m memetracker={use_meme!r} "
        f"ARCADE_P2P_LOG={_p2p_log_level()} (0=quiet 1=normal 2=verbose)"
    )
    if use_meme:
        if not meme_base:
            log_always("USE_MEMETRACKER set but MEMETRACKER_BASE_URL empty — payment detection disabled")
        else:
            log_always(f"MemeTracker mode: poll {meme_base!r} every {hub.memetracker_poll_sec}s")
            if meme_callback_base:
                log_always(f"MemeTracker callback target base: {meme_callback_base!r}")
    else:
        if not watch_to_games and not invoice_p2p:
            log_always("P2P payment listener DISABLED — no valid per-game P2PKH addresses for this network")
        elif invoice_p2p and not watch_to_games:
            log_always("P2P payment listener enabled (GigaWallet invoice addresses registered dynamically)")
        else:
            log_always(f"P2P payment listener enabled for {len(watch_to_games)} static + invoice deposit(s)")

    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    loop.create_task(ticker(hub))
    if use_meme and meme_base:
        loop.create_task(memetracker_poller(hub))
    elif watch_to_games or invoice_p2p:
        loop.create_task(p2p_sniffer(hub, network))

    import threading

    t = threading.Thread(
        target=start_http,
        args=(static_dir, hub, leaderboard, rewards, payout_engine, bind_ip, port),
        daemon=True,
    )
    t.start()

    loop.run_forever()


if __name__ == "__main__":
    main()
