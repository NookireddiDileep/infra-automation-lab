# Interview Bank  #interview

Short answers you can say in 30 seconds. Every Saturday, cover the answers, say them aloud, and tag the weak ones `#revise`.

## Bash: loops and input
- **Why use `while IFS= read -r line` rather than `for line in $(cat file)`?**  
  The `for` version splits on any whitespace and expands globs. `while read` handles one line at a time and keeps it intact.
- **Why are variables lost after `cmd | while read`?**  
  The pipeline runs the loop in a subshell. Use `done < file` or process substitution `done < <(cmd)`.
- **How do you run checks on many hosts in parallel?**  
  Background jobs with `&` plus `wait`, or `xargs -P N`. Cap the concurrency and log results per host.

## Bash: production habits
<!-- fill from Week 3 -->

## Text processing (grep / awk / sed / jq)
<!-- fill from Week 2 -->

## Scenario stories (STAR) — link scripts to real work
<!-- e.g. "During DR drills at T-Systems I checked reachability of N hosts by hand; I'd now script it like ping_check.sh, which cuts the check from X min to Y sec." -->

## Daily labs
- **How do you verify a patch window beyond "it pings"?** (Daily 001)  
  Snapshot mounts, listening ports, running services and kernel before and after, then diff them. Anything missing is CRIT and fails the change; new items and full disks are WARN. In Ansible, use `serial` + `max_fail_percentage: 0` so a bad batch stops the rollout.
- **Bash vs Ansible vs Python vs Go for the same job?** (Daily 001)  
  Bash for one host with no dependencies. Ansible for fleet collection and gating. Python for tested logic and JSON. Go for a static binary and concurrent fleet processing.
