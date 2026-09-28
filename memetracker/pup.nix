# MemeTracker PUP for DogeBox / silly-pups.
# Service attr name MUST match manifest container.services[0].name ("memetracker").
#
# Source: GitHub archive via fetchurl (flat file hash — stable from Windows) then
# unpack. Vendor golang.org/x/crypto from the module proxy the same way (flat hash).
# Upstream go.mod may require a newer Go than DogeBox nixpkgs provides; we rewrite
# the go directive to 1.24 at unpack time so pkgs.go_1_24 can build.
# Keep tarball hashes in sync with the pin, then recompute manifest.json nixFileSha256
# (LF SHA-256 of this file).
{ pkgs ? import <nixpkgs> {} }:

let
  rev = "028741ea817db17a9cda6b7d19db2f0e0c5f5e57";
  shortRev = builtins.substring 0 7 rev;

  tarball = pkgs.fetchurl {
    # Upstream v0.0.10: SPV bloom confirms, UTXO rows, payer from_address.
    url = "https://github.com/qlpqlp/memetracker/archive/${rev}.tar.gz";
    hash = "sha256-NqyTVROlP3x+FyRHImoxwMZA/zKGr8ocwmnMNMBZdSI=";
  };

  # Only ripemd160 is imported; ship a minimal vendor tree for sandboxed builds.
  xcrypto = pkgs.fetchurl {
    url = "https://proxy.golang.org/golang.org/x/crypto/@v/v0.57.0.zip";
    hash = "sha256-hWyRa5Lx/FtTmDwE9iSfYy9WxH68ADu838UDUA37WLg=";
  };

  src = pkgs.runCommand "memetracker-${shortRev}-src" {
    nativeBuildInputs = [ pkgs.gnutar pkgs.gzip pkgs.unzip ];
  } ''
    mkdir -p $out
    tar -xzf ${tarball} --strip-components=1 -C $out

    # DogeBox Go is 1.24/1.25; upstream may declare go 1.26+.
    sed -i 's/^go .*/go 1.24/' $out/go.mod

    mkdir -p $out/vendor/golang.org/x/crypto
    unzip -q ${xcrypto} -d $TMPDIR/xcrypto
    cp -a $TMPDIR/xcrypto/golang.org/x/crypto@v0.57.0/ripemd160 \
      $out/vendor/golang.org/x/crypto/ripemd160

    cat > $out/vendor/modules.txt <<'EOF'
# golang.org/x/crypto v0.57.0
## explicit; go 1.24
golang.org/x/crypto/ripemd160
EOF
  '';

  memetracker_bin = pkgs.buildGoModule {
    pname = "memetracker";
    version = "0.0.16";
    inherit src;
    vendorHash = null;
    go = pkgs.go_1_24;

    nativeBuildInputs = [ pkgs.removeReferencesTo ];

    # Source already includes vendor/; strip Go store refs so the binary
    # is allowed in the pup closure (DogeBox disallows go toolchain refs).
    buildPhase = ''
      export GOCACHE=$(pwd)/.gocache
      go build -mod=vendor -trimpath -ldflags="-s -w" -o memetracker .
    '';

    installPhase = ''
      mkdir -p $out/bin
      cp memetracker $out/bin/
      remove-references-to -t ${pkgs.go_1_24} $out/bin/memetracker
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
    CONFIRM_MODE="''${CONFIRM_MODE:-msg_block}"
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

    case "$CONFIRM_MODE" in
      node_bloom|bloom|spv|bip37) CONFIRM_MODE="node_bloom" ;;
      *) CONFIRM_MODE="msg_block" ;;
    esac

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
        "  \"confirm_mode\": \"$CONFIRM_MODE\"," \
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
