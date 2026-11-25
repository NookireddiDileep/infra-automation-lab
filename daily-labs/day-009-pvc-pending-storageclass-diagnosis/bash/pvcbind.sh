#!/usr/bin/env bash
# pvcbind.sh: PVC Pending / StorageClass binding-mode diagnosis (saved `oc get pvc -A` + `oc get sc`).
#
#   pvcbind.sh PVCS_JSON STORAGECLASSES_JSON
#
# PVCS_JSON           = saved `oc get pvc -A -o json`
# STORAGECLASSES_JSON = saved `oc get storageclass -o json`
# Exit codes: 0 OK · 1 WARN · 2 CRIT · 3 usage/input error
# Report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # byte-order sort, same as Python/Go/Ansible

readonly PROG=${0##*/}

log()   { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()   { log ERROR "$*"; exit 3; }
usage() { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

# One jq pass. $s = slurped StorageClass list. Emission order (PVCs by ns/name, then classes by name) is part of the contract.
# shellcheck disable=SC2016  # jq program: $vars are jq's, not the shell's
JQ_REPORT='
def listof($what): if type == "object" and .kind == "List" and (.items | type) == "array" then [.items[] | select(type == "object")]
                   else error("\($what): not a List (expected kind List with items[])") end;
def obj: if type == "object" then . else {} end;
def txt: if type == "string" and . != "" then . else "-" end;
def row($rank; $subj; $cls; $d; $p): {rank: $rank, subject: $subj, cls: $cls, p: $p, text: "\(["CRIT", "WARN", "INFO"][$rank]) \($subj) \($cls) \($d)"};

# name -> {mode, prov, default}
def classes:
  [ ($s[0] | listof("storageclasses"))[] | select(.kind == "StorageClass")
    | (.metadata | obj | .name) as $n
    | if ($n | type) == "string" and $n != "" then . else error("storageclasses: StorageClass without metadata.name") end
    | (if .volumeBindingMode == null then "Immediate" else .volumeBindingMode end) as $m
    | if $m == "Immediate" or $m == "WaitForFirstConsumer" then . else error("storageclasses: bad volumeBindingMode in \($n)") end
    | {key: $n, value: {mode: $m, prov: (.provisioner | if type == "string" then . else "-" end),
                        default: ((.metadata | obj | .annotations | obj | .["storageclass.kubernetes.io/is-default-class"]) == "true")}} ]
  | from_entries;

def subject: "\(.metadata.namespace)/\(.metadata.name)";

def valid:
  (.metadata | obj) as $md
  | if ($md.namespace | type) == "string" and $md.namespace != "" and ($md.name | type) == "string" and $md.name != "" then . else error("pvcs: PersistentVolumeClaim without metadata.namespace or metadata.name") end
  | subject as $sub
  | if ((.status | obj | .phase) as $p | $p == "Pending" or $p == "Bound" or $p == "Lost") then . else error("pvcs: bad phase in \($sub)") end
  | if (.spec | obj | .storageClassName) as $c | $c == null or ($c | type) == "string" then . else error("pvcs: bad storageClassName in \($sub)") end;

def judge($cl; $def):
  subject as $s0 | (.metadata | obj) as $md | (.spec | obj) as $sp | (.status | obj | .phase) as $ph | $sp.storageClassName as $sc0
  | if $ph == "Lost" then row(0; $s0; "LOST"; "volume=\($sp.volumeName | txt)"; true)
    elif $ph == "Bound" then empty
    else
      (if $sc0 == null then $def else $sc0 end) as $sc
      | (if $sc0 == null then " via=default" else "" end) as $via
      | if $sc == null then row(0; $s0; "NOSC"; "class=-"; true)
        elif $sc == "" then row(0; $s0; "NOPV"; "class=-"; true)
        elif $cl[$sc] == null then row(0; $s0; "UNKNOWNSC"; "class=\($sc)"; true)
        else
          $cl[$sc] as $c
          | if $c.mode == "WaitForFirstConsumer" then
              ($md | .annotations | obj | .["volume.kubernetes.io/selected-node"]) as $n
              | if ($n | type) == "string" and $n != "" then row(0; $s0; "SELECTEDSTUCK"; "class=\($sc) node=\($n) provisioner=\($c.prov)\($via)"; true)
                else row(2; $s0; "WAITFORCONSUMER"; "class=\($sc) mode=WaitForFirstConsumer\($via)"; true) end
            elif $c.prov == "kubernetes.io/no-provisioner" then row(0; $s0; "NOPV"; "class=\($sc)\($via)"; true)
            else row(0; $s0; "PROVISIONFAILED"; "class=\($sc) provisioner=\($c.prov)\($via)"; true) end
        end
    end;

def hints: {
  LOST: "the PV behind this claim is gone: oc get pv, check the backend volume; restore from snapshot or recreate, never delete the claim first",
  NOSC: "no storageClassName and no default StorageClass: set storageclass.kubernetes.io/is-default-class=true on one class or name a class in the claim",
  NOPV: "this claim needs a pre-created PV (empty class or no-provisioner): oc get pv, match size, access mode and class, or create one",
  UNKNOWNSC: "the named StorageClass does not exist: oc get sc, fix the typo in the claim or create the class",
  SELECTEDSTUCK: "the scheduler picked a node but the provisioner could not create the volume there: oc describe pvc (ProvisioningFailed), check zone and topology of that node",
  PROVISIONFAILED: "Immediate class, dynamic provisioner, still Pending: oc describe pvc for ProvisioningFailed, check the CSI controller pods and quota on the backend",
  MULTIDEFAULT: "more than one default StorageClass: claims without a class get an arbitrary one; keep exactly one default",
  LOCALIMMEDIATE: "a no-provisioner class bound Immediate binds before scheduling and strands pods: use volumeBindingMode WaitForFirstConsumer",
  WAITFORCONSUMER: "not a fault: the claim stays Pending until a pod that uses it is scheduled; if no pod exists yet, this is expected"
};

classes as $cl
| ([$cl | to_entries[] | select(.value.default) | .key] | sort) as $defaults
| ($defaults[0]) as $def
| [listof("pvcs")[] | select(.kind == "PersistentVolumeClaim") | valid] as $claims
| ([$claims | sort_by(subject)[] | judge($cl; $def)]
   + [$cl | keys[] as $n | $cl[$n] as $c
      | (if ($defaults | length) > 1 and $c.default then row(1; "sc/\($n)"; "MULTIDEFAULT"; "defaults=\($defaults | join(","))"; false) else empty end),
        (if $c.prov == "kubernetes.io/no-provisioner" and $c.mode != "WaitForFirstConsumer" then row(1; "sc/\($n)"; "LOCALIMMEDIATE"; "provisioner=kubernetes.io/no-provisioner mode=\($c.mode)"; false) else empty end)]
  | to_entries | map(.value + {idx: .key}) | sort_by([.rank, .subject, .idx])) as $rows
| ($rows | map(select(.rank == 0)) | length) as $crit
| ($rows | map(select(.rank == 1)) | length) as $warn
| ($rows | map(select(.rank == 2)) | length) as $info
| ($rows | map(.cls) | unique) as $present
| "pvc-binding report: \($name)",
  ($rows[] | .text),
  (["LOST", "NOSC", "NOPV", "UNKNOWNSC", "SELECTEDSTUCK", "PROVISIONFAILED", "MULTIDEFAULT", "LOCALIMMEDIATE", "WAITFORCONSUMER"][] | select(. as $c | $present | index($c)) | "HINT \(.): \(hints[.])"),
  "RESULT \(if $crit > 0 then "CRIT" elif $warn > 0 then "WARN" else "OK" end) crit=\($crit) warn=\($warn) info=\($info) pvcs=\($claims | length) healthy=\(($claims | length) - ($rows | map(select(.p)) | length))"
'

[[ $# -eq 2 ]] || usage
pvcs=$1 scs=$2
[[ -r $pvcs ]] || die "$pvcs: cannot read"
[[ -r $scs ]]  || die "$scs: cannot read"
name=${pvcs##*/}
name=${name%.json}; name=${name%.pvcs}

err=$(mktemp); trap 'rm -f "$err"' EXIT
if ! report=$(jq -r --arg name "$name" --slurpfile s "$scs" "$JQ_REPORT" "$pvcs" 2>"$err"); then
  die "$(sed -e 's/^jq: error (at [^)]*): //' -e 's/ at <[^>]*>.*$//' -e '1q' "$err")"
fi
printf '%s\n' "$report"

case ${report##*$'\n'} in
  "RESULT OK "*)   exit 0 ;;
  "RESULT WARN "*) exit 1 ;;
  "RESULT CRIT "*) exit 2 ;;
  *) die "no RESULT line in report" ;;
esac
