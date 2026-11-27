#!/usr/bin/env bash
# Golden test for timesync.sh: byte-identical reports + exit codes, error handling, and a collect test
# that runs against a fake `chronyc` on PATH (so it behaves the same on every runner, with or without chrony).
set -Eeuo pipefail
export LC_ALL=C
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
s="$here/../samples"
tool=${TIMESYNC:-$here/timesync.sh}
fail=0

check_case() { # host expected_rc
  local out rc=0
  out=$("$tool" check "$s/$1.chrony") || rc=$?
  if [[ $rc -ne $2 ]]; then echo "FAIL $1: exit $rc, want $2"; fail=1; return; fi
  if [[ $out != "$(cat "$s/expected/$1.report")" ]]; then
    echo "FAIL $1: report differs"; diff <(echo "$out") "$s/expected/$1.report" || true; fail=1; return
  fi
  echo "ok   $1 (exit $rc)"
}
check_case node-01 2
check_case node-02 0
check_case node-03 1
check_case node-04 2
check_case node-05 1

for bad in "$s"/bad/*.chrony; do
  rc=0; "$tool" check "$bad" >/dev/null 2>&1 || rc=$?
  if [[ $rc -eq 3 ]]; then echo "ok   $(basename "$bad") rejected (exit 3)"; else echo "FAIL $bad: exit $rc, want 3"; fail=1; fi
done
rc=0; "$tool" check --warn-ms 100 --crit-ms 100 "$s/node-02.chrony" >/dev/null 2>&1 || rc=$?
if [[ $rc -eq 3 ]]; then echo "ok   crit<=warn rejected (exit 3)"; else echo "FAIL threshold order: exit $rc"; fail=1; fi

# thresholds are strict ">": node-02 sits exactly on 10ms and stays OK; --warn-ms 9 flips it to WARN
rc=0; "$tool" check --warn-ms 9 "$s/node-02.chrony" >/dev/null || rc=$?
if [[ $rc -eq 1 ]]; then echo "ok   10.000ms is OK at warn=10 and WARN at warn=9"; else echo "FAIL boundary: exit $rc"; fail=1; fi

# collect: fake chronyc that replays node-02's saved sections, so the output must equal the fixture byte for byte
if ! command -v awk >/dev/null; then
  echo "SKIP collect test: awk not installed"
else
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  cat >"$tmp/chronyc" <<FAKE
#!/usr/bin/env bash
awk -v want="\$1" '
  /^# chronyc / { sec = (\$3 == want); next }
  /^#/ { next }
  sec { print }
' "$s/node-02.chrony"
FAKE
  chmod +x "$tmp/chronyc"
  PATH="$tmp:$PATH" "$tool" collect --host node-02 >"$tmp/inv.chrony"
  if cmp -s "$tmp/inv.chrony" "$s/node-02.chrony"; then echo "ok   collect reproduces the fixture"; else
    echo "FAIL collect output differs"; diff "$tmp/inv.chrony" "$s/node-02.chrony" || true; fail=1; fi
  printf '#!/usr/bin/env bash\nexit 1\n' >"$tmp/chronyc"
  rc=0; PATH="$tmp:$PATH" "$tool" collect --host x >/dev/null 2>&1 || rc=$?
  if [[ $rc -eq 3 ]]; then echo "ok   collect fails loudly when chronyd is down (exit 3)"; else echo "FAIL collect with dead chronyd: exit $rc"; fail=1; fi
fi
exit "$fail"
