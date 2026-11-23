import json
import pathlib
import subprocess
import sys

import pytest

import rightsize as rs

ROOT = pathlib.Path(__file__).resolve().parent.parent
SAMPLES = ROOT / "samples"
CODES = {"ocp-prod": 2, "ocp-stage": 1, "ocp-dev": 0}


def load(name, kind):
    return json.loads((SAMPLES / f"{name}.{kind}.json").read_text())


@pytest.mark.parametrize("name,code", CODES.items())
def test_golden(name, code):
    text, rc, _ = rs.check(load(name, "quota"), load(name, "metrics"), name)
    assert text == (SAMPLES / "expected" / f"{name}.report").read_text()
    assert rc == code


@pytest.mark.parametrize("kind,v,want", [
    ("cpu", "16", 16000), ("cpu", "500m", 500), ("cpu", "1.5", None), ("cpu", "", None), ("cpu", 16, None),
    ("mem", "1Gi", 1073741824), ("mem", "512Mi", 536870912), ("mem", "1G", None), ("mem", "1.5Gi", None),
    ("int", "60", 60), ("int", 60, None), ("int", "-1", None), ("int", "6\n", None),
])
def test_quantities(kind, v, want):
    assert rs.qty(kind, v) == want


def quota(hard, used, ns="a", name="q"):
    return {"kind": "ResourceQuota", "metadata": {"namespace": ns, "name": name}, "status": {"hard": hard, "used": used}}


def lst(*items):
    return {"kind": "List", "items": list(items)}


def pm(ns, cpu, mem):
    return {"kind": "PodMetrics", "metadata": {"namespace": ns}, "containers": [{"usage": {"cpu": cpu, "memory": mem}}]}


def classes(q, m):
    return [r[2] for r in rs.check(q, m, "t")[2]]


@pytest.mark.parametrize("used,want", [
    ("84", []), ("85", ["NEAR"]), ("99", ["NEAR"]), ("100", ["FULL"]), ("120", ["FULL"]),   # over 100% happens after a quota is lowered
])
def test_pods_thresholds(used, want):
    assert classes(lst(quota({"pods": "100"}, {"pods": used})), lst()) == want


def test_overquota_boundary_and_pods_exempt():
    q = lst(quota({"requests.cpu": "8", "requests.memory": "8Gi", "pods": "100"}, {"requests.cpu": "2", "requests.memory": "3Gi", "pods": "1"}))
    assert classes(q, lst(pm("a", "1000000000n", "2Gi"))) == ["OVERQUOTA"]      # cpu 2*4=8<=8 flagged; mem 3*4>8 fine; pods never OVERQUOTA


def test_efficiency_boundaries():
    q = lst(quota({"requests.cpu": "10", "requests.memory": "10Gi"}, {"requests.cpu": "5", "requests.memory": "5Gi"}))
    assert classes(q, lst(pm("a", "1500m", "1536Mi"))) == []                   # exactly 30%: not OVERREQUEST
    assert classes(q, lst(pm("a", "1499m", "1536Mi"))) == ["OVERREQUEST"]
    assert classes(q, lst(pm("a", "6000m", "6Gi"))) == []                       # exactly 120%: not UNDERREQUEST
    assert classes(q, lst(pm("a", "6001m", "6Gi"))) == ["UNDERREQUEST"]


def test_underrequest_severity_memory_is_crit_cpu_is_warn():
    q = lst(quota({"requests.cpu": "10", "requests.memory": "10Gi"}, {"requests.cpu": "5", "requests.memory": "5Gi"}))
    rows = rs.check(q, lst(pm("a", "9000m", "9Gi")), "t")[2]
    assert [(r[0], r[2]) for r in rows] == [(0, "UNDERREQUEST"), (1, "UNDERREQUEST")]
    assert "requests.memory" in rows[0][3] and "requests.cpu" in rows[1][3]


def test_usage_is_summed_across_pods_and_floored():
    q = lst(quota({"requests.cpu": "10"}, {"requests.cpu": "10"}))
    m = lst(pm("a", "400000n", "1Ki"), pm("a", "600000n", "1Ki"))               # 1.0m total -> 1m, eff 0%
    assert "actual=1m eff=0%" in rs.check(q, m, "t")[0]


def test_nometrics_only_when_cpu_requests_in_use():
    assert classes(lst(quota({"requests.cpu": "4"}, {"requests.cpu": "2"})), lst()) == ["NOMETRICS"]
    assert classes(lst(quota({"requests.cpu": "4"}, {"requests.cpu": "0"})), lst()) == ["OVERQUOTA"]   # nothing used: no metrics expected


def test_missing_keys_and_foreign_objects_are_ignored():
    q = lst(quota({"count/configmaps": "5"}, {"count/configmaps": "5"}), {"kind": "ConfigMap"}, "junk", {"kind": "ResourceQuota", "metadata": {"namespace": "a", "name": "n"}})
    text, rc, rows = rs.check(q, lst(), "t")
    assert rows == [] and rc == 0 and text.endswith("quotas=2 healthy=2\n")


def test_json_number_is_invalid_not_skipped():
    assert classes(lst(quota({"pods": 60}, {"pods": "10"})), lst()) == ["INVALID"]


@pytest.mark.parametrize("q,m", [
    ({"kind": "Pod"}, lst()),
    (lst(), {"kind": "Pod"}),
    (lst({"kind": "ResourceQuota", "metadata": {"name": "x"}}), lst()),                       # no namespace
    (lst(), lst(pm("a", "1.5", "1Mi"))),                                                      # not a metrics-server quantity
    (lst(), lst({"kind": "PodMetrics", "metadata": {}, "containers": []})),                  # no namespace
])
def test_bad_input_is_refused(q, m):
    with pytest.raises(rs.InputError):
        rs.check(q, m, "t")


def run_cli(*args):
    return subprocess.run([sys.executable, str(ROOT / "python" / "rightsize.py"), *args], capture_output=True, text=True)


def test_cli_exit_codes_and_json():
    p = SAMPLES / "ocp-stage"
    r = run_cli("--json", f"{p}.quota.json", f"{p}.metrics.json")
    assert r.returncode == 1
    doc = json.loads(r.stdout)
    assert doc["exit"] == 1 and [f["class"] for f in doc["findings"]] == ["OVERREQUEST", "NEAR"]
    assert run_cli("/nonexistent", f"{p}.metrics.json").returncode == 3
    assert run_cli().returncode == 3
