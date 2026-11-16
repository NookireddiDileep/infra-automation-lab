"""pytest: run with `python -m pytest -q` from this folder."""
import json
from pathlib import Path

import pytest

import disktriage as dt

SAMPLES = Path(__file__).resolve().parent.parent / "samples"
CASES = [("node-01", 2), ("node-02", 0), ("node-03", 1)]


@pytest.mark.parametrize("node,code", CASES)
def test_golden_report_and_exit_code(node, code, capsys):
    rc = dt.main(["report", str(SAMPLES / f"{node}.snap")])
    assert rc == code
    assert capsys.readouterr().out == (SAMPLES / "expected" / f"{node}.report").read_text()


def test_json_output(capsys):
    rc = dt.main(["report", "--json", str(SAMPLES / "node-01.snap")])
    data = json.loads(capsys.readouterr().out)
    assert rc == 2 and data["result"] == "CRIT" and data["crit"] == 1 and data["warn"] == 2
    var = data["findings"][0]
    assert var["mount"] == "/var" and "rotated 4 files 1736 MiB older than 7d" in var["details"]


@pytest.mark.parametrize("part,total,want", [(87, 100, 87), (871, 1000, 88), (0, 0, 0), (1, 3, 34), (5, 5, 100)])
def test_ceil_pct_is_integer_and_rounds_up(part, total, want):
    assert dt.ceil_pct(part, total) == want


@pytest.mark.parametrize("path,want", [
    ("/database", "/"),            # string prefix of /data, but NOT inside it
    ("/data", "/data"),
    ("/data/models", "/data"),
    ("/boot/efi/EFI", "/boot/efi"),  # longest match wins
    ("/boot/grub2", "/boot"),
])
def test_owner_mount_longest_prefix_on_path_components(path, want):
    assert dt.owner_mount(path, ["/", "/boot", "/boot/efi", "/data"]) == want


def test_shared_deleted_inode_is_freed_once():
    rep = dt.analyse(dt.parse_snapshot((SAMPLES / "node-01.snap").read_text()))
    var = rep.findings[0]
    # java 4096 + httpd 1024 (two pids, one inode): 5120, not 6144
    assert "after cleanup 63% (deleted-open 5120 MiB + rotated 1736 MiB)" in var.details


def test_thresholds_and_retention_flags():
    snap = dt.parse_snapshot((SAMPLES / "node-01.snap").read_text())
    assert dt.analyse(snap, warn_pct=98, crit_pct=99).result == "OK"
    assert dt.analyse(snap, warn_pct=85, crit_pct=88).crit == 3           # / at 88% and /tmp inodes 91% become CRIT
    var = dt.analyse(snap, retain_days=1).findings[0]
    assert "rotated 5 files 2248 MiB older than 1d" in var.details


@pytest.mark.parametrize("text", ["hello\n", "# disktriage snapshot v1\nfs / xfs 1 2\n", "# disktriage snapshot v1\ndu big /x\n"])
def test_rejects_bad_snapshots(tmp_path, text):
    bad = tmp_path / "bad.snap"
    bad.write_text(text)
    assert dt.main(["report", str(bad)]) == 3


def test_missing_file_and_bad_usage_are_exit_3():
    assert dt.main(["report", "/nonexistent"]) == 3
    assert dt.main(["report"]) == 3


def test_live_snapshot_is_valid_and_reports_ok_above_100pct():
    snap = dt.parse_snapshot(dt.take_snapshot(du_min_pct=101))
    assert snap.fs, "expected at least one real filesystem"
    assert dt.analyse(snap, warn_pct=101, crit_pct=101).result == "OK"
