#!/usr/bin/env python3
"""pvcbind: PVC Pending / StorageClass binding-mode diagnosis from saved `oc get pvc -A -o json` and `oc get sc -o json`.

    pvcbind.py [--json] PVCS_JSON STORAGECLASSES_JSON

Exit codes: 0 OK, 1 WARN, 2 CRIT, 3 usage/input error. Report format: see ../README.md ("The contract").
"""
from __future__ import annotations

import argparse
import json
import logging
import re
import sys
from typing import Any

LOG = logging.getLogger("pvcbind")
DEFAULT_ANN = "storageclass.kubernetes.io/is-default-class"
SELECTED_ANN = "volume.kubernetes.io/selected-node"
NO_PROV = "kubernetes.io/no-provisioner"
WFFC = "WaitForFirstConsumer"
SEVERITY = ("CRIT", "WARN", "INFO")
CLASS_ORDER = ("LOST", "NOSC", "NOPV", "UNKNOWNSC", "SELECTEDSTUCK", "PROVISIONFAILED",
               "MULTIDEFAULT", "LOCALIMMEDIATE", "WAITFORCONSUMER")
HINTS = {
    "LOST": "the PV behind this claim is gone: oc get pv, check the backend volume; restore from snapshot or recreate, never delete the claim first",
    "NOSC": "no storageClassName and no default StorageClass: set storageclass.kubernetes.io/is-default-class=true on one class or name a class in the claim",
    "NOPV": "this claim needs a pre-created PV (empty class or no-provisioner): oc get pv, match size, access mode and class, or create one",
    "UNKNOWNSC": "the named StorageClass does not exist: oc get sc, fix the typo in the claim or create the class",
    "SELECTEDSTUCK": "the scheduler picked a node but the provisioner could not create the volume there: oc describe pvc (ProvisioningFailed), check zone and topology of that node",
    "PROVISIONFAILED": "Immediate class, dynamic provisioner, still Pending: oc describe pvc for ProvisioningFailed, check the CSI controller pods and quota on the backend",
    "MULTIDEFAULT": "more than one default StorageClass: claims without a class get an arbitrary one; keep exactly one default",
    "LOCALIMMEDIATE": "a no-provisioner class bound Immediate binds before scheduling and strands pods: use volumeBindingMode WaitForFirstConsumer",
    "WAITFORCONSUMER": "not a fault: the claim stays Pending until a pod that uses it is scheduled; if no pod exists yet, this is expected",
}


class InputError(Exception):
    """Bad input: the report is refused (exit 3), never printed half done."""


def load_list(path: str, what: str) -> list[dict[str, Any]]:
    try:
        with open(path, encoding="utf-8") as fh:
            doc = json.load(fh)
    except (OSError, ValueError) as exc:
        raise InputError(f"{path}: {exc}") from exc
    if not isinstance(doc, dict) or doc.get("kind") != "List" or not isinstance(doc.get("items"), list):
        raise InputError(f"{what}: not a List (expected kind List with items[])")
    return [i for i in doc["items"] if isinstance(i, dict)]


def obj(v: Any) -> dict[str, Any]:
    return v if isinstance(v, dict) else {}


def txt(v: Any) -> str:
    return v if isinstance(v, str) and v != "" else "-"


def parse_classes(items: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    classes: dict[str, dict[str, Any]] = {}
    for it in items:
        if it.get("kind") != "StorageClass":
            continue
        name = obj(it.get("metadata")).get("name")
        if not isinstance(name, str) or name == "":
            raise InputError("storageclasses: StorageClass without metadata.name")
        mode = it.get("volumeBindingMode")
        if mode is None:
            mode = "Immediate"
        if mode not in ("Immediate", WFFC):
            raise InputError(f"storageclasses: bad volumeBindingMode in {name}")
        prov = it.get("provisioner")
        classes[name] = {
            "mode": mode,
            "prov": prov if isinstance(prov, str) else "-",
            "default": obj(obj(it.get("metadata")).get("annotations")).get(DEFAULT_ANN) == "true",
        }
    return classes


def row(rank: int, subject: str, cls: str, detail: str, is_pvc: bool) -> dict[str, Any]:
    return {"rank": rank, "subject": subject, "cls": cls, "text": f"{SEVERITY[rank]} {subject} {cls} {detail}", "pvc": is_pvc}


def judge_pvc(it: dict[str, Any], classes: dict[str, dict[str, Any]], default: str | None) -> dict[str, Any] | None:
    md, spec, status = obj(it.get("metadata")), obj(it.get("spec")), obj(it.get("status"))
    ns, name = md.get("namespace"), md.get("name")
    if not (isinstance(ns, str) and ns and isinstance(name, str) and name):
        raise InputError("pvcs: PersistentVolumeClaim without metadata.namespace or metadata.name")
    subject = f"{ns}/{name}"
    phase = status.get("phase")
    if phase not in ("Pending", "Bound", "Lost"):
        raise InputError(f"pvcs: bad phase in {subject}")
    sc = spec.get("storageClassName")
    if sc is not None and not isinstance(sc, str):
        raise InputError(f"pvcs: bad storageClassName in {subject}")
    if phase == "Bound":
        return None
    if phase == "Lost":
        return row(0, subject, "LOST", f"volume={txt(spec.get('volumeName'))}", True)
    via = ""
    if sc is None:
        if default is None:
            return row(0, subject, "NOSC", "class=-", True)
        sc, via = default, " via=default"
    if sc == "":
        return row(0, subject, "NOPV", "class=-", True)
    cl = classes.get(sc)
    if cl is None:
        return row(0, subject, "UNKNOWNSC", f"class={sc}", True)
    if cl["mode"] == WFFC:
        node = obj(md.get("annotations")).get(SELECTED_ANN)
        if isinstance(node, str) and node:
            return row(0, subject, "SELECTEDSTUCK", f"class={sc} node={node} provisioner={cl['prov']}{via}", True)
        return row(2, subject, "WAITFORCONSUMER", f"class={sc} mode={WFFC}{via}", True)
    if cl["prov"] == NO_PROV:
        return row(0, subject, "NOPV", f"class={sc}{via}", True)
    return row(0, subject, "PROVISIONFAILED", f"class={sc} provisioner={cl['prov']}{via}", True)


def check(pvcs: list[dict[str, Any]], scs: list[dict[str, Any]]) -> dict[str, Any]:
    classes = parse_classes(scs)
    defaults = sorted(n for n, c in classes.items() if c["default"])
    default = defaults[0] if defaults else None
    claims = [p for p in pvcs if p.get("kind") == "PersistentVolumeClaim"]
    rows: list[dict[str, Any]] = []
    for p in sorted(claims, key=lambda p: f"{obj(p.get('metadata')).get('namespace')}/{obj(p.get('metadata')).get('name')}"):
        r = judge_pvc(p, classes, default)
        if r:
            rows.append(r)
    for name in sorted(classes):
        c = classes[name]
        if len(defaults) > 1 and c["default"]:
            rows.append(row(1, f"sc/{name}", "MULTIDEFAULT", "defaults=" + ",".join(defaults), False))
        if c["prov"] == NO_PROV and c["mode"] != WFFC:
            rows.append(row(1, f"sc/{name}", "LOCALIMMEDIATE", f"provisioner={NO_PROV} mode={c['mode']}", False))
    for i, r in enumerate(rows):
        r["idx"] = i
    rows.sort(key=lambda r: (r["rank"], r["subject"], r["idx"]))
    return {"rows": rows, "pvcs": len(claims)}


def render(name: str, res: dict[str, Any]) -> tuple[str, str]:
    rows = res["rows"]
    n = [sum(1 for r in rows if r["rank"] == k) for k in range(3)]
    present = {r["cls"] for r in rows}
    verdict = "CRIT" if n[0] else "WARN" if n[1] else "OK"
    healthy = res["pvcs"] - sum(1 for r in rows if r["pvc"])
    lines = [f"pvc-binding report: {name}"]
    lines += [r["text"] for r in rows]
    lines += [f"HINT {c}: {HINTS[c]}" for c in CLASS_ORDER if c in present]
    lines.append(f"RESULT {verdict} crit={n[0]} warn={n[1]} info={n[2]} pvcs={res['pvcs']} healthy={healthy}")
    return "\n".join(lines) + "\n", verdict


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("pvcs", help="saved `oc get pvc -A -o json`")
    ap.add_argument("storageclasses", help="saved `oc get sc -o json`")
    ap.add_argument("--json", action="store_true", help="machine-readable view (the text report is the contract)")
    try:
        args = ap.parse_args(argv)
    except SystemExit as exc:
        return 0 if exc.code == 0 else 3
    logging.basicConfig(level=logging.INFO, format="%(asctime)s pvcbind [%(levelname)s] %(message)s", stream=sys.stderr)
    try:
        res = check(load_list(args.pvcs, "pvcs"), load_list(args.storageclasses, "storageclasses"))
    except InputError as exc:
        LOG.error("%s", exc)
        return 3
    name = re.sub(r"(\.pvcs)?\.json$", "", args.pvcs.rsplit("/", 1)[-1])
    report, verdict = render(name, res)
    if args.json:
        print(json.dumps({"name": name, "result": verdict, **res}, indent=2))
    else:
        sys.stdout.write(report)
    return {"OK": 0, "WARN": 1, "CRIT": 2}[verdict]


if __name__ == "__main__":
    sys.exit(main())
