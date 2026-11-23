#!/usr/bin/env bash
# rightsize.sh: ResourceQuota right-sizing report (quota hard/used vs real PodMetrics usage).
#
#   rightsize.sh QUOTA_JSON METRICS_JSON
#
# QUOTA_JSON   = saved `oc get resourcequota -A -o json`
# METRICS_JSON = saved `oc get --raw /apis/metrics.k8s.io/v1beta1/pods` (kind List of PodMetrics)
# Exit codes: 0 OK · 1 WARN · 2 CRIT · 3 usage/input error
# Report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # byte-order sort, same as Python/Go/Ansible

readonly PROG=${0##*/}

log()   { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()   { log ERROR "$*"; exit 3; }
usage() { sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

# One jq pass. $m = slurped metrics file. Emission order (quota, then key, then class) is part of the contract.
# shellcheck disable=SC2016  # jq program: $vars are jq's, not the shell's
JQ_REPORT='
def units: {"": 1, "Ki": 1024, "Mi": 1048576, "Gi": 1073741824, "Ti": 1099511627776};
def cpun:  {"n": 1, "u": 1000, "m": 1000000, "": 1000000000};
# cpu -> millicores, mem -> bytes, int -> int; null = not a valid quantity (anything but a string is invalid)
def qty($kind):
  (if type == "string" then . else "" end) as $s
  | if $kind == "cpu" then
      (if ($s | test("^[0-9]+m?$")) then (if ($s | endswith("m")) then ($s[:-1] | tonumber) else (($s | tonumber) * 1000) end) else null end)
    elif $kind == "mem" then
      (if ($s | test("^[0-9]+(Ki|Mi|Gi|Ti)?$")) then
         ($s | capture("^(?<n>[0-9]+)(?<u>Ki|Mi|Gi|Ti)?$")) as $x | ($x.n | tonumber) * units[$x.u // ""]
       else null end)
    else (if ($s | test("^[0-9]+$")) then ($s | tonumber) else null end) end;
def raw: if type == "string" then . else tojson end;
def listof($what): if type == "object" and .kind == "List" and (.items | type) == "array" then [.items[] | select(type == "object")]
                   else error("\($what): not a List (expected kind List with items[])") end;
def obj: if type == "object" then . else {} end;

# namespace -> {cpu: millicores, mem: bytes}
def usage_by_ns:
  [ listof("metrics")[] | select(.kind == "PodMetrics")
    | (.metadata.namespace | if type == "string" and . != "" then . else error("metrics: PodMetrics without metadata.namespace") end) as $ns
    | { ns: $ns,
        cpu: ([(.containers // [])[] | (.usage // {}) as $u
               | ($u.cpu | if type == "string" and test("^[0-9]+(n|u|m)?$") then capture("^(?<n>[0-9]+)(?<u>n|u|m)?$") as $x | ($x.n | tonumber) * cpun[$x.u // ""]
                           else error("metrics: bad container usage in \($ns): cpu=\(tojson)") end)] | add // 0),
        mem: ([(.containers // [])[] | (.usage // {}) as $u
               | ($u.memory | if type == "string" and test("^[0-9]+(Ki|Mi|Gi)?$") then capture("^(?<n>[0-9]+)(?<u>Ki|Mi|Gi)?$") as $x | ($x.n | tonumber) * units[$x.u // ""]
                              else error("metrics: bad container usage in \($ns): memory=\(tojson)") end)] | add // 0) } ]
  | group_by(.ns) | map({key: .[0].ns, value: {cpu: ((map(.cpu) | add) / 1000000 | floor), mem: (map(.mem) | add)}}) | from_entries;

def row($sev; $ns; $qn; $cls; $d):
  { rank: (if $sev == "CRIT" then 0 else 1 end), ns: $ns, q: "\($ns)/\($qn)", cls: $cls, text: "\($sev) \($ns) quota/\($qn) \($cls) \($d)" };

def findings($usage):
  ((.metadata // {}) | [.namespace, .name]) as [$ns, $qn]
  | if ($ns | type) != "string" or ($qn | type) != "string" or $ns == "" or $qn == "" then error("quota: ResourceQuota without metadata.name/namespace") else . end
  | ((.status | obj).hard | obj) as $hard
  | ((.status | obj).used | obj) as $used
  | ( ( ("requests.cpu", "requests.memory", "pods") as $k
        | select(($hard | has($k)) and ($used | has($k)))
        | ({"requests.cpu": "cpu", "requests.memory": "mem", "pods": "int"}[$k]) as $kind
        | ($hard[$k] | qty($kind)) as $h | ($used[$k] | qty($kind)) as $u
        | if $h == null or $u == null or $h == 0 then row("WARN"; $ns; $qn; "INVALID"; "\($k) hard=\($hard[$k] | raw) used=\($used[$k] | raw)")
          else
            ($u * 100 / $h | floor) as $p
            | "\($k) used=\($used[$k] | raw) hard=\($hard[$k] | raw) pct=\($p)%" as $d
            | ( ( if $p >= 100 then row("CRIT"; $ns; $qn; "FULL"; $d)
                  elif $p >= 85 then row("WARN"; $ns; $qn; "NEAR"; $d)
                  elif $kind != "int" and $u * 4 <= $h then row("WARN"; $ns; $qn; "OVERQUOTA"; $d)
                  else empty end ),
                ( if $kind != "int" and ($usage | has($ns)) and $u > 0 then
                    ($usage[$ns][if $kind == "cpu" then "cpu" else "mem" end]) as $a
                    | (if $kind == "cpu" then "\($a)m" else "\($a / 1048576 | floor)Mi" end) as $shown
                    | "\($k) requested=\($used[$k] | raw) actual=\($shown) eff=\($a * 100 / $u | floor)%" as $e
                    | if $a * 100 < $u * 30 then row("WARN"; $ns; $qn; "OVERREQUEST"; $e)
                      elif $a * 100 > $u * 120 then row((if $kind == "mem" then "CRIT" else "WARN" end); $ns; $qn; "UNDERREQUEST"; $e)
                      else empty end
                  else empty end ) )
          end
      ),
      ( ($used["requests.cpu"] | qty("cpu")) as $c
        | if ($c != null and $c > 0) and ($usage | has($ns) | not)
          then row("WARN"; $ns; $qn; "NOMETRICS"; "requests.cpu used=\($used["requests.cpu"] | raw) no PodMetrics")
          else empty end ) );

def hints: {
  FULL: "new pods are rejected (exceeded quota); find oversized requests first, then raise hard",
  NEAR: "under 15% headroom: a rollout surge can be rejected; plan the bump before the deploy",
  OVERQUOTA: "hard is 4x or more the use: shrink it and give the capacity back to the cluster",
  OVERREQUEST: "requests far above actual use: lower requests (VPA recommend, oc adm top) to free schedulable capacity",
  UNDERREQUEST: "pods use more than they request: raise requests or they are throttled or evicted first under node pressure",
  NOMETRICS: "no PodMetrics for this namespace: check metrics-server before trusting right-sizing",
  INVALID: "quota value is not a valid quantity (or hard is 0): fix the object, the check cannot reason about it"
};

($m[0] | usage_by_ns) as $usage
| [listof("quota")[] | select(.kind == "ResourceQuota")] as $quotas
| ($quotas | sort_by([.metadata.namespace, .metadata.name])) as $sorted
| ([$sorted[] | findings($usage)] | to_entries | map(.value + {idx: .key}) | sort_by([.rank, .ns, .idx])) as $rows
| ($rows | map(select(.rank == 0)) | length) as $crit
| ($rows | map(select(.rank == 1)) | length) as $warn
| ($rows | map(.cls) | unique) as $present
| "rightsize report: \($name)",
  ($rows[] | .text),
  (["FULL", "NEAR", "OVERQUOTA", "OVERREQUEST", "UNDERREQUEST", "NOMETRICS", "INVALID"][] | select(. as $c | $present | index($c)) | "HINT \(.): \(hints[.])"),
  "RESULT \(if $crit > 0 then "CRIT" elif $warn > 0 then "WARN" else "OK" end) crit=\($crit) warn=\($warn) quotas=\($quotas | length) healthy=\(($quotas | length) - ($rows | map(.q) | unique | length))"
'

[[ $# -eq 2 ]] || usage
quota=$1 metrics=$2
[[ -r $quota ]]   || die "$quota: cannot read"
[[ -r $metrics ]] || die "$metrics: cannot read"
name=${quota##*/}
name=${name%.json}; name=${name%.quota}

err=$(mktemp); trap 'rm -f "$err"' EXIT
if ! report=$(jq -r --arg name "$name" --slurpfile m "$metrics" "$JQ_REPORT" "$quota" 2>"$err"); then
  die "$(sed -e 's/^jq: error[^:]*: //' -e 's/ at <[^>]*>.*$//' -e '1q' "$err")"
fi
printf '%s\n' "$report"

case ${report##*$'\n'} in
  "RESULT OK "*)   exit 0 ;;
  "RESULT WARN "*) exit 1 ;;
  "RESULT CRIT "*) exit 2 ;;
  *) die "no RESULT line in report" ;;
esac
