import json
import pathlib
import subprocess
import sys

import pytest

import pendingtriage as pt

ROOT = pathlib.Path(__file__).resolve().parent.parent
SAMPLES = ROOT / "samples"
CODES = {"ocp-prod": 2, "ocp-stage": 1, "ocp-dev": 0}


@pytest.mark.parametrize("name,code", CODES.items())
def test_golden(name, code):
    text, rc = pt.triage(json.loads((SAMPLES / f"{name}.pods.json").read_text()), name)
    assert text == (SAMPLES / "expected" / f"{name}.report").read_text()
    assert rc == code


@pytest.mark.parametrize("text,cls", [
    ("3 Insufficient nvidia.com/gpu", "Capacity"),
    ("4 Too many pods", "Capacity"),
    ("8 node(s) had volume node affinity conflict", "Storage"),   # must beat the Selector rule
    ("2 node(s) had untolerated taint {a: b}", "Taint"),
    ("3 node(s) didn't match Pod's node affinity/selector", "Selector"),
    ("10 node(s) didn't match pod anti-affinity rules", "Placement"),
    ("2 node(s) were unschedulable", "Cordoned"),
    ("node(s) didn't have free ports for the requested ports", "Ports"),
    ("something new", "Other"),
])
def test_classify(text, cls):
    assert pt.classify(text) == cls


def test_clause_without_count_applies_to_every_node_and_preemption_tail_is_cut():
    msg = ("0/9 nodes are available: pod has unbound immediate PersistentVolumeClaims. "
           "preemption: 0/9 nodes are available: 9 Preemption is not helpful for scheduling.")
    assert pt.parse_message("Unschedulable", msg) == ("Storage", "9", [("Storage", 9)], "")


def test_insufficient_clauses_overlap_and_ties_break_by_class_name():
    cls, nodes, causes, need = pt.parse_message(
        "Unschedulable", "0/6 nodes are available: 4 Insufficient cpu, 5 Insufficient memory, 1 node(s) were unschedulable.")
    assert (cls, causes[0], need) == ("Capacity", ("Capacity", 5), "cpu+memory")
    tie = pt.parse_message("Unschedulable", "0/6 nodes are available: 3 Insufficient cpu, 3 node(s) had untolerated taint {k: v}.")
    assert tie[0] == "Capacity"   # 'Capacity' < 'Taint'


def test_gated_and_unparsed():
    assert pt.parse_message("SchedulingGated", "x")[0] == "Gated"
    assert pt.parse_message("Unschedulable", "weird")[0] == "Unparsed"


def pod(phase, cond):
    return {"metadata": {"name": "p", "namespace": "n"}, "status": {"phase": phase, "conditions": cond}}


def test_scheduled_or_non_pending_pods_are_ignored():
    scheduled = [{"type": "PodScheduled", "status": "True"}]
    unsched = [{"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
                "message": "0/3 nodes are available: 3 Insufficient cpu."}]
    doc = {"kind": "List", "items": [pod("Pending", scheduled), pod("Running", unsched), pod("Succeeded", unsched)]}
    assert pt.triage(doc, "x")[1] == 0
    doc["items"].append(pod("Pending", unsched))
    assert pt.triage(doc, "x")[1] == 1


@pytest.mark.parametrize("bad", [[], {"kind": "Pod"}, {"kind": "List", "items": {}}])
def test_rejects_non_pod_list(bad):
    with pytest.raises(ValueError):
        pt.triage(bad, "x")


def test_json_view():
    text, _ = pt.triage(json.loads((SAMPLES / "ocp-stage.pods.json").read_text()), "ocp-stage")
    out = json.loads(pt.to_json(text))
    assert out["result"].startswith("RESULT WARN")
    assert out["findings"][1] == {"severity": "WARN", "class": "Capacity", "pod": "ml/notebook-0",
                                  "nodes": "6", "causes": "Capacity:6", "need": "nvidia.com/gpu"}


def test_cli_exit_codes(tmp_path):
    cli = ROOT / "python" / "pendingtriage.py"
    run = lambda *a: subprocess.run([sys.executable, str(cli), *a], capture_output=True, text=True)  # noqa: E731
    assert run(str(SAMPLES / "ocp-prod.pods.json")).returncode == 2
    assert run("--json", str(SAMPLES / "ocp-dev.pods.json")).returncode == 0
    assert run(str(tmp_path / "missing.json")).returncode == 3
    bad = tmp_path / "bad.json"
    bad.write_text("{not json")
    assert run(str(bad)).returncode == 3
