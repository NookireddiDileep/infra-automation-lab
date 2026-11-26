#!/usr/bin/env bash
# upgradegate.sh: pre-upgrade gate for OpenShift (saved `oc get co` + `oc get mcp`).
#
#   upgradegate.sh CLUSTEROPERATORS_JSON MCPS_JSON
#
# CLUSTEROPERATORS_JSON = saved `oc get clusteroperators -o json`
# MCPS_JSON             = saved `oc get machineconfigpools -o json`
# Exit codes: 0 GO · 1 CAUTION (WARN) · 2 NO-GO (CRIT) · 3 usage/input error
# Report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # byte-order sort, same as Python/Go/Ansible

readonly PROG=${0##*/}

log()   { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()   { log ERROR "$*"; exit 3; }
usage() { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

# One jq pass. $m = slurped MCP list. Rows are sorted by (rank, subject); the sort is stable, input order breaks ties.
# shellcheck disable=SC2016  # jq program: $vars are jq's, not the shell's
JQ_REPORT='
def listof($what): if type == "object" and .kind == "List" and (.items | type) == "array" then [.items[] | select(type == "object")]
                   else error("\($what): not a List (expected kind List with items[])") end;
def obj: if type == "object" then . else {} end;
def txt: if type == "string" and . != "" then . else "-" end;
def name($what): (.metadata | obj | .name) as $n | if ($n | type) == "string" and $n != "" then $n else error("\($what): item without metadata.name") end;
def row($rank; $subj; $cls; $d): {rank: $rank, subject: $subj, cls: $cls, text: "\(["CRIT", "WARN"][$rank]) \($subj) \($cls) \($d)"};

# type -> {status, reason}; refuses anything that is not True/False/Unknown
def conds($what; $n):
  (.status | obj | .conditions) as $c
  | if $c == null then {}
    elif ($c | type) != "array" then error("\($what): bad conditions in \($n)")
    else [$c[] | if type == "object" and (.type | type) == "string" and (.status == "True" or .status == "False" or .status == "Unknown")
                 then {key: .type, value: {status: .status, reason: (.reason | txt)}}
                 else error("\($what): bad condition status in \($n)") end] | from_entries end;

def count($f; $n):
  (.status | obj | .[$f]) as $v
  | if ($v | type) == "number" and $v == ($v | floor) and $v >= 0 then $v else error("mcps: bad \($f) in \($n)") end;

def judge_co:
  name("clusteroperators") as $n | conds("clusteroperators"; $n) as $c | "co/\($n)" as $s
  | if $c.Available == null then row(0; $s; "NOSTATUS"; "reason=-")
    elif $c.Available.status != "True" then row(0; $s; "UNAVAILABLE"; "reason=\($c.Available.reason)")
    elif $c.Degraded.status == "True" then row(0; $s; "DEGRADED"; "reason=\($c.Degraded.reason)")
    elif $c.Upgradeable.status == "False" then row(0; $s; "BLOCKED"; "reason=\($c.Upgradeable.reason)")
    elif $c.Progressing.status == "True" then row(1; $s; "PROGRESSING"; "reason=\($c.Progressing.reason)")
    else empty end;

def judge_mcp:
  name("mcps") as $n | conds("mcps"; $n) as $c | "mcp/\($n)" as $s
  | . as $p
  | ($p | count("machineCount"; $n)) as $total | ($p | count("updatedMachineCount"; $n)) as $upd
  | ($p | count("readyMachineCount"; $n)) as $rdy | ($p | count("unavailableMachineCount"; $n)) as $un
  | ($p | count("degradedMachineCount"; $n)) as $dg
  | "degraded=\($dg) unavailable=\($un) updated=\($upd)/\($total)" as $d
  | if $dg > 0 or $c.Degraded.status == "True" then row(0; $s; "DEGRADED"; $d)
    elif $c.Updating.status == "True" then row(1; $s; "UPDATING"; $d)
    elif $un > 0 then row(0; $s; "UNAVAILABLE"; $d)
    elif (.spec | obj | .paused) == true then row(1; $s; "PAUSED"; $d)
    elif $upd < $total then row(0; $s; "STALLED"; $d)
    else empty end;

def hints: {
  UNAVAILABLE: "operator Available is not True, or pool nodes are down outside a rollout: oc describe co/mcp, oc get nodes; do not upgrade until it is resolved",
  NOSTATUS: "the operator reports no Available condition, so it is not reporting at all: check its pod and events in its own namespace",
  DEGRADED: "Degraded=True: oc describe co/<name> for an operator, oc describe mcp plus machine-config-daemon logs for a pool; fix it before the upgrade",
  BLOCKED: "Upgradeable=False blocks minor-version updates (patch updates still run): read the condition message, fix it or give the admin acknowledgement it asks for",
  STALLED: "updated is below machineCount, the pool is not updating and not paused: it is stuck, check nodes for a stale desiredConfig and the machine-config-controller logs",
  PROGRESSING: "an operator is mid-rollout: wait for Progressing=False, or the upgrade stacks a second rollout on top",
  UPDATING: "the pool is rolling out a rendered config: let it finish, a cluster upgrade now would queue a second change on the same nodes",
  PAUSED: "spec.paused=true: the pool ignores new rendered configs and stays on the old version; unpause before the upgrade unless it is a deliberate canary"
};

(listof("clusteroperators") | map(select(.kind == "ClusterOperator"))) as $cos
| ($m[0] | listof("mcps") | map(select(.kind == "MachineConfigPool"))) as $pools
| ([$cos[] | judge_co] + [$pools[] | judge_mcp] | sort_by([.rank, .subject])) as $rows
| ($rows | map(select(.rank == 0)) | length) as $crit
| ($rows | map(select(.rank == 1)) | length) as $warn
| ($rows | map(.cls) | unique) as $present
| "upgrade-gate report: \($name)",
  ($rows[] | .text),
  (["UNAVAILABLE", "NOSTATUS", "DEGRADED", "BLOCKED", "STALLED", "PROGRESSING", "UPDATING", "PAUSED"][] | select(. as $c | $present | index($c)) | "HINT \(.): \(hints[.])"),
  "RESULT \(if $crit > 0 then "CRIT verdict=NO-GO" elif $warn > 0 then "WARN verdict=CAUTION" else "OK verdict=GO" end) crit=\($crit) warn=\($warn) operators=\($cos | length) pools=\($pools | length) healthy=\(($cos | length) + ($pools | length) - ($rows | length))"
'

[[ $# -eq 2 ]] || usage
cos=$1 mcps=$2
[[ -r $cos ]]  || die "$cos: cannot read"
[[ -r $mcps ]] || die "$mcps: cannot read"
name=${cos##*/}
name=${name%.json}; name=${name%.clusteroperators}

err=$(mktemp); trap 'rm -f "$err"' EXIT
if ! report=$(jq -r --arg name "$name" --slurpfile m "$mcps" "$JQ_REPORT" "$cos" 2>"$err"); then
  die "$(sed -e 's/^jq: error (at [^)]*): //' -e 's/ at <[^>]*>.*$//' -e '1q' "$err")"
fi
printf '%s\n' "$report"

case ${report##*$'\n'} in
  "RESULT OK "*)   exit 0 ;;
  "RESULT WARN "*) exit 1 ;;
  "RESULT CRIT "*) exit 2 ;;
  *) die "no RESULT line in report" ;;
esac
