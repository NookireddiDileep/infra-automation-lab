#!/usr/bin/env python3
"""podtriage: classify unhealthy pods from saved `oc get pods -A -o json` output.

Exit codes: 0 OK, 1 WARN, 2 CRIT, 3 usage/runtime error.
Contract: see ../README.md ("The contract").
"""
from __future__ import annotations

import argparse
import json
import logging
import os
import sys
from typing import Any

LOG = logging.getLogger("podtriage")

HINTS = {
    "ContainerConfigError": "oc describe pod: missing Secret/ConfigMap key or bad securityContext",
    "CrashLoopBackOff": "oc logs -p <pod> -c <container>; check exit code, probes, startup deps",
    "Evicted": "node pressure: oc describe node; set ephemeral-storage requests/limits",
    "HighRestarts": "oc logs -p; check liveness probe and exit codes before it becomes a CrashLoop",
    "ImagePullBackOff": "oc describe pod: image tag, pull secret, registry reachability",
    "OOMKilled": "compare limit with working set (oc adm top pod); raise limit or fix the leak",
}
PULL = {"ImagePullBackOff", "ErrImagePull"}
CONFIG = {"CreateContainerConfigError", "CreateContainerError"}
TAB = "\t"


def classify_container(pod: dict, cs: dict, restart_warn: int) -> tuple[int, str, str] | None:
    """Return (rank, class, detail) for one container status, or None if healthy.

    rank 0 = CRIT, 1 = WARN. First matching rule wins (order is part of the contract).
    """
    state = cs.get("state") or {}
    last = (cs.get("lastState") or {}).get("terminated") or {}
    waiting = (state.get("waiting") or {}).get("reason", "")
    cur_term = state.get("terminated") or {}
    restarts = int(cs.get("restartCount", 0))

    if waiting in PULL:
        return 0, "ImagePullBackOff", f"image={cs.get('image') or '?'}"
    if waiting in CONFIG:
        return 0, "ContainerConfigError", f"reason={waiting}"
    if last.get("reason") == "OOMKilled" or cur_term.get("reason") == "OOMKilled":
        down = bool(waiting) or bool(cur_term)
        limit = "none"
        for c in (pod.get("spec") or {}).get("containers") or []:
            if c.get("name") == cs.get("name"):
                limit = ((c.get("resources") or {}).get("limits") or {}).get("memory", "none")
        return (0 if down else 1), "OOMKilled", f"restarts={restarts} limit={limit}"
    if waiting == "CrashLoopBackOff":
        return 0, "CrashLoopBackOff", f"restarts={restarts} exit={last.get('exitCode', '?')}"
    if restarts >= restart_warn:
        return 1, "HighRestarts", f"restarts={restarts}"
    return None


def triage(doc: Any, name: str, restart_warn: int = 5) -> tuple[str, int]:
    """Return (report text, exit code)."""
    if not isinstance(doc, dict) or doc.get("kind") not in ("List", "PodList") or not isinstance(doc.get("items"), list):
        raise ValueError("not a Pod List (expected kind List/PodList with items[])")
    rows: list[tuple[int, str, str, str]] = []   # rank, ns/pod, container, text
    for pod in doc["items"]:
        meta = pod.get("metadata") or {}
        key = f"{meta.get('namespace') or '?'}/{meta.get('name') or '?'}"
        status = pod.get("status") or {}
        phase = status.get("phase", "")
        if phase == "Failed" and status.get("reason") == "Evicted":
            node = (pod.get("spec") or {}).get("nodeName") or "?"
            rows.append((1, key, "-", f"WARN Evicted {key} - node={node}"))
            continue
        if phase not in ("Running", "Pending"):
            continue
        for cs in status.get("containerStatuses") or []:
            hit = classify_container(pod, cs, restart_warn)
            if hit:
                rank, cls, detail = hit
                sev = "CRIT" if rank == 0 else "WARN"
                cname = cs.get("name") or "?"
                rows.append((rank, key, cname, f"{sev} {cls} {key} {cname} {detail}"))
    rows.sort(key=lambda r: (r[0], r[1], r[2]))
    crit = sum(1 for r in rows if r[0] == 0)
    warn = len(rows) - crit
    classes = sorted({r[3].split(" ")[1] for r in rows})
    lines = [f"podtriage report: {name}"] + [r[3] for r in rows]
    lines += [f"HINT {c}: {HINTS[c]}" for c in classes]
    result = "CRIT" if crit else ("WARN" if warn else "OK")
    lines.append(f"RESULT {result} crit={crit} warn={warn} pods={len(doc['items'])}")
    return "\n".join(lines) + "\n", {"OK": 0, "WARN": 1, "CRIT": 2}[result]


def report_name(path: str) -> str:
    base = os.path.basename(path)
    for suffix in (".pods.json", ".json"):
        if base.endswith(suffix):
            return base[: -len(suffix)]
    return base


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("pods_json", help="saved output of: oc get pods -A -o json")
    ap.add_argument("-r", "--restart-warn", type=int, default=5, help="WARN at this restartCount (default 5)")
    ap.add_argument("-v", "--verbose", action="store_true")
    args = ap.parse_args(argv)
    logging.basicConfig(level=logging.DEBUG if args.verbose else logging.WARNING,
                        format="%(asctime)s podtriage [%(levelname)s] %(message)s", stream=sys.stderr)
    if args.restart_warn < 1:
        LOG.error("--restart-warn must be >= 1")
        return 3
    try:
        with open(args.pods_json, encoding="utf-8") as fh:
            doc = json.load(fh)
        text, code = triage(doc, report_name(args.pods_json), args.restart_warn)
    except (OSError, ValueError) as exc:   # JSONDecodeError is a ValueError
        LOG.error("%s: %s", args.pods_json, exc)
        return 3
    sys.stdout.write(text)
    return code


if __name__ == "__main__":
    sys.exit(main())
