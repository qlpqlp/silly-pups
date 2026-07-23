# DogeGo PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("dogego").
#
# Starts the setup wizard (no -datadir / no seeded dogecoinconf.json).
# cwd is /storage/dogego so wizard default ./dogedata → /storage/dogego/dogedata.
#
# Plain HTTP: native DogeGo -notls + DOGEGO_NO_TLS (commit 02932c9+). Do NOT pin
# pre-notls revs (e.g. 9d88c34) — wizard defaults force webui_tls_local + CA install,
# and omitempty JSON makes the old setup UI treat "TLS off" as "TLS on" when saving.
#
# Avoid pkgs.buildGoModule: DogeBox nixpkgs sets env.CGO_ENABLED, and a
# top-level CGO_ENABLED = "0" (legacy/injected) makes evaluation fail with
# overlapping env vs derivation attributes.
#
# After first build, replace src.hash / goModules outputHash with nix "got:" values,
# then recompute manifest.json container.build.nixFileSha256 (LF SHA-256 of this file).
{ pkgs ? import <nixpkgs> {} }:

let
  src = pkgs.fetchgit {
    url = "https://github.com/qlpqlp/dogego.git";
    # Includes native -notls / DOGEGO_NO_TLS (02932c9) and later fixes.
    rev = "2eb7e69da8712ee40563d7541455681e35ffd2c7";
    # Bootstrap: first nix build fails and prints the correct sha256-...
    hash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
  };

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
    # Bootstrap: replace with got: from first failed build.
    outputHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
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
      cp dogego $out/bin/dogego
      runHook postInstall
    '';
  };

  dogego = pkgs.writeShellScriptBin "run.sh" ''
    set -euo pipefail

    WORKDIR="/storage/dogego"
    WEBUI_PORT="2013"
    BIND="''${DBX_PUP_IP:-0.0.0.0}"

    mkdir -p "$WORKDIR/dogedata" \
      "$WORKDIR/.config/DogeGo" \
      "$WORKDIR/.local/share" \
      "$WORKDIR/.cache"
    chmod -R u+rwX "$WORKDIR" || true

    # Drop stale conf from older pup builds that enabled local HTTPS / CA install.
    if [ -f "$WORKDIR/dogecoinconf.json" ]; then
      if [ ! -d "$WORKDIR/dogedata/mainnet" ] \
         && [ ! -d "$WORKDIR/dogedata/testnet" ] \
         && [ ! -d "$WORKDIR/mainnet" ] \
         && [ ! -d "$WORKDIR/testnet" ]; then
        rm -f "$WORKDIR/dogecoinconf.json"
        rm -f "$WORKDIR/.config/DogeGo/dogecoinconf.json"
      fi
    fi
    # If conf still has TLS on from a prior wizard save, strip it for this host.
    if [ -f "$WORKDIR/dogecoinconf.json" ] && command -v ${pkgs.python3}/bin/python3 >/dev/null 2>&1; then
      ${pkgs.python3}/bin/python3 - "$WORKDIR/dogecoinconf.json" <<'PY' || true
import json, sys
path = sys.argv[1]
try:
    with open(path, encoding="utf-8") as f:
        conf = json.load(f)
except Exception:
    raise SystemExit(0)
changed = False
for k, v in (
    ("webui_tls_local", False),
    ("rpc_tls_local", False),
    ("local_tls_trust_ca", False),
    ("no_tls", True),
):
    if conf.get(k) != v:
        conf[k] = v
        changed = True
for k in ("webui_tls_cert", "webui_tls_key", "rpc_tls_cert", "rpc_tls_key"):
    if conf.pop(k, None) is not None:
        changed = True
if changed:
    with open(path, "w", encoding="utf-8") as f:
        json.dump(conf, f, indent=2)
        f.write("\n")
    print("DogeGo pup: cleared local HTTPS flags in", path)
PY
    fi

    cd "$WORKDIR"

    echo "DogeGo pup: wizard HTTP webui=''${BIND}:''${WEBUI_PORT} workdir=$WORKDIR (native -notls)"
    exec ${pkgs.coreutils}/bin/env \
      HOME="$WORKDIR" \
      XDG_CONFIG_HOME="$WORKDIR/.config" \
      XDG_DATA_HOME="$WORKDIR/.local/share" \
      XDG_CACHE_HOME="$WORKDIR/.cache" \
      DOGEGO_NO_TLS=1 \
      DOGEGO_NOTLS=1 \
      ${dogego_bin}/bin/dogego node \
        -webui "''${BIND}:''${WEBUI_PORT}" \
        -nobrowser \
        -notls
  '';

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
