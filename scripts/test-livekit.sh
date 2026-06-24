#!/usr/bin/env sh
# Run the cgo-enabled tests for the picoclaw-livekit package (TEN VAD).
#
# Why this script exists
# ----------------------
# pkg/voice/vad/ten_vad.go uses cgo, so this package cannot be exercised by the
# default `make test` target (that runs with CGO_ENABLED=0). It needs a working
# MinGW gcc to compile, and the TEN VAD DLL on PATH to run.
#
# On Windows under an MSYS2 *msys* shell, a plain `go test ./cmd/picoclaw-livekit/`
# fails the cgo build with:
#     runtime/cgo: .../cgo.exe: exit status 2
# The real cause: the msys shell's PATH exposes the MSYS2 runtime (msys-2.0.dll in
# /usr/bin) to the MinGW gcc that cgo spawns. gcc/cc1 then load a conflicting
# runtime and die silently (exit 1/2, no diagnostics). A plain `go build` can still
# appear to pass because the runtime/cgo objects are already in the build cache;
# `go test` forces a fresh cgo build (different flags -> cache miss) and trips it.
#
# The fix is to run go test with a PATH where MinGW comes FIRST (so gcc loads the
# correct DLLs at build time) and the TEN VAD DLL directory is present (so the test
# binary can load ten_vad.dll at runtime, avoiding 0xc0000135 DLL-not-found).
#
# Usage:  sh scripts/test-livekit.sh [extra `go test` args...]
# Override the MinGW location with MINGW_BIN if your toolchain lives elsewhere.
set -e

# Resolve repo root from this script's location so it works from any CWD.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
cd "$REPO_ROOT"

export CGO_ENABLED=1

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    # Windows. Put MinGW gcc first, then the TEN VAD DLL directory.
    MINGW_BIN=${MINGW_BIN:-/c/msys64/mingw64/bin}
    TENVAD_DIR="$REPO_ROOT/third_party/ten-vad/lib/Windows/x64"
    if [ ! -x "$MINGW_BIN/gcc.exe" ]; then
      echo "error: MinGW gcc not found at $MINGW_BIN/gcc.exe (set MINGW_BIN)" >&2
      exit 1
    fi
    export PATH="$MINGW_BIN:$TENVAD_DIR:$PATH"
    ;;
esac

exec go test ./cmd/picoclaw-livekit/... "$@"
