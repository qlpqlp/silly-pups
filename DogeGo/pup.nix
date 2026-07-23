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

    DATADIR="/storage/dogego"
    CONF="$DATADIR/dogecoinconf.json"
    WEBUI_PORT="2013"
    BIND="''${DBX_PUP_IP:-0.0.0.0}"

    mkdir -p "$DATADIR" \
      "$DATADIR/.config/DogeGo" \
      "$DATADIR/.local/share" \
      "$DATADIR/.cache"

    # Seed conf so LoadFirst finds ./dogecoinconf.json (SearchPaths) and never
    # calls PreferredSaveDir → mkdir $HOME/.config (HOME is /var/empty here).
    if [ ! -f "$CONF" ]; then
      cat > "$CONF" <<EOF
{
  "datadir": "$DATADIR",
  "webui": "$BIND:$WEBUI_PORT",
  "nobrowser": true,
  "network": "mainnet"
}
EOF
    fi

    cd "$DATADIR"

    echo "DogeGo pup: webui=''${BIND}:''${WEBUI_PORT} datadir=$DATADIR conf=$CONF home=$DATADIR"
    # env forces HOME/XDG/DOGECOINCONF onto the binary even if the unit resets them.
    exec ${pkgs.coreutils}/bin/env \
      HOME="$DATADIR" \
      XDG_CONFIG_HOME="$DATADIR/.config" \
      XDG_DATA_HOME="$DATADIR/.local/share" \
      XDG_CACHE_HOME="$DATADIR/.cache" \
      DOGECOINCONF="$CONF" \
      ${dogego_bin}/bin/dogego node \
        -datadir "$DATADIR" \
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
