#!/usr/bin/env bash
# Golden tests for pendingtriage.sh: same samples and exit codes as Python/Go/Ansible.
set -Eeuo pipefail
cd "$(dirname "$0")"
export LC_ALL=C
S=../samples
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0

check() {   # name expected_rc
  local name=$1 want=$2 rc=0
  ./pendingtriage.sh "$S/$name.pods.json" > "$tmp/$name.out" 2> "$tmp/$name.err" || rc=$?
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
echo '{"kind":"Pod"}' > "$tmp/pod.json"
expect_rc "missing file"      3 ./pendingtriage.sh "$tmp/nope.json"
expect_rc "invalid JSON"      3 ./pendingtriage.sh "$tmp/bad.json"
expect_rc "not a pod list"    3 ./pendingtriage.sh "$tmp/pod.json"
expect_rc "no args"           3 ./pendingtriage.sh
expect_rc "two args"          3 ./pendingtriage.sh "$S/ocp-dev.pods.json" "$S/ocp-dev.pods.json"

exit "$fail"
