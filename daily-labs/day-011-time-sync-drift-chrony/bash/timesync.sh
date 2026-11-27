#!/usr/bin/env bash
# timesync: chrony clock-drift audit (inventory v1 -> report). See ../README.md for the contract.
#   timesync.sh check [--warn-ms N] [--crit-ms N] INVENTORY
#   timesync.sh collect --host NAME
# Exit: 0 OK, 1 WARN, 2 CRIT, 3 usage/runtime error.
set -Eeuo pipefail
export LC_ALL=C
trap 'echo "timesync: unexpected error at line $LINENO" >&2' ERR

readonly MAX_STRATUM=5
readonly MIN_SOURCES=3
readonly RE_LEAP='^Leap status *: *(.+)$'
readonly RE_STRATUM='^Stratum *: *([0-9]+)$'
readonly RE_SYSTIME='^System time *: *([0-9]+)\.([0-9]{9}) seconds (fast|slow) of NTP time$'
readonly RE_SOURCE='^[=^]([*+?x~-]) +([^ ]+) +([0-9]+) +([0-9]+) +([0-7]+) +[^ ]+( .*)?$'

# exit status of a finished check; a non-zero `return` would fire the ERR trap, so it travels in a global
RC=0

die() { echo "timesync: $*" >&2; exit 3; }

usage() {
  cat >&2 <<'USAGE'
usage: timesync.sh check [--warn-ms N] [--crit-ms N] INVENTORY
       timesync.sh collect --host NAME
USAGE
  exit 3
}

cmd_check() {
  local warn=10 crit=100 file="" line lineno=0 section="" host=""
  while (($#)); do
    case $1 in
      --warn-ms) warn=${2:?--warn-ms needs a value}; shift 2 ;;
      --crit-ms) crit=${2:?--crit-ms needs a value}; shift 2 ;;
      -*) usage ;;
      *) [[ -z $file ]] || usage; file=$1; shift ;;
    esac
  done
  [[ -n $file ]] || usage
  [[ -r $file ]] || die "cannot read $file"
  [[ $warn =~ ^[0-9]+$ && $crit =~ ^[0-9]+$ ]] || die "thresholds must be non-negative integers"
  ((crit > warn)) || die "--crit-ms must be higher than --warn-ms"

  local leap="" stratum="" off_ns="" dir="" nsrc=0 selected=0
  local -a findings=()
  while IFS= read -r line || [[ -n $line ]]; do
    lineno=$((lineno + 1))
    case $line in
      "# host="*) host=${line#\# host=}; continue ;;
      "# chronyc tracking") section=tracking; continue ;;
      "# chronyc sources") section=sources; continue ;;
      "" | "#"*) continue ;;
    esac
    case $section in
      tracking)
        if [[ $line =~ ^Leap\ status\ *: ]]; then
          [[ $line =~ $RE_LEAP ]] || die "invalid line $lineno in $file"
          leap=${BASH_REMATCH[1]}
        elif [[ $line =~ ^Stratum\ *: ]]; then
          [[ $line =~ $RE_STRATUM ]] || die "invalid line $lineno in $file"
          stratum=${BASH_REMATCH[1]}
        elif [[ $line =~ ^System\ time\ *: ]]; then
          [[ $line =~ $RE_SYSTIME ]] || die "invalid line $lineno in $file"
          off_ns=$((10#${BASH_REMATCH[1]} * 1000000000 + 10#${BASH_REMATCH[2]}))
          dir=${BASH_REMATCH[3]}
        fi
        ;;
      sources)
        [[ $line == "MS "* || $line =~ ^=+$ ]] && continue
        [[ $line =~ $RE_SOURCE ]] || die "invalid line $lineno in $file"
        local state=${BASH_REMATCH[1]} name=${BASH_REMATCH[2]} reach=${BASH_REMATCH[5]}
        nsrc=$((nsrc + 1))
        [[ $state == "*" ]] && selected=$((selected + 1))
        [[ $state == x ]] && findings+=("WARN source $name falseticker")
        [[ $reach =~ ^0+$ ]] && findings+=("WARN source $name unreachable (reach 0)")
        ;;
      *) die "invalid line $lineno in $file" ;;
    esac
  done <"$file"
  [[ -n $host ]] || die "missing '# host=' header in $file"
  [[ -n $leap && -n $stratum && -n $off_ns ]] || die "missing tracking fields in $file"
  ((nsrc > 0)) || die "no sources in $file"

  local us
  if [[ $leap == "Not synchronised" ]]; then
    # chrony still prints a stale offset and stratum 0 here; neither means anything
    findings+=("CRIT unsynchronised (leap status: $leap)")
  else
    us=$((off_ns / 1000))
    if ((off_ns > crit * 1000000)); then
      findings+=("$(printf 'CRIT offset %d.%03dms %s (crit %dms)' $((us / 1000)) $((us % 1000)) "$dir" "$crit")")
    elif ((off_ns > warn * 1000000)); then
      findings+=("$(printf 'WARN offset %d.%03dms %s (warn %dms)' $((us / 1000)) $((us % 1000)) "$dir" "$warn")")
    fi
    ((stratum > MAX_STRATUM)) && findings+=("WARN stratum $stratum (max $MAX_STRATUM)")
  fi
  ((selected > 0)) || findings+=("WARN no selected source")
  ((nsrc >= MIN_SOURCES)) || findings+=("WARN sources $nsrc (need at least $MIN_SOURCES to out-vote a falseticker)")

  local crit_n=0 warn_n=0 f
  for f in "${findings[@]}"; do
    case $f in CRIT*) crit_n=$((crit_n + 1)) ;; WARN*) warn_n=$((warn_n + 1)) ;; esac
  done
  local result=OK
  RC=0
  if ((crit_n > 0)); then result=CRIT; RC=2; elif ((warn_n > 0)); then result=WARN; RC=1; fi
  printf 'timesync report: %s\n' "$host"
  if ((${#findings[@]})); then printf '%s\n' "${findings[@]}" | sort; fi
  printf 'RESULT %s crit=%d warn=%d sources=%d\n' "$result" "$crit_n" "$warn_n" "$nsrc"
}

cmd_collect() {
  local host=""
  while (($#)); do
    case $1 in
      --host) host=${2:?--host needs a value}; shift 2 ;;
      *) usage ;;
    esac
  done
  [[ -n $host ]] || usage
  command -v chronyc >/dev/null || die "chronyc not found"
  local tracking sources
  # a dead chronyd is an error, not an empty inventory that looks like "no findings"
  tracking=$(chronyc tracking) || die "chronyc tracking failed (is chronyd running?)"
  sources=$(chronyc sources) || die "chronyc sources failed (is chronyd running?)"
  printf '# timesync inventory v1\n# host=%s\n# chronyc tracking\n%s\n# chronyc sources\n%s\n' "$host" "$tracking" "$sources"
}

main() {
  local sub=${1:-}
  [[ -n $sub ]] || usage
  shift
  case $sub in
    check) cmd_check "$@" ;;
    collect) cmd_collect "$@" ;;
    *) usage ;;
  esac
}
main "$@"
exit "$RC"
