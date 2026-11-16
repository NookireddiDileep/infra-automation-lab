#!/usr/bin/env python3
"""disktriage: disk-full triage for one RHEL host (Python edition).

    disktriage.py snapshot [-o FILE] [-m DU_MIN_PCT] [-l LOGDIR]
    disktriage.py report [-w PCT] [-c PCT] [-r DAYS] [--json] SNAPSHOT

Exit codes (report): 0 OK, 1 WARN, 2 CRIT, 3 usage/runtime error.
Snapshot and report format: see ../README.md ("The contract").
"""
from __future__ import annotations

import argparse
import json
import logging
import os
import re
import socket
import stat
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

HEADER = "# disktriage snapshot v1"
REAL_FSTYPES = {"xfs", "ext4", "ext3", "btrfs", "vfat", "nfs", "nfs4"}
ROTATED_RE = re.compile(r"(\.gz|\.xz|\.bz2|\.[0-9]+|-[0-9]{8})$")
SEV_RANK = {"CRIT": 0, "WARN": 1}
EXIT = {"OK": 0, "WARN": 1, "CRIT": 2}

log = logging.getLogger("disktriage")


class SnapshotError(ValueError):
    """The input is not a valid v1 snapshot."""


# ---------- model ----------

@dataclass
class Fs:
    mount: str
    fstype: str
    used_kb: int
    avail_kb: int
    iused: int
    ifree: int


@dataclass
class Deleted:
    pid: int
    inode: int
    kb: int
    comm: str
    path: str


@dataclass
class Snapshot:
    host: str = "unknown"
    fs: list[Fs] = field(default_factory=list)
    du: list[tuple[int, str]] = field(default_factory=list)          # (kb, path)
    deleted: list[Deleted] = field(default_factory=list)
    rotated: list[tuple[int, int, str]] = field(default_factory=list)  # (age_days, kb, path)


def ceil_pct(part: int, total: int) -> int:
    """df's formula: used / (used + avail), rounded UP, integer only (no floats anywhere)."""
    if total <= 0:
        return 0
    return -(-part * 100 // total)


def mib(kb: int) -> int:
    return kb // 1024


def owner_mount(path: str, mounts: list[str]) -> str | None:
    """Longest-prefix match on whole path components: /database is NOT under /data."""
    best = None
    for m in mounts:
        if m == "/" or path == m or path.startswith(m + "/"):
            if best is None or len(m) > len(best):
                best = m
    return best


def parse_snapshot(text: str) -> Snapshot:
    lines = text.splitlines()
    if not lines or lines[0] != HEADER:
        raise SnapshotError("not a disktriage v1 snapshot")
    snap = Snapshot()
    for n, line in enumerate(lines[1:], start=2):
        if line.startswith("# host="):
            snap.host = line.split()[1][len("host="):]
            continue
        if not line.strip() or line.startswith("#"):
            continue
        kind = line.split(" ", 1)[0]
        try:
            if kind == "fs":
                _, mount, fstype, *nums = line.split()
                if len(nums) != 4:
                    raise ValueError
                snap.fs.append(Fs(mount, fstype, *map(int, nums)))
            elif kind == "du":
                _, kb, path = line.split(" ", 2)
                snap.du.append((int(kb), path))
            elif kind == "deleted":
                _, pid, inode, kb, comm, path = line.split(" ", 5)
                snap.deleted.append(Deleted(int(pid), int(inode), int(kb), comm, path))
            elif kind == "rotated":
                _, age, kb, path = line.split(" ", 3)
                snap.rotated.append((int(age), int(kb), path))
            # unknown keywords are ignored: newer collectors can add facts
        except ValueError:
            raise SnapshotError(f"line {n}: malformed {kind!r} line: {line!r}") from None
    return snap


# ---------- analysis ----------

@dataclass
class Finding:
    severity: str
    kind: str            # "space" or "inodes"
    mount: str
    text: str
    details: list[str] = field(default_factory=list)


@dataclass
class Report:
    host: str
    findings: list[Finding]

    @property
    def crit(self) -> int:
        return sum(f.severity == "CRIT" for f in self.findings)

    @property
    def warn(self) -> int:
        return sum(f.severity == "WARN" for f in self.findings)

    @property
    def result(self) -> str:
        return "CRIT" if self.crit else "WARN" if self.warn else "OK"

    def text(self) -> str:
        out = [f"disktriage report: {self.host}"]
        for f in self.findings:
            out.append(f"{f.severity} {f.text}")
            out.extend(f"  {d}" for d in f.details)
        out.append(f"RESULT {self.result} crit={self.crit} warn={self.warn}")
        return "\n".join(out) + "\n"

    def as_dict(self) -> dict:
        return {
            "host": self.host, "result": self.result, "crit": self.crit, "warn": self.warn,
            "findings": [
                {"severity": f.severity, "kind": f.kind, "mount": f.mount, "message": f.text, "details": f.details}
                for f in self.findings
            ],
        }


def severity(pct: int, warn: int, crit: int) -> str | None:
    if pct >= crit:
        return "CRIT"
    if pct >= warn:
        return "WARN"
    return None


def analyse(snap: Snapshot, warn_pct: int = 85, crit_pct: int = 95, retain_days: int = 7) -> Report:
    mounts = [f.mount for f in snap.fs]
    findings: list[Finding] = []
    for fs in snap.fs:
        total = fs.used_kb + fs.avail_kb
        pct = ceil_pct(fs.used_kb, total)
        sev = severity(pct, warn_pct, crit_pct)
        if sev:
            findings.append(_space_finding(snap, fs, sev, pct, mounts, warn_pct, retain_days))
        ipct = ceil_pct(fs.iused, fs.iused + fs.ifree)   # vfat reports 0/0 inodes: 0%, not ZeroDivisionError
        isev = severity(ipct, warn_pct, crit_pct)
        if isev:
            findings.append(Finding(isev, "inodes", fs.mount, f"inodes {fs.mount} {ipct}% used"))
    findings.sort(key=lambda f: (SEV_RANK[f.severity], f.mount.encode(), f.kind != "space"))
    return Report(snap.host, findings)


def _space_finding(snap: Snapshot, fs: Fs, sev: str, pct: int, mounts: list[str],
                   warn_pct: int, retain_days: int) -> Finding:
    mine = lambda path: owner_mount(path, mounts) == fs.mount  # noqa: E731
    total = fs.used_kb + fs.avail_kb
    details: list[str] = []

    top = sorted(((kb, p) for kb, p in snap.du if mine(p)), key=lambda t: (-t[0], t[1].encode()))[:3]
    details += [f"top {mib(kb)} MiB {p}" for kb, p in top]

    held = sorted((d for d in snap.deleted if mine(d.path)), key=lambda d: (-d.kb, d.pid))
    details += [f"deleted {mib(d.kb)} MiB pid={d.pid} {d.comm} {d.path}" for d in held]
    # Two processes holding the same deleted file (same inode) free the space only once.
    deleted_kb = sum({d.inode: d.kb for d in held}.values())

    old = [(age, kb, p) for age, kb, p in snap.rotated if mine(p) and age >= retain_days]
    rotated_kb = sum(kb for _, kb, _ in old)
    if old:
        details.append(f"rotated {len(old)} files {mib(rotated_kb)} MiB older than {retain_days}d")

    freed = deleted_kb + rotated_kb
    after = ceil_pct(max(fs.used_kb - freed, 0), total)
    if freed:
        details.append(f"after cleanup {after}% (deleted-open {mib(deleted_kb)} MiB + rotated {mib(rotated_kb)} MiB)")
    if after >= warn_pct:
        details.append(f"needs capacity: still {after}% after cleanup (>= {warn_pct}%)")
    return Finding(sev, "space", fs.mount, f"space {fs.mount} {pct}% used, {mib(fs.avail_kb)} MiB free", details)


# ---------- snapshot (live) ----------

def list_mounts(proc_mounts: str = "/proc/mounts") -> list[tuple[str, str]]:
    seen: dict[str, str] = {}
    for line in Path(proc_mounts).read_text().splitlines():
        parts = line.split()
        if len(parts) >= 3 and parts[2] in REAL_FSTYPES:
            mp = parts[1].replace("\\040", " ")
            seen.setdefault(mp, parts[2])
    return sorted(seen.items(), key=lambda kv: kv[0].encode())


def _same_dev(path: str, dev: int) -> bool:
    try:
        return os.lstat(path).st_dev == dev
    except OSError:
        return False


def du_depth1(mount: str) -> list[tuple[int, str]]:
    """du -xk -d 1 MOUNT: allocated KB per first-level directory, one filesystem, hardlinks once."""
    try:
        root_dev = os.lstat(mount).st_dev
        entries = list(os.scandir(mount))
    except OSError:
        return []
    out = []
    for e in entries:
        try:
            if not e.is_dir(follow_symlinks=False):
                continue
            top = e.stat(follow_symlinks=False)
        except OSError:
            continue
        if top.st_dev != root_dev:
            continue                                  # another filesystem mounted here (-x)
        seen: set[int] = {top.st_ino}
        blocks = top.st_blocks                        # du counts the directory itself too
        for dirpath, dirnames, filenames in os.walk(e.path, onerror=lambda _e: None):
            for name in dirnames + filenames:
                try:
                    st = os.lstat(os.path.join(dirpath, name))
                except OSError:
                    continue
                if st.st_dev != root_dev or st.st_ino in seen:
                    continue
                seen.add(st.st_ino)
                blocks += st.st_blocks
            dirnames[:] = [d for d in dirnames if _same_dev(os.path.join(dirpath, d), root_dev)]
        out.append((blocks * 512 // 1024, e.path))
    return out


def scan_deleted(proc: str = "/proc") -> list[Deleted]:
    found: dict[tuple[int, int], Deleted] = {}
    for pid_dir in Path(proc).glob("[0-9]*"):
        try:
            comm = (pid_dir / "comm").read_text().strip().replace(" ", "_") or "?"
            fds = list((pid_dir / "fd").iterdir())
        except OSError:
            continue
        for fd in fds:
            try:
                target = os.readlink(fd)
                if not target.endswith(" (deleted)") or not target.startswith("/") or target.startswith("/memfd:"):
                    continue
                st = os.stat(fd)
            except OSError:
                continue
            if stat.S_ISREG(st.st_mode):
                found[(int(pid_dir.name), st.st_ino)] = Deleted(
                    int(pid_dir.name), st.st_ino, st.st_blocks * 512 // 1024, comm, target[: -len(" (deleted)")])
    return sorted(found.values(), key=lambda d: (d.pid, d.inode))


def scan_rotated(logdir: str, now: float) -> list[tuple[int, int, str]]:
    out = []
    for dirpath, _, files in os.walk(logdir, onerror=lambda _e: None):
        for name in files:
            if not ROTATED_RE.search(name):
                continue
            p = os.path.join(dirpath, name)
            try:
                st = os.lstat(p)
            except OSError:
                continue
            if stat.S_ISREG(st.st_mode):
                out.append((int((now - st.st_mtime) // 86400), st.st_blocks * 512 // 1024, p))
    return sorted(out, key=lambda t: t[2].encode())


def take_snapshot(du_min_pct: int = 80, logdir: str = "/var/log") -> str:
    now = time.time()
    lines = [HEADER, f"# host={socket.gethostname().split('.')[0]} "
                     f"taken={datetime.fromtimestamp(now, timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')}"]
    du_lines = []
    for mp, fstype in list_mounts():
        try:
            sv = os.statvfs(mp)
        except OSError:
            continue
        used = (sv.f_blocks - sv.f_bfree) * sv.f_frsize // 1024
        avail = sv.f_bavail * sv.f_frsize // 1024        # what non-root can use, like df
        lines.append(f"fs {mp} {fstype} {used} {avail} {sv.f_files - sv.f_ffree} {sv.f_ffree}")
        if ceil_pct(used, used + avail) >= du_min_pct:
            du_lines += [f"du {kb} {p}" for kb, p in du_depth1(mp)]
    lines += du_lines
    lines += [f"deleted {d.pid} {d.inode} {d.kb} {d.comm} {d.path}" for d in scan_deleted()]
    lines += [f"rotated {a} {kb} {p}" for a, kb, p in scan_rotated(logdir, now)]
    return "\n".join(lines) + "\n"


# ---------- CLI ----------

def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="disktriage", description="Disk-full triage for RHEL hosts")
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("snapshot", help="capture fs usage, top dirs, deleted-open files, rotated logs")
    s.add_argument("-o", "--output")
    s.add_argument("-m", "--du-min", type=int, default=80, help="run du only on filesystems >= this %% (default 80)")
    s.add_argument("-l", "--logdir", default="/var/log")
    r = sub.add_parser("report", help="analyse a snapshot")
    r.add_argument("-w", "--warn", type=int, default=85)
    r.add_argument("-c", "--crit", type=int, default=95)
    r.add_argument("-r", "--retain-days", type=int, default=7)
    r.add_argument("--json", action="store_true")
    r.add_argument("snapshot")
    return p


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s [%(levelname)s] %(message)s", stream=sys.stderr)
    try:
        args = build_parser().parse_args(argv)
    except SystemExit:
        return 3
    try:
        if args.cmd == "snapshot":
            text = take_snapshot(args.du_min, args.logdir)
            if args.output:
                Path(args.output).write_text(text)
                log.info("snapshot written to %s", args.output)
            else:
                sys.stdout.write(text)
            return 0
        rep = analyse(parse_snapshot(Path(args.snapshot).read_text()), args.warn, args.crit, args.retain_days)
    except (OSError, SnapshotError) as e:
        log.error("%s", e)
        return 3
    sys.stdout.write(json.dumps(rep.as_dict(), indent=2) + "\n" if args.json else rep.text())
    return EXIT[rep.result]


if __name__ == "__main__":
    sys.exit(main())
