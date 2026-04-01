{ pkgs ? import <nixpkgs> {} }:

let
  serviceDir = ./.;

  memetracker_bin = pkgs.buildGoModule {
    pname = "memetracker";
    version = "0.0.6";
    src = ./service;
    vendorHash = null;
    go = pkgs.go_1_24;

    buildPhase = ''
      export GO111MODULE=off
      export GOCACHE=$(pwd)/.gocache
      go build -o memetracker main.go
    '';

    installPhase = ''
      mkdir -p $out/bin
      cp memetracker $out/bin/
    '';
  };

  memetracker = pkgs.writeShellScriptBin "run.sh" ''
    set -e

    PUBLIC_PORT="''${PUBLIC_PORT:-33555}"
    NETWORK="''${NETWORK:-mainnet}"
    LIST_LIMIT="''${LIST_LIMIT:-10}"
    RETENTION_DAYS="''${RETENTION_DAYS:-7}"

    P2P_HOST="''${P2P_HOST:-}"
    P2P_PORT="''${P2P_PORT:-22556}"
    P2P_LOG="''${P2P_LOG:-1}"

    STORAGE_DIR="/storage/memetracker"
    mkdir -p "$STORAGE_DIR/addresses"

    export MTR_HTTP_BIND="''${DBX_PUP_IP:-0.0.0.0}"
    export MTR_HTTP_PORT="$PUBLIC_PORT"
    export MTR_NETWORK="$NETWORK"
    export MTR_LIST_LIMIT="$LIST_LIMIT"
    export MTR_RETENTION_DAYS="$RETENTION_DAYS"
    export MTR_P2P_HOST="$P2P_HOST"
    export MTR_P2P_PORT="$P2P_PORT"
    export MTR_P2P_LOG="$P2P_LOG"
    export MTR_P2P_PARALLEL="$P2P_PARALLEL"
    export MTR_STORAGE_DIR="$STORAGE_DIR"

    exec ${memetracker_bin}/bin/memetracker
  '';
in
{
  memetracker = memetracker;
}

