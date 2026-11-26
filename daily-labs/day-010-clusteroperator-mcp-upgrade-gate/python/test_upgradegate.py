"""Golden + edge tests: same samples and exit codes as Bash/Go/Ansible."""
import json
from pathlib import Path

import pytest

import upgradegate as ug

S = Path(__file__).resolve().parent.parent / "samples"


def run(cos, mcps, capsys, *extra):
    rc = ug.main([*extra, str(cos), str(mcps)])
    return rc, capsys.readouterr()


@pytest.mark.parametrize("name,rc", [("ocp-prod", 2), ("ocp-lab", 2), ("ocp-stage", 1), ("ocp-dev", 0)])
def test_golden(name, rc, capsys):
    got, out = run(S / f"{name}.clusteroperators.json", S / f"{name}.mcps.json", capsys)
    assert got == rc
    assert out.out == (S / "expected" / f"{name}.report").read_text()


@pytest.mark.parametrize("cos,mcps,msg", [
    ("bad/not-a-list.json", "ocp-dev.mcps.json", "clusteroperators: not a List"),
    ("ocp-dev.clusteroperators.json", "bad/not-a-list.json", "mcps: not a List"),
    ("bad/co-bad-status.json", "ocp-dev.mcps.json", "clusteroperators: bad condition status in etcd"),
    ("ocp-dev.clusteroperators.json", "bad/mcps-bad-count.json", "mcps: bad machineCount in worker"),
    ("nope.json", "ocp-dev.mcps.json", "cannot read"),
])
def test_bad_input_is_refused(cos, mcps, msg, capsys, caplog):
    rc, out = run(S / cos, S / mcps, capsys)
    assert rc == 3 and out.out == "" and msg in caplog.text


def test_usage_error_is_3(capsys):
    assert ug.main([]) == 3


def doc(*items):
    return {"kind": "List", "items": list(items)}


def co(name, **conds):
    return {"kind": "ClusterOperator", "metadata": {"name": name},
            "status": {"conditions": [{"type": k, "status": v} for k, v in conds.items()]}}


def pool(name, total=2, upd=2, rdy=2, un=0, dg=0, paused=False, **conds):
    return {"kind": "MachineConfigPool", "metadata": {"name": name}, "spec": {"paused": paused},
            "status": {"machineCount": total, "updatedMachineCount": upd, "readyMachineCount": rdy,
                       "unavailableMachineCount": un, "degradedMachineCount": dg,
                       "conditions": [{"type": k, "status": v} for k, v in conds.items()]}}


def test_unavailable_wins_over_degraded():
    r = ug.check(doc(co("a", Available="False", Degraded="True")), doc())
    assert [x.cls for x in r.rows] == ["UNAVAILABLE"]


def test_available_unknown_is_unavailable_not_ok():
    assert ug.check(doc(co("a", Available="Unknown")), doc()).rows[0].cls == "UNAVAILABLE"


def test_missing_upgradeable_is_fine():
    assert ug.check(doc(co("a", Available="True", Degraded="False")), doc()).rows == []


def test_updating_pool_with_unavailable_node_is_only_warn():
    r = ug.check(doc(), doc(pool("w", upd=1, un=1, Updating="True")))
    assert [(x.rank, x.cls) for x in r.rows] == [(1, "UPDATING")]


def test_paused_behind_pool_is_warn_not_stalled():
    r = ug.check(doc(), doc(pool("w", upd=1, paused=True)))
    assert [x.cls for x in r.rows] == ["PAUSED"]


def test_idle_pool_behind_is_stalled():
    assert ug.check(doc(), doc(pool("w", upd=1))).rows[0].cls == "STALLED"


def test_degraded_count_alone_triggers_degraded():
    assert ug.check(doc(), doc(pool("w", dg=1))).rows[0].cls == "DEGRADED"


def test_bool_count_is_refused():
    p = pool("w")
    p["status"]["machineCount"] = True
    with pytest.raises(ug.InputError):
        ug.check(doc(), doc(p))


def test_sort_is_bytewise_not_locale():
    r = ug.check(doc(co("b", Available="False"), co("B", Available="False")), doc())
    assert [x.subject for x in r.rows] == ["co/B", "co/b"]


def test_json_output(capsys):
    rc, out = run(S / "ocp-stage.clusteroperators.json", S / "ocp-stage.mcps.json", capsys, "--json")
    d = json.loads(out.out)
    assert rc == 1 and d["result"] == "WARN" and len(d["rows"]) == 2
