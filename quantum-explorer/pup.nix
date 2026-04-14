{ pkgs ? import <nixpkgs> {} }:

let
  postgresql = pkgs.postgresql_16;

  libdogecoin = pkgs.stdenv.mkDerivation rec {
    pname = "libdogecoin-bins";
    version = "0.1.5-git-a120e03";

    src = pkgs.fetchFromGitHub {
      owner = "dogecoinfoundation";
      repo = "libdogecoin";
      rev = "a120e0377650f247398b8b76c5d74e5ed89ec437";
      hash = "sha256-O0Km5jlSFFtXfN6aO2FsPzQfdo7YvI7LwFy4l3ldXkc=";
    };

    nativeBuildInputs = [ pkgs.cmake pkgs.pkg-config pkgs.ninja ];
    buildInputs = [ pkgs.gmp pkgs.openssl pkgs.libevent ];
    strictDeps = true;

    postPatch = ''
      # Match pq-wallet fixes for libevent resolution + net link target.
      substituteInPlace CMakeLists.txt \
        --replace-fail 'FIND_LIBRARY(LIBEVENT NAMES event event_core event_extras event_pthreads HINTS "''${PROJECT_SOURCE_DIR}/src/libevent/build/lib/''${CMAKE_BUILD_TYPE}" REQUIRED)' \
                       'FIND_LIBRARY(LIBEVENT NAMES event event_core event_extras event_pthreads HINTS "''${PROJECT_SOURCE_DIR}/src/libevent/build/lib" "''${PROJECT_SOURCE_DIR}/src/libevent/build/lib/''${CMAKE_BUILD_TYPE}" REQUIRED)' \
        --replace-fail 'TARGET_LINK_LIBRARIES(''${LIBS} ''${LIBEVENT} ''${LIBEVENT_PTHREADS} tbs ncrypt crypt32)' \
                       'TARGET_LINK_LIBRARIES(''${LIBDOGECOIN_NAME} PUBLIC ''${LIBEVENT} ''${LIBEVENT_PTHREADS} tbs ncrypt crypt32)' \
        --replace-fail 'TARGET_LINK_LIBRARIES(''${LIBS} ''${LIBEVENT} ''${LIBEVENT_PTHREADS})' \
                       'TARGET_LINK_LIBRARIES(''${LIBDOGECOIN_NAME} PUBLIC ''${LIBEVENT} ''${LIBEVENT_PTHREADS})' \
        --replace-fail 'TARGET_LINK_LIBRARIES(such ''${LIBS} tbs ncrypt crypt32)' \
                       'TARGET_LINK_LIBRARIES(such ''${LIBDOGECOIN_NAME} tbs ncrypt crypt32)' \
        --replace-fail 'TARGET_LINK_LIBRARIES(such ''${LIBS})' \
                       'TARGET_LINK_LIBRARIES(such ''${LIBDOGECOIN_NAME})'
    '';

    cmakeFlags = [
      "-GNinja"
      "-DCMAKE_BUILD_TYPE=Release"
      "-DBUILD_TESTING=OFF"
      "-DUSE_LIBOQS=OFF"
      "-DUSE_TPM2=OFF"
      "-DWITH_BENCH=OFF"
    ];

    buildPhase = ''
      runHook preBuild
      cmake --build .
      runHook postBuild
    '';

    installPhase = ''
      runHook preInstall
      cmake --install . --prefix "$out"
      runHook postInstall
    '';
  };

  qe_bin = pkgs.buildGoModule {
    pname = "quantum-explorer";
    version = "0.1.0";
    src = ./service;
    vendorHash = null;
    go = pkgs.go_1_24;

    buildPhase = ''
      go build -trimpath -ldflags="-s -w" -o quantum-explorer .
    '';

    installPhase = ''
      mkdir -p $out/bin
      cp quantum-explorer $out/bin/
    '';
  };

  quantum-explorer = pkgs.writeShellScriptBin "run.sh" ''
    set -euo pipefail
    STORAGE_DIR="/storage/quantum-explorer"
    mkdir -p "$STORAGE_DIR"
    PGDATA="$STORAGE_DIR/pgdata"
    PGPORT="''${PGPORT:-55432}"

    export PATH="${postgresql}/bin:${libdogecoin}/bin:${pkgs.coreutils}/bin:${pkgs.gnugrep}/bin:$PATH"
    export QE_STORAGE_DIR="$STORAGE_DIR"
    export PUBLIC_PORT="''${PUBLIC_PORT:-33666}"
    export QE_ADMIN_PORT="''${QE_ADMIN_PORT:-33667}"
    export NETWORK="''${NETWORK:-mainnet}"
    export QE_EXPLORER_TX_API="''${QE_EXPLORER_TX_API:-}"
    export QE_ADMIN_USER="''${QE_ADMIN_USER:-shibe}"
    export QE_ADMIN_PASS="''${QE_ADMIN_PASS:-suchpass}"
    export QE_SPV_USE_CHECKPOINT="''${QE_SPV_USE_CHECKPOINT:-1}"
    export QE_MEMPOOL_AUTO_START="''${QE_MEMPOOL_AUTO_START:-0}"
    export QE_SPV_AUTO_START="''${QE_SPV_AUTO_START:-0}"
    export QE_LEGACY_REFRESH_LOOP="''${QE_LEGACY_REFRESH_LOOP:-0}"
    export LIBDOGECOIN_SPVNODE="''${LIBDOGECOIN_SPVNODE:-${libdogecoin}/bin/spvnode}"

    if [ -z "''${QE_POSTGRES_URL:-}" ]; then
      if [ ! -f "$PGDATA/PG_VERSION" ]; then
        initdb -D "$PGDATA" -U qeuser --locale=C -E UTF8 --auth-local=trust --auth-host=trust
      fi
      # Containers often have a tiny /dev/shm (e.g. 64MB); default SysV shared memory fails to start.
      if ! grep -q '^# quantum-explorer-tune' "$PGDATA/postgresql.conf" 2>/dev/null; then
        cat >> "$PGDATA/postgresql.conf" <<'EOF'
# quantum-explorer-tune (small /dev/shm in Docker / systemd-nspawn)
shared_memory_type = mmap
shared_buffers = 32MB
dynamic_shared_memory_type = posix
EOF
      fi
      cleanup() {
        if pg_ctl -D "$PGDATA" status >/dev/null 2>&1; then
          pg_ctl -D "$PGDATA" -m fast stop || true
        fi
      }
      trap cleanup EXIT INT TERM
      if ! pg_ctl -D "$PGDATA" status >/dev/null 2>&1; then
        if ! pg_ctl -D "$PGDATA" -l "$PGDATA/postgres.log" -o "-p $PGPORT -h 127.0.0.1" start; then
          echo "[quantum-explorer] postgres failed to start; tail of $PGDATA/postgres.log:" >&2
          tail -n 120 "$PGDATA/postgres.log" >&2 || true
          exit 1
        fi
        sleep 0.4
      fi
      for i in $(seq 1 80); do
        if pg_isready -h 127.0.0.1 -p "$PGPORT" >/dev/null 2>&1; then
          break
        fi
        sleep 0.2
      done
      if ! psql -h 127.0.0.1 -p "$PGPORT" -U qeuser -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='quantum_explorer'" | grep -q 1; then
        createdb -h 127.0.0.1 -p "$PGPORT" -U qeuser quantum_explorer || true
      fi
      export QE_POSTGRES_URL="postgres://qeuser@127.0.0.1:$PGPORT/quantum_explorer?sslmode=disable"
    fi

    ${qe_bin}/bin/quantum-explorer
    exit $?
  '';
in
{
  quantum-explorer = quantum-explorer;
}
