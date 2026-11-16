# Day 002: Disk-full triage (top consumers, deleted-but-open files, safe log cleanup)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: RHEL ops · Stage 1 · Focus language: **Bash** · Difficulty: ⭐⭐ · Time: about 2–3 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

02:40, pager: **`/var` is at 97% on node-01**. The app team already "cleaned up": someone ran `rm /var/log/app/app.log.1` (4 GiB). `df` didn't move. Then they deleted the rotated httpd access log. `df` still didn't move. Now they want to `rm -rf /var/log/*`.

The space is still in use because **a deleted file stays on disk while a process still has it open**. `java` is still writing to the inode of `app.log.1`, and two `httpd` workers share the old `access_log` inode. `du` can't see these files (they have no name any more), but `df` still counts them. That gap between `df` and `du` is the classic clue.

**Goal:** one read-only triage that answers the on-call questions in order:

1. Which filesystems are hot (space **and** inodes)?
2. What's big on them (`du -x -d 1`)?
3. What space is held by **deleted-but-open** files, and by which PID? (Restart it or truncate it via `/proc/PID/fd/N`. Don't `rm` again.)
4. How much would a **safe** cleanup free (rotated logs older than N days, never the live log)?
5. Does that cleanup actually fix it, or **do we need capacity** (grow the LV, move data)?

node-03 is the AI-infra version of the same incident: `/data` on a training node is 87% full of model weights and checkpoints. A trainer still holds a deleted 2 GiB checkpoint, but even after freeing it, the filesystem is still exactly at the 85% line. Cleanup can't fix this; it needs capacity or a checkpoint retention policy.

## 2. The contract (every implementation must follow it)

**Snapshot v1** (plain text, one fact per line, single spaces, path always the last field so it may contain spaces):

```
# disktriage snapshot v1
# host=node-01 taken=2026-11-16T02:40:00Z
fs /var xfs 19681280 655360 48211 10437389        ← fs MOUNT FSTYPE USED_KB AVAIL_KB IUSED IFREE
du 7340032 /var/log                               ← du KB PATH   (du -xk -d 1, filesystems >= 80% only)
deleted 2314 131 4194304 java /var/log/app/app.log.1   ← deleted PID INODE KB COMM PATH
rotated 9 524288 /var/log/app/app.log.3.gz        ← rotated AGE_DAYS KB PATH  (*.gz *.xz *.bz2 *.N *-YYYYMMDD)
```

**Rules** (defaults: `-w 85 -c 95 -r 7`):

| Check | Rule |
|---|---|
| Space % | `ceil(used × 100 / (used + avail))`, df's formula, integers only. `>= crit` → **CRIT**, `>= warn` → WARN |
| Inode % | same formula on `iused / (iused + ifree)`. `0/0` (vfat) = 0%, not a crash |
| Which fs owns a path | **longest prefix on whole path components**: `/database` belongs to `/`, not `/data` |
| `top` | the 3 biggest `du` entries on the hot fs, by size desc, then path (byte order) |
| `deleted` | every deleted-open file on the hot fs, by size desc, then PID. Space counts **once per inode** (two httpd workers share one file) |
| `rotated` | rotated logs on the hot fs with age `>= retain` days: count and total |
| `after cleanup` | `% used` if all deleted-open + old rotated space were freed (printed only if > 0) |
| `needs capacity` | printed when `after cleanup` is still `>= warn` |

Sizes print as **MiB = KB // 1024** (floor). **Order:** CRIT before WARN, then mount (byte order), then space before inodes. **Exit codes:** `0` OK · `1` WARN · `2` CRIT · `3` usage / malformed input.
Golden files: [`samples/expected/`](samples/expected/). node-01 = CRIT, node-02 = OK, node-03 = WARN.

```
disktriage report: node-01
CRIT space /var 97% used, 640 MiB free
  top 7168 MiB /var/log
  top 2560 MiB /var/lib
  top 1024 MiB /var/cache
  deleted 4096 MiB pid=2314 java /var/log/app/app.log.1
  deleted 1024 MiB pid=1201 httpd /var/log/httpd/access_log
  deleted 1024 MiB pid=1388 httpd /var/log/httpd/access_log
  rotated 4 files 1736 MiB older than 7d
  after cleanup 63% (deleted-open 5120 MiB + rotated 1736 MiB)
WARN space / 88% used, 4900 MiB free
  top 12288 MiB /opt
  top 8192 MiB /usr
  top 2048 MiB /root
  needs capacity: still 88% after cleanup (>= 85%)
WARN inodes /tmp 91% used
RESULT CRIT crit=1 warn=2
```

Traps built into the fixtures: shared inode (5120, not 6144), `/database` vs `/data`, an exact 87.000% that must not round up to 88, an "after cleanup" of exactly 85% that is still `>= warn`, a 2-day-old rotated log that must be kept, and vfat with 0 inodes.

## 3. Four implementations: what each tool is *for*

| | Bash `bash/disktriage.sh` | Ansible `ansible/` | Python `python/disktriage.py` | Go `go/` |
|---|---|---|---|---|
| Best at | the 2 a.m. bastion one-liner turned into a tool; no dependencies | **fleet** triage, read-only, `become` to see every process | tested logic, **`--json`** for the ticket / alert annotation | static binary; **concurrent `du`** per filesystem, `fleet` over N snapshots |
| Collects via | `df -P`, `du -xk -d 1`, `find /proc/*/fd -lname '*(deleted)'`, `find -regex` | `ansible_facts['mounts']`, `df --output`, `du`, `shell`, **`find` module** | `os.statvfs`, `os.scandir`/`os.walk`, `os.readlink` | `syscall.Statfs`, `filepath.WalkDir`, `os.Readlink` |
| Analysis | awk (POSIX/mawk-safe) → `sort` with 6 keys → awk | Jinja with `namespace`, stable multi-pass `sort` | dataclasses + `sorted(key=...)` | structs + `sort.Slice` |
| Test | `bash/test.sh` (golden + 8 checks) | `report_samples.yml` | `pytest` (21 tests) | `go test` (golden, table tests, fleet) |

Run everything with `make test`. CI runs it too ([`.github/workflows/daily-labs.yml`](../../.github/workflows/daily-labs.yml)).

## 4. Run it

```bash
cd daily-labs/day-002-disk-full-triage

# Bash
bash/disktriage.sh report samples/node-01.snap; echo "exit=$?"
sudo bash/disktriage.sh snapshot -o /tmp/me.snap && bash/disktriage.sh report /tmp/me.snap

# Python
python3 python/disktriage.py report --json samples/node-03.snap

# Go
cd go && go build -o disktriage . && ./disktriage fleet -j 4 ../samples; cd ..
#   HOST         RESULT CRIT WARN  WORST
#   node-01      CRIT      1    2  space /var 97% used, 640 MiB free
#   node-02      OK        0    0  -
#   node-03      WARN      0    1  space /data 87% used, 13312 MiB free

# Ansible (pip install ansible-core)
cd ansible
ansible-playbook report_samples.yml                              # contract test
ansible-playbook disk_triage.yml -e triage_become=true           # snapshots/<host>.snap + .report, fails CRIT hosts
```

## 5. Run it for real

**GCP RHEL VM or the J2C lab bastion** (reproduce the incident safely on a small test LV or a loop device, never on `/`):

```bash
sudo dd if=/dev/zero of=/var/tmp/fill.img bs=1M count=0 seek=2048 && sudo mkfs.xfs -q /var/tmp/fill.img
sudo mkdir -p /mnt/triage && sudo mount -o loop /var/tmp/fill.img /mnt/triage
sudo fallocate -l 1800M /mnt/triage/app.log
sudo bash -c 'exec 9>>/mnt/triage/app.log; sleep 600' &      # a "process" holding the log
sudo rm /mnt/triage/app.log                                    # the classic mistake
df -h /mnt/triage; sudo du -sh /mnt/triage                     # df says ~90%, du says ~0
sudo bash/disktriage.sh snapshot -o /tmp/me.snap && bash/disktriage.sh report /tmp/me.snap
# fix it without killing the process:  sudo truncate -s 0 /proc/<PID>/fd/9   → re-run: OK
```

**OpenShift (J2C lab):** the same thing happens inside nodes when container logs or `emptyDir` fill `/var/lib/containers` or `/var/lib/kubelet`. Kubelet then sets `DiskPressure` and starts evicting pods. Look at it with `oc debug node/<node> -- chroot /host df -h /var` and `crictl ps` (Day 19 covers node pressure properly).

**kind (AROHAN clusters):** `docker exec -it <cluster>-worker bash`, then copy `bash/disktriage.sh` in with `docker cp` and run it there. Nodes are containers, so you'll see `overlay` filesystems being skipped: that's the fstype filter working.

## 6. Break it (do these yourself, then record what you saw)

1. **Prefix trap:** in `bash/disktriage.sh` `owner()`, change the condition to `substr(p, 1, length(m)) == m` (no trailing `/`). Run `bash/disktriage.sh report samples/node-03.snap`. Where does `/database` (40 GiB) show up now, and why is that a dangerous wrong answer at 2 a.m.?
2. **Double counting:** in Python, replace the `{d.inode: d.kb ...}` dedupe with `sum(d.kb for d in held)`. Which golden line changes? Then check on a real host: `ls -l /proc/*/fd 2>/dev/null | grep deleted`. Why do prefork servers (httpd, gunicorn) always show the same file several times?
3. **Rounding:** make Go's `ceilPct` use `math.Ceil(float64(part)*100/float64(total))`. Does node-03's exact 87% still pass? Now try `used=1 avail=2` with `float32`. Why do ops tools do percentages in integers?
4. **`cmd | grep -q` under `pipefail`:** in `bash/test.sh`, change the `-r 1` check back to `./disktriage.sh report -r 1 ... | grep -qx '...'`. It fails even though the line is there. Two reasons: exit 2 from the report, and SIGPIPE when `grep -q` quits early.
5. **Jinja sort:** in `ansible/tasks/report.yml`, drop `case_sensitive=true` and add `du 1048576 /var/Zarchive` to a copy of node-01 (same size as `/var/cache`, so the path breaks the tie). Which one makes the top 3 in Ansible now, and which in Bash/Python/Go? Why does `LC_ALL=C` put `Z` before `c`?
6. **The real fix:** with the loop-device lab above, compare `kill -HUP` (logrotate's `postrotate`), `truncate -s 0 /proc/PID/fd/N`, and `systemctl restart`. Which ones free the space, and which one is safe for a database?

## 7. Architect's lens: 1,000 nodes / 10,000 GPUs

- **Don't wait for 97% at 2 a.m.** At fleet scale, disk-full is a *capacity forecasting* problem (Day 7: days-to-full), not an incident. Alert on **rate** ("full in < 6 h") as well as level. Triage like this tool becomes the *runbook action* attached to the alert, so the pager arrives with the answer.
- **`du` is the expensive part.** On a 50 TB `/data` with millions of checkpoint shards, a full walk takes minutes and hammers metadata (worse on NFS/Lustre/GPFS). Hence `du_min_pct`, `-d 1`, `timeout`, and the Go version's one goroutine per filesystem. At scale, use XFS project quotas or the storage system's own accounting instead of walking.
- **Read-only first, remediation second.** Automated deletion across 1,000 nodes is how you lose a dataset. Pattern: triage → proposed action in the report → human approval or a narrow, allow-listed auto-action (truncate known log files held by known units) → verify with a second snapshot. That's the closed-loop design from Day 52.
- **GPU nodes are special.** Local NVMe is used for container image layers (multi-GB CUDA images), dataset cache and checkpoints. Kubelet image GC thresholds (`imageGCHighThresholdPercent`), `ephemeral-storage` requests/limits and a separate filesystem for `/var/lib/containers` stop one job's checkpoints from evicting everyone's pods.
- **Trade-off:** pull model (node agents push metrics such as `node_filesystem_avail_bytes` to Prometheus) for detection, push model (Ansible / SSH fan-out, as here) for deep triage on the few hot nodes. Never run deep triage on all 1,000 nodes at once.

## 8. Interview talking points

- **"`df` says full, `du` says not. Why?"** Deleted-but-open files: the inode stays allocated until the last FD closes. Find them with `lsof +L1` or `find /proc/*/fd -lname '*(deleted)'`. Fix by restarting or reloading the process, or `truncate -s 0 /proc/PID/fd/N`. Other causes: files hidden under a mount point, reserved blocks (ext4 5%), snapshots (LVM/btrfs).
- **"Disk is full, walk me through it."** `df -h` and `df -i` → `du -xh -d 1 | sort -h` (the `-x` matters) → `lsof +L1` → rotated logs and caches → decide whether cleanup is enough or it needs capacity → fix the root cause (logrotate, retention, quotas).
- **"Inodes 100%, space 40%?"** Millions of tiny files (session files, mail queue, a runaway cache). `df -i`, then `find /path -xdev -type f | cut -d/ -f2-3 | sort | uniq -c | sort -n`. XFS allocates inodes dynamically, but ext4 has a fixed count set at `mkfs`.
- **"Why not just `rm` big logs?"** If the writer still has it open, nothing is freed and you lose the evidence. Use logrotate with `copytruncate` (some lines lost) or a `postrotate` signal (clean), and truncate instead of deleting a live file.

## 9. What I'd add for production

- `lsof`-free deleted-file detection is here; add **owner unit** (`/proc/PID/cgroup` → systemd unit or pod name) so the report says *which service* to restart.
- Detect **files hidden under mount points** (bind-mount `/` to `/mnt/root` and `du` it), and **ext4 reserved blocks** (`tune2fs -l`).
- An **allow-listed remediation** mode: truncate deleted logs held by known units, `journalctl --vacuum-size`, `dnf clean all`, `podman image prune`, always dry-run first and re-snapshot after.
- Export `--json` to the alert annotation / ticket; push a days-to-full forecast next to it (Day 7).
- Run it as a systemd timer with a `flock` so two on-call engineers don't both run `du` on the same NFS mount.

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite the **`report`** half of the Bash version yourself in `mine/bash/disktriage.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/   # same tests, your script
mine/bash/test.sh
```

Rules: `set -Eeuo pipefail`, `LC_ALL=C`, an ERR trap, shellcheck-clean, POSIX awk only (it must run on `mawk`). Start with only `fs` lines and the space finding, get node-02 and node-03's first line right, then add `top`, then `deleted` with the inode dedupe. Commit with a 5-line note: what you got wrong, and what you learned.
