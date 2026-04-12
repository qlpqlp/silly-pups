#!/usr/bin/env python3
"""Emit libdogecoin-with-net-link.patch (LIBS unset when BUILD_TESTING=OFF).

Fixes WITH_NET link lines and `such` CLI link lines: both used ${LIBS}, which is
only set inside USE_TESTS, so builds with BUILD_TESTING=OFF failed to link.
"""
import difflib
import urllib.request
from pathlib import Path

URL = (
    "https://raw.githubusercontent.com/dogecoinfoundation/libdogecoin/"
    "a120e0377650f247398b8b76c5d74e5ed89ec437/CMakeLists.txt"
)


def after_oqs_libevent(u: str) -> str:
    old1 = """IF(USE_LIBOQS)
    ADD_DEFINITIONS(-DUSE_LIBOQS=1)
ENDIF()"""
    new1 = """IF(USE_LIBOQS)
    ADD_DEFINITIONS(-DUSE_LIBOQS=1)
    find_package(PkgConfig REQUIRED)
    pkg_check_modules(OQS REQUIRED liboqs)
ENDIF()"""
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
    p = p.replace(old2, new2, 1)

    old_le = """        FIND_LIBRARY(LIBEVENT NAMES event event_core event_extras event_pthreads HINTS "${PROJECT_SOURCE_DIR}/src/libevent/build/lib/${CMAKE_BUILD_TYPE}" REQUIRED)"""
    new_le = """        FIND_LIBRARY(LIBEVENT NAMES event event_core event_extras event_pthreads
            HINTS "${PROJECT_SOURCE_DIR}/src/libevent/build/lib" "${PROJECT_SOURCE_DIR}/src/libevent/build/lib/${CMAKE_BUILD_TYPE}"
            REQUIRED)"""
    p = p.replace(old_le, new_le, 1)
    return p


def main() -> None:
    u = urllib.request.urlopen(URL).read().decode("utf-8").replace("\r\n", "\n")
    mid = after_oqs_libevent(u)

    # Must use keyword form (PUBLIC) to match target_link_libraries(... PUBLIC ...) for liboqs
    fixed = mid.replace(
        "TARGET_LINK_LIBRARIES(${LIBS} ${LIBEVENT} ${LIBEVENT_PTHREADS} tbs ncrypt crypt32)",
        "TARGET_LINK_LIBRARIES(${LIBDOGECOIN_NAME} PUBLIC ${LIBEVENT} ${LIBEVENT_PTHREADS} tbs ncrypt crypt32)",
    ).replace(
        "TARGET_LINK_LIBRARIES(${LIBS} ${LIBEVENT} ${LIBEVENT_PTHREADS})",
        "TARGET_LINK_LIBRARIES(${LIBDOGECOIN_NAME} PUBLIC ${LIBEVENT} ${LIBEVENT_PTHREADS})",
    ).replace(
        "TARGET_LINK_LIBRARIES(such ${LIBS} tbs ncrypt crypt32)",
        "TARGET_LINK_LIBRARIES(such ${LIBDOGECOIN_NAME} tbs ncrypt crypt32)",
    ).replace(
        "TARGET_LINK_LIBRARIES(such ${LIBS})",
        "TARGET_LINK_LIBRARIES(such ${LIBDOGECOIN_NAME})",
    )

    if mid == fixed:
        raise SystemExit("no changes — pattern not found")

    diff = difflib.unified_diff(
        mid.splitlines(keepends=True),
        fixed.splitlines(keepends=True),
        fromfile="a/CMakeLists.txt",
        tofile="b/CMakeLists.txt",
        n=3,
    )
    out = "".join(diff)
    Path("libdogecoin-with-net-link.patch").write_text(out, encoding="utf-8", newline="\n")
    print("Wrote libdogecoin-with-net-link.patch", len(out), "bytes")


if __name__ == "__main__":
    main()
