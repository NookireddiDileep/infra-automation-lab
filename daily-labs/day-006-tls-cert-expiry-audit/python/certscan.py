#!/usr/bin/env python3
"""certscan: TLS certificate expiry audit (inventory v1 -> report). Exit 0 OK, 1 WARN, 2 CRIT, 3 error."""
from __future__ import annotations

import argparse
import json
import logging
import re
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

log = logging.getLogger("certscan")

ISO_RE = re.compile(
    r"^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]Z$"
)
SRC_RE = re.compile(r"^(file|endpoint):.")
DAY = 86400


class InputError(Exception):
    """Bad inventory or arguments; maps to exit code 3."""


@dataclass(frozen=True)
class Cert:
    source: str
    not_after: str  # ISO-8601 UTC, kept as text: it sorts and compares correctly
    subject: str


@dataclass
class Report:
    host: str
    lines: list[str]
    crit: int
    warn: int
    ok: int

    @property
    def result(self) -> str:
        return "CRIT" if self.crit else "WARN" if self.warn else "OK"

    @property
    def exit_code(self) -> int:
        return {"OK": 0, "WARN": 1, "CRIT": 2}[self.result]

    def render(self) -> str:
        body = "".join(line + "\n" for line in self.lines)
        return (f"certscan report: {self.host}\n{body}"
                f"RESULT {self.result} crit={self.crit} warn={self.warn} ok={self.ok}\n")


def to_epoch(ts: str) -> int:
    if not ISO_RE.match(ts):
        raise InputError(f"invalid timestamp: {ts}")
    try:
        return int(datetime.strptime(ts, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc).timestamp())
    except ValueError as exc:  # e.g. 2026-02-30
        raise InputError(f"invalid timestamp: {ts}") from exc


def parse_inventory(text: str) -> tuple[str, list[Cert]]:
    host = ""
    certs: list[Cert] = []
    for n, line in enumerate(text.splitlines(), 1):
        if line.startswith("# host="):
            host = line[len("# host="):]
            continue
        if not line or line.startswith("#"):
            continue
        fields = line.split("\t")
        if len(fields) != 3 or not SRC_RE.match(fields[0]) or not fields[2] or not ISO_RE.match(fields[1]):
            raise InputError(f"invalid line {n}")
        to_epoch(fields[1])
        certs.append(Cert(*fields))
    if not host:
        raise InputError("missing '# host=' header")
    if not certs:
        raise InputError("no certificates")
    return host, certs


def evaluate(host: str, certs: list[Cert], now: str, warn_days: int = 30, crit_days: int = 14) -> Report:
    if not 0 <= crit_days < warn_days:
        raise InputError("--crit-days must be lower than --warn-days")
    now_e = to_epoch(now)
    lines: list[str] = []
    crit = warn = ok = 0
    for c in certs:
        days = (to_epoch(c.not_after) - now_e) // DAY  # floor, also for negatives
        tail = f" ({c.not_after[:10]}) {c.subject}"
        if days < 0:
            sev, msg = "CRIT", f"expired {-days}d ago"
        elif days <= crit_days:
            sev, msg = "CRIT", f"expires in {days}d"
        elif days <= warn_days:
            sev, msg = "WARN", f"expires in {days}d"
        else:
            ok += 1
            continue
        lines.append(f"{sev} {c.source} {msg}{tail}")
        if sev == "CRIT":
            crit += 1
        else:
            warn += 1
    # renewed on disk but the running service still serves the old certificate
    for e in (c for c in certs if c.source.startswith("endpoint:")):
        for f in (c for c in certs if c.source.startswith("file:") and c.subject == e.subject):
            if e.not_after < f.not_after:
                lines.append(f"WARN reload-needed {e.subject} endpoint serves {e.not_after[:10]} "
                             f"but file has {f.not_after[:10]}")
                warn += 1
    return Report(host, sorted(lines), crit, warn, ok)


def check_file(path: Path, now: str, warn_days: int, crit_days: int) -> Report:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise InputError(f"cannot read {path}: {exc.strerror}") from exc
    host, certs = parse_inventory(text)
    log.debug("%s: %d certificates", host, len(certs))
    return evaluate(host, certs, now, warn_days, crit_days)


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="certscan", description=__doc__)
    p.add_argument("-v", "--verbose", action="store_true")
    sub = p.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("check", help="audit one inventory file")
    c.add_argument("inventory", type=Path)
    c.add_argument("--now", default=None, help="ISO-8601 UTC, default: current time")
    c.add_argument("--warn-days", type=int, default=30)
    c.add_argument("--crit-days", type=int, default=14)
    c.add_argument("--json", action="store_true", help="machine-readable output")
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    logging.basicConfig(level=logging.DEBUG if args.verbose else logging.WARNING,
                        format="certscan: %(message)s", stream=sys.stderr)
    now = args.now or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    try:
        rep = check_file(args.inventory, now, args.warn_days, args.crit_days)
    except InputError as exc:
        log.error("%s", exc)
        return 3
    if args.json:
        print(json.dumps({"host": rep.host, "result": rep.result, "crit": rep.crit, "warn": rep.warn,
                          "ok": rep.ok, "findings": rep.lines}, indent=2))
    else:
        sys.stdout.write(rep.render())
    return rep.exit_code


if __name__ == "__main__":
    sys.exit(main())
