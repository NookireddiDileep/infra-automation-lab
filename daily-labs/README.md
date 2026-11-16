# Daily Labs: one scenario, four tools

Every day there's one realistic infra scenario (RHEL ops → OpenShift/Kubernetes → GPU/AI infra), solved **end to end in Bash, Ansible, Python and Go** against one shared contract with golden test files. CI runs all four (`make test` in each day folder).

**Plan:** [CURRICULUM.md](CURRICULUM.md): Linux ops → Kubernetes/OpenShift → GPU & HPC ops → AI platform architecture.

**How to use a day:** read the scenario → run all four → do the "Break it" exercises → rewrite the **focus-language** version yourself in `mine/` without looking, until it passes the same golden tests → add traps to `notes/GOTCHAS.md` and Q&A to `notes/INTERVIEW-BANK.md`.

| Day | Date | Scenario | Stage | Reference (B·A·P·G) | Mine |
|---|---|---|---|---|---|
| [001](day-001-patch-window-drift-check/) | 2026-11-16 | Patch-window drift check: what vanished after the reboot? | 1 · Linux ops | ✅✅✅✅ | ⬜ Bash |
| [002](day-002-disk-full-triage/) | 2026-11-16 | Disk-full triage: `df` says full, `du` disagrees (deleted-but-open files, safe cleanup) | 1 · Linux ops | ✅✅✅✅ | ⬜ Bash |

### Folder convention
```
day-NNN-<slug>/
  README.md     scenario · contract · tool comparison · run · break it · interview points
  samples/      input fixtures + expected/ golden outputs (the contract)
  bash/  ansible/  python/  go/     one complete solution each, with tests
  mine/         MY rewrite of the focus language (proof of skill)
  Makefile      make test → runs all four
```

> Reference solutions are AI-assisted (see commit co-author lines). The `mine/` folders are my own rewrites against the same tests.
