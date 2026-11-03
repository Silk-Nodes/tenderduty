#!/bin/sh
# Fails when govulncheck finds a vulnerability that tenderduty's code reaches and docs/security-exceptions.md
# does not list. Advisories only present in modules we require but never call are reported, not fatal.
set -eu
cd "$(dirname "$0")/.."
out=$(mktemp)
go run golang.org/x/vuln/cmd/govulncheck@latest -format json ./... > "$out"
# a finding whose first trace frame names a function is one our code calls into
called=$(jq -r 'select(.finding) | .finding | select(.trace[0].function != null) | .osv' "$out" | sort -u)
rm -f "$out"
fail=0
for id in $called; do
  if grep -q "$id" docs/security-exceptions.md; then
    echo "accepted: $id (listed in docs/security-exceptions.md)"
  else
    echo "BLOCKING: $id reaches tenderduty code and is not in docs/security-exceptions.md"
    fail=1
  fi
done
[ -z "$called" ] && echo "govulncheck: nothing reachable"
exit $fail
