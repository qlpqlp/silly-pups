#!/usr/bin/env bash
# Legacy: PQ keygen is built-in via `such -c falcon_keygen` (see pq-wallet API / wallet create with pq_keys).
# This script matches the old JSON contract if you still want a helper:
set -euo pipefail
SUCH="${LIBDOGECOIN_SUCH:-such}"
OUT=$("$SUCH" -c falcon_keygen 2>&1) || true
PUB=$(echo "$OUT" | grep -i 'public key:' | head -1 | sed 's/.*public key:[[:space:]]*//')
SEC=$(echo "$OUT" | grep -i 'secret key:' | head -1 | sed 's/.*secret key:[[:space:]]*//')
if [[ -n "$PUB" && -n "$SEC" ]]; then
  printf '{"ok":true,"pq_public_key_hex":"%s","pq_private_key_hex":"%s"}\n' "$PUB" "$SEC"
else
  echo '{"ok":false,"error":"parse falcon_keygen output"}'
  exit 1
fi
