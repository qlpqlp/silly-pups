{ pkgs ? import <nixpkgs> {} }:

let
  arcadeService = pkgs.writeShellScriptBin "run-arcade.sh" ''
    set -e

    export PAYOUT_GAME_DOGE_INDY_500="''${PAYOUT_GAME_DOGE_INDY_500:-DTqAFgNNUgiPEfGmc4HZUkqJ4sz5vADd1n}"
    export PAYOUT_GAME_FEAR_THE_DOGE="''${PAYOUT_GAME_FEAR_THE_DOGE:-DAd3QzjJ2Yh1rwTxxVz7GEKxtNQh2s9zJe}"
    export PAYOUT_GAME_DOGE_MINE_QUEST="''${PAYOUT_GAME_DOGE_MINE_QUEST:-D9ArHWcegdLwLq6wtDDhwsR6BHZNr6rkEu}"
    export DOGE_AMOUNT_TO_PLAY="''${DOGE_AMOUNT_TO_PLAY:-1}"
    export MINUTES_PER_PAYMENT="''${MINUTES_PER_PAYMENT:-3}"
    export DOGE_NETWORK="''${DOGE_NETWORK:-mainnet}"
    export USE_MEMETRACKER="''${USE_MEMETRACKER:-}"
    export MEMETRACKER_BASE_URL="''${MEMETRACKER_BASE_URL:-}"
    export ARCADE_BIND_IP="''${DBX_PUP_IP:-0.0.0.0}"
    export ARCADE_PORT="8099"
    export ARCADE_STORAGE="/storage/arcade"
    export ARCADE_STATIC_DIR=${./static}
    export ARCADE_P2P_LOG="''${ARCADE_P2P_LOG:-1}"
    export ARCADE_ENABLE_PAYOUTS="''${ARCADE_ENABLE_PAYOUTS:-}"
    export ARCADE_PAYOUT_BACKEND="''${ARCADE_PAYOUT_BACKEND:-rpc}"
    export ARCADE_LIBDOGECOIN_HELPER="''${ARCADE_LIBDOGECOIN_HELPER:-}"
    export DOGE_RPC_URL="''${DOGE_RPC_URL:-http://127.0.0.1:22555}"
    export DOGE_RPC_USER="''${DOGE_RPC_USER:-}"
    export DOGE_RPC_PASS="''${DOGE_RPC_PASS:-}"
    export ARCADE_GIGAWALLET_ADMIN_URL="''${ARCADE_GIGAWALLET_ADMIN_URL:-}"
    export ARCADE_GIGAWALLET_INVOICE_GAMES="''${ARCADE_GIGAWALLET_INVOICE_GAMES:-}"
    export ARCADE_GIGAWALLET_ACCOUNT_DOGE_MINE_QUEST="''${ARCADE_GIGAWALLET_ACCOUNT_DOGE_MINE_QUEST:-}"
    export ARCADE_GIGAWALLET_ACCOUNT_ID="''${ARCADE_GIGAWALLET_ACCOUNT_ID:-}"
    export ARCADE_GIGAWALLET_MAX_ADDRESSES_PER_GAME="''${ARCADE_GIGAWALLET_MAX_ADDRESSES_PER_GAME:-}"

    mkdir -p "$ARCADE_STORAGE"
    cp -f ${./service/server.py} "$ARCADE_STORAGE/server.py"

    exec ${pkgs.python3}/bin/python "$ARCADE_STORAGE/server.py"
  '';
in
{
  arcade = arcadeService;
}
