import copy
import json
import pathlib
import subprocess
import sys

import pytest

import onboardcheck as oc

ROOT = pathlib.Path(__file__).resolve().parent.parent
SAMPLES = ROOT / "samples"
CODES = {"ocp-prod": 2, "ocp-stage": 1, "ocp-dev": 0}


def load(name, kind):
    return json.loads((SAMPLES / f"{name}.{kind}.json").read_text())


@pytest.mark.parametrize("name,code", CODES.items())
def test_golden(name, code):
    text, rc = oc.check(load(name, "spec"), load(name, "cluster"), name)
    assert text == (SAMPLES / "expected" / f"{name}.report").read_text()
    assert rc == code


@pytest.mark.parametrize("kind,want,have,same", [
    ("cpu", "16", "16000m", True),
    ("cpu", "500m", "0.5", True),
    ("cpu", "1", "1500m", False),
    ("cpu", "1", "1.5m", False),        # not a valid quantity: never "equal"
    ("mem", "32Gi", "32768Mi", True),
    ("mem", "1Gi", "1073741824", True),
    ("mem", "1Gi", "1G", False),        # decimal suffixes are not supported: loud drift, not a silent pass
    ("int", "60", "60", True),
    ("int", "60", 60, False),           # JSON number instead of string: invalid, loud drift
    ("cpu", "1", "1.0005", False),      # more than 3 decimals is not a valid cpu quantity
    ("int", "60", "sixty", False),
])
def test_quantities(kind, want, have, same):
    assert oc.same(kind, want, have) is same


def spec_of(**over):
    ns = {"name": "app", "tier": "small", "admin_group": "app-admins", "viewer_group": "app-viewers"}
    ns.update(over)
    return {"namespaces": [ns]}


def clean_cluster():
    return copy.deepcopy(load("ocp-dev", "cluster"))


def test_clean_namespace_has_no_findings():
    c = clean_cluster()
    text, rc = oc.check(load("ocp-dev", "spec"), c, "x")
    assert rc == 0 and text.splitlines()[-1] == "RESULT OK crit=0 warn=0 namespaces=3 clean=3"


def test_missing_namespace_stops_further_checks():
    text, rc = oc.check(spec_of(name="ghost"), clean_cluster(), "x")
    assert text.splitlines()[1] == "CRIT ghost MISSING Namespace/ghost" and rc == 2
    assert "ResourceQuota" not in text


def test_deny_all_with_ingress_rule_or_selector_is_crit():
    for mutate in (lambda s: s.update(ingress=[{"from": []}]),
                   lambda s: s.update(podSelector={"matchLabels": {"app": "x"}}),
                   lambda s: s.update(policyTypes=["Egress"])):
        c = clean_cluster()
        np = next(i for i in c["items"] if i["kind"] == "NetworkPolicy" and i["metadata"]["namespace"] == "sandbox"
                  and i["metadata"]["name"] == "default-deny-ingress")
        mutate(np["spec"])
        text, rc = oc.check(spec_of(name="sandbox"), c, "x")
        assert "CRIT sandbox DRIFT NetworkPolicy/default-deny-ingress not deny-all" in text and rc == 2


def test_cluster_admin_outside_the_spec_is_ignored():
    spec = spec_of(name="sandbox", admin_group="sandbox-admins", viewer_group="sandbox-viewers")
    text, rc = oc.check(spec, clean_cluster(), "x")   # openshift-monitoring has a cluster-admin binding
    assert "EXTRA RoleBinding" not in text and rc == 0


def test_json_output_and_exit_codes(tmp_path):
    run = lambda *a: subprocess.run([sys.executable, str(ROOT / "python" / "onboardcheck.py"), *a], capture_output=True, text=True)
    p = run("--json", str(SAMPLES / "ocp-stage.spec.json"), str(SAMPLES / "ocp-stage.cluster.json"))
    doc = json.loads(p.stdout)
    assert p.returncode == 1 and doc["findings"][1]["object"] == "ResourceQuota/onboarding-quota"
    bad = tmp_path / "bad.json"
    bad.write_text("{not json")
    assert run(str(bad), str(SAMPLES / "ocp-dev.cluster.json")).returncode == 3
    assert run().returncode == 3
    assert run(str(SAMPLES / "ocp-dev.spec.json"), str(SAMPLES / "ocp-dev.spec.json")).returncode == 3   # spec is not a cluster List


@pytest.mark.parametrize("spec", [
    {}, {"namespaces": []}, {"namespaces": [{"name": "a"}]},
    {"namespaces": [{"name": "a", "tier": "huge", "admin_group": "x", "viewer_group": "y"}]},
])
def test_bad_spec_is_rejected(spec):
    with pytest.raises(ValueError):
        oc.check(spec, clean_cluster(), "x")
