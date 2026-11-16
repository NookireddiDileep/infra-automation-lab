"""pytest: run with `python -m pytest -q` from this folder."""
import json
from pathlib import Path

import pytest

import patchcheck as pc

SAMPLES = Path(__file__).resolve().parent.parent / "samples"
CASES = [("node-01", 2), ("node-02", 0), ("node-03", 1)]


@pytest.mark.parametrize("node,code", CASES)
def test_golden_report_and_exit_code(node, code, capsys):
    rc = pc.main(["compare", str(SAMPLES / f"{node}-pre.txt"), str(SAMPLES / f"{node}-post.txt")])
    assert rc == code
    assert capsys.readouterr().out == (SAMPLES / "expected" / f"{node}.report").read_text()


def test_json_output(capsys):
    rc = pc.main(["compare", "--json", str(SAMPLES / "node-01-pre.txt"), str(SAMPLES / "node-01-post.txt")])
    data = json.loads(capsys.readouterr().out)
    assert rc == 2 and data["result"] == "CRIT" and data["crit"] == 3
    assert {"severity": "CRIT", "message": "missing service httpd.service"} in data["findings"]


def test_threshold_flag_changes_fs_warnings():
    pre = pc.parse_snapshot((SAMPLES / "node-03-pre.txt").read_text())
    post = pc.parse_snapshot((SAMPLES / "node-03-post.txt").read_text())
    assert pc.compare(pre, post, warn_pct=96).warn == 1        # only the new service left
    assert pc.compare(pre, post, warn_pct=50).warn == 4        # /, /var, /var/log + service


def test_rejects_non_snapshot(tmp_path):
    bad = tmp_path / "bad.txt"
    bad.write_text("hello\n")
    assert pc.main(["compare", str(bad), str(bad)]) == 3


def test_missing_file_is_exit_3():
    assert pc.main(["compare", "/nonexistent", str(SAMPLES / "node-01-post.txt")]) == 3


def test_bad_usage_is_exit_3():
    assert pc.main(["compare", "only-one-arg"]) == 3


def test_parse_proc_net_tcp_only_listen_state():
    text = (
        "  sl  local_address rem_address   st\n"
        "   0: 00000000:0016 00000000:0000 0A 0\n"      # :22 LISTEN
        "   1: 0100007F:1F90 0100007F:9C40 01 0\n"      # :8080 ESTABLISHED -> ignored
    )
    assert pc.parse_proc_net_tcp(text) == {22}


def test_live_snapshot_is_valid_and_self_compares_clean():
    snap_text = pc.take_snapshot()
    snap = pc.parse_snapshot(snap_text)
    assert snap.kernel
    assert pc.compare(snap, snap, warn_pct=101).result == "OK"
