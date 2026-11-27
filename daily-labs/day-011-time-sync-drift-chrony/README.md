# Day 011: Time sync drift (chrony): one bad clock in a fleet of good ones

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Linux / RHEL fleet ops · Stage 1 · Focus language: **Bash** · Difficulty: ⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Friday 16:40, a rolling etcd member keeps losing leader elections, a Kerberos login fails with `Clock skew too great`, and a distributed-training job dies at a barrier with a timeout nobody can reproduce. Everyone's `date` looks "about right". The cause: `node-01` is **412 ms slow**, because its preferred NTP server went bad and chrony followed it before the clock was sanity-checked. A second host, `node-04`, has chronyd up but **never synchronised**: both of its sources are unreachable (a firewall rule dropped UDP 123 during the last patch window).

Clock skew is an invisible failure: nothing is "down", yet consensus protocols, certificate validity, token expiry and log correlation all quietly break. A fleet audit has to answer four questions per host: is it **synchronised at all**, how **far off** is it, is it following a **sane source**, and does it have enough sources to **out-vote a liar**.

**Goal:** per host, one inventory made of `chronyc tracking` and `chronyc sources`, and a report with findings and an exit code a pipeline can gate on.

Fixtures: `node-01` (CRIT: 412 ms slow + a falseticker), `node-02` (OK, with the offset *exactly* on the 10 ms boundary), `node-03` (WARN: 25 ms fast, stratum 7, one unreachable source), `node-04` (CRIT: not synchronised), `node-05` (WARN: healthy clock, only two sources).

## 2. The contract (every implementation must follow it)

**Inventory v1** is what `bash/timesync.sh collect` prints: two header comments, then the raw `chronyc tracking` and `chronyc sources` output, each under its own marker line.

```
# timesync inventory v1
# host=node-01
# chronyc tracking
Reference ID    : C0A80A01 (ntp1.example.com)
Stratum         : 3
System time     : 0.412337912 seconds slow of NTP time
Leap status     : Normal
  ... (other tracking lines are ignored)
# chronyc sources
MS Name/IP address         Stratum Poll Reach LastRx Last sample
===============================================================================
^* ntp1.example.com              2  10   377   311   -412ms[ -412ms] +/-   18ms
^x ntp3.example.com              2  10   377   215   +388ms[ +388ms] +/-   19ms
```

Parsed fields: `Leap status`, `Stratum`, `System time` (exactly 9 decimals, as chrony prints it; kept as **integer nanoseconds**, no floats anywhere), and per source the state character (`*` selected, `+` combined, `-` not combined, `?` unusable, `x` falseticker, `~` too variable), name and `Reach` (octal). A present-but-malformed line of those keys, a source line that doesn't parse, or a missing section is an **input error**.

**Rules** (defaults `--warn-ms 10`, `--crit-ms 100`, `crit > warn` enforced; all comparisons are strict `>`):

| Finding | Severity |
|---|---|
| `Leap status: Not synchronised` → `unsynchronised` (offset and stratum are then **not** evaluated: chrony prints stale values) | **CRIT** |
| `abs(System time) > crit` → `offset 412.337ms slow (crit 100ms)` | **CRIT** |
| `warn < abs(System time) <= crit` → `offset 25.100ms fast (warn 10ms)` | WARN |
| `Stratum > 5` | WARN |
| a source with `Reach` all zeros → `source NAME unreachable (reach 0)` | WARN |
| a source in state `x` → `source NAME falseticker` | WARN |
| no source in state `*` → `no selected source` | WARN |
| fewer than 3 sources → `sources N (need at least 3 to out-vote a falseticker)` | WARN |

The offset is printed truncated (never rounded) to microseconds: `1999.999999ms` shows as `1999.999ms`, so every language prints the same digits.

**Report:** header, findings in byte order (`LC_ALL=C`, so `CRIT` before `WARN`), then `RESULT <OK|WARN|CRIT> crit=N warn=N sources=N`. **Exit codes:** `0` OK · `1` WARN · `2` CRIT · `3` usage/input error. Golden files: [`samples/expected/`](samples/expected/); bad inputs: [`samples/bad/`](samples/bad/).

```
timesync report: node-04
CRIT unsynchronised (leap status: Not synchronised)
WARN no selected source
WARN source ntp1.example.com unreachable (reach 0)
WARN source ntp2.example.com unreachable (reach 0)
WARN sources 2 (need at least 3 to out-vote a falseticker)
RESULT CRIT crit=1 warn=4 sources=2
```

## 3. Four implementations: what each tool is *for*

| | Bash `bash/timesync.sh` | Ansible `ansible/` | Python `python/timesync.py` | Go `go/` |
|---|---|---|---|---|
| Best at | one host, plus the **collector** (`chronyc tracking` / `sources`) | **fleet**: gate a pipeline on `fail_on_crit`, write reports idempotently | tested logic, **`--json`** for tickets/dashboards | one static binary, **concurrent** `fleet DIR -j N` |
| Offset maths | `BASH_REMATCH` into integer nanoseconds (`10#` stops `0000…` being read as octal) | template: `regex_search` groups, `//` and `%` on ints | `int()` on the two regex groups, `//` | `strconv.ParseInt`, `int64` |
| Sorting | `LC_ALL=C sort` | `sort(case_sensitive=true)` | `sorted()` | `sort.Strings` |
| Extra | strict mode, ERR trap, `collect` tested against a **fake `chronyc`** on `PATH` | template returns JSON, `assert` as the CI gate | 21 pytest cases, boundary table, argparse exit-code fix | goroutine worker pool, `-race` clean |
| Test | `bash/test.sh` | `compare_samples.yml` | `pytest` | `go test` |

Run everything with `make test`. CI runs it too ([`.github/workflows/daily-labs.yml`](../../.github/workflows/daily-labs.yml)).

## 4. Run it

```bash
cd daily-labs/day-011-time-sync-drift-chrony

# Bash
bash/timesync.sh check samples/node-01.chrony; echo "exit=$?"

# Python
python3 python/timesync.py check --json samples/node-03.chrony

# Go
cd go && go run . fleet -j 4 ../samples; cd ..
#   HOST     RESULT  CRIT  WARN  SOURCES
#   node-01  CRIT    1     1     3
#   node-02  OK      0     0     4
#   node-03  WARN    0     3     3
#   node-04  CRIT    1     4     2
#   node-05  WARN    0     1     2

# Ansible (pip install ansible-core)
cd ansible
ansible-playbook compare_samples.yml                 # contract test
ansible-playbook timesync.yml -e fail_on_crit=false  # reports/<host>.report
```

## 5. Run it for real

On an OpenShift training lab node (RHCOS runs chronyd), a GCP RHEL VM or any chrony host:

```bash
bash/timesync.sh collect --host "$(hostname -s)" > /tmp/$(hostname -s).chrony
bash/timesync.sh check /tmp/$(hostname -s).chrony; echo "exit=$?"
```

Notes: on OpenShift nodes use `oc debug node/<node> -- chroot /host chronyc tracking` (and `sources`) to collect. On GCP the usual source is the metadata server `metadata.google.internal` (a single source, so the "fewer than 3" WARN is expected and worth discussing). `chronyc makestep` steps the clock immediately; it is the repair for a large offset, but stepping a running etcd or database node needs care.

## 6. Break it (do these yourself, then record what you saw)

1. **Boundary:** change `>` to `>=` in the warn comparison. `node-02` (exactly 10.000 ms) flips from OK to WARN. Which tests catch it, and why does `--warn-ms 9` in `test.sh` exist?
2. **Octal trap:** in Bash remove the `10#` prefix from the nanosecond arithmetic. Which value in the fixtures makes Bash fail or silently misread it (`000412345`, `0.025…`)?
3. **Float temptation:** parse the offset as a float (`float("0.412337912")`) in Python and print it with `:.3f`. Find a value where it rounds instead of truncating and the golden file no longer matches. Which unit test already covers it?
4. **Unsynchronised noise:** delete the `unsynchronised` branch's suppression so offset and stratum are evaluated on `node-04`. What extra finding appears, and why is it misleading?
5. **argparse exit code:** run `python3 timesync.py check` with no file. Why must a usage error exit `3` and not Python's default `2`? (Hint: `2` means CRIT.)
6. **Dead chronyd:** make the fake `chronyc` in `bash/test.sh` exit 1. Why must `collect` fail loudly instead of writing an empty inventory?

## 7. Architect's lens

- **Offset is not the whole story.** A host can be 1 ms off yet following a falseticker that is wrong for everyone, so the audit also looks at source quality (selected source, falsetickers, quorum). Three or more independent sources let chrony out-vote one liar; two sources cannot tell which is wrong.
- **What breaks at what skew.** Rough orders of magnitude: etcd/Raft and leases misbehave from tens of ms, Kerberos tolerates 5 minutes by default, TLS validity windows are minutes-to-days, HPC/AI collectives and tracing need sub-ms. Thresholds should follow the **most sensitive workload on the node**, not one global number; GPU training nodes deserve tighter ones than a batch farm.
- **Step vs slew.** chrony slews small offsets (never goes backwards) and steps large ones only at start-up unless `makestep` allows it. A fleet-wide "fix" that steps clocks under running databases is its own incident.
- **Scale.** Collection is cheap, per host, and I/O bound, so it shards trivially (Ansible `forks`, Go workers). Keep evaluation a pure function of the inventory so it is unit-testable. At 1,000 nodes prefer **pull into Prometheus** (`node_timex_offset_seconds`, `chrony_*` exporter) with alerts, and keep this tool as the audit/diagnosis view.
- **Topology:** point the fleet at 3-4 internal servers (themselves synced to several upstreams or GPS/PTP), never at the public pool from every node. For tight bounds use PTP with hardware timestamping.
- **Trade-off:** one chronyc snapshot is a point in time. A host that drifted and recovered between runs is invisible, so the continuous metric is the real control and the snapshot is the evidence.

## 8. Interview talking points

- **"Services fail intermittently and everything looks healthy. What do you check about time?"** `chronyc tracking` (Leap status, System time, Stratum), `chronyc sources -v` (which source is selected, reach), `timedatectl` (`System clock synchronized: yes`). Compare hosts to each other, not just to the wall clock.
- **"What's the difference between `chronyc tracking` offset and `Last offset`?"** `System time` is the current estimated error of the clock against NTP time; `Last offset` is the error measured at the last update. Alert on the first, use the second for trend and debugging.
- **"Why at least three (ideally four) time sources?"** To outvote a falseticker. With two, chrony sees disagreement and cannot tell who is wrong; three give a majority. Four tolerate one failure while still having a quorum.
- **"A host is `Not synchronised`. What now?"** Reachability first (UDP 123, firewall, DNS for the pool names), `chronyc sources` reach column, `chronyc activity`, `journalctl -u chronyd`. If the offset is huge and `makestep` is not allowed, `chronyc makestep` once, with the workload in mind.
- **"Why integers, not floats?"** The same 9-digit text must produce the same output in four languages. Integer nanoseconds make thresholds exact (`> 10 ms` is `> 10_000_000 ns`) and avoid rounding differences.

## 9. What I'd add for production

- Collect `Last offset`, `RMS offset`, `Skew` and `Root dispersion` too, and alert on a *trend* (offset growing over N samples), not one snapshot.
- Flag `Ref time` older than a few poll intervals (chronyd alive but not updating) and a leap-second insert/delete status ahead of 30 June / 31 December.
- A fleet mode that also reports the **max pairwise skew** between hosts in one cluster (etcd members, GPU job peers), which matters more than the offset to wall clock.
- Export metrics (`node_exporter` timex collector, a chrony exporter) and alert on them; keep a per-workload threshold map.
- A `--fix` mode that only **prints** the remediation per finding (`chronyc makestep`, firewall rule, `server` lines to add).

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`timesync.sh`** (just the `check` subcommand) yourself in `mine/bash/timesync.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
TIMESYNC=mine/bash/timesync.sh mine/bash/test.sh
```

(the `collect` checks at the end of `test.sh` need `collect` implemented; do it last). Start with `node-02` (exit 0), then the offset boundaries, then sources (falseticker, reach 0, quorum), and the `10#` octal trap last.

### Friday interview round: solve it in 30 minutes

> *"Our etcd cluster loses leadership every few days and we suspect clock problems on some masters. You have 30 minutes: show me how you would prove or rule that out across 400 hosts, and what you'd put in place so it can't silently happen again."*

Aim for: collect `chronyc tracking` and `sources` fleet-wide (Ansible `forks`, or pull via node_exporter), a pure evaluator with per-workload thresholds, the quorum/falseticker logic, a pairwise-skew view for the etcd members, alerting (ticket on WARN, page on CRIT or unsynchronised), and the safe remediation (`makestep` only with the workload in mind).
