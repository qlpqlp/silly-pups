{ pkgs ? import <nixpkgs> {} }:

let
  gigawallet_bin = pkgs.buildGoModule {
  pname = "gigawallet";
  version = "1.0.1";

  go = pkgs.go_1_24;

  src = pkgs.fetchgit {
    url = "https://github.com/dogecoinfoundation/gigawallet.git";
    rev = "refs/tags/v1.0.1";
    hash = "sha256-PIKb4WnhJVE4Cj/UqAxeB8C/7An+UvpEemubziz4YNk=";
  };

  vendorHash = "sha256-mW5SStSabjWIlLWarI0OfyCTRWRQnEbk2BXabJCJ2h4";

  nativeBuildInputs = [
    pkgs.pkg-config
  ];

  buildInputs = [
    pkgs.zeromq
  ];

  # This is needed because gigawallet depends on things that have native, vendored header files.
  proxyVendor = true;

  buildPhase = ''
    go build -tags libdogecoin -o gigawallet ./cmd/gigawallet
  '';

  installPhase = ''
    mkdir -p $out/bin
    cp gigawallet $out/bin/
  '';
};

  # Optional PostgreSQL service
  postgresSetup = pkgs.writeScriptBin "postgres-setup.sh" ''
#!${pkgs.stdenv.shell}
set -e

POSTGRES_HOST="''${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="''${POSTGRES_PORT:-5432}"
POSTGRES_DB="''${POSTGRES_DB:-gigawallet}"
POSTGRES_USER="''${POSTGRES_USER:-gigawallet}"
POSTGRES_PASSWORD="''${POSTGRES_PASSWORD:-changeme}"

# Initialize PostgreSQL data directory
if [ ! -d "/storage/postgres" ]; then
  mkdir -p /storage/postgres
  ${pkgs.postgresql}/bin/initdb -D /storage/postgres --username=postgres --pwprompt=no
fi

# Start PostgreSQL
${pkgs.postgresql}/bin/postgres -D /storage/postgres -p $POSTGRES_PORT -k /storage/postgres 2>&1 &

# Wait for PostgreSQL to be ready
sleep 5

# Create database and user if not exists
${pkgs.postgresql}/bin/psql -h /storage/postgres -U postgres -c "CREATE USER $POSTGRES_USER WITH PASSWORD '$POSTGRES_PASSWORD';" || true
${pkgs.postgresql}/bin/psql -h /storage/postgres -U postgres -c "CREATE DATABASE $POSTGRES_DB OWNER $POSTGRES_USER;" || true
${pkgs.postgresql}/bin/psql -h /storage/postgres -U postgres -c "ALTER USER $POSTGRES_USER CREATEDB;" || true

# Keep service running
wait
  '';

  gigawallet = pkgs.writeScriptBin "run.sh" ''
#!${pkgs.stdenv.shell}

if [ ! -d "/storage/.gigawallet" ]; then
  mkdir /storage/.gigawallet
fi

ADMIN_PORT="''${ADMIN_PORT:-8081}"
PUBLIC_PORT="''${PUBLIC_PORT:-8082}"
PUBLIC_API_ROOT_URL="''${PUBLIC_API_ROOT_URL:-http://localhost:8082}"
ENABLE_POSTGRESQL="''${ENABLE_POSTGRESQL:-false}"
DB_FILE="''${DB_FILE:-/storage/gigawallet.db}"
POSTGRES_HOST="''${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="''${POSTGRES_PORT:-5432}"
POSTGRES_DB="''${POSTGRES_DB:-gigawallet}"
POSTGRES_USER="''${POSTGRES_USER:-gigawallet}"
POSTGRES_PASSWORD="''${POSTGRES_PASSWORD:-changeme}"
NETWORK="''${NETWORK:-mainnet}"
CORE_RPC_USER="''${CORE_RPC_USER:-dogebox_core_pup_temporary_static_username}"
CORE_RPC_PASSWORD="''${CORE_RPC_PASSWORD:-dogebox_core_pup_temporary_static_password}"

# Set DB_TYPE based on ENABLE_POSTGRESQL toggle
if [ "$ENABLE_POSTGRESQL" = "true" ]; then
  DB_TYPE="postgresql"
else
  DB_TYPE="sqlite"
fi

# Start PostgreSQL if selected
if [ "$DB_TYPE" = "postgresql" ]; then
  echo "Starting PostgreSQL for GigaWallet..."
  mkdir -p /storage/postgres
  if [ ! -f /storage/postgres/PG_VERSION ]; then
    ${pkgs.postgresql}/bin/initdb -D /storage/postgres --username=postgres -A trust
  fi
  ${pkgs.postgresql}/bin/postgres -D /storage/postgres -p $POSTGRES_PORT -k /storage/postgres > /storage/.gigawallet/postgres.log 2>&1 &
  sleep 5
  # Create user and database
  ${pkgs.postgresql}/bin/psql -h /storage/postgres -U postgres -c "CREATE USER $POSTGRES_USER WITH PASSWORD '$POSTGRES_PASSWORD';" 2>/dev/null || true
  ${pkgs.postgresql}/bin/psql -h /storage/postgres -U postgres -c "CREATE DATABASE $POSTGRES_DB OWNER $POSTGRES_USER;" 2>/dev/null || true
fi

  cat <<EOF > /storage/.gigawallet/dogebox.toml
[WebAPI]
  adminbind = "$DBX_PUP_IP"
  adminport = "$ADMIN_PORT"
  pubbind = "$DBX_PUP_IP"
  pubport = "$PUBLIC_PORT"
  pubapirooturl = "$PUBLIC_API_ROOT_URL"

[Store]
DBFile = "$DB_FILE"
[Store.PostgreSQL]
Host = "$POSTGRES_HOST"
Port = $POSTGRES_PORT
Database = "$POSTGRES_DB"
User = "$POSTGRES_USER"
Password = "$POSTGRES_PASSWORD"
DBType = "$DB_TYPE"

[gigawallet]
  network = "$NETWORK"

[dogecoind.mainnet]
  host    = "$DBX_IFACE_CORE_ZMQ_HOST"
  zmqport = $DBX_IFACE_CORE_ZMQ_PORT
  rpchost = "$DBX_IFACE_CORE_RPC_HOST"
  rpcport = $DBX_IFACE_CORE_RPC_PORT
  rpcpass = "$CORE_RPC_PASSWORD"
  rpcuser = "$CORE_RPC_USER"
EOF

HOME=/storage GIGA_ENV=dogebox ${gigawallet_bin}/bin/gigawallet server
  '';
in
{
  inherit gigawallet;
}
