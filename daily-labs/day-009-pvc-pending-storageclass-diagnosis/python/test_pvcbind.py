"""Golden and edge tests for pvcbind.py (same samples and exit codes as Bash, Ansible and Go)."""
import json
import pathlib

import pytest

import pvcbind

S = pathlib.Path(__file__).resolve().parent.parent / "samples"


def run(pvcs, scs, capsys):
    rc = pvcbind.main([str(pvcs), str(scs)])
    return rc, capsys.readouterr().out


@pytest.mark.parametrize("name,rc", [("ocp-prod", 2), ("ocp-lab", 2), ("ocp-stage", 1), ("ocp-dev", 0)])
def test_golden(name, rc, capsys):
    got_rc, out = run(S / f"{name}.pvcs.json", S / f"{name}.storageclasses.json", capsys)
    assert got_rc == rc
    assert out == (S / "expected" / f"{name}.report").read_text()


@pytest.mark.parametrize("pvcs,scs", [
    ("bad/not-a-list.json", "ocp-dev.storageclasses.json"),
    ("ocp-dev.pvcs.json", "bad/not-a-list.json"),
    ("bad/pvcs-bad-phase.json", "ocp-dev.storageclasses.json"),
    ("ocp-dev.pvcs.json", "bad/sc-bad-mode.json"),
    ("nope.json", "ocp-dev.storageclasses.json"),
])
def test_bad_input_is_refused_with_empty_stdout(pvcs, scs, capsys):
    rc, out = run(S / pvcs, S / scs, capsys)
    assert rc == 3 and out == ""


def test_usage_error_is_exit_3(capsys):
    assert pvcbind.main([]) == 3


def doc(*items):
    return {"kind": "List", "items": list(items)}


def pvc(sc="gp3", phase="Pending", **ann):
    md = {"name": "a", "namespace": "n"}
    if ann:
        md["annotations"] = ann
    spec = {} if sc is None else {"storageClassName": sc}
    return {"kind": "PersistentVolumeClaim", "metadata": md, "spec": spec, "status": {"phase": phase}}


def sc(name="gp3", mode=None, prov="x.csi", default=False):
    d = {"kind": "StorageClass", "metadata": {"name": name}, "provisioner": prov}
    if mode:
        d["volumeBindingMode"] = mode
    if default:
        d["metadata"]["annotations"] = {pvcbind.DEFAULT_ANN: "true"}
    return d


def classes(*c):
    return pvcbind.check([], [*c])


def verdict(p, *c):
    res = pvcbind.check([p], [*c])
    return [r["cls"] for r in res["rows"]]


def test_missing_mode_means_immediate():
    assert verdict(pvc(), sc()) == ["PROVISIONFAILED"]


def test_wffc_without_selected_node_is_info_and_with_it_is_crit():
    assert verdict(pvc(), sc(mode="WaitForFirstConsumer")) == ["WAITFORCONSUMER"]
    assert verdict(pvc(**{pvcbind.SELECTED_ANN: "node-01"}), sc(mode="WaitForFirstConsumer")) == ["SELECTEDSTUCK"]


def test_default_class_applies_only_to_a_missing_name_not_to_empty_string():
    assert verdict(pvc(sc=None), sc(default=True, mode="WaitForFirstConsumer")) == ["WAITFORCONSUMER"]
    assert verdict(pvc(sc=""), sc(default=True)) == ["NOPV"]
    assert verdict(pvc(sc=None), sc()) == ["NOSC"]


def test_bound_pvc_is_never_flagged_even_with_unknown_class():
    assert verdict(pvc(sc="ghost", phase="Bound"), sc()) == []


def test_two_defaults_flag_both_and_local_immediate():
    res = pvcbind.check([], [sc("a", default=True), sc("b", default=True), sc("l", prov=pvcbind.NO_PROV)])
    assert [(r["subject"], r["cls"]) for r in res["rows"]] == [
        ("sc/a", "MULTIDEFAULT"), ("sc/b", "MULTIDEFAULT"), ("sc/l", "LOCALIMMEDIATE")]


def test_info_does_not_change_the_verdict():
    res = pvcbind.check([pvc()], [sc(mode="WaitForFirstConsumer")])
    assert pvcbind.render("x", res)[1] == "OK"


def test_json_view(capsys):
    rc = pvcbind.main(["--json", str(S / "ocp-stage.pvcs.json"), str(S / "ocp-stage.storageclasses.json")])
    out = json.loads(capsys.readouterr().out)
    assert rc == 1 and out["result"] == "WARN" and out["pvcs"] == 3
