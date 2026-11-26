#!/usr/bin/env python3
"""upgradegate.py: pre-upgrade gate for OpenShift (saved `oc get co` + `oc get mcp`).

    upgradegate.py [--json] CLUSTEROPERATORS_JSON MCPS_JSON

Exit codes: 0 GO · 1 CAUTION (WARN) · 2 NO-GO (CRIT) · 3 usage/input error.
Report format: see ../README.md ("The contract").
"""
from __future__ import annotations

import argparse
import json
import logging
import sys
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

log = logging.getLogger("upgradegate")

CRIT, WARN = 0, 1
LEVEL = ("CRIT", "WARN")
HINT_ORDER = ("UNAVAILABLE", "NOSTATUS", "DEGRADED", "BLOCKED", "STALLED", "PROGRESSING", "UPDATING", "PAUSED")
HINTS = {
    "UNAVAILABLE": "operator Available is not True, or pool nodes are down outside a rollout: oc describe co/mcp, oc get nodes; do not upgrade until it is resolved",
    "NOSTATUS": "the operator reports no Available condition, so it is not reporting at all: check its pod and events in its own namespace",
    "DEGRADED": "Degraded=True: oc describe co/<name> for an operator, oc describe mcp plus machine-config-daemon logs for a pool; fix it before the upgrade",
    "BLOCKED": "Upgradeable=False blocks minor-version updates (patch updates still run): read the condition message, fix it or give the admin acknowledgement it asks for",
    "STALLED": "updated is below machineCount, the pool is not updating and not paused: it is stuck, check nodes for a stale desiredConfig and the machine-config-controller logs",
    "PROGRESSING": "an operator is mid-rollout: wait for Progressing=False, or the upgrade stacks a second rollout on top",
    "UPDATING": "the pool is rolling out a rendered config: let it finish, a cluster upgrade now would queue a second change on the same nodes",
    "PAUSED": "spec.paused=true: the pool ignores new rendered configs and stays on the old version; unpause before the upgrade unless it is a deliberate canary",
}


class InputError(ValueError):
    """Bad input: exit 3, never a half report."""


@dataclass(frozen=True)
class Row:
    rank: int
    subject: str
    cls: str
    detail: str

    @property
    def text(self) -> str:
        return f"{LEVEL[self.rank]} {self.subject} {self.cls} {self.detail}"


@dataclass(frozen=True)
class Result:
    rows: list[Row]
    operators: int
    pools: int


def _obj(v: Any) -> dict:
    return v if isinstance(v, dict) else {}


def _txt(v: Any) -> str:
    return v if isinstance(v, str) and v else "-"


def list_of(doc: Any, what: str, kind: str) -> list[dict]:
    if not (isinstance(doc, dict) and doc.get("kind") == "List" and isinstance(doc.get("items"), list)):
        raise InputError(f"{what}: not a List (expected kind List with items[])")
    return [i for i in doc["items"] if isinstance(i, dict) and i.get("kind") == kind]


def name_of(item: dict, what: str) -> str:
    n = _obj(item.get("metadata")).get("name")
    if not isinstance(n, str) or not n:
        raise InputError(f"{what}: item without metadata.name")
    return n


def conditions(item: dict, what: str, name: str) -> dict[str, tuple[str, str]]:
    raw = _obj(item.get("status")).get("conditions")
    if raw is None:
        return {}
    if not isinstance(raw, list):
        raise InputError(f"{what}: bad conditions in {name}")
    out: dict[str, tuple[str, str]] = {}
    for c in raw:
        if not (isinstance(c, dict) and isinstance(c.get("type"), str) and c.get("status") in ("True", "False", "Unknown")):
            raise InputError(f"{what}: bad condition status in {name}")
        out[c["type"]] = (c["status"], _txt(c.get("reason")))  # last one wins, like jq from_entries
    return out


def count(item: dict, field: str, name: str) -> int:
    v = _obj(item.get("status")).get(field)
    # bool is an int subclass in Python: refuse it explicitly. 2.0 counts as 2 (JSON has one number type).
    if isinstance(v, bool) or not isinstance(v, (int, float)) or v != int(v) or v < 0:
        raise InputError(f"mcps: bad {field} in {name}")
    return int(v)


def judge_co(item: dict) -> Row | None:
    n = name_of(item, "clusteroperators")
    c = conditions(item, "clusteroperators", n)
    s = f"co/{n}"
    if "Available" not in c:
        return Row(CRIT, s, "NOSTATUS", "reason=-")
    if c["Available"][0] != "True":
        return Row(CRIT, s, "UNAVAILABLE", f"reason={c['Available'][1]}")
    if c.get("Degraded", ("", ""))[0] == "True":
        return Row(CRIT, s, "DEGRADED", f"reason={c['Degraded'][1]}")
    if c.get("Upgradeable", ("", ""))[0] == "False":
        return Row(CRIT, s, "BLOCKED", f"reason={c['Upgradeable'][1]}")
    if c.get("Progressing", ("", ""))[0] == "True":
        return Row(WARN, s, "PROGRESSING", f"reason={c['Progressing'][1]}")
    return None


def judge_mcp(item: dict) -> Row | None:
    n = name_of(item, "mcps")
    c = conditions(item, "mcps", n)
    s = f"mcp/{n}"
    total = count(item, "machineCount", n)
    upd = count(item, "updatedMachineCount", n)
    count(item, "readyMachineCount", n)
    un = count(item, "unavailableMachineCount", n)
    dg = count(item, "degradedMachineCount", n)
    d = f"degraded={dg} unavailable={un} updated={upd}/{total}"
    if dg > 0 or c.get("Degraded", ("", ""))[0] == "True":
        return Row(CRIT, s, "DEGRADED", d)
    if c.get("Updating", ("", ""))[0] == "True":
        return Row(WARN, s, "UPDATING", d)
    if un > 0:
        return Row(CRIT, s, "UNAVAILABLE", d)
    if _obj(item.get("spec")).get("paused") is True:
        return Row(WARN, s, "PAUSED", d)
    if upd < total:
        return Row(CRIT, s, "STALLED", d)
    return None


def check(cos_doc: Any, mcps_doc: Any) -> Result:
    cos = list_of(cos_doc, "clusteroperators", "ClusterOperator")
    pools = list_of(mcps_doc, "mcps", "MachineConfigPool")
    rows = [r for r in (judge_co(i) for i in cos) if r] + [r for r in (judge_mcp(i) for i in pools) if r]
    rows.sort(key=lambda r: (r.rank, r.subject.encode()))  # stable, bytewise: LC_ALL=C order
    return Result(rows, len(cos), len(pools))


def render(name: str, res: Result) -> tuple[str, str]:
    crit = sum(r.rank == CRIT for r in res.rows)
    warn = sum(r.rank == WARN for r in res.rows)
    verdict = "CRIT" if crit else "WARN" if warn else "OK"
    words = {"CRIT": "NO-GO", "WARN": "CAUTION", "OK": "GO"}[verdict]
    present = {r.cls for r in res.rows}
    lines = [f"upgrade-gate report: {name}", *(r.text for r in res.rows)]
    lines += [f"HINT {c}: {HINTS[c]}" for c in HINT_ORDER if c in present]
    healthy = res.operators + res.pools - len(res.rows)
    lines.append(f"RESULT {verdict} verdict={words} crit={crit} warn={warn} operators={res.operators} pools={res.pools} healthy={healthy}")
    return "\n".join(lines) + "\n", verdict


def load(path: str, what: str) -> Any:
    try:
        return json.loads(Path(path).read_text(encoding="utf-8"))
    except OSError:
        raise InputError(f"{path}: cannot read") from None
    except ValueError as e:
        raise InputError(f"{what}: invalid JSON ({e})") from None


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="OpenShift pre-upgrade gate from saved oc JSON")
    ap.add_argument("clusteroperators")
    ap.add_argument("mcps")
    ap.add_argument("--json", action="store_true", help="machine-readable output")
    try:
        args = ap.parse_args(argv)
    except SystemExit:
        return 3
    logging.basicConfig(stream=sys.stderr, level=logging.INFO, format="%(asctime)s upgradegate [%(levelname)s] %(message)s")
    try:
        res = check(load(args.clusteroperators, "clusteroperators"), load(args.mcps, "mcps"))
    except InputError as e:
        log.error("%s", e)
        return 3
    name = Path(args.clusteroperators).name.removesuffix(".json").removesuffix(".clusteroperators")
    report, verdict = render(name, res)
    if args.json:
        print(json.dumps({"name": name, "result": verdict, "rows": [asdict(r) for r in res.rows],
                          "operators": res.operators, "pools": res.pools}, indent=2))
    else:
        sys.stdout.write(report)
    return {"OK": 0, "WARN": 1, "CRIT": 2}[verdict]


if __name__ == "__main__":
    sys.exit(main())
