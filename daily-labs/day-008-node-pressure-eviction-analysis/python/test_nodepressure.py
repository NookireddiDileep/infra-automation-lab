"""Golden and edge tests for nodepressure.py (same samples and exit codes as Bash/Go/Ansible)."""
import json
import pathlib

import pytest

import nodepressure as np_

S = pathlib.Path(__file__).resolve().parent.parent / "samples"


def run(nodes, events, capsys, *extra):
    rc = np_.main([*extra, str(nodes), str(events)])
    return rc, capsys.readouterr()


@pytest.mark.parametrize("name,rc", [("ocp-prod", 2), ("ocp-stage", 1), ("ocp-dev", 0)])
def test_golden(name, rc, capsys):
    got, out = run(S / f"{name}.nodes.json", S / f"{name}.events.json", capsys)
    assert got == rc
    assert out.out == (S / "expected" / f"{name}.report").read_text()


def node(name="node-01", **conds):
    c = [{"type": t, "status": s} for t, s in conds.items()]
    return {"kind": "Node", "metadata": {"name": name}, "status": {"conditions": c}}


def evt(host, msg="The node was low on resource: memory. x", count=None, reason="Evicted"):
    e = {"reason": reason, "message": msg, "source": {"host": host}}
    if count is not None:
        e["count"] = count
    return e


def lst(*items):
    return {"kind": "List", "items": list(items)}


def test_storm_boundary_is_five():
    nodes = lst(node("a", Ready="True"))
    four = np_.check(nodes, lst(evt("a", count=4)), "t")
    five = np_.check(nodes, lst(evt("a", count=5)), "t")
    assert (four["result"], five["result"]) == ("WARN", "CRIT")
    assert five["rows"][0]["cls"] == "EVICTIONSTORM"


def test_count_defaults_to_one_and_sums_per_resource():
    r = np_.check(lst(node("a", Ready="True")), lst(evt("a"), evt("a", count=2), evt("a", "low on resource: pids. y")), "t")
    assert r["rows"][0]["detail"] == "total=4 memory=3 pids=1"


def test_unknown_resource_and_foreign_host():
    r = np_.check(lst(node("a", Ready="True")), lst(evt("a", "no resource here"), evt("b")), "t")
    assert r["rows"][0]["detail"] == "total=1 unknown=1"


def test_missing_ready_is_notready():
    r = np_.check(lst(node("a")), lst(), "t")
    assert r["rows"][0]["detail"] == "status=Missing reason=-"


def test_notready_node_does_not_also_report_unknown_pressure():
    r = np_.check(lst(node("a", Ready="Unknown", MemoryPressure="Unknown")), lst(), "t")
    assert [x["cls"] for x in r["rows"]] == ["NOTREADY"]


@pytest.mark.parametrize("bad", [0, -1, 1.5, "2", True])
def test_bad_count_refused(bad):
    with pytest.raises(np_.InputError, match="bad count"):
        np_.check(lst(node("a", Ready="True")), lst(evt("a", count=bad)), "t")


def test_non_evicted_events_are_not_validated():
    np_.check(lst(node("a", Ready="True")), lst({"reason": "BackOff", "count": 0}), "t")


def test_bad_status_and_missing_name_and_host():
    with pytest.raises(np_.InputError, match="bad condition status"):
        np_.check(lst(node("a", Ready="Maybe")), lst(), "t")
    with pytest.raises(np_.InputError, match="without metadata.name"):
        np_.check(lst({"kind": "Node", "metadata": {}}), lst(), "t")
    with pytest.raises(np_.InputError, match="without source.host"):
        np_.check(lst(), lst({"reason": "Evicted"}), "t")


def test_bad_files(tmp_path, capsys):
    (tmp_path / "bad.json").write_text("{not json")
    for nodes, events in [(S / "bad/not-a-list.json", S / "ocp-dev.events.json"),
                          (S / "ocp-dev.nodes.json", S / "bad/not-a-list.json"),
                          (S / "bad/nodes-bad-status.json", S / "ocp-dev.events.json"),
                          (S / "ocp-dev.nodes.json", S / "bad/events-bad-count.json"),
                          (tmp_path / "bad.json", S / "ocp-dev.events.json"),
                          (tmp_path / "nope.json", S / "ocp-dev.events.json")]:
        rc, out = run(nodes, events, capsys)
        assert rc == 3 and out.out == ""


def test_json_view(capsys):
    rc, out = run(S / "ocp-stage.nodes.json", S / "ocp-stage.events.json", capsys, "--json")
    assert rc == 1 and json.loads(out.out)["warn"] == 2


def test_explicit_null_count_refused():
    e = evt("a")
    e["count"] = None
    with pytest.raises(np_.InputError, match="bad count"):
        np_.check(lst(node("a", Ready="True")), lst(e), "t")
