# Day 001: Patch-window drift check (pre/post reboot)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: RHEL ops · Difficulty: ⭐⭐ · Time: about 2–3 h to read, run and break; about 1 day to rewrite from scratch yourself

---

## 1. The scenario

It's a monthly patch window: 40 RHEL 9 hosts, `dnf update`, reboot. The change ticket says "post-checks OK". Next morning the app team reports that **`/data` is not mounted on node-01** (an `fstab` typo nobody noticed), **httpd didn't come back**, and port 8443 is dead. Someone ticked the box after checking that the host pinged.

**Goal:** take a machine-readable snapshot before patching and another after the reboot, and get a report that says exactly what disappeared, what appeared, and what's about to fill up. It should **fail the change** (non-zero exit / failed Ansible host) when something critical vanished.

This is the pre/post-check routine from real enterprise patch windows, automated.

## 2. The contract (every implementation must follow it)

**Snapshot v1** (plain text, one fact per line, diff-friendly):

```
# patchcheck snapshot v1
# host=node-01 taken=2026-11-15T22:00:00Z
kernel 5.14.0-362.24.1.el9_3.x86_64
mount /data xfs             ← real filesystems only (xfs ext4 ext3 btrfs vfat nfs nfs4)
port tcp/8443               ← listening TCP ports
service httpd.service       ← running systemd services
fsuse /data 72              ← % used, df formula: used / (used + avail), rounded up
```

**Rules for comparing PRE vs POST**

| Finding | Severity |
|---|---|
| kernel changed / unchanged | INFO (expected after patching; unchanged means *was the reboot done?*) |
| a `mount` / `port` / `service` in PRE is missing in POST | **CRIT** |
| a `mount` / `port` / `service` is new in POST | WARN |
| a filesystem in POST is ≥ threshold (default 90%) | WARN |

**Report:** lines sorted by severity, then in byte order (`LC_ALL=C`). **Exit codes:** `0` OK · `1` WARN · `2` CRIT · `3` usage/runtime error.
Golden files: [`samples/expected/`](samples/expected/). Node-01 = CRIT, node-02 = OK, node-03 = WARN.

```
patchcheck report: node-01
INFO kernel 5.14.0-362.24.1.el9_3.x86_64 -> 5.14.0-427.13.1.el9_4.x86_64
CRIT missing mount /data xfs
CRIT missing port tcp/8443
CRIT missing service httpd.service
WARN new port tcp/9090
WARN fs / at 93% (>= 90%)
RESULT CRIT crit=3 warn=2
```

## 3. Four implementations: what each tool is *for*

| | Bash `bash/patchcheck.sh` | Ansible `ansible/` | Python `python/patchcheck.py` | Go `go/` |
|---|---|---|---|---|
| Best at | one host, zero dependencies, cron/systemd timer | **fleet**: collect from N hosts, gate a rolling patch | tested logic, **JSON** for tickets/dashboards | one static binary, **concurrent** fleet compare |
| Collects data via | `/proc/mounts`, `ss` (fallback `/proc/net/tcp`), `systemctl`, `df` | facts: `ansible_facts.mounts`, `service_facts`, `ss` | `/proc/*`, `os.statvfs`, `subprocess` | `/proc/*`, `syscall.Statfs`, `os/exec` |
| Set difference | `comm -23` / `comm -13` on sorted input | `difference` filter | `set - set` | map lookups |
| Extra | ERR trap, strict mode | `serial` + `max_fail_percentage: 0` stops a bad rollout | `--json`, 10 pytest tests | `fleet DIR -j N` worker pool |
| Test | `bash/test.sh` | `compare_samples.yml` | `pytest` | `go test` |

Run everything with `make test`. CI runs it too ([`.github/workflows/daily-labs.yml`](../../.github/workflows/daily-labs.yml)).

## 4. Run it

```bash
cd daily-labs/day-001-patch-window-drift-check

# Bash
bash/patchcheck.sh snapshot -o /tmp/pre.txt
bash/patchcheck.sh compare samples/node-01-pre.txt samples/node-01-post.txt; echo "exit=$?"

# Python
python3 python/patchcheck.py compare --json samples/node-01-pre.txt samples/node-01-post.txt

# Go
cd go && go build -o patchcheck . && ./patchcheck fleet -j 4 ../samples; cd ..
#   HOST         RESULT CRIT WARN
#   node-01      CRIT      3    2
#   node-02      OK        0    0
#   node-03      WARN      0    3

# Ansible (pip install ansible-core)
cd ansible
ansible-playbook compare_samples.yml             # contract test
ansible-playbook patch_window.yml -e phase=pre   # snapshots/<host>-pre.txt
ansible-playbook patch_window.yml -e phase=post  # report + fail on CRIT
```

**On the OpenShift training lab or a GCP RHEL VM:** take a `pre` snapshot, then `systemctl stop chronyd` and `umount` a test filesystem, take `post`, and compare. You should get CRIT. Undo the changes and the result goes back to OK.

## 5. Break it (do these yourself, then record what you saw)

1. **The `set -e` trap:** in `bash/patchcheck.sh`, change `crit=$((crit + 1))` to `((crit++))`. Run the test. Why does the script die on the *first* missing item? (Hint: [GOTCHAS](../../notes/GOTCHAS.md), W1 Wed.)
2. **Locale:** run `LC_ALL=en_US.UTF-8 sort` vs `LC_ALL=C sort` on `port tcp/22`, `port tcp/443`, `Port X`. Why does `comm` complain "file not in sorted order" if the two sides were sorted differently?
3. **Non-zero `return` under `set -e`:** make `cmd_compare` `return 2` instead of setting `RC`. What does the ERR trap do? Why did the script use a global?
4. **Ansible `serial`:** put 3 hosts in the inventory, break one, and run `post` with `-e patch_batch=1`. Confirm the play stops after the bad batch.
5. **Python:** make `fs_used_pct` use `f_bfree` instead of `f_bavail`. Your numbers no longer match `df`. Why? (Reserved blocks for root.)
6. **Go:** remove `sort.Strings` in `diff()`. Run `go test -count=20`. Why does it fail only *sometimes*? (Map iteration order is random.)

## 6. Interview talking points

- **"How do you verify a patch window?"** Snapshot the state before and after, diff it, and gate on the diff instead of on "it pings". Missing mounts, services and listeners are CRIT and stop the rollout. New items and full disks are WARN. Kernel change is the expected result.
- **"Why one text format?"** Every tool can produce it and `diff` can read it. It's also a contract that four implementations are tested against, so a refactor can't silently change behaviour.
- **"When would you pick each tool?"** Bash for one host and no dependencies. Ansible for fleet orchestration and rollout gating. Python for logic you need to test and for JSON integration. Go for a fast static binary and concurrency.
- **"`comm` vs `diff`?"** `comm` gives set membership on sorted input (lines only in A or only in B). `diff` gives an edit script. For "what disappeared", `comm` is the right tool.
- **"Why `f_bavail` and not `f_bfree`?"** `df` reports what non-root users can use. RHEL reserves about 5% on ext4.

## 7. What I'd add for production
- Run it from Ansible in the real patching playbook (pre task → patch → reboot → post task) with `serial: 10%`.
- Add more facts: `/etc/fstab` vs mounted, NTP sync state, `crontab` hashes, SELinux mode, `rpm -qa` count, multipath paths.
- Store reports with the change ticket number. Push the JSON to the CMDB or a dashboard.
- An allow-list for expected changes and **on-demand units**. Services like `systemd-timedated` or `netavark-dhcp-proxy` are started by a socket or D-Bus call and exit when idle, so they "vanish" between snapshots. This tool's own CI hit exactly this, which is why the live CI run uses `-e fail_on_crit=false`.
