import json
import pathlib
import subprocess
import sys

import pytest

import podtriage

ROOT = pathlib.Path(__file__).resolve().parent.parent
SAMPLES = ROOT / "samples"
CODES = {"ocp-prod": 2, "ocp-stage": 1, "ocp-dev": 0}


@pytest.mark.parametrize("name,code", CODES.items())
def test_golden(name, code):
    src = SAMPLES / f"{name}.pods.json"
    text, rc = podtriage.triage(json.loads(src.read_text()), name)
    assert text == (SAMPLES / "expected" / f"{name}.report").read_text()
    assert rc == code


def pod(**status):
    return {"metadata": {"name": "p", "namespace": "n"}, "spec": {"containers": [{"name": "c"}]}, "status": status}


def test_oom_running_is_warn_but_down_is_crit():
    last = {"terminated": {"reason": "OOMKilled", "exitCode": 137}}
    running = {"name": "c", "restartCount": 1, "state": {"running": {}}, "lastState": last}
    down = {"name": "c", "restartCount": 1, "state": {"waiting": {"reason": "CrashLoopBackOff"}}, "lastState": last}
    assert podtriage.classify_container(pod(), running, 5)[:2] == (1, "OOMKilled")
    assert podtriage.classify_container(pod(), down, 5)[:2] == (0, "OOMKilled")


def test_restart_threshold_is_configurable():
    cs = {"name": "c", "restartCount": 3, "state": {"running": {}}}
    assert podtriage.classify_container(pod(), cs, 5) is None
    assert podtriage.classify_container(pod(), cs, 3)[:2] == (1, "HighRestarts")


def test_succeeded_pods_ignored():
    doc = {"kind": "List", "items": [pod(phase="Succeeded", containerStatuses=[
        {"name": "c", "restartCount": 99, "state": {"terminated": {"reason": "Completed"}}}])]}
    assert podtriage.triage(doc, "x")[1] == 0


@pytest.mark.parametrize("bad", [[], {"kind": "Pod"}, {"kind": "List", "items": {}}])
def test_rejects_non_pod_list(bad):
    with pytest.raises(ValueError):
        podtriage.triage(bad, "x")


def test_cli_exit_codes(tmp_path):
    cli = ROOT / "python" / "podtriage.py"
    run = lambda *a: subprocess.run([sys.executable, str(cli), *a], capture_output=True, text=True)  # noqa: E731
    assert run(str(SAMPLES / "ocp-prod.pods.json")).returncode == 2
    assert run(str(tmp_path / "missing.json")).returncode == 3
    bad = tmp_path / "bad.json"
    bad.write_text("{not json")
    assert run(str(bad)).returncode == 3
    assert run("-r", "0", str(SAMPLES / "ocp-dev.pods.json")).returncode == 3
