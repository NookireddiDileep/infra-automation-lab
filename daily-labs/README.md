# Daily Labs: one scenario, four tools

Every day there's one realistic infra scenario (RHEL ops → OpenShift/Kubernetes → GPU/AI infra), solved **end to end in Bash, Ansible, Python and Go** against one shared contract with golden test files. CI runs all four (`make test` in each day folder).

**How to use a day:** read the scenario → run all four → do the "Break it" exercises → rewrite the Bash (current phase) yourself without looking → add traps to `notes/GOTCHAS.md` and Q&A to `notes/INTERVIEW-BANK.md`.

| Day | Date | Scenario | Track | Bash | Ansible | Python | Go |
|---|---|---|---|---|---|---|---|
| [001](day-001-patch-window-drift-check/) | 2026-11-16 | Patch-window drift check: what vanished after the reboot? | RHEL ops | ✅ | ✅ | ✅ | ✅ |

### Folder convention
```
day-NNN-<slug>/
  README.md     scenario · contract · tool comparison · run · break it · interview points
  samples/      input fixtures + expected/ golden outputs (the contract)
  bash/  ansible/  python/  go/     one complete solution each, with tests
  Makefile      make test → runs all four
```
