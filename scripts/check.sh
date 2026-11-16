#!/usr/bin/env bash
# The one check command: format, vet, test, web build. Run before saying work is done.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v go >/dev/null 2>&1 || export PATH="$PATH:/c/Program Files/Go/bin"

test -z "$(gofmt -l cmd internal)" || { echo "gofmt needed:"; gofmt -l cmd internal; exit 1; }
go vet ./...
go test ./...
npm --prefix web run build

echo "CHECK OK"
