# libdogecoin with liboqs (Falcon-512 / Dilithium2 / Raccoon-G carrier) + such, sendtx, spvnode CLI tools.
# Source: pq-wallet/vendors/libdogecoin at dogecoinfoundation/libdogecoin PR #294 head (4bd9b49).
# Nix applies ./libdogecoin-vendor-patches.patch (liboqs pkg-config, libevent hints, link fixes, PQ wallet log hooks).
{ lib, stdenv, cmake, pkg-config, gmp, liboqs, openssl, ninja }:

stdenv.mkDerivation rec {
  pname = "libdogecoin-with-oqs";
  version = "vendor";

  src = ../vendors/libdogecoin;

  patches = [
    ./libdogecoin-vendor-patches.patch
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
