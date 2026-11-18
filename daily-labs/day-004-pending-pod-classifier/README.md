# Day 004: Pending pod root-cause classifier (why won't the scheduler place it?)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Kubernetes / OpenShift Day-2 · Stage 2 · Focus language: **Bash** · Difficulty: ⭐⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Wednesday 10:20. A data scientist: "my training pod has been Pending since last night". `oc get pods` says `Pending` and nothing else. The answer is in the `PodScheduled` condition, as one long scheduler sentence:

`0/12 nodes are available: 6 Insufficient nvidia.com/gpu, 3 node(s) had untolerated taint {node-role.kubernetes.io/infra: }, 3 node(s) didn't match Pod's node affinity/selector. preemption: 0/12 nodes are available: 12 Preemption is not helpful for scheduling.`

Read it wrong and you buy GPUs when the real fix is a toleration, or add a toleration when the PVC is the blocker. **Pending is a family of different problems**: some fix themselves (capacity, a node coming back from maintenance), some never will without a human (bad `nodeSelector`, unbindable PVC, missing toleration).

**Goal:** one read-only pass over `oc get pods -A -o json` that answers, per Pending pod:

1. What is the **dominant** reason (the one blocking the most nodes), and what else is blocking?
2. Is it **CRIT** (won't schedule until someone changes the spec, storage or taints) or **WARN** (capacity, maintenance or a queue: may clear on its own)?
3. For capacity, **which resource** is short (`need=`: cpu, memory, `nvidia.com/gpu`, pods)?
4. What is the **first command** to run per class (HINT lines), and one `RESULT` line with an exit code a pipeline can gate on.

Fixtures: `ocp-prod` (3 CRIT + 6 WARN, 12 nodes), `ocp-stage` (WARN only), `ocp-dev` (clean: a pod that is scheduled but still `ContainerCreating` and a `Succeeded` Job pod must **not** be flagged).

## 2. The contract (every implementation must follow it)

**Input:** `oc get pods -A -o json` (or `kubectl`), `kind: List` or `PodList`, saved as `NAME.pods.json`. Anything else → exit 3.

**Which pods:** `status.phase == Pending` **and** a condition `type=PodScheduled, status=False` (first one). Scheduled-but-not-running pods (image pull, container creation) are Day 003's job and are ignored.

**Parsing the condition:**

1. `reason == SchedulingGated` → class `Gated`, `nodes=-`, `causes=-` (Kueue/quota gate: nothing to fix on the pod).
2. Else the message must match `^0/N nodes are available: REST`; if not (or REST has no clauses) → class `Unparsed`, `nodes=-`, `causes=-`.
3. Cut REST at the first `". "` (that drops the `preemption: ...` tail) and one trailing `"."`. Split on `", "` into clauses. A clause `K text` counts `K` nodes; a clause **without a leading number** (`pod has unbound immediate PersistentVolumeClaims`) applies to all `N` nodes.
4. Classify each clause's text. **First matching rule wins** (order is part of the contract):

| # | Clause text contains | Class | Severity |
|---|---|---|---|
| 1 | `Insufficient ` or `Too many pods` | `Capacity` | WARN |
| 2 | (case-insensitive) `persistentvolume`, `unbound` or `volume node affinity` | `Storage` | CRIT |
| 3 | `taint` | `Taint` | CRIT |
| 4 | `node affinity` or `node selector` | `Selector` | CRIT |
| 5 | `pod affinity`, `anti-affinity` or `topology spread` | `Placement` | WARN |
| 6 | `unschedulable` | `Cordoned` | WARN |
| 7 | `free ports` | `Ports` | WARN |
| 8 | anything else | `Other` | WARN |

5. **Aggregate per class:** `Capacity` clauses use the **max** (one node can be short of both cpu *and* memory, so counts overlap); every other class **sums** (one reason per node). `need=` = the resources of the Capacity clauses (`Insufficient X` → `X`, `Too many pods` → `pods`), byte-sorted, joined with `+`, printed only if non-empty.
6. **Dominant class** = highest count; ties break by class name (byte order). Its severity is the pod's severity. `causes=` lists all classes as `Class:count`, in that same order.

**Output order:** CRIT before WARN, then `namespace/pod` (byte order). HINT lines: one per dominant class present, byte order. Missing namespace/name print `?`. **Exit codes:** `0` OK · `1` WARN · `2` CRIT · `3` usage / unreadable / not a Pod List.

```
pendingtriage report: ocp-prod
CRIT Storage payments/ledger-db-0 nodes=12 causes=Storage:12
CRIT Selector payments/report-7d9f6b8c5-r7s9t nodes=12 causes=Selector:12
CRIT Storage platform/tools-5b7c9d8f6-d2q8w nodes=12 causes=Storage:8,Capacity:4 need=pods
WARN Gated ai/vllm-queue-0 nodes=- causes=-
WARN Placement batch/etl-sync-8c6d5f7b4-v3x9z nodes=12 causes=Placement:10,Cordoned:2
WARN Capacity ml/trainer-0 nodes=12 causes=Capacity:6,Selector:3,Taint:3 need=nvidia.com/gpu
...
RESULT CRIT crit=3 warn=6 pods=13
```

Golden files: [`samples/expected/`](samples/expected/). Inputs: [`samples/*.pods.json`](samples/) (sanitised: `example.com`, `node-NN`, invented namespaces).

## 3. Four implementations: what each tool is *for*

| | Bash + jq | Ansible | Python | Go |
|---|---|---|---|---|
| Shape | one `jq` program: `capture` the message, classify clauses, `group_by` class | Jinja template parses with `regex_search`, `groupby`, stable two-pass `sort`; second template renders | `re` + small pure functions (`classify`, `parse_message`) | `regexp`, typed structs, goroutine-per-cluster test |
| Strength | runs on any bastion with `jq`; same shape as the `oc ... \| jq` you type by hand | fits a pipeline (`fail_on_crit`), lives next to patching/onboarding plays | easiest to unit-test the rules; has an extra `--json` view | one static binary, the language of `client-go` controllers |
| Weak spot | regex + aggregation in jq gets dense | string parsing in Jinja is painful; slow on big lists | needs a Python on the box | verbose |
| Exit codes | native 0/1/2/3 | cannot return 1/2: use `fail_on_crit` or read `RESULT` | native | native |

`--json` exists in Python only (the text report is the contract). Ansible uses `ansible.builtin` only, so it runs offline; [`ansible/collect_cluster.yml`](ansible/collect_cluster.yml) shows the live snapshot with `kubernetes.core.k8s_info` and a `status.phase=Pending` field selector (not part of `make test`, needs a cluster and the collection).

## 4. Run it

```bash
cd daily-labs/day-004-pending-pod-classifier
make test                                   # all four (ansible is skipped if not installed)

bash/pendingtriage.sh samples/ocp-prod.pods.json; echo "exit=$?"      # 2
python3 python/pendingtriage.py --json samples/ocp-stage.pods.json     # structured findings
(cd go && go run . ../samples/ocp-dev.pods.json)                        # 0
(cd ansible && ansible-playbook pending_triage.yml -e pods_file=../samples/ocp-prod.pods.json)
```

## 5. Run it for real

**OpenShift training lab / any cluster** (read-only):

```bash
oc get pods -A --field-selector=status.phase=Pending -o json > /tmp/mycluster.pods.json
bash/pendingtriage.sh /tmp/mycluster.pods.json
```

**GCP VM or laptop with kind** (AROHAN clusters work too):

```bash
kind create cluster --name pending
kubectl run toobig --image=registry.example.com/none:1 --overrides='{"spec":{"containers":[{"name":"toobig","image":"registry.example.com/none:1","resources":{"requests":{"cpu":"64"}}}]}}'   # Capacity: Insufficient cpu
kubectl run nosel --image=registry.example.com/none:1 --overrides='{"spec":{"nodeSelector":{"disktype":"nvme"}}}'   # Selector
kubectl get pods -A --field-selector=status.phase=Pending -o json > /tmp/kind.pods.json && bash/pendingtriage.sh /tmp/kind.pods.json
```

Expect `Capacity need=cpu` (WARN) and `Selector` (CRIT). The messages are the scheduler's real wording, so the same classes appear on a real cluster. Message wording changes between Kubernetes versions: that's what the `Unparsed`/`Other` buckets are for.

## 6. Break it (do these yourself, then record what you saw)

1. In `ocp-prod`, change `trainer-0` so Capacity is `3` and Taint is `6`. Which class and severity does the pod get now, and why does a taint beat a GPU shortage?
2. Make the tie `3 Insufficient cpu, 3 ... untolerated taint`. Who wins and why is a deterministic tie-break (class name) required for byte-identical output?
3. Add `5 Insufficient cpu, 6 Insufficient memory` on a 6-node cluster. Why is `max` (6) right and a sum (11) impossible?
4. Delete the `preemption: ...` tail from one message, then add a trailing newline inside another. Which implementations still agree, and which edge would you test next?
5. Set a Pending pod's `PodScheduled` condition to `True`. It must vanish from the report. Whose problem is it now (Day 003)?
6. Change a message to `Failed to schedule: ...`. It must be `Unparsed` (WARN) and not crash or silently disappear. Why is "unknown → loud, not skipped" the right default for a health check?

## 7. Architect's lens: 1,000 nodes / 10,000 GPUs

- **Don't pull everything.** Use the field selector above, or a watch/informer cache, or `kube_pod_status_unschedulable` + `kube_pod_status_scheduled` from kube-state-metrics. Pending is also a *rate* problem: alert on pending **age** (add `lastTransitionTime`), not on existence, or every normal autoscaler scale-up pages someone.
- **Scheduler messages are an API with no contract.** Wording changes across versions and plugins. Match short, stable phrases, keep an `Other`/`Unparsed` bucket, and put a golden test per supported Kubernetes/OpenShift version.
- **Capacity is not one number.** `Insufficient nvidia.com/gpu` on 6 of 12 nodes may be fragmentation (free GPUs exist, but never 8 on one node), not shortage. That is Stage 3 (GPU fragmentation report).
- **`Gated` is policy, not failure.** With Kueue/quota admission the pod is *meant* to wait. A platform should surface queue position and quota, not page.
- **Autoscaler interaction.** Cluster Autoscaler only reacts to `Unschedulable` capacity reasons. A pod blocked by `Selector`/`Taint` will never trigger a scale-up, which is exactly why the CRIT/WARN split matters.

## 8. Interview talking points

- **"A pod is Pending. Walk me through it."** `oc describe pod`: events and the `PodScheduled` condition → read the per-node reason counts → Insufficient X: compare requests with `oc describe node` allocatable/allocated (requests, not usage, drive scheduling) → taint/selector/affinity: fix the spec or the node labels → PVC: `oc get pvc,sc`, binding mode, zone → only then add capacity.
- **"Why 'Insufficient cpu' when the node is idle?"** The scheduler uses **requests**, not live usage. Over-requested pods block placement while the CPU sits idle (links to the right-sizing lab).
- **"WaitForFirstConsumer vs Immediate?"** Immediate binds the PV before scheduling and can pick a zone the pod can't use (`volume node affinity conflict`). WaitForFirstConsumer delays binding until the pod is placed. Prefer it for zonal storage.
- **"What is preemption's role in the message?"** After filtering fails, the scheduler tries to evict lower-priority pods. `Preemption is not helpful` means evicting wouldn't help, so the blocker isn't capacity-shaped.

## 9. What I'd add for production

- Pending **age** from `lastTransitionTime` with severity thresholds, plus owner resolution (Deployment/Job) so the report names the workload.
- Join with node data (`oc get nodes -o json`) to say *which* nodes fail and why a GPU request can't fit (fragmentation).
- Pull the PVC/StorageClass for `Storage` cases and the queue/quota for `Gated` (Kueue `Workload`).
- Run as a CronJob with a read-only ServiceAccount, dedupe by workload, and page only on CRIT older than N minutes.

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`pendingtriage.sh`** yourself in `mine/bash/pendingtriage.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
mine/bash/test.sh
```

Rules: `set -Eeuo pipefail`, `LC_ALL=C`, an ERR trap, shellcheck-clean. Start with a single clause class on `ocp-stage` (`Capacity` with `need=`), then the count-less clause and the `". "` cut, then aggregation (max vs sum), then CRIT/WARN and exit codes. Commit with a 5-line note: what you got wrong and what you learned.

**Interview round (solve it in 30 min):** you get `ocp-prod.pods.json` and one question: "A GPU team says three jobs are stuck. Which of these Pending pods can wait for the autoscaler and which need a human today?" Defend the CRIT/WARN split out loud.
