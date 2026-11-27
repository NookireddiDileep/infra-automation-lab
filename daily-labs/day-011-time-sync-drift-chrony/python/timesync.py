#!/usr/bin/env python3
"""timesync: chrony clock-drift audit (inventory v1 -> report). Exit 0 OK, 1 WARN, 2 CRIT, 3 error."""
from __future__ import annotations

import argparse
import json
import logging
import re
import sys
from dataclasses import dataclass
from pathlib import Path

log = logging.getLogger("timesync")

MAX_STRATUM = 5
MIN_SOURCES = 3
RE_LEAP = re.compile(r"^Leap status *: *(.+)$")
RE_STRATUM = re.compile(r"^Stratum *: *([0-9]+)$")
RE_SYSTIME = re.compile(r"^System time *: *([0-9]+)\.([0-9]{9}) seconds (fast|slow) of NTP time$")
RE_SOURCE = re.compile(r"^[=^]([*+?x~-]) +([^ ]+) +([0-9]+) +([0-9]+) +([0-7]+) +[^ ]+( .*)?$")
NS_PER_MS = 1_000_000


class InputError(Exception):
    """Bad inventory or arguments; maps to exit code 3."""


@dataclass(frozen=True)
class Source:
    state: str
    name: str
    reach: str  # octal digits as printed by chronyc


@dataclass(frozen=True)
class Inventory:
    host: str
    leap: str
    stratum: int
    offset_ns: int  # absolute value, integer nanoseconds: no float rounding anywhere
    direction: str  # "fast" | "slow"
    sources: tuple[Source, ...]


@dataclass
class Report:
    host: str
    lines: list[str]
    crit: int
    warn: int
    sources: int

    @property
    def result(self) -> str:
        return "CRIT" if self.crit else "WARN" if self.warn else "OK"

    @property
    def exit_code(self) -> int:
        return {"OK": 0, "WARN": 1, "CRIT": 2}[self.result]

    def render(self) -> str:
        body = "".join(line + "\n" for line in self.lines)
        return (f"timesync report: {self.host}\n{body}"
                f"RESULT {self.result} crit={self.crit} warn={self.warn} sources={self.sources}\n")


def parse_inventory(text: str) -> Inventory:
    host = leap = ""
    stratum: int | None = None
    offset_ns: int | None = None
    direction = ""
    sources: list[Source] = []
    section = ""
    for n, line in enumerate(text.splitlines(), 1):
        if line.startswith("# host="):
            host = line[len("# host="):]
            continue
        if line == "# chronyc tracking":
            section = "tracking"
            continue
        if line == "# chronyc sources":
            section = "sources"
            continue
        if not line or line.startswith("#"):
            continue
        if section == "tracking":
            if re.match(r"^Leap status *:", line):
                m = RE_LEAP.match(line) or _bad(n)
                leap = m.group(1)
            elif re.match(r"^Stratum *:", line):
                m = RE_STRATUM.match(line) or _bad(n)
                stratum = int(m.group(1))
            elif re.match(r"^System time *:", line):
                m = RE_SYSTIME.match(line) or _bad(n)
                offset_ns = int(m.group(1)) * 1_000_000_000 + int(m.group(2))
                direction = m.group(3)
        elif section == "sources":
            if line.startswith("MS ") or re.fullmatch(r"=+", line):
                continue
            m = RE_SOURCE.match(line) or _bad(n)
            sources.append(Source(m.group(1), m.group(2), m.group(5)))
        else:
            _bad(n)
    if not host:
        raise InputError("missing '# host=' header")
    if not leap or stratum is None or offset_ns is None:
        raise InputError("missing tracking fields")
    if not sources:
        raise InputError("no sources")
    return Inventory(host, leap, stratum, offset_ns, direction, tuple(sources))


def _bad(n: int):
    raise InputError(f"invalid line {n}")


def fmt_ms(ns: int) -> str:
    us = ns // 1000  # truncate, never round: the same digits in every language
    return f"{us // 1000}.{us % 1000:03d}ms"


def evaluate(inv: Inventory, warn_ms: int = 10, crit_ms: int = 100) -> Report:
    if not 0 <= warn_ms < crit_ms:
        raise InputError("--crit-ms must be higher than --warn-ms")
    lines: list[str] = []
    selected = 0
    for s in inv.sources:
        if s.state == "*":
            selected += 1
        if s.state == "x":
            lines.append(f"WARN source {s.name} falseticker")
        if re.fullmatch(r"0+", s.reach):
            lines.append(f"WARN source {s.name} unreachable (reach 0)")
    if inv.leap == "Not synchronised":
        # chrony still prints a stale offset and stratum 0 here; neither means anything
        lines.append(f"CRIT unsynchronised (leap status: {inv.leap})")
    else:
        if inv.offset_ns > crit_ms * NS_PER_MS:
            lines.append(f"CRIT offset {fmt_ms(inv.offset_ns)} {inv.direction} (crit {crit_ms}ms)")
        elif inv.offset_ns > warn_ms * NS_PER_MS:
            lines.append(f"WARN offset {fmt_ms(inv.offset_ns)} {inv.direction} (warn {warn_ms}ms)")
        if inv.stratum > MAX_STRATUM:
            lines.append(f"WARN stratum {inv.stratum} (max {MAX_STRATUM})")
    if selected == 0:
        lines.append("WARN no selected source")
    if len(inv.sources) < MIN_SOURCES:
        lines.append(f"WARN sources {len(inv.sources)} (need at least {MIN_SOURCES} to out-vote a falseticker)")
    crit = sum(1 for x in lines if x.startswith("CRIT"))
    warn = sum(1 for x in lines if x.startswith("WARN"))
    return Report(inv.host, sorted(lines), crit, warn, len(inv.sources))


def check_file(path: Path, warn_ms: int, crit_ms: int) -> Report:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise InputError(f"cannot read {path}: {exc.strerror}") from exc
    inv = parse_inventory(text)
    log.debug("%s: %d sources, stratum %d", inv.host, len(inv.sources), inv.stratum)
    return evaluate(inv, warn_ms, crit_ms)


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="timesync", description=__doc__)
    p.add_argument("-v", "--verbose", action="store_true")
    sub = p.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("check", help="audit one inventory file")
    c.add_argument("inventory", type=Path)
    c.add_argument("--warn-ms", type=int, default=10)
    c.add_argument("--crit-ms", type=int, default=100)
    c.add_argument("--json", action="store_true", help="machine-readable output")
    return p


def main(argv: list[str] | None = None) -> int:
    try:
        args = build_parser().parse_args(argv)
    except SystemExit as exc:  # argparse exits 2, which would collide with CRIT
        return 3 if exc.code else 0
    logging.basicConfig(level=logging.DEBUG if args.verbose else logging.WARNING,
                        format="timesync: %(message)s", stream=sys.stderr)
    try:
        rep = check_file(args.inventory, args.warn_ms, args.crit_ms)
    except InputError as exc:
        log.error("%s", exc)
        return 3
    if args.json:
        print(json.dumps({"host": rep.host, "result": rep.result, "crit": rep.crit, "warn": rep.warn,
                          "sources": rep.sources, "findings": rep.lines}, indent=2))
    else:
        sys.stdout.write(rep.render())
    return rep.exit_code


if __name__ == "__main__":
    sys.exit(main())
