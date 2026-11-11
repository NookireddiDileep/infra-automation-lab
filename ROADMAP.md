# Roadmap: Bash → Ansible → Python → Go

**Pace:** 45–60 min/day around shift work; Saturdays are for the mini project + revision. OpenShift (J2C) training stays the main track; every phase uses the J2C lab as its real-world target.
**Exit criteria:** a phase is done when I can *show* it (working code + README) and *explain* it (interview bank answered aloud), not when the weeks are over.

Dates are targets. If a week is easy (likely for Bash basics after 13 yrs of RHEL), compress it and move on.

---

## Phase 1 · Bash: 3 weeks (9–28 Nov 2026)

| Week | Focus | Mini project (Sat) |
|---|---|---|
| W1 | Variables, quoting, exit codes, `if`/`[[ ]]`, loops, `while read`, args/`getopts`, functions | `sys-health.sh`: load, memory, disks >80%, failed services |
| W2 | `grep -E`, `awk`, `sed -i.bak`, `sort/uniq/cut/xargs`, `find -exec`, `jq` | `log-analyser.sh`: errors per hour, top messages (journal + `oc logs`) |
| W3 | `set -euo pipefail`, `trap`, logging, `flock`, arrays, config files, systemd timers, shellcheck | `ocp-health.sh`: COs, nodes, non-Running pods, Pending PVCs, cert expiry |

**OCP angle every day:** rerun the exercise against the J2C cluster (`oc get ... -o json | jq`).

**Exit criteria:**
- [ ] `ocp-health.sh` passes shellcheck, uses strict mode, trap, logging, flock, and runs from a systemd timer
- [ ] I can explain: subshells and pipes, `"$@"` vs `$*`, `set -e` pitfalls, `trap EXIT`, why `while read` beats `for $(cat)`
- [ ] I can write a working one-liner pipeline (`awk`/`jq`) live in under 2 minutes

---

## Phase 2 · Ansible: 4 weeks (30 Nov – 26 Dec 2026)

| Week | Focus | Exercise / project |
|---|---|---|
| A1 | Inventory, ad-hoc commands, playbooks, modules vs `shell`, idempotency, facts, handlers | Playbook: baseline RHEL config (chrony, sshd hardening, users) |
| A2 | Variables and precedence, Jinja2 templates, loops/conditionals, `block/rescue`, tags | **RHEL patching playbook:** pre-check → snapshot/backup hook → `dnf update` → reboot → post-check → report (my real T-Systems job, automated) |
| A3 | Roles, collections, `ansible-vault`, `ansible-lint`, Molecule (or `--check --diff`) | Turn the patching playbook into a role with defaults, vault and lint clean |
| A4 | `kubernetes.core` / `redhat.openshift` collections | **OCP onboarding role:** namespace + quota + limitrange + RBAC + NetworkPolicy from one vars file |

**Exit criteria:**
- [ ] Two roles, lint clean, idempotent (second run reports `changed=0`)
- [ ] I can explain: variable precedence (top layers), `serial`/rolling updates, handlers, `delegate_to`, vault, when *not* to use `shell`
- [ ] Capstone write-up: *"Automated monthly RHEL patching for N hosts"*

---

## Phase 3 · Python for ops: 5 weeks (28 Dec 2026 – 30 Jan 2027)

| Week | Focus | Exercise / project |
|---|---|---|
| P1 | Core: types, dicts/lists, functions, files, exceptions, `venv`, `pip` | Rewrite `ping_check.sh` in Python (concurrent with `ThreadPoolExecutor`) |
| P2 | `subprocess`, `argparse`, `logging`, `json`/`yaml`/`csv`, `pathlib` | Port `sys-health.sh` to a Python CLI with JSON output |
| P3 | `requests`, REST APIs, auth tokens, retries/timeouts | Call the OpenShift API directly with a token (no `oc`) |
| P4 | `kubernetes` Python client, `pytest`, packaging (`pyproject.toml`) | **`ocp-audit`:** pods not Running, restart storms, PVCs Pending, nodes under pressure → JSON/CSV/Markdown |
| P5 | GPU/AI angle: parse `nvidia-smi --query-gpu ... --format=csv` (saved sample files), Prometheus HTTP API queries | `gpu-report`: utilisation/memory/temperature per GPU, flags thresholds |

**Exit criteria:**
- [ ] `ocp-audit` installs with `pip install .`, has `--help`, logging, and pytest tests (CI runs them)
- [ ] I can explain: virtualenvs, exceptions, generators vs lists, threads vs processes for I/O, how I'd test code that calls the cluster (mocking)

---

## Phase 4 · Go for Kubernetes: 6 weeks (1 Feb – 13 Mar 2027)

| Week | Focus | Exercise / project |
|---|---|---|
| G1 | Syntax, types, structs, slices/maps, errors, modules (`go mod`) | Port `ping_check` to Go (goroutines + `sync.WaitGroup`) |
| G2 | Interfaces, packages, testing (`go test`), `cobra` CLI | CLI tool with subcommands + tests |
| G3 | Goroutines, channels, `context`, timeouts | Concurrent health checker with a timeout per host |
| G4 | `client-go`: kubeconfig, list/watch, informers | **`oc-podhealth` plugin:** `oc podhealth -n <ns>` (a binary named `oc-*` on PATH becomes an `oc` plugin) |
| G5–G6 | Kubebuilder / Operator SDK: CRD, reconcile loop, status, RBAC | **Tiny operator:** `NamespaceOnboarding` CR → creates quota, limits, RBAC (same job as the Ansible role, done the Kubernetes way) |

**Exit criteria:**
- [ ] Plugin + operator build, have tests, and run against the J2C cluster
- [ ] I can explain: the reconcile loop and idempotency, informers/caches, why operators exist, how this relates to OpenShift ClusterOperators

---

## Capstones (one per phase, in `capstones/`)

Each one is written up with `templates/project-readme.md`: problem, design, how to run, demo output, failure modes, what I'd change in production. These are the pieces of work I walk interviewers through.

1. **Bash:** `ocp-health`: cluster health report on a systemd timer
2. **Ansible:** `rhel-patching` role + `ocp-onboarding` role
3. **Python:** `ocp-audit`: tested, packaged cluster audit CLI
4. **Go:** `oc-podhealth` plugin + `namespace-onboarding` operator

## Rules for a public repo
- No real company hostnames, IPs, customer names, tokens or kubeconfigs. Use `example.com`, `10.0.0.x`, `node-01`.
- Interview stories can name the *type* of work ("enterprise private cloud, ~N RHEL hosts"), not the client.
- `.gitignore` covers kubeconfig, `.vault_pass`, `.env` and `venv/`.
