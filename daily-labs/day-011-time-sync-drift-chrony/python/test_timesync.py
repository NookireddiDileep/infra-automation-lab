import json
from pathlib import Path

import pytest

import timesync as ts

SAMPLES = Path(__file__).resolve().parent.parent / "samples"
CASES = [("node-01", 2), ("node-02", 0), ("node-03", 1), ("node-04", 2), ("node-05", 1)]


@pytest.mark.parametrize("host,rc", CASES)
def test_golden(host, rc, capsys):
    assert ts.main(["check", str(SAMPLES / f"{host}.chrony")]) == rc
    assert capsys.readouterr().out == (SAMPLES / "expected" / f"{host}.report").read_text()


@pytest.mark.parametrize("bad", sorted((SAMPLES / "bad").glob("*.chrony")), ids=lambda p: p.name)
def test_bad_inventories_exit_3(bad):
    assert ts.main(["check", str(bad)]) == 3


def inv(offset_ns, leap="Normal", stratum=2, direction="fast", sources=None):
    srcs = sources or [ts.Source("*", "a", "377"), ts.Source("+", "b", "377"), ts.Source("+", "c", "377")]
    return ts.Inventory("h", leap, stratum, offset_ns, direction, tuple(srcs))


@pytest.mark.parametrize("ns,line", [
    (10_000_000, None),                                         # exactly warn: strict ">" keeps it OK
    (10_000_001, "WARN offset 10.000ms fast (warn 10ms)"),      # 1ns over; display truncates, never rounds
    (100_000_000, "WARN offset 100.000ms fast (warn 10ms)"),    # exactly crit is still only WARN
    (100_000_001, "CRIT offset 100.000ms fast (crit 100ms)"),
    (1_999_999_999, "CRIT offset 1999.999ms fast (crit 100ms)"),
])
def test_offset_boundaries(ns, line):
    rep = ts.evaluate(inv(ns))
    assert rep.lines == ([line] if line else [])


def test_unsynchronised_suppresses_offset_and_stratum():
    rep = ts.evaluate(inv(900_000_000, leap="Not synchronised", stratum=0))
    assert rep.lines == ["CRIT unsynchronised (leap status: Not synchronised)"]


def test_falseticker_and_unreachable_and_quorum():
    srcs = [ts.Source("*", "a", "377"), ts.Source("x", "b", "377"), ts.Source("?", "c", "0")]
    rep = ts.evaluate(inv(0, sources=srcs))
    assert rep.lines == ["WARN source b falseticker", "WARN source c unreachable (reach 0)"]
    two = ts.evaluate(inv(0, sources=srcs[:2]))
    assert any(x.startswith("WARN sources 2 ") for x in two.lines)


def test_threshold_order_enforced():
    with pytest.raises(ts.InputError):
        ts.evaluate(inv(0), warn_ms=100, crit_ms=100)


def test_parser_rejects_fraction_that_is_not_nine_digits():
    with pytest.raises(ts.InputError):
        ts.parse_inventory((SAMPLES / "bad" / "bad-offset.chrony").read_text())


def test_usage_error_exits_3_not_argparse_2():
    assert ts.main(["check"]) == 3


def test_json_output(capsys):
    assert ts.main(["check", "--json", str(SAMPLES / "node-03.chrony")]) == 1
    doc = json.loads(capsys.readouterr().out)
    assert doc["result"] == "WARN" and doc["warn"] == 3 and doc["sources"] == 3
