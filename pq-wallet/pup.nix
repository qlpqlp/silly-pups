# PQ Doge Wallet — Go HTTP service + libdogecoin (such, sendtx, spvnode) on PATH.
# Service name must match manifest container.services[0].name.
{ pkgs ? import <nixpkgs> {} }:

let
  libdogecoin = pkgs.callPackage ./nix/libdogecoin.nix {};

  pq_bin = pkgs.buildGoModule {
    pname = "pq-wallet";
    version = "0.0.10";
    src = ./service;
    vendorHash = null;
    go = pkgs.go_1_24;

    buildPhase = ''
      go build -trimpath -ldflags="-s -w" -o pq-wallet .
    '';

    installPhase = ''
      mkdir -p $out/bin
      cp pq-wallet $out/bin/
    '';
  };

  pq-wallet = pkgs.writeShellScriptBin "run.sh" ''
    set -e
    PUBLIC_PORT="''${PUBLIC_PORT:-33880}"
    STORAGE="''${PQ_STORAGE_DIR:-/storage/pq-wallet}"
    mkdir -p "$STORAGE"

    export PATH="${libdogecoin}/bin:${pkgs.jq}/bin:${pkgs.coreutils}/bin:$PATH"

    export PUBLIC_PORT
    export PQ_STORAGE_DIR="$STORAGE"
    export LIBDOGECOIN_SUCH="''${LIBDOGECOIN_SUCH:-${libdogecoin}/bin/such}"
    export LIBDOGECOIN_SENDTX="''${LIBDOGECOIN_SENDTX:-${libdogecoin}/bin/sendtx}"
    export LIBDOGECOIN_SPVNODE="''${LIBDOGECOIN_SPVNODE:-${libdogecoin}/bin/spvnode}"
    export EXPLORER_TX_API="''${EXPLORER_TX_API:-}"
    export EXPLORER_ADDRESS_API="''${EXPLORER_ADDRESS_API:-}"
    export SPVNODE_ENABLE="''${SPVNODE_ENABLE:-1}"
    export MTR_P2P_PORT="''${MTR_P2P_PORT:-}"
    export MTR_P2P_PARALLEL="''${MTR_P2P_PARALLEL:-}"
    export MTR_LIST_LIMIT="''${MTR_LIST_LIMIT:-}"

    if [ "$SPVNODE_ENABLE" = "1" ] && [ -f "$STORAGE/wallet.json" ]; then
      if [ ! -f "$STORAGE/spv.pid" ] || ! kill -0 "$(cat "$STORAGE/spv.pid" 2>/dev/null)" 2>/dev/null; then
        rm -f "$STORAGE/spv.pid"
        ADDR=$(jq -r '.p2pkh_address // empty' "$STORAGE/wallet.json")
        NET=$(jq -r '.network // "mainnet"' "$STORAGE/wallet.json")
        if [ -n "$ADDR" ]; then
          TN_FLAG=""
          if [ "$NET" = "testnet" ]; then TN_FLAG="-t"; fi
          if command -v stdbuf >/dev/null 2>&1; then
            nohup stdbuf -oL -eL spvnode $TN_FLAG -f 0 -c -l -a "$ADDR" -w "$STORAGE/spv_wallet.db" -h "$STORAGE/headers.db" -b scan >>"$STORAGE/spv.log" 2>&1 &
          else
            nohup spvnode $TN_FLAG -f 0 -c -l -a "$ADDR" -w "$STORAGE/spv_wallet.db" -h "$STORAGE/headers.db" -b scan >>"$STORAGE/spv.log" 2>&1 &
          fi
          echo $! >"$STORAGE/spv.pid"
        fi
      fi
    fi

    exec ${pq_bin}/bin/pq-wallet
  '';
in
{
  pq-wallet = pq-wallet;
}
