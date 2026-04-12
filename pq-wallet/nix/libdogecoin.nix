# libdogecoin with liboqs (Falcon-512 / Dilithium2) + such, sendtx, spvnode CLI tools.
{ lib, stdenv, fetchFromGitHub, cmake, pkg-config, gmp, liboqs, openssl, ninja }:

stdenv.mkDerivation rec {
  pname = "libdogecoin-with-oqs";
  version = "0.1.5-git-a120e03";

  src = fetchFromGitHub {
    owner = "dogecoinfoundation";
    repo = "libdogecoin";
    rev = "a120e0377650f247398b8b76c5d74e5ed89ec437";
    hash = "sha256-O0Km5jlSFFtXfN6aO2FsPzQfdo7YvI7LwFy4l3ldXkc=";
  };

  patches = [
    ./libdogecoin-oqs.patch
    ./libdogecoin-libevent-hints.patch
    ./libdogecoin-with-net-link.patch
    ./libdogecoin-pq-peer-log.patch
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
