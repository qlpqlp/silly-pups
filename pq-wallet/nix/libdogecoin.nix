# libdogecoin with liboqs (Falcon-512 / Dilithium2) + such, sendtx, spvnode CLI tools.
# Source of truth: pq-wallet/vendors/libdogecoin (your PQC-ready tree), not a fixed GitHub tarball.
{ lib, stdenv, cmake, pkg-config, gmp, liboqs, openssl, ninja }:

stdenv.mkDerivation rec {
  pname = "libdogecoin-with-oqs";
  version = "vendor";

  src = ../vendors/libdogecoin;

  patches = [
    ./libdogecoin-oqs.patch
    ./libdogecoin-libevent-hints.patch
    ./libdogecoin-with-net-link.patch
    ./libdogecoin-pq-peer-log.patch
    ./libdogecoin-spv-tx-raw.patch
  ];

  nativeBuildInputs = [ cmake pkg-config ninja ];
  buildInputs = [ gmp liboqs openssl ];

  strictDeps = true;

  cmakeFlags = [
    "-GNinja"
    "-DCMAKE_BUILD_TYPE=Release"
    "-DBUILD_TESTING=OFF"
    "-DUSE_LIBOQS=ON"
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

  meta = with lib; {
    description = "libdogecoin with liboqs-enabled such/sendtx/spvnode";
    license = licenses.mit;
  };
}
