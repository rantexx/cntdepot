#!/usr/bin/env bash
# The one check command: generated code is current, format, vet, test, web build. Run before saying work is done.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v go >/dev/null 2>&1 || export PATH="$PATH:/c/Program Files/Go/bin"

bash scripts/gen.sh
git diff --exit-code -- internal/store/db internal/api web/src/gen
test -z "$(gofmt -l cmd internal)" || { echo "gofmt needed:"; gofmt -l cmd internal; exit 1; }
go vet ./...
go test ./...
npm --prefix web run build

echo "CHECK OK"
