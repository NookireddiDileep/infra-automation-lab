#!/usr/bin/env python3
"""rightsize: ResourceQuota right-sizing report (quota hard/used vs real PodMetrics usage).

    rightsize.py [--json] QUOTA_JSON METRICS_JSON

QUOTA_JSON   saved `oc get resourcequota -A -o json`
METRICS_JSON saved `oc get --raw /apis/metrics.k8s.io/v1beta1/pods` (kind List of PodMetrics)

Exit codes: 0 OK, 1 WARN, 2 CRIT, 3 usage/input error. Contract: ../README.md.
"""
import argparse
import json
import logging
import pathlib
import re
import sys
from typing import Any, Optional

log = logging.getLogger("rightsize")

KEYS = (("requests.cpu", "cpu"), ("requests.memory", "mem"), ("pods", "int"))
UNITS = {"": 1, "Ki": 1024, "Mi": 1048576, "Gi": 1073741824, "Ti": 1099511627776}
CPU_N = {"n": 1, "u": 1000, "m": 1_000_000, "": 1_000_000_000}
CLASSES = ["FULL", "NEAR", "OVERQUOTA", "OVERREQUEST", "UNDERREQUEST", "NOMETRICS", "INVALID"]
HINTS = {
    "FULL": "new pods are rejected (exceeded quota); find oversized requests first, then raise hard",
    "NEAR": "under 15% headroom: a rollout surge can be rejected; plan the bump before the deploy",
    "OVERQUOTA": "hard is 4x or more the use: shrink it and give the capacity back to the cluster",
    "OVERREQUEST": "requests far above actual use: lower requests (VPA recommend, oc adm top) to free schedulable capacity",
    "UNDERREQUEST": "pods use more than they request: raise requests or they are throttled or evicted first under node pressure",
    "NOMETRICS": "no PodMetrics for this namespace: check metrics-server before trusting right-sizing",
    "INVALID": "quota value is not a valid quantity (or hard is 0): fix the object, the check cannot reason about it",
}


class InputError(ValueError):
    """Bad input: refuse (exit 3) instead of producing a half report."""


def qty(kind: str, v: Any) -> Optional[int]:
    """cpu -> millicores, mem -> bytes, int -> int. None = not a valid quantity (anything but a string is invalid)."""
    if not isinstance(v, str):
        return None
    if kind == "cpu":
        m = re.fullmatch(r"([0-9]+)(m?)", v)
        return None if not m else (int(m[1]) if m[2] else int(m[1]) * 1000)
    if kind == "mem":
        m = re.fullmatch(r"([0-9]+)(Ki|Mi|Gi|Ti)?", v)
        return None if not m else int(m[1]) * UNITS[m[2] or ""]
    m = re.fullmatch(r"[0-9]+", v)
    return int(v) if m else None


def raw(v: Any) -> str:
    return v if isinstance(v, str) else json.dumps(v, separators=(",", ":"))


def items_of(doc: Any, what: str) -> list:
    if not isinstance(doc, dict) or doc.get("kind") != "List" or not isinstance(doc.get("items"), list):
        raise InputError(f"{what}: not a List (expected kind List with items[])")
    return [i for i in doc["items"] if isinstance(i, dict)]


def namespace_usage(metrics_doc: Any) -> dict:
    """namespace -> {cpu: millicores (summed in nanocores, then floored), mem: bytes, pods: n}"""
    acc: dict = {}
    for pm in items_of(metrics_doc, "metrics"):
        if pm.get("kind") != "PodMetrics":
            continue
        ns = (pm.get("metadata") or {}).get("namespace")
        if not isinstance(ns, str) or not ns:
            raise InputError("metrics: PodMetrics without metadata.namespace")
        u = acc.setdefault(ns, {"cpu": 0, "mem": 0})
        for c in pm.get("containers") or []:
            usage = (c or {}).get("usage") or {}
            cpu, mem = usage.get("cpu"), usage.get("memory")
            mc = re.fullmatch(r"([0-9]+)(n|u|m)?", cpu) if isinstance(cpu, str) else None
            mm = re.fullmatch(r"([0-9]+)(Ki|Mi|Gi)?", mem) if isinstance(mem, str) else None
            if not mc or not mm:
                raise InputError(f"metrics: bad container usage in {ns}: cpu={raw(cpu)} memory={raw(mem)}")
            u["cpu"] += int(mc[1]) * CPU_N[mc[2] or ""]
            u["mem"] += int(mm[1]) * UNITS[mm[2] or ""]
    return {ns: {"cpu": v["cpu"] // 1_000_000, "mem": v["mem"]} for ns, v in acc.items()}


def quota_findings(q: dict, usage: dict) -> list:
    """Findings for one ResourceQuota in emission order: (rank, ns, cls, text). rank 0 = CRIT, 1 = WARN."""
    md = q.get("metadata") or {}
    ns, qn = md.get("namespace"), md.get("name")
    if not isinstance(ns, str) or not isinstance(qn, str) or not ns or not qn:
        raise InputError("quota: ResourceQuota without metadata.name/namespace")
    status = q.get("status") if isinstance(q.get("status"), dict) else {}
    hard = status.get("hard") if isinstance(status.get("hard"), dict) else {}
    used = status.get("used") if isinstance(status.get("used"), dict) else {}
    out: list = []

    def add(rank: int, cls: str, detail: str) -> None:
        out.append((rank, ns, cls, f"{'CRIT' if rank == 0 else 'WARN'} {ns} quota/{qn} {cls} {detail}"))

    for key, kind in KEYS:
        if key not in hard or key not in used:
            continue
        h, u = qty(kind, hard[key]), qty(kind, used[key])
        if h is None or u is None or h == 0:
            add(1, "INVALID", f"{key} hard={raw(hard[key])} used={raw(used[key])}")
            continue
        pct = u * 100 // h
        detail = f"{key} used={raw(used[key])} hard={raw(hard[key])} pct={pct}%"
        if pct >= 100:
            add(0, "FULL", detail)
        elif pct >= 85:
            add(1, "NEAR", detail)
        elif kind != "int" and u * 4 <= h:
            add(1, "OVERQUOTA", detail)
        if kind != "int" and ns in usage and u > 0:
            a = usage[ns]["cpu" if kind == "cpu" else "mem"]
            shown = f"{a}m" if kind == "cpu" else f"{a // 1048576}Mi"
            if a * 100 < u * 30:
                add(1, "OVERREQUEST", f"{key} requested={raw(used[key])} actual={shown} eff={a * 100 // u}%")
            elif a * 100 > u * 120:
                add(0 if kind == "mem" else 1, "UNDERREQUEST", f"{key} requested={raw(used[key])} actual={shown} eff={a * 100 // u}%")
    cpu_used = qty("cpu", used.get("requests.cpu"))
    if cpu_used and ns not in usage:
        add(1, "NOMETRICS", f"requests.cpu used={raw(used['requests.cpu'])} no PodMetrics")
    return out


def check(quota_doc: Any, metrics_doc: Any, name: str) -> tuple:
    """Return (report_text, exit_code, rows)."""
    quotas = [q for q in items_of(quota_doc, "quota") if q.get("kind") == "ResourceQuota"]
    usage = namespace_usage(metrics_doc)
    rows: list = []
    quotas.sort(key=lambda q: ((q.get("metadata") or {}).get("namespace") or "", (q.get("metadata") or {}).get("name") or ""))
    flagged = set()
    for q in quotas:
        found = quota_findings(q, usage)
        if found:
            flagged.add(((q["metadata"]["namespace"]), q["metadata"]["name"]))
        rows.extend(found)
    ordered = sorted(enumerate(rows), key=lambda t: (t[1][0], t[1][1], t[0]))   # CRIT first, then namespace, then emission order
    rows = [r for _, r in ordered]
    crit = sum(1 for r in rows if r[0] == 0)
    warn = len(rows) - crit
    lines = [f"rightsize report: {name}"] + [r[3] for r in rows]
    present = {r[2] for r in rows}
    lines += [f"HINT {c}: {HINTS[c]}" for c in CLASSES if c in present]
    result = "CRIT" if crit else ("WARN" if warn else "OK")
    lines.append(f"RESULT {result} crit={crit} warn={warn} quotas={len(quotas)} healthy={len(quotas) - len(flagged)}")
    code = {"OK": 0, "WARN": 1, "CRIT": 2}[result]
    return "\n".join(lines) + "\n", code, rows


def report_name(path: str) -> str:
    base = pathlib.Path(path).name
    return re.sub(r"(\.quota)?\.json$", "", base)


def load(path: str) -> Any:
    try:
        return json.loads(pathlib.Path(path).read_text())
    except (OSError, ValueError) as exc:
        raise InputError(f"{path}: {exc}") from exc


def main(argv: Optional[list] = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("quota_json")
    ap.add_argument("metrics_json")
    ap.add_argument("--json", action="store_true", help="print findings as JSON instead of the text report")
    try:
        args = ap.parse_args(argv)
    except SystemExit:
        return 3
    logging.basicConfig(level=logging.INFO, stream=sys.stderr, format="%(asctime)s rightsize [%(levelname)s] %(message)s")
    try:
        text, code, rows = check(load(args.quota_json), load(args.metrics_json), report_name(args.quota_json))
    except InputError as exc:
        log.error("%s", exc)
        return 3
    if args.json:
        print(json.dumps({"exit": code, "findings": [{"severity": "CRIT" if r[0] == 0 else "WARN", "namespace": r[1], "class": r[2], "text": r[3]} for r in rows]}, indent=2))
    else:
        sys.stdout.write(text)
    return code


if __name__ == "__main__":
    sys.exit(main())
