{ pkgs ? import <nixpkgs> {} }:

let
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
    set -e
    STORAGE_DIR="/storage/quantum-explorer"
    mkdir -p "$STORAGE_DIR"

    export PATH="${libdogecoin}/bin:${pkgs.coreutils}/bin:$PATH"
    export QE_STORAGE_DIR="$STORAGE_DIR"
    export PUBLIC_PORT="''${PUBLIC_PORT:-33666}"
    export QE_ADMIN_PORT="''${QE_ADMIN_PORT:-33667}"
    export NETWORK="''${NETWORK:-mainnet}"
    export QE_EXPLORER_TX_API="''${QE_EXPLORER_TX_API:-}"
    export QE_ADMIN_USER="''${QE_ADMIN_USER:-shibe}"
    export QE_ADMIN_PASS="''${QE_ADMIN_PASS:-suchpass}"
    export QE_SPV_USE_CHECKPOINT="''${QE_SPV_USE_CHECKPOINT:-1}"
    export QE_SPV_AUTO_START="''${QE_SPV_AUTO_START:-1}"
    export LIBDOGECOIN_SPVNODE="''${LIBDOGECOIN_SPVNODE:-${libdogecoin}/bin/spvnode}"

    exec ${qe_bin}/bin/quantum-explorer
  '';
in
{
  quantum-explorer = quantum-explorer;
}
