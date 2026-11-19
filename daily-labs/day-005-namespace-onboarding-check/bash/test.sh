#!/usr/bin/env bash
# Golden tests for onboardcheck.sh: same samples and exit codes as Python/Go/Ansible.
set -Eeuo pipefail
cd "$(dirname "$0")"
export LC_ALL=C
S=../samples
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0

check() {   # name expected_rc
  local name=$1 want=$2 rc=0
  ./onboardcheck.sh "$S/$name.spec.json" "$S/$name.cluster.json" > "$tmp/$name.out" 2> "$tmp/$name.err" || rc=$?
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
echo '{"namespaces":[{"name":"a","tier":"huge","admin_group":"x","viewer_group":"y"}]}' > "$tmp/badtier.json"
expect_rc "missing spec file"   3 ./onboardcheck.sh "$tmp/nope.json" "$S/ocp-dev.cluster.json"
expect_rc "invalid JSON"        3 ./onboardcheck.sh "$S/ocp-dev.spec.json" "$tmp/bad.json"
expect_rc "unknown tier"        3 ./onboardcheck.sh "$tmp/badtier.json" "$S/ocp-dev.cluster.json"
expect_rc "spec as cluster"     3 ./onboardcheck.sh "$S/ocp-dev.spec.json" "$S/ocp-dev.spec.json"
expect_rc "no args"             3 ./onboardcheck.sh
expect_rc "one arg"             3 ./onboardcheck.sh "$S/ocp-dev.spec.json"

exit "$fail"
