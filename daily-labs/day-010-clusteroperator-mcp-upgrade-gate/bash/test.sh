#!/usr/bin/env bash
# Golden tests for upgradegate.sh: same samples and exit codes as Python/Go/Ansible.
set -Eeuo pipefail
cd "$(dirname "$0")"
export LC_ALL=C
S=../samples
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0

check() {   # name expected_rc
  local name=$1 want=$2 rc=0
  ./upgradegate.sh "$S/$name.clusteroperators.json" "$S/$name.mcps.json" > "$tmp/$name.out" 2> "$tmp/$name.err" || rc=$?
  if [[ $rc != "$want" ]]; then echo "FAIL $name: exit $rc, want $want"; fail=1; return; fi
  if ! diff -u "$S/expected/$name.report" "$tmp/$name.out"; then echo "FAIL $name: output differs"; fail=1; return; fi
  echo "ok   $name (exit $rc)"
}
expect_rc() {   # description expected_rc cmd...
  local d=$1 want=$2 rc=0; shift 2
  "$@" >/dev/null 2>&1 || rc=$?
  if [[ $rc == "$want" ]]; then echo "ok   $d (exit $rc)"; else echo "FAIL $d: exit $rc, want $want"; fail=1; fi
}
expect_line() {   # description file grep-pattern
  if grep -qF -- "$3" "$2"; then echo "ok   $1"; else echo "FAIL $1: no '$3' in $2"; fail=1; fi
}

check ocp-prod 2
check ocp-lab 2
check ocp-stage 1
check ocp-dev 0

echo '{not json' > "$tmp/bad.json"
expect_rc "missing operators file"    3 ./upgradegate.sh "$tmp/nope.json" "$S/ocp-dev.mcps.json"
expect_rc "invalid JSON"              3 ./upgradegate.sh "$tmp/bad.json" "$S/ocp-dev.mcps.json"
expect_rc "operators not a List"      3 ./upgradegate.sh "$S/bad/not-a-list.json" "$S/ocp-dev.mcps.json"
expect_rc "mcps not a List"           3 ./upgradegate.sh "$S/ocp-dev.clusteroperators.json" "$S/bad/not-a-list.json"
expect_rc "bad condition status"      3 ./upgradegate.sh "$S/bad/co-bad-status.json" "$S/ocp-dev.mcps.json"
expect_rc "bad machineCount"          3 ./upgradegate.sh "$S/ocp-dev.clusteroperators.json" "$S/bad/mcps-bad-count.json"
expect_rc "no args"                   3 ./upgradegate.sh
expect_rc "one arg"                   3 ./upgradegate.sh "$S/ocp-dev.clusteroperators.json"

# edge cases built on the fly
mk() { printf '{"kind":"List","items":[%s]}' "$1" > "$tmp/$2"; }
co() { printf '{"kind":"ClusterOperator","metadata":{"name":"a"},"status":{"conditions":[%s]}}' "$1"; }
pool() {  # total updated unavailable degraded paused conds
  printf '{"kind":"MachineConfigPool","metadata":{"name":"w"},"spec":{"paused":%s},"status":{"machineCount":%s,"updatedMachineCount":%s,"readyMachineCount":%s,"unavailableMachineCount":%s,"degradedMachineCount":%s,"conditions":[%s]}}' \
    "$5" "$1" "$2" "$1" "$3" "$4" "$6"
}
mk "" none.json
mk "$(co '{"type":"Available","status":"False","reason":"X"},{"type":"Degraded","status":"True","reason":"Y"}')" unavail.json
mk "$(co '')" nostatus.json
mk "$(co '{"type":"Available","status":"True"},{"type":"Degraded","status":"False"}')" fine.json
mk "$(pool 2 1 1 0 false '{"type":"Updating","status":"True"}')" updating.json
mk "$(pool 2 1 0 0 true '')" paused.json
mk "$(pool 2 1 0 0 false '')" stalled.json
mk "$(pool 2 2 0 1 false '')" degraded.json

./upgradegate.sh "$tmp/unavail.json" "$tmp/none.json" > "$tmp/o1" || true
expect_line "Available=False beats Degraded=True"   "$tmp/o1" "CRIT co/a UNAVAILABLE reason=X"
./upgradegate.sh "$tmp/nostatus.json" "$tmp/none.json" > "$tmp/o2" || true
expect_line "no conditions is NOSTATUS"             "$tmp/o2" "CRIT co/a NOSTATUS reason=-"
expect_rc  "missing Upgradeable is fine"         0 ./upgradegate.sh "$tmp/fine.json" "$tmp/none.json"
expect_rc  "updating pool is WARN not CRIT"      1 ./upgradegate.sh "$tmp/none.json" "$tmp/updating.json"
expect_rc  "paused pool is WARN"                 1 ./upgradegate.sh "$tmp/none.json" "$tmp/paused.json"
expect_rc  "idle pool behind is STALLED (CRIT)"  2 ./upgradegate.sh "$tmp/none.json" "$tmp/stalled.json"
expect_rc  "degraded count alone is CRIT"        2 ./upgradegate.sh "$tmp/none.json" "$tmp/degraded.json"

exit "$fail"
