#!/usr/bin/env bash
# Golden tests for nodepressure.sh: same samples and exit codes as Python/Go/Ansible.
set -Eeuo pipefail
cd "$(dirname "$0")"
export LC_ALL=C
S=../samples
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0

check() {   # name expected_rc
  local name=$1 want=$2 rc=0
  ./nodepressure.sh "$S/$name.nodes.json" "$S/$name.events.json" > "$tmp/$name.out" 2> "$tmp/$name.err" || rc=$?
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
  if grep -q -- "$3" "$2"; then echo "ok   $1"; else echo "FAIL $1: no '$3' in $2"; fail=1; fi
}

check ocp-prod 2
check ocp-stage 1
check ocp-dev 0

echo '{not json' > "$tmp/bad.json"
expect_rc "missing nodes file"       3 ./nodepressure.sh "$tmp/nope.json" "$S/ocp-dev.events.json"
expect_rc "invalid JSON"             3 ./nodepressure.sh "$tmp/bad.json" "$S/ocp-dev.events.json"
expect_rc "nodes is not a List"      3 ./nodepressure.sh "$S/bad/not-a-list.json" "$S/ocp-dev.events.json"
expect_rc "events is not a List"     3 ./nodepressure.sh "$S/ocp-dev.nodes.json" "$S/bad/not-a-list.json"
expect_rc "bad condition status"     3 ./nodepressure.sh "$S/bad/nodes-bad-status.json" "$S/ocp-dev.events.json"
expect_rc "bad eviction count"       3 ./nodepressure.sh "$S/ocp-dev.nodes.json" "$S/bad/events-bad-count.json"
expect_rc "no args"                  3 ./nodepressure.sh
expect_rc "one arg"                  3 ./nodepressure.sh "$S/ocp-dev.nodes.json"

# edge cases built on the fly: storm boundary (4 -> WARN, 5 -> CRIT), default count, unknown resource, missing Ready
node='{"kind":"Node","metadata":{"name":"a"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}'
mk() { printf '{"kind":"List","items":[%s]}' "$1" > "$tmp/$2"; }
mk "$node" n.json
mk '{"reason":"Evicted","message":"The node was low on resource: memory. x","source":{"host":"a"},"count":4}' e4.json
mk '{"reason":"Evicted","message":"The node was low on resource: memory. x","source":{"host":"a"},"count":5}' e5.json
mk '{"reason":"Evicted","message":"no resource here","source":{"host":"a"}}' eu.json
expect_rc "4 evictions is WARN"      1 ./nodepressure.sh "$tmp/n.json" "$tmp/e4.json"
expect_rc "5 evictions is CRIT"      2 ./nodepressure.sh "$tmp/n.json" "$tmp/e5.json"
./nodepressure.sh "$tmp/n.json" "$tmp/eu.json" > "$tmp/eu.out" || true
expect_line "unknown resource, count defaults to 1" "$tmp/eu.out" "WARN a EVICTIONS total=1 unknown=1"
mk '{"kind":"Node","metadata":{"name":"a"}}' nr.json
./nodepressure.sh "$tmp/nr.json" "$tmp/e4.json" > "$tmp/nr.out" || true
expect_line "missing Ready is NOTREADY" "$tmp/nr.out" "CRIT a NOTREADY status=Missing reason=-"

exit "$fail"
