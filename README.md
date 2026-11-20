# infra-automation-lab

Hands-on automation for enterprise Linux, OpenShift/Kubernetes and (later) GPU/AI infrastructure.
**Bash → Ansible → Python → Go.** Every folder has working code, the notes behind it, and what I learned when it broke.

> Built by a senior RHEL/infra engineer (13+ yrs: enterprise private cloud, HPC, DR) moving into platform engineering on OpenShift.
> Part of [Project AROHAN AI](https://github.com/NookireddiDileep/project-arohan-ai), my long-term hands-on learning track toward AI infrastructure.

---

## Why this order

| # | Tool | What it's for in a senior role | What I need to be able to show |
|---|---|---|---|
| 1 | **Bash** | Glue for everything: bastion scripts, `oc`/`jq` triage, pre/post checks, cron/systemd jobs | A safe, shellcheck-clean, logged, locked, timer-driven health check for hosts and the cluster |
| 2 | **Ansible** | Fleet configuration and Day-2 ops at scale; Red Hat's own automation stack | Idempotent roles (patching, hardening, OCP onboarding), vault, tests, run across many hosts |
| 3 | **Python** | APIs, reporting and tooling; the language of the AI/ML infra ecosystem | Tested CLI tools (argparse, logging, pytest) using the Kubernetes API, with reports in JSON/CSV |
| 4 | **Go** | The language Kubernetes and OpenShift are written in: operators, controllers, `oc`/`kubectl` plugins | A `client-go` CLI plugin and a small operator built with Kubebuilder |

Rule of thumb when picking a tool: **Bash** if it's under ~100 lines and glues commands together → **Ansible** if it's about configuring the *desired state* of many machines or clusters → **Python** if it needs data structures, APIs or tests → **Go** if it's part of how Kubernetes itself is extended.

See **[ROADMAP.md](ROADMAP.md)** for the week-by-week plan and the exit criteria for each phase.

---

## Repo layout

```
01-bash/       weekNN-*/  → session notes + scripts;  projects/ → mini projects
02-ansible/    inventories, playbooks, roles
03-python/     tools/ with tests
04-go/         CLI plugin, operator
daily-labs/    one scenario a day, solved in all four tools with shared golden tests
capstones/     one end-to-end project per tool, written up like a design doc
notes/         GOTCHAS.md (traps I hit) · INTERVIEW-BANK.md (Q&A for revision)
templates/     session-note.md · project-readme.md
.github/       CI: shellcheck (more linters added per phase)
```

## How I learn and record (every session)

1. Write the code myself first, then compare it with a reference.
2. Copy `templates/session-note.md` next to the code and fill it in during the same session.
3. Add traps to `notes/GOTCHAS.md` and questions to `notes/INTERVIEW-BANK.md`.
4. Commit the code and the note together: `git commit -m "bash W1 Wed: while-read ping checker"`
5. **Saturday, 15 min:** answer the week's interview questions aloud without notes and tag the weak ones `#revise`.

## Progress

| Phase | Status | Highlights |
|---|---|---|
| 01 Bash | 🟡 in progress (W1) | `ping_check.sh` |
| 02 Ansible | ⚪ planned | |
| 03 Python | ⚪ planned | |
| 04 Go | ⚪ planned | |

## Index

| Phase | Topic | Code | Note |
|---|---|---|---|
| Daily 001 | Patch-window drift check (Bash/Ansible/Python/Go) | `daily-labs/day-001-patch-window-drift-check/` | [README](daily-labs/day-001-patch-window-drift-check/README.md) |
| Daily 002 | Disk-full triage: deleted-but-open files, safe cleanup (Bash/Ansible/Python/Go) | `daily-labs/day-002-disk-full-triage/` | [README](daily-labs/day-002-disk-full-triage/README.md) |
| Daily 003 | Pod triage: CrashLoop vs OOMKilled vs ImagePull (Bash/Ansible/Python/Go) | `daily-labs/day-003-pod-triage/` | [README](daily-labs/day-003-pod-triage/README.md) |
| Daily 004 | Pending pod root-cause classifier: capacity vs taint vs selector vs PVC (Bash/Ansible/Python/Go) | `daily-labs/day-004-pending-pod-classifier/` | [README](daily-labs/day-004-pending-pod-classifier/README.md) |
| Daily 005 | Namespace onboarding check: quota, limits, RBAC, NetworkPolicy vs one spec (Bash/Ansible/Python/Go) | `daily-labs/day-005-namespace-onboarding-check/` | [README](daily-labs/day-005-namespace-onboarding-check/README.md) |
| Daily 006 | TLS certificate expiry audit: expired, expiring, renewed-but-not-reloaded (Bash/Ansible/Python/Go) | `daily-labs/day-006-tls-cert-expiry-audit/` | [README](daily-labs/day-006-tls-cert-expiry-audit/README.md) |
| Bash W1 Wed | for / while / while read | `01-bash/week01-foundations/ping_check.sh` | [wed-loops.md](01-bash/week01-foundations/wed-loops.md) |

---

## Related repositories

Part of one hands-on platform-engineering portfolio. Start at my [GitHub profile](https://github.com/NookireddiDileep) for the overview.

| Repo | What's there |
|---|---|
| [kubernetes-labs](https://github.com/NookireddiDileep/kubernetes-labs) | Upstream Kubernetes on a K3s cluster I built myself: 13 labs, 9 break-and-fix scenarios, timed incident drills |
| [openshift-lab-records](https://github.com/NookireddiDileep/openshift-lab-records) | OpenShift 4.x on multi-node clusters: Jira-style stories, RCAs, break-fix runbooks, `oc` command bank, OpenShift vs Kubernetes guide |
| [project-arohan-ai](https://github.com/NookireddiDileep/project-arohan-ai) | AI infrastructure track: Linux internals, OpenShift vs Kubernetes, GPU scheduling, inference reliability, Prometheus SLOs |
