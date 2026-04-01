#!/usr/bin/env bash
# Example ARCADE_LIBDOGECOIN_HELPER — copy to a real path, chmod +x, point the pup env there.
# Libdogecoin: https://lib.dogecoin.org/ — source: https://github.com/dogecoinfoundation/libdogecoin
#
# Contract (Arcade server):
#   stdin:  one line JSON — {"to_address":"D...","amount_doge":0.1,"wif":"..."}
#   stdout: one line JSON — {"ok":true,"txid":"..."} or {"ok":false,"error":"..."}
#   exit 0 on success JSON; non-zero also treated as failure
#
# Replace the body below with your built `sendtx` / `such` pipeline (UTXO selection,
# sign, broadcast). Do not log the WIF.

set -euo pipefail
read -r LINE || { echo '{"ok":false,"error":"no_stdin"}'; exit 1; }
# TODO: parse LINE, call libdogecoin tools, emit txid
echo '{"ok":false,"error":"stub: replace with libdogecoin send/broadcast"}'
exit 1
