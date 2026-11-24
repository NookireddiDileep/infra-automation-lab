#!/usr/bin/env bash
# nodepressure.sh: node pressure & eviction report (Node conditions + kubelet Evicted events).
#
#   nodepressure.sh NODES_JSON EVENTS_JSON
#
# NODES_JSON  = saved `oc get nodes -o json`
# EVENTS_JSON = saved `oc get events -A -o json`
# Exit codes: 0 OK · 1 WARN · 2 CRIT · 3 usage/input error
# Report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # byte-order sort, same as Python/Go/Ansible

readonly PROG=${0##*/}

log()   { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()   { log ERROR "$*"; exit 3; }
usage() { sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

# One jq pass. $e = slurped events file. Emission order within a node is part of the contract.
# shellcheck disable=SC2016  # jq program: $vars are jq's, not the shell's
JQ_REPORT='
def listof($what): if type == "object" and .kind == "List" and (.items | type) == "array" then [.items[] | select(type == "object")]
                   else error("\($what): not a List (expected kind List with items[])") end;
def obj: if type == "object" then . else {} end;
def txt: if type == "string" and . != "" then . else "-" end;
def pressures: [["MemoryPressure", "MEMORYPRESSURE", "node.kubernetes.io/memory-pressure"],
                ["DiskPressure", "DISKPRESSURE", "node.kubernetes.io/disk-pressure"],
                ["PIDPressure", "PIDPRESSURE", "node.kubernetes.io/pid-pressure"]];

# host -> {resource: count}
def evictions:
  [ ($e[0] | listof("events"))[] | select(.reason == "Evicted")
    | (.source | obj | .host | if type == "string" and . != "" then . else error("events: Evicted event without source.host") end) as $h
    | (if has("count") then .count else 1 end) as $n
    | if ($n | type) != "number" or $n < 1 or $n != ($n | floor) then error("events: bad count in Evicted event on \($h)") else . end
    | {host: $h, res: ((.message | if type == "string" then (capture("low on resource: (?<r>[A-Za-z0-9-]+)\\.") | .r) else null end) // "unknown"), n: $n} ]
  | group_by(.host)
  | map({key: .[0].host, value: (group_by(.res) | map({key: .[0].res, value: (map(.n) | add)}) | from_entries)}) | from_entries;

def row($sev; $node; $cls; $d): {rank: (if $sev == "CRIT" then 0 else 1 end), node: $node, cls: $cls, text: "\($sev) \($node) \($cls) \($d)"};

def findings($ev):
  .metadata.name as $name
  | ([(.status | obj).conditions // [] | .[] | select(type == "object")
      | select(.type == "Ready" or .type == "MemoryPressure" or .type == "DiskPressure" or .type == "PIDPressure")
      | if (.status == "True" or .status == "False" or .status == "Unknown") then . else error("nodes: bad condition status in \($name): \(.type)") end
      | {key: .type, value: .}] | from_entries) as $c
  | ([(.spec | obj).taints // [] | .[] | obj | .key | select(type == "string")]) as $taints
  | ( ( $c.Ready as $r
        | if $r == null then row("CRIT"; $name; "NOTREADY"; "status=Missing reason=-")
          elif $r.status != "True" then row("CRIT"; $name; "NOTREADY"; "status=\($r.status) reason=\($r.reason | txt)")
          else empty end ),
      ( pressures[] as [$t, $cls, $taint]
        | select($c[$t].status == "True")
        | row("CRIT"; $name; $cls; "since=\($c[$t].lastTransitionTime | txt) reason=\($c[$t].reason | txt)"),
          (if ($taints | index($taint)) == null then row("WARN"; $name; "NOTAINT"; $taint) else empty end) ),
      ( ($ev[$name] // {}) as $m
        | ([$m[]] | add // 0) as $total
        | if $total > 0 then
            row((if $total >= 5 then "CRIT" else "WARN" end); $name; (if $total >= 5 then "EVICTIONSTORM" else "EVICTIONS" end);
                "total=\($total) " + ([$m | keys[] as $k | "\($k)=\($m[$k])"] | join(" ")))
          else empty end ) );

def hints: {
  NOTREADY: "kubelet is not posting Ready: oc describe node, oc adm node-logs NODE -u kubelet; pods are evicted after the unreachable toleration (300s)",
  MEMORYPRESSURE: "memory.available is under the eviction threshold: find the top consumers (oc adm top pods), fix requests/limits, check kubelet systemReserved",
  DISKPRESSURE: "nodefs/imagefs is low: crictl images / du on /var/lib/containers, rotate logs, check ephemeral-storage requests",
  PIDPRESSURE: "pid.available is low: find a fork bomb or leaking process (ps -eLf), raise podPidsLimit only after you know why",
  NOTAINT: "pressure is True but the node.kubernetes.io/*-pressure taint is missing: the scheduler may still place pods here",
  EVICTIONSTORM: "5+ evictions on one node: the pressure is chronic; cordon, drain and fix the cause before it takes the node down",
  EVICTIONS: "kubelet evicted pods here: the pressure came and went; compare victims'"'"' usage with their requests (BestEffort/Burstable go first)"
};

evictions as $ev
| [listof("nodes")[] | select(.kind == "Node")
   | if (.metadata | obj | .name | type) == "string" and .metadata.name != "" then . else error("nodes: Node without metadata.name") end] as $nodes
| ([$nodes | sort_by(.metadata.name)[] | findings($ev)] | to_entries | map(.value + {idx: .key}) | sort_by([.rank, .node, .idx])) as $rows
| ($rows | map(select(.rank == 0)) | length) as $crit
| ($rows | map(select(.rank == 1)) | length) as $warn
| ($rows | map(.cls) | unique) as $present
| "node-pressure report: \($name)",
  ($rows[] | .text),
  (["NOTREADY", "MEMORYPRESSURE", "DISKPRESSURE", "PIDPRESSURE", "NOTAINT", "EVICTIONSTORM", "EVICTIONS"][] | select(. as $c | $present | index($c)) | "HINT \(.): \(hints[.])"),
  "RESULT \(if $crit > 0 then "CRIT" elif $warn > 0 then "WARN" else "OK" end) crit=\($crit) warn=\($warn) nodes=\($nodes | length) healthy=\(($nodes | length) - ($rows | map(.node) | unique | length))"
'

[[ $# -eq 2 ]] || usage
nodes=$1 events=$2
[[ -r $nodes ]]  || die "$nodes: cannot read"
[[ -r $events ]] || die "$events: cannot read"
name=${nodes##*/}
name=${name%.json}; name=${name%.nodes}

err=$(mktemp); trap 'rm -f "$err"' EXIT
if ! report=$(jq -r --arg name "$name" --slurpfile e "$events" "$JQ_REPORT" "$nodes" 2>"$err"); then
  die "$(sed -e 's/^jq: error[^:]*: //' -e 's/ at <[^>]*>.*$//' -e '1q' "$err")"
fi
printf '%s\n' "$report"

case ${report##*$'\n'} in
  "RESULT OK "*)   exit 0 ;;
  "RESULT WARN "*) exit 1 ;;
  "RESULT CRIT "*) exit 2 ;;
  *) die "no RESULT line in report" ;;
esac
