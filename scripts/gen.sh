#!/usr/bin/env bash
# Regenerates all generated code: sqlc, go generate, and the web client when present.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v go >/dev/null 2>&1 || export PATH="$PATH:/c/Program Files/Go/bin"

bash scripts/tools.sh
case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) sqlc=.tools/sqlc.exe ;; *) sqlc=.tools/sqlc ;; esac
"$sqlc" generate
go generate ./...
if grep -q '"gen"' web/package.json 2>/dev/null; then npm --prefix web run gen; fi
