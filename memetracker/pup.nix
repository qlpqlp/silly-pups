# MemeTracker PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("memetracker").
#
# Source: GitHub archive via fetchurl (flat file hash — stable from Windows) then
# unpack. Avoids recursive NAR mismatches from fetchgit/fetchFromGitHub on DogeBox.
# Keep tarball hash in sync with the pin, then recompute manifest.json nixFileSha256
# (LF SHA-256 of this file).
{ pkgs ? import <nixpkgs> {} }:

let
  rev = "e29ced781881b1c6f9867efe2492ac49cd465ba4";
  shortRev = builtins.substring 0 7 rev;

  tarball = pkgs.fetchurl {
    # Upstream v0.0.6: split user/admin tokens + confirmation UX polish.
    url = "https://github.com/qlpqlp/memetracker/archive/${rev}.tar.gz";
    hash = "sha256-BiQyFGgqB9783MbIKotuG6ONOZE9dzadJpn8+a6XQlI=";
  };

  src = pkgs.runCommand "memetracker-${shortRev}-src" {
    nativeBuildInputs = [ pkgs.gnutar pkgs.gzip ];
  } ''
    mkdir -p $out
    tar -xzf ${tarball} --strip-components=1 -C $out
  '';

  memetracker_bin = pkgs.buildGoModule {
    pname = "memetracker";
    version = "0.0.10";
    inherit src;
    vendorHash = null;
    go = pkgs.go_1_24;

    # No third-party modules; build the whole module (main.go + headers.go + embed).
    buildPhase = ''
      export GOCACHE=$(pwd)/.gocache
      go build -o memetracker .
    '';

    installPhase = ''
      mkdir -p $out/bin
      cp memetracker $out/bin/
    '';
  };

  memetracker = pkgs.writeShellScriptBin "run.sh" ''
    set -e

    PUBLIC_PORT="''${PUBLIC_PORT:-33555}"
    NETWORK="''${NETWORK:-mainnet}"
    LIST_LIMIT="''${LIST_LIMIT:-10}"
    RETENTION_DAYS="''${RETENTION_DAYS:-7}"

    P2P_HOST="''${P2P_HOST:-}"
    P2P_PORT="''${P2P_PORT:-22556}"
    P2P_LOG="''${P2P_LOG:-1}"
    P2P_PARALLEL="''${P2P_PARALLEL:-3}"
    API_ALLOWED_IPS="''${API_ALLOWED_IPS:-}"
    TRUST_XFF_RAW="''${MTR_TRUST_XFF:-0}"
    USER_TOKEN="''${MTR_USER_TOKEN:-}"
    ADMIN_TOKEN="''${MTR_ADMIN_TOKEN:-}"

    STORAGE_DIR="/storage/memetracker"
    mkdir -p "$STORAGE_DIR/addresses"

    export MTR_HTTP_BIND="0.0.0.0"
    export MTR_HTTP_PORT="$PUBLIC_PORT"
    export MTR_NETWORK="$NETWORK"
    export MTR_LIST_LIMIT="$LIST_LIMIT"
    export MTR_RETENTION_DAYS="$RETENTION_DAYS"
    export MTR_P2P_HOST="$P2P_HOST"
    export MTR_P2P_PORT="$P2P_PORT"
    export MTR_P2P_LOG="$P2P_LOG"
    export MTR_P2P_PARALLEL="$P2P_PARALLEL"
    export MTR_STORAGE_DIR="$STORAGE_DIR"
    export MTR_CONFIG_PATH="$STORAGE_DIR/memetracker_config.json"
    export MTR_NO_BROWSER=1
    export MTR_USER_TOKEN="$USER_TOKEN"
    export MTR_ADMIN_TOKEN="$ADMIN_TOKEN"

    export MTR_TRUST_XFF="0"
    case "$TRUST_XFF_RAW" in 1|true|TRUE|yes|YES) export MTR_TRUST_XFF=1 ;; esac

    if [ ! -f "$STORAGE_DIR/memetracker_config.json" ]; then
      IPS_JSON="[]"
      _ips_flat=$(printf '%s' "$API_ALLOWED_IPS" | tr '\n' ',')
      if [ -n "$(echo "$_ips_flat" | tr -d '[:space:],')" ]; then
        IPS_JSON="["
        _first=1
        _oifs="$IFS"
        IFS=','
        for _part in $_ips_flat; do
          IFS="$_oifs"
          _part=$(echo "$_part" | xargs)
          IFS=','
          [ -z "$_part" ] && continue
          if [ "$_first" -eq 0 ]; then IPS_JSON="$IPS_JSON,"; fi
          _first=0
          IPS_JSON="$IPS_JSON\"$_part\""
        done
        IFS="$_oifs"
        IPS_JSON="$IPS_JSON]"
      fi
      _json_escape() {
        printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
      }
      USER_TOKEN_JSON=$(_json_escape "$USER_TOKEN")
      ADMIN_TOKEN_JSON=$(_json_escape "$ADMIN_TOKEN")
      printf '%s\n' "{" \
        "  \"http_port\": $PUBLIC_PORT," \
        "  \"http_bind\": \"0.0.0.0\"," \
        "  \"network\": \"$NETWORK\"," \
        "  \"storage_dir\": \"$STORAGE_DIR\"," \
        "  \"list_limit\": $LIST_LIMIT," \
        "  \"retention_days\": $RETENTION_DAYS," \
        "  \"p2p_host\": \"$P2P_HOST\"," \
        "  \"p2p_port\": $P2P_PORT," \
        "  \"p2p_parallel\": $P2P_PARALLEL," \
        "  \"p2p_log\": $P2P_LOG," \
        "  \"api_allowed_ips\": $IPS_JSON," \
        "  \"user_token\": \"$USER_TOKEN_JSON\"," \
        "  \"admin_token\": \"$ADMIN_TOKEN_JSON\"" \
        "}" > "$STORAGE_DIR/memetracker_config.json"
    fi

    exec ${memetracker_bin}/bin/memetracker
  '';
in
{
  memetracker = memetracker;
}
