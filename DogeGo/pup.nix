# DogeGo PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("dogego").
#
# Builds the Go node from https://github.com/qlpqlp/dogego (modRoot = DogeGo).
# Configure network / wallet / RPC in the DogeGo web UI after start.
{ pkgs ? import <nixpkgs> {} }:

let
  dogego_bin = pkgs.buildGoModule {
    pname = "dogego";
    version = "0.1.0";

    # Matches other silly-pups Go pups (gigawallet, pq-wallet, …).
    go = pkgs.go_1_24;

    src = pkgs.fetchgit {
      url = "https://github.com/qlpqlp/dogego.git";
      # Pin a full commit SHA (not a moving tag).
      rev = "9d88c34dd3f8f64bc2c5c6afb58062b0da2adb5c";
      hash = "sha256-r1OzX4f9whHdDEruH0/n+yW7kydgGl5S2cAWwaK2xuE=";
    };

    modRoot = "DogeGo";
    vendorHash = "sha256-xwHNyDyPMEXSY7A71/t/mGdgtoXxibiHghu8OvfVOYI=";

    CGO_ENABLED = "0";
    doCheck = false;

    subPackages = [ "cmd/dogego" ];

    ldflags = [
      "-s"
      "-w"
    ];
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
