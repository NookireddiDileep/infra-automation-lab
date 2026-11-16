#!/usr/bin/env bash
# Contract test: report every sample and diff against the golden report + exit code.
set -Eeuo pipefail
cd "$(dirname "$0")"
S=../samples
fail=0
declare -A want=([node-01]=2 [node-02]=0 [node-03]=1)

check() {   # check NAME WANT_RC CMD...
  local name=$1 want_rc=$2 rc=0; shift 2
  "$@" >/dev/null 2>&1 || rc=$?
  if [[ $rc -eq $want_rc ]]; then echo "PASS $name -> $rc"; else echo "FAIL $name -> $rc want $want_rc"; fail=1; fi
}

for node in node-01 node-02 node-03; do
  rc=0
  out=$(./disktriage.sh report "$S/$node.snap") || rc=$?
  if diff -u "$S/expected/$node.report" <(printf '%s\n' "$out"); then
    echo "PASS report $node"
  else
    echo "FAIL report $node"; fail=1
  fi
  if [[ $rc -eq ${want[$node]} ]]; then echo "PASS exit $node=$rc"; else echo "FAIL exit $node=$rc want ${want[$node]}"; fail=1; fi
done

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

# Error handling: anything wrong with the input is exit 3, never a half report
printf 'hello\n' > "$tmp/not-a-snapshot"
printf '# disktriage snapshot v1\nfs / xfs 1 2\n' > "$tmp/short-fs"
printf '# disktriage snapshot v1\ndu big /x\n' > "$tmp/bad-du"
check "missing file" 3 ./disktriage.sh report /nonexistent
check "not a snapshot" 3 ./disktriage.sh report "$tmp/not-a-snapshot"
check "short fs line" 3 ./disktriage.sh report "$tmp/short-fs"
check "non-numeric du" 3 ./disktriage.sh report "$tmp/bad-du"
check "bad threshold" 3 ./disktriage.sh report -w abc "$S/node-01.snap"

# Flags: thresholds and retention change the verdict
check "node-01 with -w 98 -c 99" 0 ./disktriage.sh report -w 98 -c 99 "$S/node-01.snap"
out=$(./disktriage.sh report -r 1 "$S/node-01.snap") || true   # not `cmd | grep -q`: exit 2 + pipefail = false
if grep -qx '  rotated 5 files 2248 MiB older than 1d' <<<"$out"; then
  echo "PASS -r 1 counts the 2-day-old log"; else echo "FAIL -r 1"; fail=1; fi

# Live smoke test: a snapshot of this machine is valid and reports OK when thresholds are above 100%
./disktriage.sh snapshot -m 101 -o "$tmp/live.snap" 2>/dev/null
out=$(./disktriage.sh report -w 101 -c 101 "$tmp/live.snap") || true
if grep -q '^RESULT OK' <<<"$out"; then
  echo "PASS live snapshot"; else echo "FAIL live snapshot"; fail=1; fi

exit "$fail"
