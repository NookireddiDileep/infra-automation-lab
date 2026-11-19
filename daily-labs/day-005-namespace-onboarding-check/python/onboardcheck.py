#!/usr/bin/env python3
"""onboardcheck: does each namespace in the onboarding spec have the quota, limits, RBAC and NetworkPolicy it should?

    onboardcheck.py SPEC_JSON CLUSTER_JSON [--json]

CLUSTER_JSON is saved `oc get ns,resourcequota,limitrange,rolebinding,networkpolicy -A -o json`.
Exit codes: 0 OK, 1 WARN, 2 CRIT, 3 usage/runtime error.
Contract: see ../README.md ("The contract").
"""
from __future__ import annotations

import argparse
import json
import logging
import os
import re
import sys
from typing import Any

LOG = logging.getLogger("onboardcheck")

# tier -> requests.cpu, requests.memory, pods, default container limit cpu, memory
TIERS = {
    "small": ("4", "8Gi", "20", "500m", "512Mi"),
    "medium": ("16", "32Gi", "60", "1", "1Gi"),
    "large": ("64", "128Gi", "200", "2", "2Gi"),
}
UNITS = {"": 1, "Ki": 1024, "Mi": 1024**2, "Gi": 1024**3, "Ti": 1024**4}
CPU_RE = re.compile(r"^([0-9]+m|[0-9]+(\.[0-9]{1,3})?)$")
MEM_RE = re.compile(r"^([0-9]+)(Ki|Mi|Gi|Ti)?$")
INT_RE = re.compile(r"^[0-9]+$")
HINTS = {
    "DRIFT": "re-apply the spec (ansible-playbook apply_onboarding.yml), then re-run this check",
    "EXTRA": "oc get rolebindings -A | grep -i admin; remove it or record the exception",
    "MISSING": "onboarding never ran or was reverted: apply the spec (ansible-playbook apply_onboarding.yml)",
}


def qty(kind: str, value: Any) -> int | None:
    """Normalise a quantity: cpu -> millicores, memory -> bytes, pods -> int. None = unparsable."""
    s = value if isinstance(value, str) else ""   # the API serialises quantities as strings; anything else is invalid
    if kind == "cpu":
        if not CPU_RE.match(s):
            return None
        return int(s[:-1]) if s.endswith("m") else round(float(s) * 1000)
    if kind == "mem":
        m = MEM_RE.match(s)
        return int(m.group(1)) * UNITS[m.group(2) or ""] if m else None
    return int(s) if INT_RE.match(s) else None


def same(kind: str, want: str, have: Any) -> bool:
    w, h = qty(kind, want), qty(kind, have)
    return w is not None and h is not None and w == h


def validate_spec(spec: Any) -> list[dict[str, str]]:
    if not isinstance(spec, dict) or not isinstance(spec.get("namespaces"), list) or not spec["namespaces"]:
        raise ValueError("spec: expected an object with a non-empty namespaces[]")
    for n in spec["namespaces"]:
        if not isinstance(n, dict) or any(not isinstance(n.get(k), str) or n[k] == "" for k in ("name", "tier", "admin_group", "viewer_group")):
            raise ValueError("spec: every namespace needs name, tier, admin_group and viewer_group (non-empty strings)")
        if n["tier"] not in TIERS:
            raise ValueError(f"spec: {n['name']}: unknown tier {n['tier']!r} (small|medium|large)")
    return spec["namespaces"]


def subj(subjects: Any) -> str:
    s = [f"{x.get('kind') or '?'}:{x.get('name') or '?'}" for x in (subjects or []) if isinstance(x, dict)]
    return ",".join(s) or "-"


def check_namespace(ns: dict[str, str], objs: dict[tuple[str, str], dict[str, Any]], rolebindings: list[dict[str, Any]]) -> list[tuple[int, str, str]]:
    """Findings for one namespace as (rank, class, text) in emission order. rank 0 = CRIT, 1 = WARN."""
    name = ns["name"]
    out: list[tuple[int, str, str]] = []

    def add(rank: int, cls: str, obj: str, detail: str = "") -> None:
        out.append((rank, cls, f"{'CRIT' if rank == 0 else 'WARN'} {name} {cls} {obj}" + (f" {detail}" if detail else "")))

    if ("Namespace", name) not in objs:
        add(0, "MISSING", f"Namespace/{name}")
        return out
    wcpu, wmem, wpods, dcpu, dmem = TIERS[ns["tier"]]

    q = objs.get(("ResourceQuota", "onboarding-quota"))
    if q is None:
        add(0, "MISSING", "ResourceQuota/onboarding-quota")
    else:
        hard = (q.get("spec") or {}).get("hard") or {}
        for key, want, kind in (("requests.cpu", wcpu, "cpu"), ("requests.memory", wmem, "mem"), ("pods", wpods, "int")):
            if key not in hard:
                add(0, "DRIFT", "ResourceQuota/onboarding-quota", f"{key} want={want} have=-")   # unbounded: dangerous
            elif not same(kind, want, hard[key]):
                add(1, "DRIFT", "ResourceQuota/onboarding-quota", f"{key} want={want} have={hard[key]}")

    lim = objs.get(("LimitRange", "onboarding-limits"))
    if lim is None:
        add(1, "MISSING", "LimitRange/onboarding-limits")
    else:
        item = next((i for i in (lim.get("spec") or {}).get("limits") or [] if i.get("type") == "Container"), {})
        dflt = item.get("default") or {}
        for key, want, kind in (("default.cpu", dcpu, "cpu"), ("default.memory", dmem, "mem")):
            have = dflt.get(key.split(".")[1])
            if have is None:
                add(1, "DRIFT", "LimitRange/onboarding-limits", f"{key} want={want} have=-")
            elif not same(kind, want, have):
                add(1, "DRIFT", "LimitRange/onboarding-limits", f"{key} want={want} have={have}")

    for rb_name, role, group in (("onboarding-admin", "admin", ns["admin_group"]), ("onboarding-view", "view", ns["viewer_group"])):
        rb = objs.get(("RoleBinding", rb_name))
        if rb is None:
            add(0, "MISSING", f"RoleBinding/{rb_name}")
            continue
        have_role = (rb.get("roleRef") or {}).get("name") or "-"
        if have_role != role:
            add(0, "DRIFT", f"RoleBinding/{rb_name}", f"roleRef want={role} have={have_role}")
        if not any(isinstance(s, dict) and s.get("kind") == "Group" and s.get("name") == group for s in rb.get("subjects") or []):
            add(0, "DRIFT", f"RoleBinding/{rb_name}", f"subjects want=Group:{group} have={subj(rb.get('subjects'))}")
    for rb in rolebindings:
        if (rb.get("roleRef") or {}).get("name") == "cluster-admin":
            add(0, "EXTRA", f"RoleBinding/{rb['metadata'].get('name') or '?'}", f"cluster-admin to {subj(rb.get('subjects'))}")

    deny = objs.get(("NetworkPolicy", "default-deny-ingress"))
    if deny is None:
        add(0, "MISSING", "NetworkPolicy/default-deny-ingress")
    else:
        sp = deny.get("spec") or {}
        if not ((sp.get("podSelector") or {}) == {} and not sp.get("ingress") and "Ingress" in (sp.get("policyTypes") or [])):
            add(0, "DRIFT", "NetworkPolicy/default-deny-ingress", "not deny-all")
    if ("NetworkPolicy", "allow-from-openshift-ingress") not in objs:
        add(1, "MISSING", "NetworkPolicy/allow-from-openshift-ingress")
    return out


def check(spec: Any, cluster: Any, name: str) -> tuple[str, int]:
    """Return (report text, exit code)."""
    namespaces = validate_spec(spec)
    if not isinstance(cluster, dict) or cluster.get("kind") not in ("List",) or not isinstance(cluster.get("items"), list):
        raise ValueError("cluster: not a List (expected kind List with items[])")
    by_ns: dict[str, dict[tuple[str, str], dict[str, Any]]] = {}
    for it in cluster["items"]:
        if not isinstance(it, dict):
            continue
        meta = it.get("metadata") or {}
        kind, oname = it.get("kind"), meta.get("name")
        owner = oname if kind == "Namespace" else meta.get("namespace")
        if kind in ("Namespace", "ResourceQuota", "LimitRange", "RoleBinding", "NetworkPolicy") and owner and oname:
            by_ns.setdefault(owner, {})[(kind, oname)] = it
    rows: list[tuple[int, str, str, str]] = []   # rank, namespace, class, text
    for ns in namespaces:
        objs = by_ns.get(ns["name"], {})
        rbs = [o for (k, _), o in objs.items() if k == "RoleBinding"]
        rows += [(r, ns["name"], c, t) for r, c, t in check_namespace(ns, objs, rbs)]
    rows.sort(key=lambda r: (r[0], r[1]))   # stable: keeps emission order inside a namespace
    crit = sum(1 for r in rows if r[0] == 0)
    warn = len(rows) - crit
    bad = {r[1] for r in rows}
    lines = [f"onboardcheck report: {name}"] + [r[3] for r in rows]
    lines += [f"HINT {c}: {HINTS[c]}" for c in sorted({r[2] for r in rows})]
    result, code = ("CRIT", 2) if crit else (("WARN", 1) if warn else ("OK", 0))
    lines.append(f"RESULT {result} crit={crit} warn={warn} namespaces={len(namespaces)} clean={len(namespaces) - len(bad)}")
    return "\n".join(lines) + "\n", code


def report_name(path: str) -> str:
    base = os.path.basename(path)
    for suf in (".cluster.json", ".json"):
        if base.endswith(suf):
            return base[: -len(suf)]
    return base


def load(path: str) -> Any:
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(level=logging.INFO, stream=sys.stderr, format="%(asctime)sZ onboardcheck [%(levelname)s] %(message)s")
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("spec")
    ap.add_argument("cluster")
    ap.add_argument("--json", action="store_true", help="print findings as JSON instead of the text report")
    try:
        args = ap.parse_args(argv)
    except SystemExit as e:
        return 3 if e.code else 0
    try:
        spec, cluster = load(args.spec), load(args.cluster)
        text, code = check(spec, cluster, report_name(args.cluster))
    except (OSError, ValueError) as e:   # json.JSONDecodeError is a ValueError
        LOG.error("%s", e)
        return 3
    if args.json:
        findings = [ln.split(" ", 4) for ln in text.splitlines() if ln[:5] in ("CRIT ", "WARN ")]
        print(json.dumps({"report": report_name(args.cluster), "exit": code,
                          "findings": [{"severity": f[0], "namespace": f[1], "class": f[2], "object": f[3],
                                        "detail": f[4] if len(f) > 4 else ""} for f in findings]}, indent=2))
    else:
        sys.stdout.write(text)
    return code


if __name__ == "__main__":
    sys.exit(main())
