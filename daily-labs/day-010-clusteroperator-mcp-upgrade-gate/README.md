# Day 010: OpenShift pre-upgrade gate (ClusterOperators + MachineConfigPools: is it safe to press Upgrade?)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Kubernetes / OpenShift Day-2 · Stage 2 · Focus language: **Bash** · Difficulty: ⭐⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Thursday 16:40, change window opens at 22:00. The plan says "upgrade the cluster tonight". Before anyone starts the rollout, someone has to answer one question: **is the cluster healthy enough to be upgraded?** `oc get co` and `oc get mcp` have the answer, but across 30+ operators and several pools it is easy to read past the one line that matters:

- An operator is `Available=False` or `Degraded=True`. Upgrading on top of that turns one fault into two.
- An operator says `Upgradeable=False` (for example `AdminAckRequired` on `cloud-credential`): a **minor** upgrade will be refused or blocked.
- An operator is still `Progressing=True`: a previous rollout has not finished.
- A MachineConfigPool is `Degraded` (a node cannot apply its rendered config), `Updating`, **paused**, or has **fewer updated machines than machines while doing nothing** (stalled).
- An operator publishes **no `Available` condition at all**: it is not reporting, which is not the same as healthy.

**Goal:** one pass over a saved `oc get clusteroperators -o json` and a saved `oc get machineconfigpools -o json`, and one verdict a pipeline can gate on: `GO`, `CAUTION` or `NO-GO`, with the reason and the first command to run for each finding.

Fixtures: `ocp-prod` (5 CRIT + 2 WARN across 10 operators and 4 pools: every CRIT class and a paused pool), `ocp-lab` (an operator with no conditions, a pool with a node down outside a rollout, one progressing operator), `ocp-stage` (WARN only: a console rollout and a pool that is legitimately mid-update), `ocp-dev` (clean). All synthetic, shaped like real `oc -o json` output.

## 2. The contract (every implementation must follow it)

**Usage**: `upgradegate CLUSTEROPERATORS_JSON MCPS_JSON`. Both are `kind: List` documents (`items[]`). The report name is the first file's name without `.clusteroperators.json`.

- Operators: items with `kind: ClusterOperator`. Read `metadata.name` and `status.conditions[]` (`type`, `status`, `reason`). Missing `reason` prints `-`.
- Pools: items with `kind: MachineConfigPool`. Read `metadata.name`, `spec.paused` (only the boolean `true` counts), `status.machineCount`, `updatedMachineCount`, `readyMachineCount`, `unavailableMachineCount`, `degradedMachineCount` and the conditions `Degraded`, `Updating`.

Refuse with exit **3** (and print nothing on stdout): unreadable file, not JSON, not a `List`, an item without a non-empty `metadata.name`, `status.conditions` present but not a list, a condition whose `type` is not a string or whose `status` is not `True|False|Unknown`, or a pool count that is missing, not an integer, negative or a boolean. The first problem found wins; operators are checked before pools.

**Operator checks.** First match wins, one finding per operator:

| Class | Rule | Sev |
|---|---|---|
| `NOSTATUS` | no `Available` condition at all (`reason=-`) | CRIT |
| `UNAVAILABLE` | `Available` is not `True` (`False` **or** `Unknown`) | CRIT |
| `DEGRADED` | `Degraded=True` | CRIT |
| `BLOCKED` | `Upgradeable=False` (a missing `Upgradeable` is fine) | CRIT |
| `PROGRESSING` | `Progressing=True` | WARN |

**Pool checks.** First match wins, one finding per pool:

| Class | Rule | Sev |
|---|---|---|
| `DEGRADED` | `degradedMachineCount > 0` **or** condition `Degraded=True` | CRIT |
| `UPDATING` | condition `Updating=True` (a node is down *because* of the rollout, so no `UNAVAILABLE`) | WARN |
| `UNAVAILABLE` | `unavailableMachineCount > 0` while not updating | CRIT |
| `PAUSED` | `spec.paused` is `true` | WARN |
| `STALLED` | `updatedMachineCount < machineCount`, not updating, not paused | CRIT |

**Report** (stdout). Findings are sorted by severity (CRIT, WARN), then subject (`co/NAME` before `mcp/NAME`, byte order); the sort is stable, input order breaks ties:

```
upgrade-gate report: NAME
CRIT co/authentication DEGRADED reason=OAuthServerRouteEndpointAccessibleController_EndpointUnavailable
CRIT mcp/gpu STALLED degraded=0 unavailable=0 updated=3/4
WARN mcp/infra PAUSED degraded=0 unavailable=0 updated=2/3
HINT DEGRADED: ...
RESULT CRIT verdict=NO-GO crit=5 warn=2 operators=10 pools=4 healthy=7
```

Operator rows end in `reason=<Reason of the deciding condition>`; pool rows in `degraded=D unavailable=U updated=X/M`. One `HINT CLASS:` line per class present, in the order UNAVAILABLE, NOSTATUS, DEGRADED, BLOCKED, STALLED, PROGRESSING, UPDATING, PAUSED. `healthy` = operators + pools − findings. Verdict: any CRIT is `NO-GO`, else any WARN is `CAUTION`, else `GO`. Exit codes: `0` GO · `1` CAUTION · `2` NO-GO · `3` input error.

Golden files: [`samples/expected/`](samples/expected/). Inputs: [`samples/*.clusteroperators.json`](samples/) and [`samples/*.mcps.json`](samples/).

## 3. Four implementations: what each tool is *for*

| | Bash + jq | Ansible | Python | Go |
|---|---|---|---|---|
| Shape | one `jq` program: a condition map per item, `judge_co` and `judge_mcp` as `if/elif` chains | one Jinja template with a `namespace` accumulator builds the findings JSON; a second template renders | dataclasses + pure functions (`judge_co`, `judge_mcp`, `check`) + `--json` | typed helpers over decoded JSON; the two files are **read and parsed concurrently** (one goroutine each) |
| Strength | the same `oc ... \| jq` you type by hand; runs on any bastion | `collect_cluster.yml` snapshots the live cluster with `kubernetes.core.k8s_info`; `fail_on_crit=true` gates a pipeline | easiest to unit-test the precedence rules (`UPDATING` before `UNAVAILABLE`) | static binary; the operators' language; natural next step is `client-go` |
| Weak spot | validation and judging share one long program | Jinja has no early return and no `int` check for bool | needs a Python on the box | verbose around untyped JSON |
| Exit codes | native 0/1/2/3 | cannot return 1/2: `fail_on_crit` or read `RESULT` | native | native |

The Ansible check uses `ansible.builtin` only, so it runs offline; [`collect_cluster.yml`](ansible/collect_cluster.yml) needs a cluster and `kubernetes.core` and is **not** part of `make test`.

## 4. Run it

```bash
cd daily-labs/day-010-clusteroperator-mcp-upgrade-gate
make test                                   # all four (ansible is skipped if not installed)

bash/upgradegate.sh samples/ocp-prod.clusteroperators.json samples/ocp-prod.mcps.json; echo "exit=$?"   # 2
python3 python/upgradegate.py --json samples/ocp-stage.clusteroperators.json samples/ocp-stage.mcps.json
(cd go && go run . ../samples/ocp-dev.clusteroperators.json ../samples/ocp-dev.mcps.json)               # 0
(cd ansible && ansible-playbook upgradegate.yml \
   -e cos_file=../samples/ocp-prod.clusteroperators.json -e mcps_file=../samples/ocp-prod.mcps.json)
```

## 5. Run it for real

**OpenShift training lab / any OpenShift 4 cluster** (read-only):

```bash
oc get clusteroperators -o json > /tmp/mycluster.clusteroperators.json
oc get machineconfigpools -o json > /tmp/mycluster.mcps.json
bash/upgradegate.sh /tmp/mycluster.clusteroperators.json /tmp/mycluster.mcps.json
# the evidence behind a finding
oc describe co/<name>                         # conditions with messages
oc get clusterversion -o jsonpath='{.items[0].status.conditions}'   # Upgradeable / Failing at cluster level
oc describe mcp/<pool>; oc get nodes          # which node is behind, and its desiredConfig vs currentConfig
```

**GCP VM or kind:** `ClusterOperator` and `MachineConfigPool` only exist on OpenShift/OKD, so kind has nothing to read. Use the fixtures, or copy a saved `oc` output from the training lab. To see the logic react, edit a copy of `ocp-dev` (see "Break it").

## 6. Break it (do these yourself, then record what you saw)

1. In `ocp-dev.clusteroperators.json` set `Degraded` of `etcd` to `"True"`. Verdict and exit code? Now also set `Available` to `"False"`: which class wins and why is that the better label?
2. Delete the whole `conditions` array of one operator. Why is `NOSTATUS` CRIT and not "nothing to report"?
3. In `ocp-dev.mcps.json` set `paused: true` on `worker` and `updatedMachineCount: 1`. Why is it only WARN (`PAUSED`) and not `STALLED`? When is a paused pool deliberate, and what must you do before the upgrade?
4. Put `Updating=True` on a pool that has `unavailableMachineCount: 1`. Why does it stay WARN? Then remove `Updating` and see it turn CRIT.
5. Change a pool's `machineCount` to `"two"` or `true`. Exit must be 3 with an empty stdout. Why refuse instead of guessing?
6. Add `Upgradeable=False` to an operator and decide: is this a NO-GO for a **patch** upgrade? (Read the hint.) What would you add to the tool so it only blocks on minor-version moves?

## 7. Architect's lens: 40 clusters, one upgrade train

- **A gate is a contract, not a dashboard.** The value is one machine-readable verdict that a pipeline can refuse on, plus the reason in the same output. At fleet scale you run it from the pipeline against every cluster before each wave and publish the rows, not a screenshot.
- **Order of precedence is the design.** `UNAVAILABLE` beats `DEGRADED`; `UPDATING` beats `UNAVAILABLE` for pools. Without a fixed order, one root cause produces three alerts. The tests pin the order so nobody "improves" it by accident.
- **Expected is not healthy, and healthy is not expected.** A pool mid-rollout is WARN (wait), not CRIT. A paused pool is WARN because canary pools are a real pattern, but it silently stays behind: the hint says so. Decide your own policy per environment: in prod you may want `PAUSED` to be CRIT outside a change window.
- **Snapshot vs live.** A saved JSON never ages, which makes the test deterministic, but a real gate must run *close to* the change (minutes, not hours) because `Progressing` flips quickly. Record the snapshot time in the evidence.
- **What it cannot see.** It reads cluster-level conditions only. PodDisruptionBudgets that will block drains, deprecated APIs in use, expiring certs, etcd health and the update graph (is the target version even recommended for this channel?) are separate checks: Days 022–024.
- **Scale:** the cost is two API calls per cluster; the work is in the fleet loop (sharding clusters, per-cluster timeouts, a hard rule that "could not read" is NO-GO, never GO).

## 8. Interview talking points

- **"What do you check before an OpenShift upgrade?"** `oc get clusterversion` (Upgradeable, Failing, channel and target), all ClusterOperators Available and not Degraded or Progressing, all MachineConfigPools Updated with no degraded machines and no unexpected pause, nodes Ready, PDBs and etcd backup. Then I record the state, so I can compare after.
- **"Available, Progressing, Degraded: what do they mean?"** Available: the operand works for users. Progressing: the operator is rolling out a change right now. Degraded: the operator sees something wrong that needs attention, even if the service still works. A healthy operator is `True/False/False`; mid-upgrade `True/True/False` is normal.
- **"What does `Upgradeable=False` do?"** It blocks minor-version updates (4.15 → 4.16), not z-stream updates. Typical cause: an admin acknowledgement is required (for example removed APIs or credentials mode).
- **"What does pausing a MachineConfigPool do?"** The MachineConfigController stops rolling that pool to new rendered configs, so its nodes stay on the old config and OS image. It is the canary/worker-by-worker pattern, but an unpaused-forgotten pool is how you end up with a skewed cluster.
- **"A pool shows `updated 3/4` and nothing is happening. Now what?"** Not updating, not paused, not degraded: find the node whose `desiredConfig` differs from its `currentConfig` (annotations), read the machine-config-daemon log on it and the controller log, check for a drain blocked by a PDB.

## 9. What I'd add for production

- Read `clusterversion` (channel, target, `Upgradeable`, `Failing`) and refuse a target that is not in `availableUpdates`.
- Join with nodes (`NotReady`, cordoned) and with PDBs whose `disruptionsAllowed` is 0 (they stall drains), and with an etcd snapshot age.
- Age: a `Progressing` operator for 5 minutes is normal, for 2 hours is a stall; use `lastTransitionTime` and a clock parameter.
- Per-cluster policy file (which classes are CRIT in prod, WARN in dev), output as JUnit/Markdown for the change ticket, exit code per fleet wave.

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`upgradegate.sh`** yourself in `mine/bash/upgradegate.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
mine/bash/test.sh
```

Start with `ocp-dev` (exit 0), then `ocp-stage` (two WARN rows), then `ocp-lab`, then `ocp-prod`. Build the per-item condition map (`type → status, reason`) first and print it; then write `judge_co` as one `if/elif` chain in the order of the table. Do the input checks last. Remember `export LC_ALL=C`, or `sort` will order `co/B` and `co/b` differently from the golden files.
