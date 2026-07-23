# DogeGo PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("dogego").
#
# Builds from https://github.com/qlpqlp/dogego (modRoot = DogeGo).
# Configure network / wallet / RPC in the DogeGo web UI after start.
#
# Avoid pkgs.buildGoModule: DogeBox nixpkgs sets env.CGO_ENABLED, and a
# top-level CGO_ENABLED = "0" (legacy/injected) makes evaluation fail with
# overlapping env vs derivation attributes.
{ pkgs ? import <nixpkgs> {} }:

let
  src = pkgs.fetchgit {
    url = "https://github.com/qlpqlp/dogego.git";
    rev = "9d88c34dd3f8f64bc2c5c6afb58062b0da2adb5c";
    hash = "sha256-r1OzX4f9whHdDEruH0/n+yW7kydgGl5S2cAWwaK2xuE=";
  };

  # Fixed-output vendor dir (same hash as former buildGoModule vendorHash).
  goModules = pkgs.stdenv.mkDerivation {
    name = "dogego-go-modules";
    inherit src;
    nativeBuildInputs = [
      pkgs.go_1_24
      pkgs.git
      pkgs.cacert
    ];
    impureEnvVars = pkgs.lib.fetchers.proxyImpureEnvVars ++ [
      "GIT_PROXY_COMMAND"
      "SOCKS_SERVER"
      "GOPROXY"
    ];
    configurePhase = ''
      runHook preConfigure
      export GOCACHE=$TMPDIR/go-cache
      export GOPATH=$TMPDIR/go
      cd DogeGo
      runHook postConfigure
    '';
    buildPhase = ''
      runHook preBuild
      export GIT_SSL_CAINFO=$NIX_SSL_CERT_FILE
      go mod vendor
      mkdir -p vendor
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      cp -r --reflink=auto vendor $out
      runHook postInstall
    '';
    dontFixup = true;
    outputHashMode = "recursive";
    outputHash = "sha256-xwHNyDyPMEXSY7A71/t/mGdgtoXxibiHghu8OvfVOYI=";
  };

  dogego_bin = pkgs.stdenv.mkDerivation {
    pname = "dogego";
    version = "0.1.0";
    inherit src;

    nativeBuildInputs = [ pkgs.go_1_24 ];

    dontConfigure = true;

    buildPhase = ''
      runHook preBuild
      export GOCACHE=$TMPDIR/go-cache
      export GOPATH=$TMPDIR/go
      export GO111MODULE=on
      export GOTOOLCHAIN=local
      export CGO_ENABLED=0
      export GOPROXY=off
      export GOSUMDB=off
      cd DogeGo
      rm -rf vendor
      cp -r --reflink=auto ${goModules} vendor
      go build -mod=vendor -trimpath -ldflags="-s -w" -o dogego ./cmd/dogego
      runHook postBuild
    '';

    installPhase = ''
      runHook preInstall
      mkdir -p $out/bin
      # buildPhase cds into DogeGo and leaves cwd there
      cp dogego $out/bin/dogego
      runHook postInstall
    '';
  };

  dogego = pkgs.writeShellScriptBin "run.sh" ''
    set -euo pipefail

    # Writable home for the pup user (passwd HOME is /var/empty).
    # No dogecoinconf.json is seeded — the WebUI setup wizard creates it.
    STOREROOT="/storage/dogego"
    WEBUI_PORT="2013"
    BIND="''${DBX_PUP_IP:-0.0.0.0}"

    mkdir -p "$STOREROOT/.config/DogeGo" \
      "$STOREROOT/.local/share" \
      "$STOREROOT/.cache"

    echo "DogeGo pup: webui=''${BIND}:''${WEBUI_PORT} (setup wizard; no pre-seeded conf)"
    exec ${pkgs.coreutils}/bin/env \
      HOME="$STOREROOT" \
      XDG_CONFIG_HOME="$STOREROOT/.config" \
      XDG_DATA_HOME="$STOREROOT/.local/share" \
      XDG_CACHE_HOME="$STOREROOT/.cache" \
      ${dogego_bin}/bin/dogego node \
        -webui "''${BIND}:''${WEBUI_PORT}" \
        -nobrowser
  '';

  # Dogebox metrics sidecar (same /dbx/metrics contract as CORE monitor).
  monitor = pkgs.stdenv.mkDerivation {
    pname = "dogego-monitor";
    version = "0.1.0";
    src = ./monitor;
    nativeBuildInputs = [ pkgs.go_1_24 ];
    dontConfigure = true;
    buildPhase = ''
      export GOCACHE=$TMPDIR/go-cache
      export GOPATH=$TMPDIR/go
      export GO111MODULE=off
      export CGO_ENABLED=0
      go build -trimpath -ldflags="-s -w" -o monitor monitor.go
    '';
    installPhase = ''
      mkdir -p $out/bin
      cp monitor $out/bin/monitor
    '';
  };
in
{
  inherit dogego monitor;
}
