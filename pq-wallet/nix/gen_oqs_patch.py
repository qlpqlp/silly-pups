#!/usr/bin/env python3
"""Regenerate libdogecoin-oqs.patch from upstream CMakeLists.txt (LF-only)."""
import difflib
import urllib.request
from pathlib import Path

URL = (
    "https://raw.githubusercontent.com/dogecoinfoundation/libdogecoin/"
    "a120e0377650f247398b8b76c5d74e5ed89ec437/CMakeLists.txt"
)

def main() -> None:
    u = urllib.request.urlopen(URL).read().decode("utf-8").replace("\r\n", "\n")

    old1 = """IF(USE_LIBOQS)
    ADD_DEFINITIONS(-DUSE_LIBOQS=1)
ENDIF()"""
    new1 = """IF(USE_LIBOQS)
    ADD_DEFINITIONS(-DUSE_LIBOQS=1)
    find_package(PkgConfig REQUIRED)
    pkg_check_modules(OQS REQUIRED liboqs)
ENDIF()"""
    assert old1 in u, "block1 not found"
    p = u.replace(old1, new1, 1)

    old2 = """IF(USE_LIBOQS)
TARGET_SOURCES(${LIBDOGECOIN_NAME} PRIVATE
    src/pqc_dilithium.c
    src/pqc_falcon.c
)
ENDIF()
TARGET_SOURCES(${LIBDOGECOIN_NAME} PUBLIC"""
    new2 = """IF(USE_LIBOQS)
TARGET_SOURCES(${LIBDOGECOIN_NAME} PRIVATE
    src/pqc_dilithium.c
    src/pqc_falcon.c
)
ENDIF()
IF(USE_LIBOQS)
    target_include_directories(${LIBDOGECOIN_NAME} PUBLIC ${OQS_INCLUDE_DIRS})
    target_link_libraries(${LIBDOGECOIN_NAME} PUBLIC ${OQS_LIBRARIES})
ENDIF()

TARGET_SOURCES(${LIBDOGECOIN_NAME} PUBLIC"""
    assert old2 in p, "block2 not found"
    p = p.replace(old2, new2, 1)

    ud = u.splitlines(keepends=True)
    pd = p.splitlines(keepends=True)
    diff = difflib.unified_diff(
        ud,
        pd,
        fromfile="a/CMakeLists.txt",
        tofile="b/CMakeLists.txt",
        n=3,
    )
    out = "".join(diff)
    Path("libdogecoin-oqs.patch").write_text(out, encoding="utf-8", newline="\n")
    print("Wrote libdogecoin-oqs.patch", len(out), "bytes")


if __name__ == "__main__":
    main()
