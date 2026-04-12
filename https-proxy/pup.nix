# HTTPS reverse proxy for Dogebox — TLS (local SANs) + configurable upstream pups.
{ pkgs ? import <nixpkgs> {} }:

let
  proxy_bin = pkgs.buildGoModule {
    pname = "https-proxy";
    version = "0.1.6";
    src = ./service;
    vendorHash = null;
    go = pkgs.go_1_24;

    buildPhase = ''
      go build -trimpath -ldflags="-s -w" -o https-proxy .
    '';

    installPhase = ''
      mkdir -p $out/bin
      cp https-proxy $out/bin/
    '';
  };

  https-proxy = pkgs.writeShellScriptBin "run.sh" ''
    set -e
    PUBLIC_PORT="''${PUBLIC_PORT:-10000}"
    STORAGE="''${HTTPS_PROXY_STORAGE:-/storage/https-proxy}"
    mkdir -p "$STORAGE"

    export PUBLIC_PORT
    # Optional: TLS port when HTTPS is on (default PUBLIC_PORT+1). Second container expose must match.
    export HTTPS_PROXY_TLS_PORT="''${HTTPS_PROXY_TLS_PORT:-}"
    export HTTPS_PROXY_STORAGE="$STORAGE"
    export HTTPS_PROXY_ADMIN_TOKEN="''${HTTPS_PROXY_ADMIN_TOKEN:-DOGECOIN}"

    exec ${proxy_bin}/bin/https-proxy
  '';
in
{
  inherit https-proxy;
}
