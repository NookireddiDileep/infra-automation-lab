#!/usr/bin/env python3
"""pendingtriage: root-cause Pending pods from saved `oc get pods -A -o json` output.

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

LOG = logging.getLogger("pendingtriage")

HINTS = {
    "Capacity": "oc describe node | grep -A8 'Allocated resources'; add nodes or shrink requests",
    "Cordoned": "oc get nodes; finish the maintenance, then oc adm uncordon",
    "Gated": "scheduling gate (Kueue/quota admission?): check the queue and its quota",
    "Other": "read the full event: oc describe pod",
    "Placement": "check pod (anti-)affinity and topologySpreadConstraints against node/zone count",
    "Ports": "hostPort conflict: use a Service/NodePort or drop hostPort",
    "Selector": "oc get nodes --show-labels; fix nodeSelector/affinity or label the nodes",
    "Storage": "oc get pvc,pv,sc; check StorageClass, volumeBindingMode and the PV zone",
    "Taint": "oc describe node | grep Taints; add a toleration or remove the taint",
    "Unparsed": "read the full event: oc describe pod",
}
CRIT_CLASSES = {"Selector", "Storage", "Taint"}
PREFIX = re.compile(r"^0/([0-9]+) nodes are available: (.*)$")
COUNTED = re.compile(r"^([0-9]+) (.*)$")


def classify(text: str) -> str:
    """Map one scheduler clause to a class. First matching rule wins (order is part of the contract)."""
    low = text.lower()
    if "Insufficient " in text or "Too many pods" in text:
        return "Capacity"
    if any(s in low for s in ("persistentvolume", "unbound", "volume node affinity")):
        return "Storage"
    if "taint" in low:
        return "Taint"
    if any(s in low for s in ("node affinity", "node selector")):
        return "Selector"
    if any(s in low for s in ("pod affinity", "anti-affinity", "topology spread")):
        return "Placement"
    if "unschedulable" in low:
        return "Cordoned"
    if "free ports" in low:
        return "Ports"
    return "Other"


def resource(text: str) -> str:
    if "Insufficient " in text:
        return text.split("Insufficient ", 1)[1]
    return "pods" if "Too many pods" in text else ""


def parse_message(reason: str, message: str) -> tuple[str, str, list[tuple[str, int]], str]:
    """Return (dominant class, nodes, [(class, nodes)...] sorted by count desc then class, need)."""
    if reason == "SchedulingGated":
        return "Gated", "-", [], ""
    m = PREFIX.match(message)
    if not m:
        return "Unparsed", "-", [], ""
    total = int(m.group(1))
    body = m.group(2).split(". ", 1)[0]
    if body.endswith("."):
        body = body[:-1]
    clauses = []   # (class, count, resource)
    for raw in body.split(", "):
        if raw == "":
            continue
        q = COUNTED.match(raw)
        count, text = (int(q.group(1)), q.group(2)) if q else (total, raw)
        clauses.append((classify(text), count, resource(text)))
    if not clauses:
        return "Unparsed", "-", [], ""
    agg: dict[str, int] = {}
    needs: set[str] = set()
    for cls, count, res in clauses:
        # Insufficient clauses may count one node twice (cpu AND memory): take the max.
        # Every other reason is reported once per node: sum.
        agg[cls] = max(agg.get(cls, 0), count) if cls == "Capacity" else agg.get(cls, 0) + count
        if res:
            needs.add(res)
    causes = sorted(agg.items(), key=lambda kv: (-kv[1], kv[0]))
    return causes[0][0], str(total), causes, "+".join(sorted(needs))


def triage(doc: Any, name: str) -> tuple[str, int]:
    """Return (report text, exit code)."""
    if not isinstance(doc, dict) or doc.get("kind") not in ("List", "PodList") or not isinstance(doc.get("items"), list):
        raise ValueError("not a Pod List (expected kind List/PodList with items[])")
    rows: list[tuple[int, str, str, str]] = []   # rank, ns/pod, class, text
    for pod in doc["items"]:
        meta = pod.get("metadata") or {}
        key = f"{meta.get('namespace') or '?'}/{meta.get('name') or '?'}"
        status = pod.get("status") or {}
        if status.get("phase") != "Pending":
            continue
        cond = next((c for c in status.get("conditions") or []
                     if c.get("type") == "PodScheduled" and c.get("status") == "False"), None)
        if cond is None:
            continue   # scheduled already: image pull / container creation is Day 3's job
        cls, nodes, causes, need = parse_message(cond.get("reason") or "", cond.get("message") or "")
        rank = 0 if cls in CRIT_CLASSES else 1
        causes_txt = ",".join(f"{c}:{n}" for c, n in causes) or "-"
        text = f"{'CRIT' if rank == 0 else 'WARN'} {cls} {key} nodes={nodes} causes={causes_txt}"
        if need:
            text += f" need={need}"
        rows.append((rank, key, cls, text))
    rows.sort(key=lambda r: (r[0], r[1]))
    crit = sum(1 for r in rows if r[0] == 0)
    warn = len(rows) - crit
    lines = [f"pendingtriage report: {name}"] + [r[3] for r in rows]
    lines += [f"HINT {c}: {HINTS[c]}" for c in sorted({r[2] for r in rows})]
    result = "CRIT" if crit else ("WARN" if warn else "OK")
    lines.append(f"RESULT {result} crit={crit} warn={warn} pods={len(doc['items'])}")
    return "\n".join(lines) + "\n", {"OK": 0, "WARN": 1, "CRIT": 2}[result]


def to_json(text: str) -> str:
    """Optional machine-readable view of a text report (Python-only extra; not part of the golden contract)."""
    findings = []
    for line in text.splitlines():
        parts = line.split(" ")
        if parts[0] in ("CRIT", "WARN"):
            kv = dict(p.split("=", 1) for p in parts[3:])
            findings.append({"severity": parts[0], "class": parts[1], "pod": parts[2], **kv})
    return json.dumps({"findings": findings, "result": text.splitlines()[-1]}, indent=2) + "\n"


def report_name(path: str) -> str:
    base = os.path.basename(path)
    for suffix in (".pods.json", ".json"):
        if base.endswith(suffix):
            return base[: -len(suffix)]
    return base


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("pods_json", help="saved output of: oc get pods -A -o json")
    ap.add_argument("--json", action="store_true", help="emit findings as JSON instead of the text report")
    ap.add_argument("-v", "--verbose", action="store_true")
    args = ap.parse_args(argv)
    logging.basicConfig(level=logging.DEBUG if args.verbose else logging.WARNING,
                        format="%(asctime)s pendingtriage [%(levelname)s] %(message)s", stream=sys.stderr)
    try:
        with open(args.pods_json, encoding="utf-8") as fh:
            doc = json.load(fh)
        text, code = triage(doc, report_name(args.pods_json))
    except (OSError, ValueError) as exc:   # JSONDecodeError is a ValueError
        LOG.error("%s: %s", args.pods_json, exc)
        return 3
    sys.stdout.write(to_json(text) if args.json else text)
    return code


if __name__ == "__main__":
    sys.exit(main())
