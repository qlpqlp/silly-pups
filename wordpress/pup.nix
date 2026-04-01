{ pkgs ? import <nixpkgs> {} }:

let
  storageDirectory = "/storage";

  wpVersion = "6.9.4";

  wordpressArchive = pkgs.fetchurl {
    url = "https://wordpress.org/wordpress-${wpVersion}.tar.gz";
    sha256 = "db610ad9f549e0abb5af3e3d5c6729c58d874439654cdf0afea425b2ea895f35";
  };

  # One process, strict order: Caddy (static) → MySQL → WordPress → plugins → activate → PHP-FPM → Caddy (full)
  startWordpress = pkgs.writeShellScriptBin "start-wordpress.sh" ''
    set -e

    WP_SITE_TITLE="''${WP_SITE_TITLE:-Dogecoin Store}"
    WP_ADMIN_USER="''${WP_ADMIN_USER:-shibe}"
    WP_ADMIN_PASSWORD="''${WP_ADMIN_PASSWORD:-suchpass}"
    WP_ADMIN_EMAIL="''${WP_ADMIN_EMAIL:-shibe@dogebox.local}"

    DOGECOIN_ENABLED="''${DOGECOIN_ENABLED:-true}"
    EASY_DOGECOIN_GATEWAY_PAYMENT_ADDRESS="''${EASY_DOGECOIN_GATEWAY_PAYMENT_ADDRESS:-DXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX}"
    DB_NAME="''${DB_NAME:-wordpress}"
    DB_USER="''${DB_USER:-wordpress}"
    DB_PASSWORD="''${DB_PASSWORD:-wordpress_pass}"
    # Use TCP to match mysqld --bind-address=127.0.0.1 (avoid localhost socket quirks)
    DB_HOST="''${DB_HOST:-127.0.0.1}"
    WP_DIR="${storageDirectory}/wordpress"
    CADDY_DIR="${storageDirectory}/caddy"
    PUBLIC_DIR="$CADDY_DIR/public"
    LOG="${storageDirectory}/wordpress-setup.log"

    log() { echo "$1" | tee -a "$LOG"; }

    mkdir -p "$CADDY_DIR" "$PUBLIC_DIR" "${storageDirectory}/php-fpm"
    # Writable config/data for Caddy (avoids errors writing to /var/empty when HOME is unset)
    export XDG_CONFIG_HOME="$CADDY_DIR/xdg-config"
    export XDG_DATA_HOME="$CADDY_DIR/xdg-data"
    mkdir -p "$XDG_CONFIG_HOME" "$XDG_DATA_HOME"

    # --- 1) Caddy: static "preparing" page only ---
    log "Phase 1: Starting Caddy with static preparing page..."
    {
      echo "<!DOCTYPE html>"
      echo "<html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">"
      echo "<title>Much preparing…</title>"
      echo "<link rel=\"preconnect\" href=\"https://fonts.googleapis.com\">"
      echo "<link rel=\"preconnect\" href=\"https://fonts.gstatic.com\" crossorigin>"
      echo "<link href=\"https://fonts.googleapis.com/css2?family=Comic+Neue:wght@400;700&display=swap\" rel=\"stylesheet\">"
      echo "<link href=\"https://fonts.googleapis.com/icon?family=Material+Icons\" rel=\"stylesheet\">"
      echo "<style>"
      echo "body{font-family:'Comic Neue',cursive,system-ui,sans-serif;display:flex;justify-content:center;align-items:center;"
      echo "min-height:100vh;margin:0;background:linear-gradient(165deg,#0073aa 0%,#00a0d2 42%,#23282d 100%);}"
      echo ".box{background:#f0f6fc;padding:2.5rem 2rem;border-radius:16px;box-shadow:0 12px 40px rgba(0,20,40,.35);"
      echo "text-align:center;max-width:30rem;border:3px solid #0073aa;}"
      echo ".material-icons.paw{font-size:4.5rem;line-height:1;color:#0073aa;display:block;margin:0 0 .25rem;"
      echo "filter:drop-shadow(0 2px 0 rgba(0,0,0,.08));}"
      echo "h1{margin:0 0 .35rem;font-size:1.65rem;font-weight:700;color:#23282d;}"
      echo ".tagline{font-size:1.05rem;color:#0073aa;margin:.5rem 0 .75rem;font-weight:700;}"
      echo "p{color:#3c434a;margin:.65rem 0;line-height:1.55;font-size:1rem;}"
      echo ".sub{font-size:.9rem;color:#646970;margin-top:1rem;}"
      echo ".spinner{width:44px;height:44px;margin:1rem auto;border:4px solid #c3c4c7;border-top-color:#00a0d2;border-radius:50%;"
      echo "animation:spin 1s linear infinite;}@keyframes spin{to{transform:rotate(360deg);}}"
      echo "</style></head><body><div class=\"box\">"
      echo "<span class=\"material-icons paw\" aria-hidden=\"true\">pets</span>"
      echo "<h1>Much preparing WordPress</h1>"
      echo "<p class=\"tagline\">Very store. Such Doge payment. Wow.</p>"
      echo "<div class=\"spinner\"></div>"
      echo "<p>Plz wait while MySQL, WordPress, and plugins get sorted. Many install. So patience.</p>"
      echo "<p class=\"sub\">This page auto-refreshes every few seconds.</p>"
      echo "<script>setTimeout(function(){location.reload();},4000);</script>"
      echo "</div></body></html>"
    } > "$PUBLIC_DIR/index.html"

    {
      echo ":8081 {"
      echo "  root * $PUBLIC_DIR"
      echo "  file_server"
      echo "}"
    } > "$CADDY_DIR/Caddyfile"

    ${pkgs.caddy}/bin/caddy run --config "$CADDY_DIR/Caddyfile" --adapter caddyfile &
    CADDY_PID=$!
    sleep 1

    # --- 2) MySQL: init, start, create DB/user ---
    log "Phase 2: MySQL — initialize and start..."
    if [ ! -d "${storageDirectory}/mysql" ]; then
      mkdir -p "${storageDirectory}/mysql"
      ${pkgs.mysql80}/bin/mysqld --initialize-insecure --datadir="${storageDirectory}/mysql" --user=mysql
    fi

    ${pkgs.mysql80}/bin/mysqld \
      --datadir="${storageDirectory}/mysql" \
      --socket="${storageDirectory}/mysql.sock" \
      --port=3306 \
      --bind-address=127.0.0.1 \
      --mysqlx=OFF \
      --user=mysql &

    export MYSQL_PWD=""
    for i in {1..60}; do
      if ${pkgs.mysql80}/bin/mysql -u root --socket="${storageDirectory}/mysql.sock" -e "SELECT 1" 2>/dev/null; then
        log "MySQL is accepting connections."
        break
      fi
      log "Waiting for MySQL ($i/60)..."
      sleep 1
    done

    ${pkgs.mysql80}/bin/mysql -u root --socket="${storageDirectory}/mysql.sock" \
      -e "CREATE DATABASE IF NOT EXISTS \`$DB_NAME\`;"
    ${pkgs.mysql80}/bin/mysql -u root --socket="${storageDirectory}/mysql.sock" \
      -e "CREATE USER IF NOT EXISTS '$DB_USER'@'localhost' IDENTIFIED BY '$DB_PASSWORD';" || true
    ${pkgs.mysql80}/bin/mysql -u root --socket="${storageDirectory}/mysql.sock" \
      -e "CREATE USER IF NOT EXISTS '$DB_USER'@'127.0.0.1' IDENTIFIED BY '$DB_PASSWORD';" || true
    ${pkgs.mysql80}/bin/mysql -u root --socket="${storageDirectory}/mysql.sock" \
      -e "GRANT ALL PRIVILEGES ON \`$DB_NAME\`.* TO '$DB_USER'@'localhost';"
    ${pkgs.mysql80}/bin/mysql -u root --socket="${storageDirectory}/mysql.sock" \
      -e "GRANT ALL PRIVILEGES ON \`$DB_NAME\`.* TO '$DB_USER'@'127.0.0.1';"
    ${pkgs.mysql80}/bin/mysql -u root --socket="${storageDirectory}/mysql.sock" \
      -e "FLUSH PRIVILEGES;"
    unset MYSQL_PWD

    for i in {1..30}; do
      if MYSQL_PWD="$DB_PASSWORD" ${pkgs.mysql80}/bin/mysql -u "$DB_USER" -h 127.0.0.1 -P 3306 "$DB_NAME" -e "SELECT 1" 2>/dev/null; then
        log "MySQL app user OK over TCP."
        break
      fi
      log "Waiting for MySQL TCP ($i/30)..."
      sleep 1
    done

    # --- 3) WordPress files + wp-config ---
    log "Phase 3: WordPress files and configuration..."
    if [ ! -f "$WP_DIR/wp-settings.php" ]; then
      log "Extracting WordPress..."
      rm -rf "$WP_DIR"
      mkdir -p "$WP_DIR"
      cd "$WP_DIR"
      ${pkgs.gzip}/bin/gunzip < ${wordpressArchive} | ${pkgs.gnutar}/bin/tar x 2>&1 | tee -a "$LOG"
      if [ -d wordpress ]; then
        mv wordpress/* . && rmdir wordpress || log "Note: move step had warnings"
      else
        log "ERROR: WordPress archive layout unexpected"
        exit 1
      fi
    else
      log "WordPress files already present."
    fi

    cd "$WP_DIR"
    {
      echo "<?php"
      echo "define(\"DB_NAME\", getenv(\"DB_NAME\") ?: \"wordpress\");"
      echo "define(\"DB_USER\", getenv(\"DB_USER\") ?: \"wordpress\");"
      echo "define(\"DB_PASSWORD\", getenv(\"DB_PASSWORD\") ?: \"wordpress_pass\");"
      echo "define(\"DB_HOST\", getenv(\"DB_HOST\") ?: \"127.0.0.1\");"
      echo "define(\"DB_CHARSET\", \"utf8mb4\");"
      echo "define(\"DB_COLLATE\", \"\");"
      echo "define(\"AUTH_KEY\", \"put your unique phrase here\");"
      echo "define(\"SECURE_AUTH_KEY\", \"put your unique phrase here\");"
      echo "define(\"LOGGED_IN_KEY\", \"put your unique phrase here\");"
      echo "define(\"NONCE_KEY\", \"put your unique phrase here\");"
      echo "define(\"AUTH_SALT\", \"put your unique phrase here\");"
      echo "define(\"SECURE_AUTH_SALT\", \"put your unique phrase here\");"
      echo "define(\"LOGGED_IN_SALT\", \"put your unique phrase here\");"
      echo "define(\"NONCE_SALT\", \"put your unique phrase here\");"
      echo "\$table_prefix = \"wp_\";"
      echo "define(\"WP_DEBUG\", false);"
      echo "if (!defined(\"ABSPATH\")) {"
      echo "  define(\"ABSPATH\", __DIR__ . \"/\");"
      echo "}"
      echo ""
      echo "/*"
      echo " * Avoid redirect loops to 127.0.0.1 by deriving canonical URLs from the"
      echo " * incoming request Host (or X-Forwarded-Host)."
      echo " */"
      echo "\$__wp_fallback_url = getenv(\"WP_URL\") ?: \"http://127.0.0.1:8081\";"
      echo "if (php_sapi_name() === 'cli') {"
      echo "  \$__wp_base = \$__wp_fallback_url;"
      echo "} else {"
      echo "  \$__host = \$_SERVER['HTTP_X_FORWARDED_HOST'] ?? (\$_SERVER['HTTP_HOST'] ?? \"\");"
      echo "  \$__proto = !empty(\$_SERVER['HTTP_X_FORWARDED_PROTO'])"
      echo "    ? \$_SERVER['HTTP_X_FORWARDED_PROTO']"
      echo "    : (!empty(\$_SERVER['HTTPS']) && \$_SERVER['HTTPS'] !== 'off' ? 'https' : 'http');"
      echo "  \$__fwd_port = \$_SERVER['HTTP_X_FORWARDED_PORT'] ?? \"\";"
      echo "  \$__base = \"\";"
      echo "  if (!empty(\$__host)) {"
      echo "    if (strpos(\$__host, ':') === false && \$__fwd_port !== \"\" && \$__fwd_port !== '80' && \$__fwd_port !== '443') {"
      echo "      \$__host = \$__host . ':' . \$__fwd_port;"
      echo "    }"
      echo "    \$__base = \$__proto . '://' . \$__host;"
      echo "  }"
      echo "  \$__wp_base = !empty(\$__base) ? \$__base : \$__wp_fallback_url;"
      echo "}"
      echo "define('WP_HOME', rtrim(\$__wp_base, '/'));"
      echo "define('WP_SITEURL', rtrim(\$__wp_base, '/'));"
      echo "require_once(ABSPATH . \"wp-settings.php\");"
    } > wp-config.php

    export DB_NAME DB_USER DB_PASSWORD DB_HOST

    CONFIG_HASH_FILE="${storageDirectory}/.wp-config-hash"
    CONFIG_DATA="$WP_SITE_TITLE|$WP_ADMIN_USER|$WP_ADMIN_EMAIL"
    CURRENT_HASH=''$(echo -n "''$CONFIG_DATA" | ${pkgs.coreutils}/bin/sha256sum | ${pkgs.gawk}/bin/awk '{print $1}')

    WP_URL="http://127.0.0.1:8081"

    if ${pkgs.wp-cli}/bin/wp core is-installed --allow-root 2>/dev/null; then
      log "WordPress core already installed."
      if [ -f "$CONFIG_HASH_FILE" ]; then
        STORED_HASH=''$(cat "''$CONFIG_HASH_FILE")
        if [ "$STORED_HASH" != "$CURRENT_HASH" ]; then
          log "Updating site title and admin email from config..."
          ${pkgs.wp-cli}/bin/wp option update blogname "$WP_SITE_TITLE" --allow-root 2>&1 | tee -a "$LOG"
          ${pkgs.wp-cli}/bin/wp user update "$WP_ADMIN_USER" --user_email="$WP_ADMIN_EMAIL" --allow-root 2>&1 | tee -a "$LOG"
          echo "$CURRENT_HASH" > "$CONFIG_HASH_FILE"
        fi
      fi
    else
      log "Running wp core install..."
      ${pkgs.wp-cli}/bin/wp core install \
        --url="$WP_URL" \
        --title="$WP_SITE_TITLE" \
        --admin_user="$WP_ADMIN_USER" \
        --admin_password="$WP_ADMIN_PASSWORD" \
        --admin_email="$WP_ADMIN_EMAIL" \
        --allow-root 2>&1 | tee -a "$LOG"
      echo "$CURRENT_HASH" > "$CONFIG_HASH_FILE"
    fi

    # --- 4) Install plugins (files) ---
    log "Phase 4: Installing plugin packages..."
    PLUGINS_DIR="$WP_DIR/wp-content/plugins"
    mkdir -p "$PLUGINS_DIR"

    install_zip() {
      name="$1"
      url="$2"
      if [ ! -d "$PLUGINS_DIR/$name" ]; then
        log "Downloading $name..."
        ${pkgs.curl}/bin/curl -fsSL -o "/tmp/$name.zip" "$url"
        ${pkgs.unzip}/bin/unzip -q -o "/tmp/$name.zip" -d "$PLUGINS_DIR"
        rm -f "/tmp/$name.zip"
      else
        log "Plugin $name already present, skipping download."
      fi
    }

    install_zip "woocommerce" "https://downloads.wordpress.org/plugin/woocommerce.zip"
    install_zip "easy-gigawallet-dogecoin-gateway" "https://downloads.wordpress.org/plugin/easy-gigawallet-dogecoin-gateway.zip"
    install_zip "easy-dogecoin-gateway" "https://downloads.wordpress.org/plugin/easy-dogecoin-gateway.zip"

    # --- 5) Activate plugins (after core + files) ---
    log "Phase 5: Activating plugins..."
    ${pkgs.wp-cli}/bin/wp plugin activate woocommerce --allow-root 2>&1 | tee -a "$LOG" || true
    ${pkgs.wp-cli}/bin/wp plugin activate easy-gigawallet-dogecoin-gateway --allow-root 2>&1 | tee -a "$LOG" || true
    ${pkgs.wp-cli}/bin/wp plugin activate easy-dogecoin-gateway --allow-root 2>&1 | tee -a "$LOG" || true

    EASY_DOGECOIN_ENABLED="no"
    if [ "$DOGECOIN_ENABLED" = "true" ]; then
      EASY_DOGECOIN_ENABLED="yes"
    fi
    export EASY_DOGECOIN_ENABLED
    export EASY_DOGECOIN_GATEWAY_PAYMENT_ADDRESS

    log "Phase 5b: WooCommerce pages, demo product, then US/TX + dismiss admin wizard..."
    ${pkgs.wp-cli}/bin/wp eval --allow-root '
      if (function_exists("wc_get_page_id") && (int) wc_get_page_id("shop") <= 0 && class_exists("WC_Install")) {
        WC_Install::create_pages();
      }
    ' 2>&1 | tee -a "$LOG" || true

    ${pkgs.wp-cli}/bin/wp eval --allow-root '
      if (!function_exists("wc_get_product_id_by_sku")) {
        return;
      }
      $sku = "doge-coffe-demo";
      $pid = wc_get_product_id_by_sku($sku);
      if ($pid) {
        $p = wc_get_product($pid);
      } else {
        $p = new WC_Product_Simple();
        $p->set_name("Doge Coffe");
        $p->set_slug("doge-coffe");
        $p->set_sku($sku);
      }
      $p->set_regular_price("1");
      $p->set_price("1");
      $p->set_catalog_visibility("visible");
      $p->set_stock_status("instock");
      $p->set_manage_stock(false);
      $p->set_status("publish");
      $p->save();
    ' 2>&1 | tee -a "$LOG" || true

    PRODUCT_ID=$(${pkgs.wp-cli}/bin/wp post list --post_type=product --meta_key=_sku --meta_value=doge-coffe-demo --field=ID --format=ids --allow-root 2>/dev/null | ${pkgs.coreutils}/bin/head -n1)
    if [ -n "$PRODUCT_ID" ]; then
      THUMB_ID=$(${pkgs.wp-cli}/bin/wp post meta get "$PRODUCT_ID" _thumbnail_id --allow-root 2>/dev/null || echo 0)
      if [ -z "$THUMB_ID" ] || [ "$THUMB_ID" = "0" ]; then
        ${pkgs.curl}/bin/curl -fsSL -o /tmp/doge-coffe.jpg \
          "https://images2.memedroid.com/images/UPLOADED60/521e9cc53ec1f.jpeg" \
          && ${pkgs.wp-cli}/bin/wp media import /tmp/doge-coffe.jpg --post_id="$PRODUCT_ID" --featured_image --allow-root 2>&1 | tee -a "$LOG" || true
      fi
    fi

    # US/Texas + demo address (Store details task), DOGE, hide setup & task lists, mark onboarding done.
    ${pkgs.wp-cli}/bin/wp eval --allow-root '
      if (get_option("woocommerce_store_address", "") === "") {
        update_option("woocommerce_store_address", "1 Demo Store Lane");
      }
      if (get_option("woocommerce_store_city", "") === "") {
        update_option("woocommerce_store_city", "Austin");
      }
      if (get_option("woocommerce_store_postcode", "") === "") {
        update_option("woocommerce_store_postcode", "78701");
      }
      update_option("woocommerce_default_country", "US:TX");
      update_option("woocommerce_currency", "DOGE");
      update_option("woocommerce_coming_soon", "no");
      update_option("woocommerce_store_pages_only", "no");
      update_option("woocommerce_show_marketplace_suggestions", "no");
      update_option("woocommerce_allow_tracking", "no");
      update_option("woocommerce_task_list_hidden", "yes");
      update_option("woocommerce_task_list_complete", "yes");
      update_option("woocommerce_task_list_welcome_modal_dismissed", "yes");
      update_option("woocommerce_extended_task_list_hidden", "yes");
      update_option("woocommerce_default_homepage_layout", "two_columns");
      $hidden_more = array("setup", "extended", "secret_tasklist");
      $cur = get_option("woocommerce_task_list_hidden_lists", array());
      if (!is_array($cur)) { $cur = array(); }
      update_option("woocommerce_task_list_hidden_lists", array_values(array_unique(array_merge($cur, $hidden_more))));
      $profile = get_option("woocommerce_onboarding_profile", array());
      if (!is_array($profile)) { $profile = array(); }
      $profile["skipped"] = true;
      $profile["completed"] = true;
      $profile["is_store_country_set"] = true;
      $profile["business_country"] = "US:TX";
      update_option("woocommerce_onboarding_profile", $profile);
      if (!get_option("woocommerce_admin_install_timestamp")) {
        update_option("woocommerce_admin_install_timestamp", time());
      }
    ' 2>&1 | tee -a "$LOG" || true

    # Easy Dogecoin after store/currency are set: WC_Payment_Gateway::update_option = same as Save in wp-admin.
    log "Configuring WooCommerce Easy Dogecoin Payment Gateway..."
    ${pkgs.wp-cli}/bin/wp eval --allow-root '
      if (!function_exists("WC") || !WC()) { return; }
      $enabled = (getenv("EASY_DOGECOIN_ENABLED") === "yes") ? "yes" : "no";
      $addr = getenv("EASY_DOGECOIN_GATEWAY_PAYMENT_ADDRESS");
      if ($addr === false) { $addr = ""; }
      $instr = "Please pay the exact amount of Dogecoin and send us the Transaction ID by email, to be able to check the payment.";
      $pm = WC()->payment_gateways();
      if (!is_object($pm)) { return; }
      $map = isset($pm->payment_gateways) && is_array($pm->payment_gateways) ? $pm->payment_gateways : array();
      if (isset($map["easydoge_payment"]) && is_object($map["easydoge_payment"])) {
        $gates = $map["easydoge_payment"];
        $gates->update_option("enabled", $enabled);
        $gates->update_option("doge_address", $addr);
        $gates->update_option("mydoge_x_user", "");
        $gates->update_option("sodoge_x_user", "");
        $gates->update_option("instructions", $instr);
      } else {
        $settings = get_option("woocommerce_easydoge_payment_settings", array());
        if (!is_array($settings)) { $settings = array(); }
        $settings["enabled"] = $enabled;
        $settings["doge_address"] = $addr;
        $settings["mydoge_x_user"] = "";
        $settings["sodoge_x_user"] = "";
        $settings["instructions"] = $instr;
        update_option("woocommerce_easydoge_payment_settings", $settings);
      }
    ' 2>&1 | tee -a "$LOG" || true

    ${pkgs.wp-cli}/bin/wp theme install storefront --activate --allow-root 2>&1 | tee -a "$LOG" || true

    # Checkout page: only the shortcode so the gateway/checkout block renders correctly (no theme boilerplate).
    ${pkgs.wp-cli}/bin/wp eval --allow-root '
      $cid = (int) get_option("woocommerce_checkout_page_id");
      if ($cid <= 0 && function_exists("wc_get_page_id")) {
        $cid = (int) wc_get_page_id("checkout");
      }
      if ($cid > 0) {
        wp_update_post(array(
          "ID" => $cid,
          "post_content" => "[woocommerce_checkout]",
        ));
      }
    ' 2>&1 | tee -a "$LOG" || true

    # Custom logo (WooCommerce mark) — skip if already set to avoid duplicate attachments each boot.
    STOREFRONT_LOG_MOD=$(${pkgs.wp-cli}/bin/wp eval 'echo (string)(int) get_theme_mod("custom_logo", 0);' --allow-root 2>/dev/null || echo 0)
    if [ "$STOREFRONT_LOG_MOD" = "0" ] || [ -z "$STOREFRONT_LOG_MOD" ]; then
      ${pkgs.curl}/bin/curl -fsSL -o /tmp/storefront-custom-logo.png \
        "https://dogegarden.com/woocommerce/wp-content/uploads/2022/04/cropped-woocommerce-logo-e1429552613105-1.png" \
        && LOGO_ATTACH=$(${pkgs.wp-cli}/bin/wp media import /tmp/storefront-custom-logo.png --porcelain --allow-root 2>/dev/null || true) \
        && { [ -n "$LOGO_ATTACH" ] && ${pkgs.wp-cli}/bin/wp theme mod set custom_logo "$LOGO_ATTACH" --allow-root 2>&1 | tee -a "$LOG" || true; }
    fi

    ${pkgs.wp-cli}/bin/wp eval --allow-root '
      $shop_id = (int) get_option("woocommerce_shop_page_id");
      if ($shop_id > 0) {
        update_option("show_on_front", "page");
        update_option("page_on_front", $shop_id);
      }
    ' 2>&1 | tee -a "$LOG" || true

    log "Setup complete; switching Caddy to WordPress + PHP-FPM."

    # Stop static Caddy before rebinding with full site
    kill "$CADDY_PID" 2>/dev/null || true
    wait "$CADDY_PID" 2>/dev/null || true
    sleep 1

    # --- PHP-FPM for WordPress ---
    {
      echo "[global]"
      echo "pid = ${storageDirectory}/php-fpm/php-fpm.pid"
      echo "error_log = ${storageDirectory}/php-fpm/error.log"
      echo ""
      echo "[www]"
      echo "listen = 127.0.0.1:9000"
      echo "user = nobody"
      echo "group = nogroup"
      echo "pm = static"
      echo "pm.max_children = 5"
    } > ${storageDirectory}/php-fpm/php-fpm.conf
    ${pkgs.php82}/bin/php-fpm -c /dev/null -y ${storageDirectory}/php-fpm/php-fpm.conf &
    sleep 2

    {
      echo ":8081 {"
      echo "  root * $WP_DIR"
      echo "  php_fastcgi 127.0.0.1:9000"
      echo "  file_server"
      echo "}"
    } > "$CADDY_DIR/Caddyfile"

    log "Starting Caddy (WordPress) on port 8081 — mysqld stays up in background."
    exec ${pkgs.caddy}/bin/caddy run --config "$CADDY_DIR/Caddyfile" --adapter caddyfile
  '';

in
{
  wordpress = startWordpress;
}
