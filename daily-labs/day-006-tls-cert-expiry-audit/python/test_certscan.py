import json
from pathlib import Path

import pytest

import certscan as cs

SAMPLES = Path(__file__).resolve().parent.parent / "samples"
NOW = "2026-11-20T00:00:00Z"


@pytest.mark.parametrize("host,rc", [("node-01", 2), ("node-02", 0), ("node-03", 1)])
def test_golden(host, rc, capsys):
    assert cs.main(["check", "--now", NOW, str(SAMPLES / f"{host}.certs")]) == rc
    assert capsys.readouterr().out == (SAMPLES / "expected" / f"{host}.report").read_text()


@pytest.mark.parametrize("bad", sorted((SAMPLES / "bad").glob("*.certs")), ids=lambda p: p.name)
def test_bad_inventories_exit_3(bad):
    assert cs.main(["check", "--now", NOW, str(bad)]) == 3


def cert(not_after, src="file:/x.pem", subj="CN=a"):
    return cs.Cert(src, not_after, subj)




@pytest.mark.parametrize("not_after,sev,text", [
    ("2026-12-21T00:00:00Z", None, None),            # 31d: OK
    ("2026-12-20T00:00:00Z", "WARN", "expires in 30d"),  # 30d: boundary is inclusive
    ("2026-12-04T00:00:00Z", "CRIT", "expires in 14d"),
    ("2026-11-20T00:00:00Z", "CRIT", "expires in 0d"),
    ("2026-11-19T23:59:59Z", "CRIT", "expired 1d ago"),  # 1s late floors to a full day
    ("2026-11-19T00:00:00Z", "CRIT", "expired 1d ago"),
])
def test_boundaries(not_after, sev, text):
    rep = cs.evaluate("h", [cert(not_after)], NOW)
    if sev is None:
        assert rep.lines == [] and rep.ok == 1
    else:
        assert rep.lines[0].startswith(f"{sev} file:/x.pem {text} ")


def test_reload_needed_only_when_endpoint_is_older():
    old, new = "2027-01-01T00:00:00Z", "2027-06-01T00:00:00Z"
    stale = cs.evaluate("h", [cert(new), cert(old, "endpoint:h:443")], NOW)
    assert stale.warn == 1 and "reload-needed" in stale.lines[0]
    ahead = cs.evaluate("h", [cert(old), cert(new, "endpoint:h:443")], NOW)
    assert ahead.warn == 0


def test_threshold_order_enforced():
    with pytest.raises(cs.InputError):
        cs.evaluate("h", [cert("2030-01-01T00:00:00Z")], NOW, warn_days=10, crit_days=10)


def test_rejects_impossible_date():
    with pytest.raises(cs.InputError):
        cs.to_epoch("2026-02-30T00:00:00Z")


def test_json_output(capsys):
    assert cs.main(["check", "--json", "--now", NOW, str(SAMPLES / "node-03.certs")]) == 1
    doc = json.loads(capsys.readouterr().out)
    assert doc["result"] == "WARN" and doc["warn"] == 1 and doc["ok"] == 2
