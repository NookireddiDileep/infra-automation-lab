#!/usr/bin/env bash
# Golden test for certscan.sh: byte-identical reports + exit codes, error handling, and a live collect smoke test.
set -Eeuo pipefail
export LC_ALL=C
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
s="$here/../samples"
now=2026-11-20T00:00:00Z
tool=${CERTSCAN:-$here/certscan.sh}
fail=0

check_case() { # host expected_rc
  local out rc=0
  out=$("$tool" check --now "$now" "$s/$1.certs") || rc=$?
  if [[ $rc -ne $2 ]]; then echo "FAIL $1: exit $rc, want $2"; fail=1; return; fi
  if [[ $out != "$(cat "$s/expected/$1.report")" ]]; then
    echo "FAIL $1: report differs"; diff <(echo "$out") "$s/expected/$1.report" || true; fail=1; return
  fi
  echo "ok   $1 (exit $rc)"
}
check_case node-01 2
check_case node-02 0
check_case node-03 1

for bad in "$s"/bad/*.certs; do
  rc=0; "$tool" check --now "$now" "$bad" >/dev/null 2>&1 || rc=$?
  if [[ $rc -eq 3 ]]; then echo "ok   $(basename "$bad") rejected (exit 3)"; else echo "FAIL $bad: exit $rc, want 3"; fail=1; fi
done
rc=0; "$tool" check --now "$now" --warn-days 10 --crit-days 20 "$s/node-02.certs" >/dev/null 2>&1 || rc=$?
if [[ $rc -eq 3 ]]; then echo "ok   crit>=warn rejected (exit 3)"; else echo "FAIL threshold order: exit $rc"; fail=1; fi

# Live smoke: collect from a throwaway self-signed cert. Only asserts what is true on any runner.
if [[ ${LIVE:-1} != 1 ]]; then
  echo "SKIP live smoke: LIVE=0"
elif command -v openssl >/dev/null; then
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  openssl req -x509 -newkey rsa:2048 -nodes -days 5 -subj "/CN=smoke.example.com" \
    -keyout "$tmp/k.pem" -out "$tmp/c.pem" 2>/dev/null
  "$here/certscan.sh" collect --host smoke --file "$tmp/c.pem" >"$tmp/inv.certs"
  rc=0; out=$("$tool" check "$tmp/inv.certs") || rc=$?
  if [[ $rc -eq 2 && $out == *"CRIT file:$tmp/c.pem expires in "*"CN=smoke.example.com"* ]]; then
    echo "ok   live collect + check (5-day cert is CRIT)"
  else echo "FAIL live smoke: exit $rc"; echo "$out"; fail=1; fi
else
  echo "SKIP live smoke: openssl not installed"
fi
exit "$fail"
