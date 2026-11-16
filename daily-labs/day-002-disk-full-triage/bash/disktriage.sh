#!/usr/bin/env bash
# disktriage.sh: disk-full triage for one RHEL host.
#
#   disktriage.sh snapshot [-o FILE] [-m DU_MIN_PCT] [-l LOGDIR]   capture fs usage, top dirs, deleted-open files, rotated logs
#   disktriage.sh report [-w PCT] [-c PCT] [-r DAYS] SNAPSHOT       triage report + safe cleanup plan
#
# Exit codes (report): 0 OK · 1 WARN · 2 CRIT · 3 usage/runtime error
# Snapshot format and report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # byte-order sort, same as Python/Go/Ansible

readonly PROG=${0##*/}
readonly HEADER="# disktriage snapshot v1"
readonly REAL_FSTYPES="xfs ext4 ext3 btrfs vfat nfs nfs4"
readonly TAB=$'\t'

log()   { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()   { log ERROR "$*"; exit 3; }
usage() { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

num() { [[ $1 =~ ^[0-9]+$ ]] || die "$2 needs a whole number, got '$1'"; }

# ---------- snapshot ----------

list_mounts() {   # "mountpoint fstype", real filesystems only, one line per mountpoint
  awk -v types="$REAL_FSTYPES" '
    BEGIN { n = split(types, t, " "); for (i = 1; i <= n; i++) ok[t[i]] = 1 }
    ok[$3] && !seen[$2]++ { print $2, $3 }' /proc/mounts | sort
}

snap_fs() {   # fs lines + du lines (only for filesystems >= du_min %)
  local du_min=$1 mp fstype used avail iused ifree
  local -a du_targets=()
  while read -r mp fstype; do
    read -r used avail < <(df -P -k -- "$mp" 2>/dev/null | awk 'NR == 2 { print $3, $4 }') || continue
    read -r iused ifree < <(df -P -i -- "$mp" 2>/dev/null | awk 'NR == 2 { print $3, $4 }') || true
    [[ ${iused:-} =~ ^[0-9]+$ ]] || iused=0     # vfat: df -i prints "-" (no inodes)
    [[ ${ifree:-} =~ ^[0-9]+$ ]] || ifree=0
    printf 'fs %s %s %s %s %s %s\n' "$mp" "$fstype" "$used" "$avail" "$iused" "$ifree"
    if (( used + avail > 0 && (used * 100 + used + avail - 1) / (used + avail) >= du_min )); then
      du_targets+=("$mp")
    fi
  done < <(list_mounts)
  for mp in "${du_targets[@]}"; do
    # -x: stay on this filesystem; -d 1: first level only. du exits 1 on unreadable dirs: still useful.
    { timeout 300 du -xk -d 1 -- "$mp" 2>/dev/null || true; } |
      awk -F "$TAB" -v mp="$mp" '$2 != mp { print "du " $1 " " $2 }'
  done
}

snap_deleted() {   # files that are deleted but still held open: df counts them, du cannot see them
  local fd target pid ino blocks comm
  { find /proc/[0-9]*/fd -mindepth 1 -maxdepth 1 -lname '/*(deleted)' 2>/dev/null || true; } |
  while IFS= read -r fd; do
    target=$(readlink -- "$fd" 2>/dev/null) || continue
    [[ $target == /memfd:* ]] && continue                 # anonymous memory, not disk
    read -r ino blocks < <(stat -L -c '%i %b' -- "$fd" 2>/dev/null) || continue
    [[ -f $fd ]] || continue                              # regular files only
    pid=${fd#/proc/}; pid=${pid%%/*}
    comm=$(tr ' ' '_' < "/proc/$pid/comm" 2>/dev/null) || comm="?"
    printf 'deleted %s %s %s %s %s\n' "$pid" "$ino" "$((blocks / 2))" "${comm:-?}" "${target% (deleted)}"
  done | sort -u -k2,2n -k3,3n
}

snap_rotated() {   # rotated logs: safe cleanup candidates (never the live log)
  local logdir=$1 now
  now=$(date +%s)
  { find "$logdir" -xdev -type f -regextype posix-extended \
      -regex '.*(\.gz|\.xz|\.bz2|\.[0-9]+|-[0-9]{8})' -printf '%T@ %b %p\n' 2>/dev/null || true; } |
    awk -v now="$now" '{ t = $1; b = $2; p = $0; sub(/^[^ ]+ [^ ]+ /, "", p)
                         printf "rotated %d %d %s\n", int((now - t) / 86400), int(b / 2), p }' | sort -k4
}

cmd_snapshot() {
  local outfile="" du_min=80 logdir=/var/log opt
  OPTIND=1
  while getopts ":o:m:l:" opt; do
    case $opt in
      o) outfile=$OPTARG ;;
      m) num "$OPTARG" -m; du_min=$OPTARG ;;
      l) logdir=$OPTARG ;;
      *) usage ;;
    esac
  done
  {
    echo "$HEADER"
    echo "# host=$(uname -n | cut -d. -f1) taken=$(date -u +%FT%TZ)"
    snap_fs "$du_min"
    snap_deleted
    snap_rotated "$logdir"
  } > "${outfile:-/dev/stdout}"
  [[ -n $outfile ]] && log INFO "snapshot written to $outfile"
  return 0
}

# ---------- report ----------
# Stage 1 (awk): parse + analyse, emit one tab-separated record per output line with its sort keys:
#   sevrank  mount  kind(0 space,1 inodes)  section(0 head,1 top,2 deleted,3 rotated,4 after,5 needs)  kb  tiebreak  text
# Stage 2 (sort): severity, mount (byte order), kind, section, size desc, tiebreak.
# Stage 3 (awk): keep top 3 "top" lines per finding, count, print RESULT.

# shellcheck disable=SC2016  # awk program: $fields are awk's, not the shell's
ANALYSE='
function big(x)     { return sprintf("%.0f", x) }        # mawk-safe printing of values > 2^31
function mib(kb)    { return big(int(kb / 1024)) }
function cpct(a, t,  n) { if (t <= 0) return 0; n = a * 100; return (n - n % t) / t + (n % t > 0) }
function sevof(p)   { return p >= crit ? "CRIT" : (p >= warn ? "WARN" : "") }
function owner(p,  i, m, best) {                         # longest prefix on whole path components
  best = ""
  for (i = 1; i <= nfs; i++) {
    m = mnt[i]
    if ((m == "/" || p == m || substr(p, 1, length(m) + 1) == m "/") && length(m) > length(best)) best = m
  }
  return best
}
function rest(line, skip,  i) { for (i = 0; i < skip; i++) sub(/^[^ ]+ /, "", line); return line }
function isnum(s)   { return s ~ /^[0-9]+$/ }
function bad()      { printf "line %d: malformed %s line: %s\n", NR, $1, $0 > "/dev/stderr"; failed = 1; exit 3 }
function emit(sev, m, kind, sec, kb, tie, text) {
  printf "%d\t%s\t%d\t%d\t%s\t%s\t%s\n", sev, m, kind, sec, big(kb), tie, text
}
NR == 1 { if ($0 != "# disktriage snapshot v1") { print "not a disktriage v1 snapshot" > "/dev/stderr"; failed = 1; exit 3 }; next }
/^# host=/ { host = substr($2, 6); next }
/^#/ || NF == 0 { next }
$1 == "fs" {
  if (NF != 7 || !isnum($4) || !isnum($5) || !isnum($6) || !isnum($7)) bad()
  nfs++; mnt[nfs] = $2; used[nfs] = $4; avail[nfs] = $5; iused[nfs] = $6; ifree[nfs] = $7; next
}
$1 == "du" { if (NF < 3 || !isnum($2)) bad(); ndu++; dukb[ndu] = $2; dup[ndu] = rest($0, 2); next }
$1 == "deleted" {
  if (NF < 6 || !isnum($2) || !isnum($3) || !isnum($4)) bad()
  nd++; dpid[nd] = $2; dino[nd] = $3; dkb[nd] = $4; dcomm[nd] = $5; dp[nd] = rest($0, 5); next
}
$1 == "rotated" { if (NF < 4 || !isnum($2) || !isnum($3)) bad(); nr++; rage[nr] = $2; rkb[nr] = $3; rp[nr] = rest($0, 3); next }
END {
  if (failed) exit 3
  emit(-1, "", 0, 0, 0, "", "disktriage report: " (host == "" ? "unknown" : host))
  for (i = 1; i <= nfs; i++) {
    m = mnt[i]; tot = used[i] + avail[i]; pct = cpct(used[i], tot); sev = sevof(pct)
    if (sev != "") {
      r = (sev == "CRIT") ? 0 : 1
      emit(r, m, 0, 0, 0, "", sev " space " m " " pct "% used, " mib(avail[i]) " MiB free")
      for (j = 1; j <= ndu; j++) if (owner(dup[j]) == m) emit(r, m, 0, 1, dukb[j], dup[j], "  top " mib(dukb[j]) " MiB " dup[j])
      delkb = 0; split("", seen)
      for (j = 1; j <= nd; j++) if (owner(dp[j]) == m) {
        emit(r, m, 0, 2, dkb[j], sprintf("%010d", dpid[j]), "  deleted " mib(dkb[j]) " MiB pid=" dpid[j] " " dcomm[j] " " dp[j])
        if (!(dino[j] in seen)) { seen[dino[j]] = 1; delkb += dkb[j] }   # two pids, one inode: freed once
      }
      rotkb = 0; rotn = 0
      for (j = 1; j <= nr; j++) if (owner(rp[j]) == m && rage[j] >= retain) { rotn++; rotkb += rkb[j] }
      if (rotn > 0) emit(r, m, 0, 3, 0, "", "  rotated " rotn " files " mib(rotkb) " MiB older than " retain "d")
      freed = delkb + rotkb; left = used[i] - freed; if (left < 0) left = 0
      after = cpct(left, tot)
      if (freed > 0) emit(r, m, 0, 4, 0, "", "  after cleanup " after "% (deleted-open " mib(delkb) " MiB + rotated " mib(rotkb) " MiB)")
      if (after >= warn) emit(r, m, 0, 5, 0, "", "  needs capacity: still " after "% after cleanup (>= " warn "%)")
    }
    ipct = cpct(iused[i], iused[i] + ifree[i]); isev = sevof(ipct)   # vfat: 0/0 inodes -> 0%
    if (isev != "") emit(isev == "CRIT" ? 0 : 1, m, 1, 0, 0, "", isev " inodes " m " " ipct "% used")
  }
}'

# shellcheck disable=SC2016
FINISH='
$4 == 1 && ++top[$1 SUBSEP $2 SUBSEP $3] > 3 { next }
$4 == 0 && $1 == 0 { c++ }
$4 == 0 && $1 == 1 { w++ }
{ print $7 }
END { printf "RESULT %s crit=%d warn=%d\n", (c > 0 ? "CRIT" : (w > 0 ? "WARN" : "OK")), c, w }'

cmd_report() {
  local warn=85 crit=95 retain=7 opt
  OPTIND=1
  while getopts ":w:c:r:" opt; do
    case $opt in
      w) num "$OPTARG" -w; warn=$OPTARG ;;
      c) num "$OPTARG" -c; crit=$OPTARG ;;
      r) num "$OPTARG" -r; retain=$OPTARG ;;
      *) usage ;;
    esac
  done
  shift $((OPTIND - 1))
  (($# == 1)) || usage
  [[ -r $1 ]] || die "cannot read $1"

  local out
  out=$(awk -v warn="$warn" -v crit="$crit" -v retain="$retain" "$ANALYSE" "$1" |
        sort -t "$TAB" -k1,1n -k2,2 -k3,3n -k4,4n -k5,5nr -k6,6 |
        awk -F "$TAB" "$FINISH") || die "invalid snapshot $1"
  printf '%s\n' "$out"
  case ${out##*RESULT } in
    CRIT*) RC=2 ;;
    WARN*) RC=1 ;;
    *)     RC=0 ;;
  esac
}

RC=0
main() {
  (($# >= 1)) || usage
  local sub=$1; shift
  case $sub in
    snapshot) cmd_snapshot "$@" ;;
    report)   cmd_report "$@"; exit "$RC" ;;   # exit code via a global: a non-zero return would trip set -e / ERR
    -h|--help|help) usage ;;
    *) die "unknown subcommand: $sub" ;;
  esac
}

main "$@"
