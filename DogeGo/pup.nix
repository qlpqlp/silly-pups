# DogeGo PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("dogego").
#
# Builds from https://github.com/qlpqlp/dogego (modRoot = DogeGo).
# Configure network / wallet / RPC in the DogeGo web UI after start.
#
# Use a custom buildPhase (same as https-proxy / pq-wallet / quantum-explorer).
# Do NOT set top-level CGO_ENABLED: DogeBox nixpkgs puts CGO_ENABLED in `env`,
# and overlapping drv-arg + env keys fails evaluation.
{ pkgs ? import <nixpkgs> {} }:

let
  dogego_bin = pkgs.buildGoModule {
    pname = "dogego";
    version = "0.1.0";

    go = pkgs.go_1_24;

    src = pkgs.fetchgit {
      url = "https://github.com/qlpqlp/dogego.git";
      rev = "9d88c34dd3f8f64bc2c5c6afb58062b0da2adb5c";
      hash = "sha256-r1OzX4f9whHdDEruH0/n+yW7kydgGl5S2cAWwaK2xuE=";
    };

    modRoot = "DogeGo";
    vendorHash = "sha256-xwHNyDyPMEXSY7A71/t/mGdgtoXxibiHghu8OvfVOYI=";

    doCheck = false;

    buildPhase = ''
      export GOCACHE=$TMPDIR/go-cache
      export CGO_ENABLED=0
      go build -trimpath -ldflags="-s -w" -o dogego ./cmd/dogego
    '';

    installPhase = ''
      mkdir -p $out/bin
      cp dogego $out/bin/dogego
    '';
  };

  dogego = pkgs.writeShellScriptBin "run.sh" ''
    set -euo pipefail

    DATADIR="/storage/dogego"
    WEBUI_PORT="2013"
    BIND="''${DBX_PUP_IP:-0.0.0.0}"

    mkdir -p "$DATADIR"

    echo "DogeGo pup: webui=''${BIND}:''${WEBUI_PORT} datadir=$DATADIR (configure in the DogeGo web UI)"
    exec ${dogego_bin}/bin/dogego node \
      -datadir "$DATADIR" \
      -webui "''${BIND}:''${WEBUI_PORT}" \
      -nobrowser
  '';
in
{
  inherit dogego;
}
