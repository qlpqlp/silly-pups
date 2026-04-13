{ pkgs ? import <nixpkgs> {} }:

let
  libdogecoin = pkgs.callPackage ../pq-wallet/nix/libdogecoin.nix {};

  qe_bin = pkgs.buildGoModule {
    pname = "quantum-explorer";
    version = "0.1.0";
    src = ./service;
    vendorHash = null;
    go = pkgs.go_1_24;
  };

  quantum-explorer = pkgs.writeShellScriptBin "run.sh" ''
    set -e
    STORAGE_DIR="/storage/quantum-explorer"
    mkdir -p "$STORAGE_DIR"

    export PATH="${libdogecoin}/bin:${pkgs.coreutils}/bin:$PATH"
    export QE_STORAGE_DIR="$STORAGE_DIR"
    export PUBLIC_PORT="''${PUBLIC_PORT:-33666}"
    export QE_ADMIN_PORT="''${QE_ADMIN_PORT:-33667}"
    export NETWORK="''${NETWORK:-mainnet}"
    export QE_EXPLORER_TX_API="''${QE_EXPLORER_TX_API:-}"
    export QE_ADMIN_USER="''${QE_ADMIN_USER:-shibe}"
    export QE_ADMIN_PASS="''${QE_ADMIN_PASS:-suchpass}"
    export LIBDOGECOIN_SPVNODE="''${LIBDOGECOIN_SPVNODE:-${libdogecoin}/bin/spvnode}"

    exec ${qe_bin}/bin/quantum-explorer
  '';
in
{
  quantum-explorer = quantum-explorer;
}
