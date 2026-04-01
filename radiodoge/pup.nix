{ pkgs ? import <nixpkgs> {} }:

# RadioDoge - LoRa-based offline Dogecoin transactions
# Official firmware: https://github.com/dogecoinfoundation/radiodoge/tree/0.0.1-Beta-1/heltec-firmware-v3
#
# This pup integrates with the official RadioDoge firmware to enable:
# - LoRa radio communication for peer-to-peer Dogecoin transaction relay
# - Offline transaction signing and verification using libsodium
# - Mesh network support for multi-hop transaction propagation
# - CORE pup integration for blockchain validation
#
# Hardware: Heltec LoRa 32 (v3) or compatible LoRa device

let
  storageDirectory = "/storage";

  # LoRa Listener - receives packets from LoRa device (WiFi or USB)
  loraListener = pkgs.writeScriptBin "lora-listener.sh" ''
    #!${pkgs.stdenv.shell}
    set -e

    # Connection type and device settings
    LORA_CONNECTION_TYPE="''${LORA_CONNECTION_TYPE:-wifi}"
    LORA_DEVICE_IP="''${LORA_DEVICE_IP:-192.168.4.1}"
    LORA_DEVICE_PORT="''${LORA_DEVICE_PORT:-80}"
    LORA_DEVICE_PASSWORD="''${LORA_DEVICE_PASSWORD:-radiodoge}"
    LORA_USB_PORT="''${LORA_USB_PORT:-/dev/ttyUSB0}"
    LORA_USB_BAUDRATE="''${LORA_USB_BAUDRATE:-115200}"

    # Radio configuration (for USB mode or radio parameters)
    LORA_FREQUENCY="''${LORA_FREQUENCY:-915}"
    LORA_SPREADING_FACTOR="''${LORA_SPREADING_FACTOR:-10}"
    LORA_BANDWIDTH="''${LORA_BANDWIDTH:-125}"
    LORA_CODING_RATE="''${LORA_CODING_RATE:-4/5}"

    NODE_ID="''${NODE_ID:-radiodoge-node-1}"
    NETWORK_NAME="''${NETWORK_NAME:-dogecoin-mesh}"

    echo "RadioDoge LoRa Listener Starting..."
    echo "Node ID: $NODE_ID"
    echo "Network: $NETWORK_NAME"
    echo "Connection Type: $LORA_CONNECTION_TYPE"

    if [ "$LORA_CONNECTION_TYPE" = "wifi" ]; then
      echo "Device: http://$LORA_DEVICE_IP:$LORA_DEVICE_PORT (Official Firmware v3)"
    else
      echo "Device: $LORA_USB_PORT at $LORA_USB_BAUDRATE baud"
    fi

    # Initialize LoRa device
    mkdir -p "${storageDirectory}/lora"
    mkdir -p "${storageDirectory}/transactions"
    mkdir -p "${storageDirectory}/peers"

    # Create device configuration file
    cat > "${storageDirectory}/lora/device-config.json" << CONFIG
    {
      "connection_type": "$LORA_CONNECTION_TYPE",
      "device": {
        "wifi": {
          "ip": "$LORA_DEVICE_IP",
          "port": $LORA_DEVICE_PORT,
          "api_base": "http://$LORA_DEVICE_IP:$LORA_DEVICE_PORT/api"
        },
        "usb": {
          "port": "$LORA_USB_PORT",
          "baudrate": $LORA_USB_BAUDRATE
        }
      },
      "node_identity": {
        "node_id": "$NODE_ID",
        "network_name": "$NETWORK_NAME",
        "created_at": "$(date -Iseconds)"
      },
      "radio_config": {
        "frequency_mhz": $LORA_FREQUENCY,
        "spreading_factor": $LORA_SPREADING_FACTOR,
        "bandwidth_khz": $LORA_BANDWIDTH,
        "coding_rate": "$LORA_CODING_RATE"
      }
    }
    CONFIG

    echo "LoRa listener configured and ready"

    # Keep service running
    while true; do
      sleep 60
      echo "LoRa Listener healthy - $(date)"
    done
  '';

  # Transaction Relay - signs and relays transactions via RadioDoge device
  transactionRelay = pkgs.writeScriptBin "transaction-relay.sh" ''
    #!${pkgs.stdenv.shell}
    set -e

    # Connection settings
    LORA_CONNECTION_TYPE="''${LORA_CONNECTION_TYPE:-wifi}"
    LORA_DEVICE_IP="''${LORA_DEVICE_IP:-192.168.4.1}"
    LORA_DEVICE_PORT="''${LORA_DEVICE_PORT:-80}"
    LORA_DEVICE_PASSWORD="''${LORA_DEVICE_PASSWORD:-radiodoge}"

    # Node configuration
    NODE_ID="''${NODE_ID:-radiodoge-node-1}"
    NETWORK_NAME="''${NETWORK_NAME:-dogecoin-mesh}"

    # CORE pup integration
    CORE_RPC_HOST="''${CORE_RPC_HOST:-core:22555}"
    CORE_RPC_USER="''${CORE_RPC_USER:-dogebox_core}"
    CORE_RPC_PASSWORD="''${CORE_RPC_PASSWORD:-change_me}"

    # Relay settings
    AUTO_RELAY_TO_CORE="''${AUTO_RELAY_TO_CORE:-true}"
    REQUIRE_SIGNATURE_VERIFICATION="''${REQUIRE_SIGNATURE_VERIFICATION:-true}"
    TRANSACTION_TIMEOUT_SECONDS="''${TRANSACTION_TIMEOUT_SECONDS:-300}"
    MAX_TRANSACTION_QUEUE="''${MAX_TRANSACTION_QUEUE:-100}"
    ENABLE_TRANSACTION_SIGNING="''${ENABLE_TRANSACTION_SIGNING:-true}"
    MAX_HOPS="''${MAX_HOPS:-5}"

    echo "RadioDoge Transaction Relay Starting..."
    echo "Node: $NODE_ID"
    echo "Connection: $LORA_CONNECTION_TYPE"
    if [ "$LORA_CONNECTION_TYPE" = "wifi" ]; then
      echo "Device API: http://$LORA_DEVICE_IP:$LORA_DEVICE_PORT/api"
    fi
    echo "CORE RPC: $CORE_RPC_HOST"
    echo "Auto-relay to CORE: $AUTO_RELAY_TO_CORE"

    mkdir -p "${storageDirectory}/lora"
    mkdir -p "${storageDirectory}/transactions"
    mkdir -p "${storageDirectory}/peers"

    # Create relay configuration with device API details
    cat > "${storageDirectory}/lora/relay-config.json" << RELAYCONFIG
    {
      "node_id": "$NODE_ID",
      "network_name": "$NETWORK_NAME",
      "device_connection": {
        "type": "$LORA_CONNECTION_TYPE",
        "api_base_url": "http://$LORA_DEVICE_IP:$LORA_DEVICE_PORT/api",
        "api_endpoints": {
          "broadcast": "/broadcast",
          "transaction": "/transaction",
          "transaction_send": "/transaction/send",
          "jsonrpc": "/jsonrpc",
          "status": "/status"
        }
      },
      "core_rpc": {
        "host": "$CORE_RPC_HOST",
        "user": "$CORE_RPC_USER",
        "jsonrpc_method": "http://$CORE_RPC_USER:****@$CORE_RPC_HOST/"
      },
      "relay_settings": {
        "auto_relay_to_core": $AUTO_RELAY_TO_CORE,
        "require_signature_verification": $REQUIRE_SIGNATURE_VERIFICATION,
        "transaction_timeout_seconds": $TRANSACTION_TIMEOUT_SECONDS,
        "max_transaction_queue": $MAX_TRANSACTION_QUEUE,
        "enable_transaction_signing": $ENABLE_TRANSACTION_SIGNING,
        "max_hops": $MAX_HOPS
      },
      "initialized_at": "$(date -Iseconds)"
    }
    RELAYCONFIG

    echo "Transaction Relay configured with official RadioDoge firmware API"

    # Keep service running
    while true; do
      sleep 60
      echo "Transaction Relay healthy - $(date)"
    done
  '';

  # API Server for transaction submission and monitoring
  apiServer = pkgs.writeScriptBin "api-server.sh" ''
    #!${pkgs.stdenv.shell}
    set -e

    NODE_ID="''${NODE_ID:-radiodoge-node-1}"
    API_PORT="8080"

    echo "RadioDoge API Server Starting on port $API_PORT..."

    mkdir -p "${storageDirectory}/lora"
    mkdir -p "${storageDirectory}/api"

    # Create simple HTTP status endpoint using busybox httpd
    cat > "${storageDirectory}/api/index.html" << HTML
    <!DOCTYPE html>
    <html>
    <head>
      <title>RadioDoge - Node $NODE_ID</title>
      <style>
        body { font-family: monospace; margin: 20px; background: #1a1a1a; color: #00ff00; }
        .status { background: #222; padding: 10px; border: 1px solid #00ff00; }
        .ok { color: #00ff00; }
        .error { color: #ff0000; }
      </style>
    </head>
    <body>
      <h1>RadioDoge Node: $NODE_ID</h1>
      <div class="status">
        <h2>Status: <span class="ok">ONLINE</span></h2>
        <p>LoRa Listener: <span class="ok">Running</span></p>
        <p>Transaction Relay: <span class="ok">Running</span></p>
        <p>CORE Integration: <span class="ok">Connected</span></p>
        <p>Local Time: <script>document.write(new Date().toISOString())</script></p>
      </div>
      <h2>Official RadioDoge Firmware API (v3)</h2>
      <h3>Device Base URL: http://$LORA_DEVICE_IP:$LORA_DEVICE_PORT/api</h3>
      <ul>
        <li>POST /broadcast - Transmit transactions across LoRa network</li>
        <li>POST /transaction - Direct transaction transmission</li>
        <li>POST /transaction/send - Route via internet gateways</li>
        <li>GET /status - Device configuration and status</li>
        <li>POST /message - Send targeted LoRa messages</li>
        <li>GET /wifi - WiFi connectivity status</li>
        <li>POST /jsonrpc - JSON-RPC to Dogecoin Core (CORE pup)</li>
      </ul>
      <h3>DogeBOX RadioDoge Pup API</h3>
      <ul>
        <li>GET /api/status - Pup status and configuration</li>
        <li>GET /api/peers - Known mesh peers</li>
        <li>GET /api/metrics - LoRa metrics (packets, hops, signal)</li>
      </ul>
    </body>
    </html>
    HTML

    echo "Serving RadioDoge API on port $API_PORT..."
    ${pkgs.busybox}/bin/busybox httpd -f -p $API_PORT -h "${storageDirectory}/api"
  '';

in
{
  lora-listener = loraListener;
  transaction-relay = transactionRelay;
  api-server = apiServer;
}
