#!/usr/bin/env bash
# Golden tests for rightsize.sh: same samples and exit codes as Python/Go/Ansible.
set -Eeuo pipefail
cd "$(dirname "$0")"
export LC_ALL=C
S=../samples
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0

check() {   # name expected_rc
  local name=$1 want=$2 rc=0
  ./rightsize.sh "$S/$name.quota.json" "$S/$name.metrics.json" > "$tmp/$name.out" 2> "$tmp/$name.err" || rc=$?
  if [[ $rc != "$want" ]]; then echo "FAIL $name: exit $rc, want $want"; fail=1; return; fi
  if ! diff -u "$S/expected/$name.report" "$tmp/$name.out"; then echo "FAIL $name: output differs"; fail=1; return; fi
  echo "ok   $name (exit $rc)"
}
expect_rc() {   # description expected_rc cmd...
  local d=$1 want=$2 rc=0; shift 2
  "$@" >/dev/null 2>&1 || rc=$?
  if [[ $rc == "$want" ]]; then echo "ok   $d (exit $rc)"; else echo "FAIL $d: exit $rc, want $want"; fail=1; fi
}

check ocp-prod 2
check ocp-stage 1
check ocp-dev 0

echo '{not json' > "$tmp/bad.json"
echo '{"kind":"Pod"}' > "$tmp/notlist.json"
echo '{"kind":"List","items":[{"kind":"PodMetrics","metadata":{"namespace":"a"},"containers":[{"usage":{"cpu":"1.5","memory":"1Mi"}}]}]}' > "$tmp/badusage.json"
expect_rc "missing quota file"    3 ./rightsize.sh "$tmp/nope.json" "$S/ocp-dev.metrics.json"
expect_rc "invalid JSON"          3 ./rightsize.sh "$tmp/bad.json" "$S/ocp-dev.metrics.json"
expect_rc "metrics is not a List" 3 ./rightsize.sh "$S/ocp-dev.quota.json" "$tmp/notlist.json"
expect_rc "bad usage quantity"    3 ./rightsize.sh "$S/ocp-dev.quota.json" "$tmp/badusage.json"
expect_rc "no args"               3 ./rightsize.sh
expect_rc "one arg"               3 ./rightsize.sh "$S/ocp-dev.quota.json"

exit "$fail"
