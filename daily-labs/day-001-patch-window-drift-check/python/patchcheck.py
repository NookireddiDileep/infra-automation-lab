#!/usr/bin/env python3
"""patchcheck.py: patch-window drift check (Python edition).

    patchcheck.py snapshot [-o FILE]
    patchcheck.py compare [-t PCT] [--json] PRE POST

Same snapshot format, report text and exit codes as the Bash/Go/Ansible versions
(0 OK, 1 WARN, 2 CRIT, 3 error). Python's extra: --json output for dashboards/tickets.
"""
from __future__ import annotations

import argparse
import json
import logging
import os
import platform
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

HEADER = "# patchcheck snapshot v1"
REAL_FSTYPES = {"xfs", "ext4", "ext3", "btrfs", "vfat", "nfs", "nfs4"}
ITEM_KINDS = ("mount", "port", "service")
EXIT = {"OK": 0, "WARN": 1, "CRIT": 2}

log = logging.getLogger("patchcheck")


# ---------------- snapshot ----------------

def list_mounts(proc_mounts: str = "/proc/mounts") -> list[tuple[str, str]]:
    seen = set()
    for line in Path(proc_mounts).read_text().splitlines():
        parts = line.split()
        if len(parts) >= 3 and parts[2] in REAL_FSTYPES:
            seen.add((parts[1], parts[2]))
    return sorted(seen)


def parse_proc_net_tcp(text: str) -> set[int]:
    """Listening ports from /proc/net/tcp[6] text. State 0A = LISTEN; port is hex."""
    ports = set()
    for line in text.splitlines()[1:]:
        cols = line.split()
        if len(cols) > 3 and cols[3] == "0A":
            ports.add(int(cols[1].rsplit(":", 1)[1], 16))
    return ports


def list_ports() -> list[int]:
    ports: set[int] = set()
    for f in ("/proc/net/tcp", "/proc/net/tcp6"):
        try:
            ports |= parse_proc_net_tcp(Path(f).read_text())
        except OSError:
            continue
    return sorted(ports)


def list_services() -> list[str]:
    if not shutil.which("systemctl"):
        log.warning("systemctl not found, no services recorded")
        return []
    res = subprocess.run(
        ["systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--plain"],
        capture_output=True, text=True, timeout=30,
    )
    if res.returncode != 0:
        log.warning("systemctl unavailable (container?), no services recorded")
        return []
    return sorted({ln.split()[0] for ln in res.stdout.splitlines() if ln.strip()})


def fs_used_pct(path: str) -> int:
    """Same formula as df: used / (used + available), rounded up."""
    st = os.statvfs(path)
    used = (st.f_blocks - st.f_bfree) * st.f_frsize
    avail = st.f_bavail * st.f_frsize
    if used + avail == 0:
        return 0
    return -(-used * 100 // (used + avail))  # ceil division


def take_snapshot() -> str:
    mounts = list_mounts()
    lines = [
        HEADER,
        f"# host={platform.node().split('.')[0]} taken={datetime.now(timezone.utc):%Y-%m-%dT%H:%M:%SZ}",
        f"kernel {platform.release()}",
    ]
    items = [f"mount {mp} {fs}" for mp, fs in mounts]
    items += [f"port tcp/{p}" for p in list_ports()]
    items += [f"service {s}" for s in list_services()]
    lines += sorted(set(items))
    for mp, _ in mounts:
        try:
            lines.append(f"fsuse {mp} {fs_used_pct(mp)}")
        except OSError as exc:
            log.warning("cannot stat %s: %s", mp, exc)
    return "\n".join(lines) + "\n"


# ---------------- compare ----------------

@dataclass
class Snapshot:
    host: str = "unknown"
    kernel: str = ""
    items: set[str] = field(default_factory=set)
    fsuse: dict[str, int] = field(default_factory=dict)


def parse_snapshot(text: str, name: str = "<snapshot>") -> Snapshot:
    lines = text.splitlines()
    if not lines or lines[0].strip() != HEADER:
        raise ValueError(f"{name} is not a v1 snapshot")
    snap = Snapshot()
    for line in lines:
        parts = line.split()
        if not parts:
            continue
        if parts[0] == "#":
            for tok in parts[1:]:
                if tok.startswith("host="):
                    snap.host = tok.split("=", 1)[1]
        elif parts[0] == "kernel" and len(parts) > 1 and not snap.kernel:
            snap.kernel = parts[1]
        elif parts[0] in ITEM_KINDS:
            snap.items.add(line.strip())
        elif parts[0] == "fsuse" and len(parts) == 3:
            snap.fsuse[parts[1]] = int(parts[2])
    return snap


@dataclass
class Report:
    host: str
    findings: list[tuple[str, str]]  # (severity, message)
    crit: int
    warn: int

    @property
    def result(self) -> str:
        return "CRIT" if self.crit else "WARN" if self.warn else "OK"

    def text(self) -> str:
        out = [f"patchcheck report: {self.host}"]
        out += [f"{sev} {msg}" for sev, msg in self.findings]
        out.append(f"RESULT {self.result} crit={self.crit} warn={self.warn}")
        return "\n".join(out) + "\n"

    def as_dict(self) -> dict:
        return {
            "host": self.host, "result": self.result, "exit_code": EXIT[self.result],
            "crit": self.crit, "warn": self.warn,
            "findings": [{"severity": s, "message": m} for s, m in self.findings],
        }


def compare(pre: Snapshot, post: Snapshot, warn_pct: int = 90) -> Report:
    findings: list[tuple[str, str]] = []
    if pre.kernel and post.kernel:
        msg = (f"kernel unchanged {post.kernel}" if pre.kernel == post.kernel
               else f"kernel {pre.kernel} -> {post.kernel}")
        findings.append(("INFO", msg))
    missing = sorted(pre.items - post.items)
    new = sorted(post.items - pre.items)
    hot = sorted(f"{mp} {pct}" for mp, pct in post.fsuse.items() if pct >= warn_pct)
    findings += [("CRIT", f"missing {i}") for i in missing]
    findings += [("WARN", f"new {i}") for i in new]
    for entry in hot:
        mp, pct = entry.rsplit(" ", 1)
        findings.append(("WARN", f"fs {mp} at {pct}% (>= {warn_pct}%)"))
    return Report(post.host, findings, crit=len(missing), warn=len(new) + len(hot))


# ---------------- CLI ----------------

def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="patchcheck.py", description="Patch-window drift check")
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("snapshot", help="capture this host's state")
    s.add_argument("-o", "--output", type=Path)
    c = sub.add_parser("compare", help="compare two snapshots")
    c.add_argument("-t", "--threshold", type=int, default=90, help="fs usage WARN threshold (%%)")
    c.add_argument("--json", action="store_true", help="JSON output")
    c.add_argument("pre", type=Path)
    c.add_argument("post", type=Path)

    try:
        args = ap.parse_args(argv)
    except SystemExit as exc:          # argparse exits 2 on bad usage; our contract says 3
        return 0 if exc.code == 0 else 3

    logging.basicConfig(level=logging.INFO, stream=sys.stderr,
                        format="%(asctime)s %(name)s [%(levelname)s] %(message)s")
    try:
        if args.cmd == "snapshot":
            text = take_snapshot()
            if args.output:
                args.output.write_text(text)
                log.info("snapshot written to %s", args.output)
            else:
                sys.stdout.write(text)
            return 0
        pre = parse_snapshot(args.pre.read_text(), str(args.pre))
        post = parse_snapshot(args.post.read_text(), str(args.post))
        report = compare(pre, post, args.threshold)
        sys.stdout.write(json.dumps(report.as_dict(), indent=2) + "\n" if args.json else report.text())
        return EXIT[report.result]
    except (OSError, ValueError) as exc:
        log.error("%s", exc)
        return 3


if __name__ == "__main__":
    sys.exit(main())
