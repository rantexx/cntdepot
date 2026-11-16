#!/usr/bin/env bash
# Downloads the pinned sqlc release binary into .tools/ (git-ignored) and checks its SHA-256.
# go run cannot build sqlc's PostgreSQL parser without cgo, so we use the release binary.
set -euo pipefail
cd "$(dirname "$0")/.."

SQLC_VERSION=1.31.1
SQLC_SHA256_WINDOWS_AMD64=352711fa7dcb05dcdfefca0ad71b2c9a74fd090f8d7fc609419de4cbc725429f # sqlc_1.31.1_windows_amd64.zip
SQLC_SHA256_LINUX_AMD64=497ae4fcdfa64c5b0c311ffe4c2bd991e43991e82e5367792ed78bc2dca27354   # sqlc_1.31.1_linux_amd64.tar.gz

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) os=windows; ext=zip; bin=sqlc.exe; want=$SQLC_SHA256_WINDOWS_AMD64 ;;
  Linux) os=linux; ext=tar.gz; bin=sqlc; want=$SQLC_SHA256_LINUX_AMD64 ;;
  *) echo "tools.sh: no pinned sqlc for $(uname -s)" >&2; exit 1 ;;
esac

if [ -x ".tools/$bin" ] && [ "$(.tools/$bin version)" = "v$SQLC_VERSION" ]; then exit 0; fi

mkdir -p .tools
archive=".tools/sqlc.$ext"
curl -fsSL -o "$archive" "https://github.com/sqlc-dev/sqlc/releases/download/v$SQLC_VERSION/sqlc_${SQLC_VERSION}_${os}_amd64.$ext"
got=$(sha256sum "$archive" | cut -d' ' -f1)
[ "$got" = "$want" ] || { echo "tools.sh: sqlc checksum mismatch: $got" >&2; rm -f "$archive"; exit 1; }
if [ "$ext" = zip ]; then unzip -o -q "$archive" -d .tools; else tar -xzf "$archive" -C .tools; fi
rm -f "$archive"
