#!/usr/bin/env bash
# patchcheck.sh: patch-window drift check for one RHEL host.
#
#   patchcheck.sh snapshot [-o FILE]           capture kernel, mounts, listening ports, running services, fs usage
#   patchcheck.sh compare [-t PCT] PRE POST    report what changed between two snapshots
#
# Exit codes (compare): 0 OK · 1 WARN · 2 CRIT · 3 usage/runtime error
# Snapshot format and report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # same sort order as Python/Go/Ansible (byte order)

readonly PROG=${0##*/}
readonly REAL_FSTYPES="xfs ext4 ext3 btrfs vfat nfs nfs4"
RC=0

log()  { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()  { log ERROR "$*"; exit 3; }
usage() { sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

# ---------- snapshot ----------

list_mounts() {
  # /proc/mounts: device mountpoint fstype options ...
  awk -v types="$REAL_FSTYPES" '
    BEGIN { n = split(types, t, " "); for (i = 1; i <= n; i++) ok[t[i]] = 1 }
    ok[$3] { print "mount " $2 " " $3 }' /proc/mounts | sort -u
}

list_ports() {
  local hex
  if command -v ss >/dev/null 2>&1; then
    ss -Htln | awk '{ n = split($4, a, ":"); print "port tcp/" a[n] }' | sort -u
    return
  fi
  # Fallback without iproute: state 0A = LISTEN, local port is hex after the colon
  log WARN "ss not found, reading /proc/net/tcp*"
  for f in /proc/net/tcp /proc/net/tcp6; do
    [[ -r $f ]] || continue
    awk 'NR > 1 && $4 == "0A" { split($2, a, ":"); print a[2] }' "$f"
  done | while read -r hex; do
    printf 'port tcp/%d\n' "0x$hex"
  done | sort -u
}

list_services() {
  local out
  if ! out=$(systemctl list-units --type=service --state=running --no-legend --plain 2>/dev/null); then
    log WARN "systemctl unavailable (container?), no services recorded"
    return 0
  fi
  [[ -n $out ]] && awk '{ print "service " $1 }' <<<"$out" | sort -u
  return 0
}

list_fsuse() {
  local mp pct
  while read -r _ mp _; do
    pct=$(df -P -- "$mp" 2>/dev/null | awk 'NR == 2 { sub(/%/, "", $5); print $5 }') || continue
    [[ -n $pct ]] && printf 'fsuse %s %s\n' "$mp" "$pct"
  done < <(list_mounts)
}

cmd_snapshot() {
  local outfile="" opt
  OPTIND=1
  while getopts ":o:" opt; do
    case $opt in
      o) outfile=$OPTARG ;;
      *) usage ;;
    esac
  done
  {
    echo "# patchcheck snapshot v1"
    echo "# host=$(uname -n | cut -d. -f1) taken=$(date -u +%FT%TZ)"
    echo "kernel $(uname -r)"
    list_mounts
    list_ports
    list_services
    list_fsuse
  } > "${outfile:-/dev/stdout}"
  [[ -n $outfile ]] && log INFO "snapshot written to $outfile"
  return 0
}

# ---------- compare ----------

items()  { grep -E '^(mount|port|service) ' "$1" | sort -u || true; }
kernel() { awk '$1 == "kernel" { print $2; exit }' "$1"; }

cmd_compare() {
  local warn_pct=90 opt
  OPTIND=1
  while getopts ":t:" opt; do
    case $opt in
      t) [[ $OPTARG =~ ^[0-9]+$ ]] || die "-t needs a number"; warn_pct=$OPTARG ;;
      *) usage ;;
    esac
  done
  shift $((OPTIND - 1))
  (($# == 2)) || usage
  local pre=$1 post=$2 f
  for f in "$pre" "$post"; do
    [[ -r $f ]] || die "cannot read $f"
    [[ $(head -n1 -- "$f") == "# patchcheck snapshot v1" ]] || die "$f is not a v1 snapshot"
  done

  local host k_pre k_post line mp pct crit=0 warn=0 missing new hot
  host=$(awk '$1 == "#" && $2 ~ /^host=/ { sub(/^host=/, "", $2); print $2; exit }' "$post")
  k_pre=$(kernel "$pre"); k_post=$(kernel "$post")
  missing=$(comm -23 <(items "$pre") <(items "$post"))
  new=$(comm -13 <(items "$pre") <(items "$post"))
  hot=$(awk -v t="$warn_pct" '$1 == "fsuse" && $3 + 0 >= t + 0 { print $2 " " $3 }' "$post" | sort)

  echo "patchcheck report: ${host:-unknown}"
  if [[ -n $k_pre && -n $k_post ]]; then
    if [[ $k_pre == "$k_post" ]]; then
      echo "INFO kernel unchanged $k_post"
    else
      echo "INFO kernel $k_pre -> $k_post"
    fi
  fi
  while IFS= read -r line; do
    [[ -n $line ]] || continue
    echo "CRIT missing $line"; crit=$((crit + 1))      # not ((crit++)): returns 1 when crit was 0 under set -e
  done <<<"$missing"
  while IFS= read -r line; do
    [[ -n $line ]] || continue
    echo "WARN new $line"; warn=$((warn + 1))
  done <<<"$new"
  while read -r mp pct; do
    [[ -n ${mp:-} ]] || continue
    echo "WARN fs $mp at $pct% (>= $warn_pct%)"; warn=$((warn + 1))
  done <<<"$hot"

  local result=OK rc=0
  if ((crit > 0)); then result=CRIT; rc=2
  elif ((warn > 0)); then result=WARN; rc=1
  fi
  echo "RESULT $result crit=$crit warn=$warn"
  RC=$rc   # returned via a global: a non-zero `return` would trip `set -e` / the ERR trap in the caller
}

main() {
  (($# >= 1)) || usage
  local sub=$1; shift
  case $sub in
    snapshot) cmd_snapshot "$@" ;;
    compare)  cmd_compare "$@"; exit "$RC" ;;
    -h|--help|help) usage ;;
    *) die "unknown subcommand: $sub" ;;
  esac
}

main "$@"
