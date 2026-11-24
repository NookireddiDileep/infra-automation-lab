#!/usr/bin/env python3
"""nodepressure: node pressure & eviction report (Node conditions + kubelet Evicted events).

    nodepressure.py [--json] NODES_JSON EVENTS_JSON

NODES_JSON  saved `oc get nodes -o json`
EVENTS_JSON saved `oc get events -A -o json`

Exit codes: 0 OK, 1 WARN, 2 CRIT, 3 usage/input error. Contract: ../README.md.
"""
import argparse
import json
import logging
import pathlib
import re
import sys
from typing import Any

log = logging.getLogger("nodepressure")

PRESSURE = (("MemoryPressure", "MEMORYPRESSURE", "node.kubernetes.io/memory-pressure"),
            ("DiskPressure", "DISKPRESSURE", "node.kubernetes.io/disk-pressure"),
            ("PIDPressure", "PIDPRESSURE", "node.kubernetes.io/pid-pressure"))
STORM = 5  # evictions on one node at which the finding becomes CRIT
CLASSES = ["NOTREADY", "MEMORYPRESSURE", "DISKPRESSURE", "PIDPRESSURE", "NOTAINT", "EVICTIONSTORM", "EVICTIONS"]
HINTS = {
    "NOTREADY": "kubelet is not posting Ready: oc describe node, oc adm node-logs NODE -u kubelet; pods are evicted after the unreachable toleration (300s)",
    "MEMORYPRESSURE": "memory.available is under the eviction threshold: find the top consumers (oc adm top pods), fix requests/limits, check kubelet systemReserved",
    "DISKPRESSURE": "nodefs/imagefs is low: crictl images / du on /var/lib/containers, rotate logs, check ephemeral-storage requests",
    "PIDPRESSURE": "pid.available is low: find a fork bomb or leaking process (ps -eLf), raise podPidsLimit only after you know why",
    "NOTAINT": "pressure is True but the node.kubernetes.io/*-pressure taint is missing: the scheduler may still place pods here",
    "EVICTIONSTORM": "5+ evictions on one node: the pressure is chronic; cordon, drain and fix the cause before it takes the node down",
    "EVICTIONS": "kubelet evicted pods here: the pressure came and went; compare victims' usage with their requests (BestEffort/Burstable go first)",
}
RESOURCE_RE = re.compile(r"low on resource: ([A-Za-z0-9-]+)\.")


class InputError(ValueError):
    """Bad input: refuse (exit 3) instead of producing a half report."""


def items_of(doc: Any, what: str) -> list:
    if not isinstance(doc, dict) or doc.get("kind") != "List" or not isinstance(doc.get("items"), list):
        raise InputError(f"{what}: not a List (expected kind List with items[])")
    return [i for i in doc["items"] if isinstance(i, dict)]


def text(v: Any) -> str:
    return v if isinstance(v, str) and v != "" else "-"


def as_dict(v: Any) -> dict:
    return v if isinstance(v, dict) else {}


def evictions_by_host(events: Any) -> dict:
    """host -> {resource: count} from kubelet Evicted events (count defaults to 1)."""
    out: dict = {}
    for e in items_of(events, "events"):
        if e.get("reason") != "Evicted":
            continue
        host = as_dict(e.get("source")).get("host")
        if not isinstance(host, str) or host == "":
            raise InputError("events: Evicted event without source.host")
        n = e.get("count", 1)
        if isinstance(n, bool) or not isinstance(n, (int, float)) or n < 1 or n != int(n):
            raise InputError(f"events: bad count in Evicted event on {host}")
        m = RESOURCE_RE.search(e.get("message") if isinstance(e.get("message"), str) else "")
        res = m[1] if m else "unknown"
        out.setdefault(host, {})
        out[host][res] = out[host].get(res, 0) + int(n)
    return out


def node_findings(node: dict, evicted: dict) -> list:
    """Findings for one node in emission order: (sev, class, detail)."""
    name = node["metadata"]["name"]
    conds: dict = {}
    for c in as_dict(node.get("status")).get("conditions") or []:
        if isinstance(c, dict) and c.get("type") in ("Ready", "MemoryPressure", "DiskPressure", "PIDPressure"):
            if c.get("status") not in ("True", "False", "Unknown"):
                raise InputError(f"nodes: bad condition status in {name}: {c.get('type')}")
            conds[c["type"]] = c
    taints = {t.get("key") for t in (as_dict(node.get("spec")).get("taints") or []) if isinstance(t, dict)}
    out = []
    ready = conds.get("Ready")
    if ready is None or ready["status"] != "True":
        out.append(("CRIT", "NOTREADY", f"status={ready['status'] if ready else 'Missing'} reason={text(ready.get('reason')) if ready else '-'}"))
    for ctype, cls, taint in PRESSURE:
        c = conds.get(ctype)
        if c is not None and c["status"] == "True":
            out.append(("CRIT", cls, f"since={text(c.get('lastTransitionTime'))} reason={text(c.get('reason'))}"))
            if taint not in taints:
                out.append(("WARN", "NOTAINT", taint))
    ev = evicted.get(name, {})
    total = sum(ev.values())
    if total > 0:
        detail = f"total={total} " + " ".join(f"{r}={ev[r]}" for r in sorted(ev))
        out.append(("CRIT" if total >= STORM else "WARN", "EVICTIONSTORM" if total >= STORM else "EVICTIONS", detail))
    return out


def check(nodes_doc: Any, events_doc: Any, name: str) -> dict:
    nodes = [n for n in items_of(nodes_doc, "nodes") if n.get("kind") == "Node"]
    for n in nodes:
        nm = as_dict(n.get("metadata")).get("name")
        if not isinstance(nm, str) or nm == "":
            raise InputError("nodes: Node without metadata.name")
    evicted = evictions_by_host(events_doc)
    rows = []
    for n in sorted(nodes, key=lambda n: n["metadata"]["name"]):
        for sev, cls, detail in node_findings(n, evicted):
            rows.append({"sev": sev, "node": n["metadata"]["name"], "cls": cls, "detail": detail, "idx": len(rows)})
    rows.sort(key=lambda r: (r["sev"] != "CRIT", r["node"], r["idx"]))
    crit = sum(r["sev"] == "CRIT" for r in rows)
    warn = len(rows) - crit
    result = "CRIT" if crit else ("WARN" if warn else "OK")
    flagged = len({r["node"] for r in rows})
    return {"name": name, "rows": rows, "crit": crit, "warn": warn, "nodes": len(nodes),
            "healthy": len(nodes) - flagged, "result": result}


def render(r: dict) -> str:
    lines = [f"node-pressure report: {r['name']}"]
    lines += [f"{x['sev']} {x['node']} {x['cls']} {x['detail']}" for x in r["rows"]]
    present = {x["cls"] for x in r["rows"]}
    lines += [f"HINT {c}: {HINTS[c]}" for c in CLASSES if c in present]
    lines.append(f"RESULT {r['result']} crit={r['crit']} warn={r['warn']} nodes={r['nodes']} healthy={r['healthy']}")
    return "\n".join(lines) + "\n"


def load(path: str) -> Any:
    try:
        return json.loads(pathlib.Path(path).read_text())
    except (OSError, ValueError) as e:
        raise InputError(f"{path}: {e}") from e


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(prog="nodepressure", description=__doc__.split("\n")[0])
    ap.add_argument("--json", action="store_true", help="print the findings as JSON instead of the text report")
    ap.add_argument("nodes")
    ap.add_argument("events")
    args = ap.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s nodepressure [%(levelname)s] %(message)s", stream=sys.stderr)
    name = re.sub(r"(\.nodes)?\.json$", "", pathlib.Path(args.nodes).name)
    try:
        r = check(load(args.nodes), load(args.events), name)
    except InputError as e:
        log.error("%s", e)
        return 3
    sys.stdout.write(json.dumps(r, indent=2) + "\n" if args.json else render(r))
    return {"OK": 0, "WARN": 1, "CRIT": 2}[r["result"]]


if __name__ == "__main__":
    sys.exit(main())
