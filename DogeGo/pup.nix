# DogeGo PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("dogego").
#
# Starts the setup wizard (no -datadir / no seeded dogecoinconf.json).
# Data lives under /storage/dogego; wizard default ./dogedata resolves there.
# -notls keeps the wizard on plain HTTP (DogeBox reverse-proxy has no TLS to the pup).
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

    nativeBuildInputs = [ pkgs.go_1_24 pkgs.python3 ];

    dontConfigure = true;

    # Upstream pin predates -notls; patch it in for DogeBox (plain HTTP behind proxy).
    postPatch = ''
      ${pkgs.python3}/bin/python3 <<'PY'
      from pathlib import Path
      p = Path("DogeGo/cmd/dogego/main.go")
      t = p.read_text()
      if 'notls :=' not in t:
          old = '\tnobrowser := fs.Bool("nobrowser", false, "do not open the dashboard in a browser automatically")\n'
          new = old + '\tnotls := fs.Bool("notls", false, "disable local HTTPS for web UI and setup wizard (plain HTTP; for reverse proxies)")\n'
          if old not in t:
              raise SystemExit("nobrowser flag anchor not found for -notls patch")
          t = t.replace(old, new, 1)
      inject = """\t\tdesktop.ApplyWizardDefaults(&seed)
\t\tif *notls {
\t\t\tseed.WebUITLSLocal = false
\t\t\tseed.RpcTLSLocal = false
\t\t\tseed.LocalTLSTrustCA = false
\t\t}
"""
      if "if *notls {" not in t:
          anchor = "\t\tdesktop.ApplyWizardDefaults(&seed)\n"
          if anchor not in t:
              raise SystemExit("ApplyWizardDefaults anchor not found for -notls patch")
          t = t.replace(anchor, inject, 1)
      # Also honor -notls when a conf already has datadir (skip wizard path).
      merge_anchor = "\tif merged.DataDir != \"\" {\n\t\tabs, err := config.ResolveDataDir(merged.DataDir)\n"
      merge_inject = """\tif *notls {
\t\tmerged.WebUITLSLocal = false
\t\tmerged.RpcTLSLocal = false
\t\tmerged.LocalTLSTrustCA = false
\t}
\tif merged.DataDir != \"\" {
\t\tabs, err := config.ResolveDataDir(merged.DataDir)
"""
      if "merged.WebUITLSLocal = false" not in t:
          if merge_anchor not in t:
              raise SystemExit("ResolveDataDir anchor not found for -notls patch")
          t = t.replace(merge_anchor, merge_inject, 1)
      p.write_text(t)
      print("patched -notls into", p)
      PY
    '';

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

    # Writable pup storage (wizard default ./dogedata resolves under cwd).
    WORKDIR="/storage/dogego"
    WEBUI_PORT="2013"
    BIND="''${DBX_PUP_IP:-0.0.0.0}"

    mkdir -p "$WORKDIR" \
      "$WORKDIR/dogedata" \
      "$WORKDIR/.config/DogeGo" \
      "$WORKDIR/.local/share" \
      "$WORKDIR/.cache"
    chmod -R u+rwX "$WORKDIR" || true

    # Older pup builds seeded dogecoinconf.json which skips the wizard. If the
    # user has not created chain data yet, drop that bootstrap file.
    if [ -f "$WORKDIR/dogecoinconf.json" ]; then
      if [ ! -d "$WORKDIR/dogedata/mainnet" ] \
         && [ ! -d "$WORKDIR/dogedata/testnet" ] \
         && [ ! -d "$WORKDIR/mainnet" ] \
         && [ ! -d "$WORKDIR/testnet" ]; then
        rm -f "$WORKDIR/dogecoinconf.json"
        rm -f "$WORKDIR/.config/DogeGo/dogecoinconf.json"
      fi
    fi

    cd "$WORKDIR"

    echo "DogeGo pup: wizard mode webui=''${BIND}:''${WEBUI_PORT} workdir=$WORKDIR (use ./dogedata in the wizard)"
    exec ${pkgs.coreutils}/bin/env \
      HOME="$WORKDIR" \
      XDG_CONFIG_HOME="$WORKDIR/.config" \
      XDG_DATA_HOME="$WORKDIR/.local/share" \
      XDG_CACHE_HOME="$WORKDIR/.cache" \
      ${dogego_bin}/bin/dogego node \
        -webui "''${BIND}:''${WEBUI_PORT}" \
        -nobrowser \
        -notls
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
