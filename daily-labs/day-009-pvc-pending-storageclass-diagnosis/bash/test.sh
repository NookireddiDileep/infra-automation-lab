#!/usr/bin/env bash
# Golden tests for pvcbind.sh: same samples and exit codes as Python/Go/Ansible.
set -Eeuo pipefail
cd "$(dirname "$0")"
export LC_ALL=C
S=../samples
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0

check() {   # name expected_rc
  local name=$1 want=$2 rc=0
  ./pvcbind.sh "$S/$name.pvcs.json" "$S/$name.storageclasses.json" > "$tmp/$name.out" 2> "$tmp/$name.err" || rc=$?
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
check ocp-lab 2
check ocp-stage 1
check ocp-dev 0

echo '{not json' > "$tmp/bad.json"
expect_rc "missing pvcs file"        3 ./pvcbind.sh "$tmp/nope.json" "$S/ocp-dev.storageclasses.json"
expect_rc "invalid JSON"             3 ./pvcbind.sh "$tmp/bad.json" "$S/ocp-dev.storageclasses.json"
expect_rc "pvcs is not a List"       3 ./pvcbind.sh "$S/bad/not-a-list.json" "$S/ocp-dev.storageclasses.json"
expect_rc "classes is not a List"    3 ./pvcbind.sh "$S/ocp-dev.pvcs.json" "$S/bad/not-a-list.json"
expect_rc "bad PVC phase"            3 ./pvcbind.sh "$S/bad/pvcs-bad-phase.json" "$S/ocp-dev.storageclasses.json"
expect_rc "bad volumeBindingMode"    3 ./pvcbind.sh "$S/ocp-dev.pvcs.json" "$S/bad/sc-bad-mode.json"
expect_rc "no args"                  3 ./pvcbind.sh
expect_rc "one arg"                  3 ./pvcbind.sh "$S/ocp-dev.pvcs.json"

# edge cases built on the fly: missing mode = Immediate, "" vs missing class name, Bound never flagged, INFO keeps exit 0
mk() { printf '{"kind":"List","items":[%s]}' "$1" > "$tmp/$2"; }
sc='{"kind":"StorageClass","metadata":{"name":"gp3"},"provisioner":"x.csi"}'
scw='{"kind":"StorageClass","metadata":{"name":"gp3","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}},"provisioner":"x.csi","volumeBindingMode":"WaitForFirstConsumer"}'
pvc() { printf '{"kind":"PersistentVolumeClaim","metadata":{"name":"a","namespace":"n"},"spec":{%s},"status":{"phase":"%s"}}' "$1" "$2"; }
mk "$sc" sc.json; mk "$scw" scw.json
mk "$(pvc '"storageClassName":"gp3"' Pending)" p-imm.json
mk "$(pvc '' Pending)" p-none.json
mk "$(pvc '"storageClassName":""' Pending)" p-empty.json
mk "$(pvc '"storageClassName":"ghost"' Bound)" p-bound.json
expect_rc "missing mode is Immediate (CRIT)"   2 ./pvcbind.sh "$tmp/p-imm.json" "$tmp/sc.json"
expect_rc "WaitForFirstConsumer is INFO (OK)"  0 ./pvcbind.sh "$tmp/p-none.json" "$tmp/scw.json"
expect_rc "Bound claim is never flagged"       0 ./pvcbind.sh "$tmp/p-bound.json" "$tmp/sc.json"
./pvcbind.sh "$tmp/p-none.json" "$tmp/sc.json" > "$tmp/nosc.out" || true
expect_line "no class and no default is NOSC"  "$tmp/nosc.out" "CRIT n/a NOSC class=-"
./pvcbind.sh "$tmp/p-empty.json" "$tmp/scw.json" > "$tmp/empty.out" || true
expect_line "empty class ignores the default"  "$tmp/empty.out" "CRIT n/a NOPV class=-"
./pvcbind.sh "$tmp/p-none.json" "$tmp/scw.json" > "$tmp/via.out" || true
expect_line "default class is named via=default" "$tmp/via.out" "INFO n/a WAITFORCONSUMER class=gp3 mode=WaitForFirstConsumer via=default"

exit "$fail"
