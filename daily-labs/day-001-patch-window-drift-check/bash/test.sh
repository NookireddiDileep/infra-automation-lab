#!/usr/bin/env bash
# Contract test: compare every sample pair and diff against the golden report + exit code.
set -Eeuo pipefail
cd "$(dirname "$0")"
S=../samples
fail=0
declare -A want=([node-01]=2 [node-02]=0 [node-03]=1)

for node in node-01 node-02 node-03; do
  rc=0
  out=$(./patchcheck.sh compare "$S/$node-pre.txt" "$S/$node-post.txt") || rc=$?
  if diff -u "$S/expected/$node.report" <(printf '%s\n' "$out"); then
    echo "PASS report $node"
  else
    echo "FAIL report $node"; fail=1
  fi
  if [[ $rc -eq ${want[$node]} ]]; then echo "PASS exit $node=$rc"; else echo "FAIL exit $node=$rc want ${want[$node]}"; fail=1; fi
done

# Error handling: bad input must give exit 3
rc=0; ./patchcheck.sh compare /nonexistent "$S/node-01-post.txt" 2>/dev/null || rc=$?
if [[ $rc -eq 3 ]]; then echo "PASS missing file -> 3"; else echo "FAIL missing file -> $rc"; fail=1; fi

# Live smoke test: a snapshot of this machine must be a valid v1 snapshot that compares clean against itself
tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
./patchcheck.sh snapshot -o "$tmp" 2>/dev/null
if ./patchcheck.sh compare -t 101 "$tmp" "$tmp" | grep -q '^RESULT OK'; then echo "PASS live snapshot self-compare"; else echo "FAIL live snapshot"; fail=1; fi

exit "$fail"
