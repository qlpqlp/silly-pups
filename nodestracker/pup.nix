# NodesTracker PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("nodestracker").
#
# Builds from https://github.com/qlpqlp/nodestracker via fetchurl (flat tarball hash).
# Keep tarball hash in sync with the pin, then recompute manifest.json nixFileSha256
# (LF SHA-256 of this file).
{ pkgs ? import <nixpkgs> {} }:

let
  # Upstream release V0.01 → ba53551c66e07013cd1a7ca60da64a730980c01d
  tag = "V0.01";

  tarball = pkgs.fetchurl {
    url = "https://github.com/qlpqlp/nodestracker/archive/refs/tags/${tag}.tar.gz";
    hash = "sha256-uas9kykyQcGqheht3KIqITTiiDgW/X53z9L4V26umLI=";
  };

  src = pkgs.runCommand "nodestracker-${tag}-src" {
    nativeBuildInputs = [ pkgs.gnutar pkgs.gzip ];
  } ''
    mkdir -p $out
    tar -xzf ${tarball} --strip-components=1 -C $out
    # Align go directive with DogeBox toolchains (upstream may be older/newer).
    sed -i 's/^go .*/go 1.24/' $out/go.mod
  '';

  nodestracker_bin = pkgs.buildGoModule {
    pname = "nodestracker";
    version = "0.0.1";
    inherit src;
    vendorHash = null;
    go = pkgs.go_1_24;

    nativeBuildInputs = [ pkgs.removeReferencesTo ];

    # No third-party modules; strip Go store refs for the pup closure.
    buildPhase = ''
      export GOCACHE=$(pwd)/.gocache
      export GO111MODULE=on
      go build -trimpath -ldflags="-s -w" -o nodestracker .
    '';

    installPhase = ''
      mkdir -p $out/bin
      cp nodestracker $out/bin/
      remove-references-to -t ${pkgs.go_1_24} $out/bin/nodestracker
    '';
  };

  nodestracker = pkgs.writeShellScriptBin "run.sh" ''
    set -e

    PUBLIC_PORT="''${PUBLIC_PORT:-6969}"
    CHECK_INTERVAL="''${CHECK_INTERVAL_MINUTES:-60}"
    DISCOVERY_MINUTES="''${DISCOVERY_MINUTES:-15}"
    STALE_DAYS="''${STALE_DAYS:-7}"
    USER_TOKEN="''${USER_TOKEN:-}"
    ADMIN_TOKEN="''${ADMIN_TOKEN:-}"
    HIDE_RAW="''${HIDE_ADDRESSES:-0}"
    SEEDS_RAW="''${DNS_SEEDS:-}"

    STORAGE_DIR="/storage/nodestracker"
    mkdir -p "$STORAGE_DIR"

    HIDE_JSON="false"
    case "$HIDE_RAW" in 1|true|TRUE|yes|YES) HIDE_JSON="true" ;; esac

    if ! printf '%s' "$CHECK_INTERVAL" | grep -Eq '^[0-9]+$' || [ "$CHECK_INTERVAL" -lt 1 ]; then
      CHECK_INTERVAL="60"
    fi
    if ! printf '%s' "$DISCOVERY_MINUTES" | grep -Eq '^[0-9]+$' || [ "$DISCOVERY_MINUTES" -lt 1 ]; then
      DISCOVERY_MINUTES="15"
    fi
    if ! printf '%s' "$STALE_DAYS" | grep -Eq '^[0-9]+$' || [ "$STALE_DAYS" -lt 1 ]; then
      STALE_DAYS="7"
    fi

    if [ ! -f "$STORAGE_DIR/settings.json" ]; then
      _json_escape() {
        printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
      }
      USER_TOKEN_JSON=$(_json_escape "$USER_TOKEN")
      ADMIN_TOKEN_JSON=$(_json_escape "$ADMIN_TOKEN")

      SEEDS_JSON='["seed.dogecoin.org","seed.multidoge.org","seed2.multidoge.org"]'
      _seeds_flat=$(printf '%s' "$SEEDS_RAW" | tr '\n' ',')
      if [ -n "$(echo "$_seeds_flat" | tr -d '[:space:],')" ]; then
        SEEDS_JSON="["
        _first=1
        _oifs="$IFS"
        IFS=','
        for _part in $_seeds_flat; do
          IFS="$_oifs"
          _part=$(echo "$_part" | xargs)
          IFS=','
          [ -z "$_part" ] && continue
          if [ "$_first" -eq 0 ]; then SEEDS_JSON="$SEEDS_JSON,"; fi
          _first=0
          SEEDS_JSON="$SEEDS_JSON\"$(_json_escape "$_part")\""
        done
        IFS="$_oifs"
        SEEDS_JSON="$SEEDS_JSON]"
      fi

      printf '%s\n' "{" \
        "  \"listen\": \"0.0.0.0:$PUBLIC_PORT\"," \
        "  \"check_interval_minutes\": $CHECK_INTERVAL," \
        "  \"discovery_minutes\": $DISCOVERY_MINUTES," \
        "  \"stale_days\": $STALE_DAYS," \
        "  \"user_token\": \"$USER_TOKEN_JSON\"," \
        "  \"admin_token\": \"$ADMIN_TOKEN_JSON\"," \
        "  \"hide_addresses\": $HIDE_JSON," \
        "  \"seeds\": $SEEDS_JSON" \
        "}" > "$STORAGE_DIR/settings.json"
    fi

    exec ${nodestracker_bin}/bin/nodestracker \
      -data "$STORAGE_DIR" \
      -listen "0.0.0.0:$PUBLIC_PORT"
  '';
in
{
  nodestracker = nodestracker;
}
