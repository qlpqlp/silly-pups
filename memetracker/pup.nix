{ pkgs ? import <nixpkgs> {} }:

let
  memetracker_bin = pkgs.buildGoModule {
    pname = "memetracker";
    version = "0.0.2";
    src = ./service;
    vendorHash = null;
    go = pkgs.go_1_24;

    buildPhase = ''
      export GO111MODULE=off
      export GOCACHE=$(pwd)/.gocache
      go build -o memetracker main.go
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
        "  \"api_allowed_ips\": $IPS_JSON" \
        "}" > "$STORAGE_DIR/memetracker_config.json"
    fi

    exec ${memetracker_bin}/bin/memetracker
  '';
in
{
  memetracker = memetracker;
}
