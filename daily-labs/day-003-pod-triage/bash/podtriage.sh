#!/usr/bin/env bash
# podtriage.sh: classify unhealthy pods from saved `oc get pods -A -o json` output.
#
#   podtriage.sh [-r RESTART_WARN] PODS_JSON
#
# Exit codes: 0 OK · 1 WARN · 2 CRIT · 3 usage/runtime error
# Report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # byte-order sort, same as Python/Go/Ansible

readonly PROG=${0##*/}

log()   { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()   { log ERROR "$*"; exit 3; }
usage() { sed -n '2,6p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

# One jq pass: one TSV record per finding: rank  ns/pod  container  class  text
# rank 0 = CRIT, 1 = WARN. First matching rule wins (same order as the other three languages).
# shellcheck disable=SC2016  # jq program: $vars are jq's, not the shell's
JQ_FINDINGS='
def orq: if . == null or . == "" then "?" else . end;
def sev: if . == 0 then "CRIT" else "WARN" end;
def row($rank; $key; $ctr; $class; $detail):
  [$rank, $key, $ctr, $class,
   ([$rank | sev, $class, $key] + (if $ctr == "-" then [] else [$ctr] end) + [$detail] | join(" "))]
  | join("\t");
.items[] as $p
| (($p.metadata.namespace | orq) + "/" + ($p.metadata.name | orq)) as $key
| ($p.status.phase // "") as $phase
| if $phase == "Failed" and ($p.status.reason // "") == "Evicted" then
    row(1; $key; "-"; "Evicted"; "- node=" + ($p.spec.nodeName | orq))
  elif $phase == "Running" or $phase == "Pending" then
    ($p.status.containerStatuses // [])[] as $cs
    | ($cs.name | orq) as $ctr
    | ($cs.state.waiting.reason // "") as $w
    | ($cs.lastState.terminated // {}) as $last
    | ($cs.state.terminated // null) as $cur
    | ($cs.restartCount // 0) as $n
    | if $w == "ImagePullBackOff" or $w == "ErrImagePull" then
        row(0; $key; $ctr; "ImagePullBackOff"; "image=" + ($cs.image | orq))
      elif $w == "CreateContainerConfigError" or $w == "CreateContainerError" then
        row(0; $key; $ctr; "ContainerConfigError"; "reason=" + $w)
      elif ($last.reason // "") == "OOMKilled" or ($cur.reason // "") == "OOMKilled" then
        ([($p.spec.containers // [])[] | select(.name == $cs.name) | .resources.limits.memory // "none"] | last // "none") as $limit
        | row(if $w != "" or $cur != null then 0 else 1 end; $key; $ctr; "OOMKilled"; "restarts=\($n) limit=\($limit)")
      elif $w == "CrashLoopBackOff" then
        row(0; $key; $ctr; "CrashLoopBackOff"; "restarts=\($n) exit=\($last.exitCode // "?")")
      elif $n >= $rw then
        row(1; $key; $ctr; "HighRestarts"; "restarts=\($n)")
      else empty end
  else empty end
'

hint() {
  case $1 in
    ContainerConfigError) echo "oc describe pod: missing Secret/ConfigMap key or bad securityContext" ;;
    CrashLoopBackOff)     echo "oc logs -p <pod> -c <container>; check exit code, probes, startup deps" ;;
    Evicted)              echo "node pressure: oc describe node; set ephemeral-storage requests/limits" ;;
    HighRestarts)         echo "oc logs -p; check liveness probe and exit codes before it becomes a CrashLoop" ;;
    ImagePullBackOff)     echo "oc describe pod: image tag, pull secret, registry reachability" ;;
    OOMKilled)            echo "compare limit with working set (oc adm top pod); raise limit or fix the leak" ;;
  esac
}

main() {
  local rw=5 opt
  OPTIND=1
  while getopts ":r:" opt; do
    case $opt in
      r) if [[ $OPTARG =~ ^[0-9]+$ ]] && (( OPTARG >= 1 )); then rw=$OPTARG; else die "-r needs a whole number >= 1, got '$OPTARG'"; fi ;;
      *) usage ;;
    esac
  done
  shift $((OPTIND - 1))
  (( $# == 1 )) || usage
  local file=$1 name
  [[ -r $file ]] || die "$file: cannot read"
  command -v jq >/dev/null || die "jq is required"
  jq -e 'type == "object" and (.kind == "List" or .kind == "PodList") and (.items | type == "array")' "$file" >/dev/null 2>&1 ||
    die "$file: not a Pod List (expected kind List/PodList with items[])"

  name=${file##*/}; name=${name%.pods.json}; [[ $name == "${file##*/}" ]] && name=${name%.json}

  local pods records
  pods=$(jq '.items | length' "$file")
  records=$(jq -r --argjson rw "$rw" "$JQ_FINDINGS" "$file" | sort -t "$(printf '\t')" -k1,1n -k2,2 -k3,3)

  printf 'podtriage report: %s\n' "$name"
  local crit=0 warn=0 rank class text
  local -a classes=()
  if [[ -n $records ]]; then
    while IFS=$'\t' read -r rank _ _ class text; do
      printf '%s\n' "$text"
      if (( rank == 0 )); then crit=$((crit + 1)); else warn=$((warn + 1)); fi
      classes+=("$class")
    done <<< "$records"
    while IFS= read -r class; do printf 'HINT %s: %s\n' "$class" "$(hint "$class")"; done \
      < <(printf '%s\n' "${classes[@]}" | sort -u)
  fi
  local result=OK code=0
  if (( crit > 0 )); then result=CRIT code=2; elif (( warn > 0 )); then result=WARN code=1; fi
  printf 'RESULT %s crit=%d warn=%d pods=%d\n' "$result" "$crit" "$warn" "$pods"
  exit "$code"                          # exit, not return: a non-zero return would trip the ERR trap
}

main "$@"
