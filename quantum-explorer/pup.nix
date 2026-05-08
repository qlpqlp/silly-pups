{ pkgs ? import <nixpkgs> {} }:

let
  postgresql = pkgs.postgresql_16;
  libdogecoinWithOqs = pkgs.callPackage ./nix/libdogecoin.nix {};

  qe_bin = pkgs.buildGoModule {
    pname = "quantum-explorer";
    version = "0.1.53";
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
    # Same pattern as pq-wallet / https-proxy: optional env override, default under /storage/<pup>/
    STORAGE_DIR="''${QE_STORAGE_DIR:-/storage/quantum-explorer}"
    mkdir -p "$STORAGE_DIR"
    export QE_STORAGE_DIR="$STORAGE_DIR"
    PGDATA="$STORAGE_DIR/pgdata"
    # Lock + Unix sockets must not use /run/postgresql (missing in Dogebox); use writable path.
    SOCKET_DIR="$STORAGE_DIR/pg-socket"
    mkdir -p "$SOCKET_DIR"
    PGPORT="''${PGPORT:-55432}"

    export PATH="${libdogecoinWithOqs}/bin:${postgresql}/bin:${pkgs.coreutils}/bin:${pkgs.gnugrep}/bin:$PATH"
    export QE_SUCH_PATH="''${QE_SUCH_PATH:-${libdogecoinWithOqs}/bin/such}"
    export PUBLIC_PORT="''${PUBLIC_PORT:-33666}"
    export QE_ADMIN_PORT="''${QE_ADMIN_PORT:-33667}"
    export NETWORK="''${NETWORK:-mainnet}"
    export QE_EXPLORER_TX_API="''${QE_EXPLORER_TX_API:-}"
    export QE_ADMIN_USER="''${QE_ADMIN_USER:-shibe}"
    export QE_ADMIN_PASS="''${QE_ADMIN_PASS:-suchpass}"
    # Dependency-aware Core RPC defaults:
    # - when linked to CORE via interface "core-rpc", Dogebox injects DBX_IFACE_CORE_RPC_HOST/PORT
    # - if user did not set QE_CORE_RPC_URL, auto-compose it from dependency host/port
    # - credentials can still be overridden manually via QE_CORE_RPC_USER/QE_CORE_RPC_PASSWORD
    # - when available, also consume dependency-provided credential envs
    if [ -z "''${QE_CORE_RPC_URL:-}" ]; then
      if [ -n "''${DBX_IFACE_CORE_RPC_HOST:-}" ] && [ -n "''${DBX_IFACE_CORE_RPC_PORT:-}" ]; then
        export QE_CORE_RPC_URL="http://''${DBX_IFACE_CORE_RPC_HOST}:''${DBX_IFACE_CORE_RPC_PORT}"
      fi
    fi
    if [ -z "''${QE_CORE_RPC_USER:-}" ]; then
      if [ -n "''${DBX_IFACE_CORE_RPC_USER:-}" ]; then
        export QE_CORE_RPC_USER="$DBX_IFACE_CORE_RPC_USER"
      elif [ -n "''${DBX_IFACE_CORE_RPC_USERNAME:-}" ]; then
        export QE_CORE_RPC_USER="$DBX_IFACE_CORE_RPC_USERNAME"
      elif [ -n "''${CORE_RPC_USER:-}" ]; then
        export QE_CORE_RPC_USER="$CORE_RPC_USER"
      else
        export QE_CORE_RPC_USER="dogebox_core_pup_temporary_static_username"
      fi
    fi
    if [ -z "''${QE_CORE_RPC_PASSWORD:-}" ]; then
      if [ -n "''${DBX_IFACE_CORE_RPC_PASSWORD:-}" ]; then
        export QE_CORE_RPC_PASSWORD="$DBX_IFACE_CORE_RPC_PASSWORD"
      elif [ -n "''${DBX_IFACE_CORE_RPC_PASS:-}" ]; then
        export QE_CORE_RPC_PASSWORD="$DBX_IFACE_CORE_RPC_PASS"
      elif [ -n "''${CORE_RPC_PASSWORD:-}" ]; then
        export QE_CORE_RPC_PASSWORD="$CORE_RPC_PASSWORD"
      else
        export QE_CORE_RPC_PASSWORD="dogebox_core_pup_temporary_static_password"
      fi
    fi

    if [ -z "''${QE_POSTGRES_URL:-}" ]; then
      if [ ! -f "$PGDATA/PG_VERSION" ]; then
        initdb -D "$PGDATA" -U qeuser --locale=C -E UTF8 --auth-local=trust --auth-host=trust
      fi
      # Containers often have a tiny /dev/shm (e.g. 64MB); default SysV shared memory fails to start.
      # Unix sockets default to /run/postgresql (missing here); pass -k to postgres and mirror in postgresql.conf.
      if ! grep -q '^# quantum-explorer-tune' "$PGDATA/postgresql.conf" 2>/dev/null; then
        cat >> "$PGDATA/postgresql.conf" <<'EOF'
# quantum-explorer-tune (small /dev/shm in Docker / systemd-nspawn)
shared_memory_type = mmap
shared_buffers = 32MB
dynamic_shared_memory_type = posix
EOF
        printf "unix_socket_directories = '%s'\n" "''$SOCKET_DIR" >> "''$PGDATA/postgresql.conf"
      fi
      if ! grep -q '^unix_socket_directories' "$PGDATA/postgresql.conf" 2>/dev/null; then
        printf "\n# quantum-explorer socket path (upgrade)\nunix_socket_directories = '%s'\n" "''$SOCKET_DIR" >> "''$PGDATA/postgresql.conf"
      fi
      # One-time: cap memory-heavy planner defaults (parallelism, work_mem) on small boards / no swap.
      if ! grep -q '^# quantum-explorer-tune-memory' "$PGDATA/postgresql.conf" 2>/dev/null; then
        cat >> "$PGDATA/postgresql.conf" <<'EOF'
# quantum-explorer-tune-memory (embedded Postgres on low-RAM hosts; mitigates OOM from large sorts / parallel workers)
max_connections = 20
work_mem = 1MB
maintenance_work_mem = 32MB
autovacuum_work_mem = 32MB
max_parallel_workers_per_gather = 0
max_parallel_maintenance_workers = 0
max_worker_processes = 4
effective_cache_size = 128MB
temp_buffers = 2MB
autovacuum_max_workers = 1
jit = off
EOF
      fi
      cleanup() {
        if pg_ctl -D "$PGDATA" status >/dev/null 2>&1; then
          pg_ctl -D "$PGDATA" -m fast stop || true
        fi
      }
      trap cleanup EXIT INT TERM
      if ! pg_ctl -D "$PGDATA" status >/dev/null 2>&1; then
        if ! pg_ctl -D "$PGDATA" -l "$PGDATA/postgres.log" -o "-k ''$SOCKET_DIR -p ''$PGPORT -h 127.0.0.1" start; then
          echo "[quantum-explorer] postgres failed to start; tail of $PGDATA/postgres.log:" >&2
          tail -n 120 "$PGDATA/postgres.log" >&2 || true
          exit 1
        fi
        sleep 0.4
      fi
      for i in $(seq 1 150); do
        if pg_isready -h 127.0.0.1 -p "$PGPORT" >/dev/null 2>&1; then
          break
        fi
        sleep 0.25
      done
      if ! psql -h 127.0.0.1 -p "$PGPORT" -U qeuser -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='quantum_explorer'" | grep -q 1; then
        createdb -h 127.0.0.1 -p "$PGPORT" -U qeuser quantum_explorer || true
      fi
      export QE_POSTGRES_URL="postgres://qeuser@127.0.0.1:''$PGPORT/quantum_explorer?sslmode=disable"
    fi

    ${qe_bin}/bin/quantum-explorer
    exit $?
  '';
in
{
  quantum-explorer = quantum-explorer;
}






