#!/usr/bin/env bash
# certscan: TLS certificate expiry audit (inventory v1 -> report). See ../README.md for the contract.
#   certscan.sh check [--now ISO] [--warn-days N] [--crit-days N] INVENTORY
#   certscan.sh collect --host NAME [--file PEM]... [--endpoint HOST:PORT]...
# Exit: 0 OK, 1 WARN, 2 CRIT, 3 usage/runtime error.
set -Eeuo pipefail
export LC_ALL=C
trap 'echo "certscan: unexpected error at line $LINENO" >&2' ERR

readonly ISO_RE='^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]Z$'

# exit status of a finished check; a non-zero `return` would fire the ERR trap, so it travels in a global
RC=0

die() { echo "certscan: $*" >&2; exit 3; }

usage() {
  cat >&2 <<'USAGE'
usage: certscan.sh check [--now ISO] [--warn-days N] [--crit-days N] INVENTORY
       certscan.sh collect --host NAME [--file PEM]... [--endpoint HOST:PORT]...
USAGE
  exit 3
}

# to_epoch ISO8601-UTC -> epoch seconds on stdout (validated, GNU date)
to_epoch() {
  local ts=$1 out
  [[ $ts =~ $ISO_RE ]] || die "invalid timestamp: $ts"
  out=$(date -u -d "$ts" +%s 2>/dev/null) || die "invalid timestamp: $ts"
  printf '%s\n' "$out"
}

cmd_check() {
  local now="" warn=30 crit=14 file="" host="" lineno=0
  while (($#)); do
    case $1 in
      --now) now=${2:?--now needs a value}; shift 2 ;;
      --warn-days) warn=${2:?}; shift 2 ;;
      --crit-days) crit=${2:?}; shift 2 ;;
      -*) usage ;;
      *) [[ -z $file ]] || usage; file=$1; shift ;;
    esac
  done
  [[ -n $file ]] || usage
  [[ -r $file ]] || die "cannot read $file"
  [[ $warn =~ ^[0-9]+$ && $crit =~ ^[0-9]+$ ]] || die "thresholds must be non-negative integers"
  ((crit < warn)) || die "--crit-days must be lower than --warn-days"
  [[ -n $now ]] || now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  local now_e
  now_e=$(to_epoch "$now")

  local -a srcs=() nas=() subjs=() lines=()
  local src na subj extra line
  while IFS= read -r line || [[ -n $line ]]; do
    lineno=$((lineno + 1))
    if [[ $line == "# host="* ]]; then host=${line#\# host=}; continue; fi
    [[ -z $line || $line == "#"* ]] && continue
    IFS=$'\t' read -r src na subj extra <<<"$line"
    if [[ -n ${extra:-} || -z ${subj:-} || ! $src =~ ^(file|endpoint):. || ! $na =~ $ISO_RE ]]; then
      die "invalid line $lineno in $file"
    fi
    to_epoch "$na" >/dev/null
    srcs+=("$src"); nas+=("$na"); subjs+=("$subj")
  done <"$file"
  [[ -n $host ]] || die "missing '# host=' header in $file"
  ((${#srcs[@]})) || die "no certificates in $file"

  local i j s d ok=0 crit_n=0 warn_n=0 tail sev msg
  for i in "${!srcs[@]}"; do
    s=$(($(to_epoch "${nas[i]}") - now_e))
    if ((s >= 0)); then d=$((s / 86400)); else d=$((-((-s + 86399) / 86400))); fi
    tail=" (${nas[i]%%T*}) ${subjs[i]}"
    if ((d < 0)); then sev=CRIT; msg="expired $((-d))d ago"
    elif ((d <= crit)); then sev=CRIT; msg="expires in ${d}d"
    elif ((d <= warn)); then sev=WARN; msg="expires in ${d}d"
    else ok=$((ok + 1)); continue
    fi
    lines+=("$sev ${srcs[i]} $msg$tail")
    if [[ $sev == CRIT ]]; then crit_n=$((crit_n + 1)); else warn_n=$((warn_n + 1)); fi
  done
  # renewed on disk but the running service still serves the old certificate
  for i in "${!srcs[@]}"; do
    [[ ${srcs[i]} == endpoint:* ]] || continue
    for j in "${!srcs[@]}"; do
      [[ ${srcs[j]} == file:* && ${subjs[j]} == "${subjs[i]}" && ${nas[i]} < ${nas[j]} ]] || continue
      lines+=("WARN reload-needed ${subjs[i]} endpoint serves ${nas[i]%%T*} but file has ${nas[j]%%T*}")
      warn_n=$((warn_n + 1))
    done
  done

  local result=OK
  RC=0
  if ((crit_n > 0)); then result=CRIT; RC=2; elif ((warn_n > 0)); then result=WARN; RC=1; fi
  printf 'certscan report: %s\n' "$host"
  if ((${#lines[@]})); then printf '%s\n' "${lines[@]}" | sort; fi
  printf 'RESULT %s crit=%d warn=%d ok=%d\n' "$result" "$crit_n" "$warn_n" "$ok"
}

# openssl x509 on stdin -> "notAfter-ISO<TAB>subject"
cert_fields() {
  local end subj iso
  local pem
  pem=$(cat)
  end=$(openssl x509 -noout -enddate <<<"$pem") || return 1
  subj=$(openssl x509 -noout -subject -nameopt RFC2253 <<<"$pem") || return 1
  iso=$(date -u -d "${end#notAfter=}" +%Y-%m-%dT%H:%M:%SZ) || return 1
  printf '%s\t%s\n' "$iso" "${subj#subject=}"
}

cmd_collect() {
  local host="" i
  local -a files=() eps=()
  while (($#)); do
    case $1 in
      --host) host=${2:?}; shift 2 ;;
      --file) files+=("${2:?}"); shift 2 ;;
      --endpoint) eps+=("${2:?}"); shift 2 ;;
      *) usage ;;
    esac
  done
  [[ -n $host ]] || usage
  command -v openssl >/dev/null || die "openssl not found"
  printf '# certscan inventory v1\n# host=%s\n' "$host"
  local f
  for i in "${!files[@]}"; do
    [[ -r ${files[i]} ]] || die "cannot read ${files[i]}"
    f=$(cert_fields <"${files[i]}") || die "not a certificate: ${files[i]}"
    printf 'file:%s\t%s\n' "${files[i]}" "$f"
  done
  for i in "${!eps[@]}"; do
    # a handshake that fails or hangs is an error, not a silent gap in the audit
    f=$(timeout 10 openssl s_client -connect "${eps[i]}" -servername "${eps[i]%%:*}" </dev/null 2>/dev/null | cert_fields) \
      || die "no certificate from ${eps[i]}"
    printf 'endpoint:%s\t%s\n' "${eps[i]}" "$f"
  done
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
