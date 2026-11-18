#!/usr/bin/env bash
# pendingtriage.sh: root-cause Pending pods from saved `oc get pods -A -o json` output.
#
#   pendingtriage.sh PODS_JSON
#
# Exit codes: 0 OK · 1 WARN · 2 CRIT · 3 usage/runtime error
# Report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # byte-order sort, same as Python/Go/Ansible

readonly PROG=${0##*/}

log()   { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()   { log ERROR "$*"; exit 3; }
usage() { sed -n '2,5p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

# One jq pass: one TSV record per Pending+unscheduled pod: rank  ns/pod  class  text
# rank 0 = CRIT (Selector/Storage/Taint dominant), 1 = WARN.
# shellcheck disable=SC2016  # jq program: $vars are jq's, not the shell's
JQ_FINDINGS='
def orq: if . == null or . == "" then "?" else . end;
def hasany($l; $words): any($words[]; . as $w | $l | contains($w));
def classify:
  . as $t | ($t | ascii_downcase) as $l
  | if ($t | contains("Insufficient ")) or ($t | contains("Too many pods")) then "Capacity"
    elif hasany($l; ["persistentvolume", "unbound", "volume node affinity"]) then "Storage"
    elif ($l | contains("taint")) then "Taint"
    elif hasany($l; ["node affinity", "node selector"]) then "Selector"
    elif hasany($l; ["pod affinity", "anti-affinity", "topology spread"]) then "Placement"
    elif ($l | contains("unschedulable")) then "Cordoned"
    elif ($l | contains("free ports")) then "Ports"
    else "Other" end;
def resource:
  if contains("Insufficient ") then split("Insufficient ")[1]
  elif contains("Too many pods") then "pods" else "" end;
def none($class): {class: $class, nodes: "-", causes: "-", need: ""};
# -> {class, nodes, causes, need}
def parse($reason; $msg):
  if $reason == "SchedulingGated" then none("Gated")
  else
    ([$msg | capture("^0/(?<n>[0-9]+) nodes are available: (?<rest>.*)$")] | first) as $m
    | if $m == null then none("Unparsed")
      else
        ($m.n | tonumber) as $total
        | ($m.rest | split(". ")[0] | rtrimstr(".")) as $body
        | [$body | split(", ")[] | select(. != "") as $raw
           | ([$raw | capture("^(?<n>[0-9]+) (?<t>.*)$")] | first) as $q
           | (if $q == null then {n: $total, t: $raw} else {n: ($q.n | tonumber), t: $q.t} end)
           | {c: (.t | classify), n: .n, r: (.t | resource)}] as $cl
        | if ($cl | length) == 0 then none("Unparsed")
          else
            ($cl | group_by(.c)
             | map({c: .[0].c,
                    n: (if .[0].c == "Capacity" then (map(.n) | max) else (map(.n) | add) end)})
             | sort_by([-.n, .c])) as $g
            | {class: $g[0].c, nodes: ($total | tostring),
               causes: ($g | map("\(.c):\(.n)") | join(",")),
               need: ([$cl[] | .r | select(. != "")] | unique | join("+"))}
          end
      end
  end;

.items[] as $p
| (($p.metadata.namespace | orq) + "/" + ($p.metadata.name | orq)) as $key
| select(($p.status.phase // "") == "Pending")
| ([($p.status.conditions // [])[] | select(.type == "PodScheduled" and .status == "False")] | first) as $c
| select($c != null)
| parse($c.reason // ""; $c.message // "") as $r
| (if ($r.class == "Selector" or $r.class == "Storage" or $r.class == "Taint") then 0 else 1 end) as $rank
| [$rank, $key, $r.class,
   (["CRIT", "WARN"][$rank] + " " + $r.class + " " + $key + " nodes=" + $r.nodes + " causes=" + $r.causes
    + (if $r.need == "" then "" else " need=" + $r.need end))]
| join("\t")
'

hint() {
  case $1 in
    Capacity)  echo "oc describe node | grep -A8 'Allocated resources'; add nodes or shrink requests" ;;
    Cordoned)  echo "oc get nodes; finish the maintenance, then oc adm uncordon" ;;
    Gated)     echo "scheduling gate (Kueue/quota admission?): check the queue and its quota" ;;
    Other)     echo "read the full event: oc describe pod" ;;
    Placement) echo "check pod (anti-)affinity and topologySpreadConstraints against node/zone count" ;;
    Ports)     echo "hostPort conflict: use a Service/NodePort or drop hostPort" ;;
    Selector)  echo "oc get nodes --show-labels; fix nodeSelector/affinity or label the nodes" ;;
    Storage)   echo "oc get pvc,pv,sc; check StorageClass, volumeBindingMode and the PV zone" ;;
    Taint)     echo "oc describe node | grep Taints; add a toleration or remove the taint" ;;
    Unparsed)  echo "read the full event: oc describe pod" ;;
  esac
}

main() {
  (( $# == 1 )) || usage
  local file=$1 name
  [[ -r $file ]] || die "$file: cannot read"
  command -v jq >/dev/null || die "jq is required"
  jq -e 'type == "object" and (.kind == "List" or .kind == "PodList") and (.items | type == "array")' "$file" >/dev/null 2>&1 ||
    die "$file: not a Pod List (expected kind List/PodList with items[])"

  name=${file##*/}; name=${name%.pods.json}; [[ $name == "${file##*/}" ]] && name=${name%.json}

  local pods records
  pods=$(jq '.items | length' "$file")
  records=$(jq -r "$JQ_FINDINGS" "$file" | sort -t "$(printf '\t')" -k1,1n -k2,2)

  printf 'pendingtriage report: %s\n' "$name"
  local crit=0 warn=0 rank class text
  local -a classes=()
  if [[ -n $records ]]; then
    while IFS=$'\t' read -r rank _ class text; do
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
