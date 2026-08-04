# DogeOS Portal PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("dogeos").
#
# Live status dashboard for DogeOS Chikyu testnet using public RPC + Blockscout.
{ pkgs ? import <nixpkgs> {} }:

let
  dogeos_bin = pkgs.stdenv.mkDerivation {
    pname = "dogeos-portal";
    version = "0.1.0";
    src = ./service;
    nativeBuildInputs = [ pkgs.go_1_24 ];
    dontConfigure = true;
    buildPhase = ''
      runHook preBuild
      export GOCACHE=$TMPDIR/go-cache
      export GOPATH=$TMPDIR/go
      export GO111MODULE=off
      export CGO_ENABLED=0
      go build -trimpath -ldflags="-s -w" -o dogeos-portal .
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      mkdir -p $out/bin
      cp dogeos-portal $out/bin/dogeos-portal
      runHook postInstall
    '';
  };

  dogeos = pkgs.writeShellScriptBin "run.sh" ''
    set -euo pipefail
    BIND="''${DBX_PUP_IP:-0.0.0.0}"
    PORT="''${DOGEOS_PORT:-8091}"
    echo "DogeOS portal: http://''${BIND}:''${PORT}/"
    exec ${pkgs.coreutils}/bin/env \
      DOGEOS_BIND="$BIND" \
      DOGEOS_PORT="$PORT" \
      DOGEOS_RPC_URL="''${DOGEOS_RPC_URL:-https://rpc.testnet.dogeos.com/}" \
      DOGEOS_EXPLORER_URL="''${DOGEOS_EXPLORER_URL:-https://blockscout.testnet.dogeos.com}" \
      DOGEOS_BRIDGE_URL="''${DOGEOS_BRIDGE_URL:-https://portal.testnet.dogeos.com/bridge}" \
      DOGEOS_FAUCET_URL="''${DOGEOS_FAUCET_URL:-https://faucet.testnet.dogeos.com}" \
      DOGEOS_PORTAL_URL="''${DOGEOS_PORTAL_URL:-https://portal.testnet.dogeos.com}" \
      ${dogeos_bin}/bin/dogeos-portal
  '';
in
{
  inherit dogeos;
}
