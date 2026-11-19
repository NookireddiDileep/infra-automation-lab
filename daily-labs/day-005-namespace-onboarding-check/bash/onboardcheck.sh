#!/usr/bin/env bash
# onboardcheck.sh: does every namespace in the onboarding spec have its quota, limits, RBAC and NetworkPolicy?
#
#   onboardcheck.sh SPEC_JSON CLUSTER_JSON
#
# CLUSTER_JSON = saved `oc get ns,resourcequota,limitrange,rolebinding,networkpolicy -A -o json`.
# Exit codes: 0 OK · 1 WARN · 2 CRIT · 3 usage/runtime error
# Report format: see ../README.md ("The contract").

set -Eeuo pipefail
export LC_ALL=C                       # byte-order sort, same as Python/Go/Ansible

readonly PROG=${0##*/}

log()   { printf '%s %s [%s] %s\n' "$(date -u +%FT%TZ)" "$PROG" "$1" "${*:2}" >&2; }
die()   { log ERROR "$*"; exit 3; }
usage() { sed -n '2,6p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 3; }

trap 'log ERROR "failed at line $LINENO: $BASH_COMMAND"; exit 3' ERR

# One jq pass: one TSV record per finding: rank  namespace  class  text   (rank 0 = CRIT, 1 = WARN)
# $spec = the slurped spec file. Emission order inside a namespace is part of the contract.
# shellcheck disable=SC2016  # jq program: $vars are jq's, not the shell's
JQ_FINDINGS='
def tiers: {small: ["4","8Gi","20","500m","512Mi"], medium: ["16","32Gi","60","1","1Gi"], large: ["64","128Gi","200","2","2Gi"]};
def units: {"": 1, "Ki": 1024, "Mi": 1048576, "Gi": 1073741824, "Ti": 1099511627776};
# cpu -> millicores, memory -> bytes, int -> int; null when not a valid quantity (anything but a string is invalid)
def qty($kind):
  (if type == "string" then . else "" end) as $s
  | if $kind == "cpu" then
      (if ($s | test("^([0-9]+m|[0-9]+(\\.[0-9]{1,3})?)$")) then
         (if ($s | endswith("m")) then ($s[:-1] | tonumber) else (($s | tonumber) * 1000 | round) end)
       else null end)
    elif $kind == "mem" then
      (if ($s | test("^[0-9]+(Ki|Mi|Gi|Ti)?$")) then
         ($s | capture("^(?<n>[0-9]+)(?<u>Ki|Mi|Gi|Ti)?$")) as $m | ($m.n | tonumber) * units[$m.u // ""]
       else null end)
    else (if ($s | test("^[0-9]+$")) then ($s | tonumber) else null end) end;
def same($kind; $want; $have):
  ($want | qty($kind)) as $w | ($have | qty($kind)) as $h | $w != null and $h != null and $w == $h;
def subj: ([(. // [])[] | "\(.kind // "?"):\(.name // "?")"] | join(",")) | if . == "" then "-" else . end;
def f($ns; $rank; $cls; $obj; $detail):
  [$rank, $ns, $cls, (["CRIT", "WARN"][$rank] + " " + $ns + " " + $cls + " " + $obj + (if $detail == "" then "" else " " + $detail end))] | join("\t");

[.items[] | select(type == "object")] as $items
| $spec[0].namespaces[] as $n
| $n.name as $ns
| tiers[$n.tier] as $t
| def obj($kind; $name): first($items[] | select(.kind == $kind and .metadata.name == $name and .metadata.namespace == $ns)) // null;
  if ([$items[] | select(.kind == "Namespace" and .metadata.name == $ns)] | length) == 0 then
    f($ns; 0; "MISSING"; "Namespace/\($ns)"; "")
  else
    # --- ResourceQuota: a missing key is unbounded (CRIT), a different value is drift (WARN)
    (obj("ResourceQuota"; "onboarding-quota") as $q
     | if $q == null then f($ns; 0; "MISSING"; "ResourceQuota/onboarding-quota"; "")
       else (($q.spec.hard // {}) as $h
             | (["requests.cpu", $t[0], "cpu"], ["requests.memory", $t[1], "mem"], ["pods", $t[2], "int"]) as [$k, $w, $kind]
             | if ($h | has($k) | not) then f($ns; 0; "DRIFT"; "ResourceQuota/onboarding-quota"; "\($k) want=\($w) have=-")
               elif same($kind; $w; $h[$k]) then empty
               else f($ns; 1; "DRIFT"; "ResourceQuota/onboarding-quota"; "\($k) want=\($w) have=\($h[$k])") end)
       end),
    # --- LimitRange default container limits
    (obj("LimitRange"; "onboarding-limits") as $l
     | if $l == null then f($ns; 1; "MISSING"; "LimitRange/onboarding-limits"; "")
       else ((first(($l.spec.limits // [])[] | select(.type == "Container")) // {}) as $i
             | ($i.default // {}) as $d
             | (["default.cpu", $t[3], "cpu", "cpu"], ["default.memory", $t[4], "mem", "memory"]) as [$k, $w, $kind, $field]
             | if $d[$field] == null then f($ns; 1; "DRIFT"; "LimitRange/onboarding-limits"; "\($k) want=\($w) have=-")
               elif same($kind; $w; $d[$field]) then empty
               else f($ns; 1; "DRIFT"; "LimitRange/onboarding-limits"; "\($k) want=\($w) have=\($d[$field])") end)
       end),
    # --- RBAC: the two onboarding bindings, then any cluster-admin binding in the namespace
    ((["onboarding-admin", "admin", $n.admin_group], ["onboarding-view", "view", $n.viewer_group]) as [$rn, $role, $group]
     | obj("RoleBinding"; $rn) as $rb
       | if $rb == null then f($ns; 0; "MISSING"; "RoleBinding/\($rn)"; "")
         else ((($rb.roleRef.name // "-") as $have
                | if $have != $role then f($ns; 0; "DRIFT"; "RoleBinding/\($rn)"; "roleRef want=\($role) have=\($have)") else empty end),
               (if any(($rb.subjects // [])[]; .kind == "Group" and .name == $group) then empty
                else f($ns; 0; "DRIFT"; "RoleBinding/\($rn)"; "subjects want=Group:\($group) have=\($rb.subjects | subj)") end))
         end),
    ($items[] | select(.kind == "RoleBinding" and .metadata.namespace == $ns and .roleRef.name == "cluster-admin")
     | f($ns; 0; "EXTRA"; "RoleBinding/\(.metadata.name // "?")"; "cluster-admin to \(.subjects | subj)")),
    # --- NetworkPolicy: default-deny must really deny everything; the ingress allow is a WARN
    (obj("NetworkPolicy"; "default-deny-ingress") as $p
     | if $p == null then f($ns; 0; "MISSING"; "NetworkPolicy/default-deny-ingress"; "")
       elif (($p.spec.podSelector // {}) == {}) and ((($p.spec.ingress // []) | length) == 0) and ((($p.spec.policyTypes // []) | index("Ingress")) != null) then empty
       else f($ns; 0; "DRIFT"; "NetworkPolicy/default-deny-ingress"; "not deny-all") end),
    (if obj("NetworkPolicy"; "allow-from-openshift-ingress") == null then f($ns; 1; "MISSING"; "NetworkPolicy/allow-from-openshift-ingress"; "") else empty end)
  end
'

# shellcheck disable=SC2016
JQ_SPEC_OK='type == "object" and (.namespaces | type == "array" and length > 0)
  and all(.namespaces[]; type == "object"
          and all([.name, .tier, .admin_group, .viewer_group][]; type == "string" and . != "")
          and (.tier | IN("small", "medium", "large")))'

hint() {
  case $1 in
    DRIFT)   echo "re-apply the spec (ansible-playbook apply_onboarding.yml), then re-run this check" ;;
    EXTRA)   echo "oc get rolebindings -A | grep -i admin; remove it or record the exception" ;;
    MISSING) echo "onboarding never ran or was reverted: apply the spec (ansible-playbook apply_onboarding.yml)" ;;
  esac
}

main() {
  (( $# == 2 )) || usage
  local spec=$1 cluster=$2 name
  [[ -r $spec ]] || die "$spec: cannot read"
  [[ -r $cluster ]] || die "$cluster: cannot read"
  command -v jq >/dev/null || die "jq is required"
  jq -e "$JQ_SPEC_OK" "$spec" >/dev/null 2>&1 ||
    die "$spec: spec needs a non-empty namespaces[] with name, tier (small|medium|large), admin_group, viewer_group"
  jq -e 'type == "object" and .kind == "List" and (.items | type == "array")' "$cluster" >/dev/null 2>&1 ||
    die "$cluster: not a List (expected kind List with items[])"

  name=${cluster##*/}; name=${name%.cluster.json}; [[ $name == "${cluster##*/}" ]] && name=${name%.json}

  local total records
  total=$(jq '.namespaces | length' "$spec")
  # -s: stable sort keeps the emission order inside a namespace
  records=$(jq -r --slurpfile spec "$spec" "$JQ_FINDINGS" "$cluster" | sort -s -t "$(printf '\t')" -k1,1n -k2,2)

  printf 'onboardcheck report: %s\n' "$name"
  local crit=0 warn=0 bad=0 rank class text
  if [[ -n $records ]]; then
    while IFS=$'\t' read -r rank _ _ text; do
      printf '%s\n' "$text"
      if (( rank == 0 )); then crit=$((crit + 1)); else warn=$((warn + 1)); fi
    done <<< "$records"
    while IFS= read -r class; do printf 'HINT %s: %s\n' "$class" "$(hint "$class")"; done \
      < <(cut -f3 <<< "$records" | sort -u)
    bad=$(cut -f2 <<< "$records" | sort -u | wc -l)
  fi
  local result=OK code=0
  if (( crit > 0 )); then result=CRIT code=2; elif (( warn > 0 )); then result=WARN code=1; fi
  printf 'RESULT %s crit=%d warn=%d namespaces=%d clean=%d\n' "$result" "$crit" "$warn" "$total" "$((total - bad))"
  exit "$code"                          # exit, not return: a non-zero return would trip the ERR trap
}

main "$@"
