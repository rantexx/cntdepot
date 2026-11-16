#!/usr/bin/env bash
# The one check command: generated code is current, format, vet, test, web build. Run before saying work is done.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v go >/dev/null 2>&1 || export PATH="$PATH:/c/Program Files/Go/bin"

# Generated code must equal what the generators produce. Compare file hashes
# (not git diff: that ignores untracked files and needs the fix staged).
gen_dirs() { for d in internal/store/db internal/api web/src/gen; do [ -d "$d" ] && echo "$d"; done; true; }
gen_hashes() { local d; d=$(gen_dirs); [ -z "$d" ] || find $d -type f | sort | xargs sha256sum; }
before=$(gen_hashes)
bash scripts/gen.sh
after=$(gen_hashes)
if [ "$before" != "$after" ]; then
  echo "generated code is out of date; scripts/gen.sh changed:"
  diff <(echo "$before") <(echo "$after") | grep '^[<>]' | sed 's/^\([<>]\) [0-9a-f]* */\1 /' | sort -u -k2
  exit 1
fi

test -z "$(gofmt -l cmd internal)" || { echo "gofmt needed:"; gofmt -l cmd internal; exit 1; }
go vet ./...
go test ./...
npm --prefix web run build

echo "CHECK OK"
