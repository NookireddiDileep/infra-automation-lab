# Day 003: Pod triage (CrashLoopBackOff, OOMKilled, ImagePullBackOff, evictions)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Kubernetes / OpenShift Day-2 · Stage 2 · Focus language: **Bash** · Difficulty: ⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Tuesday 09:05. The shop team pings: "checkout is flaky". `oc get pods -A | grep -v Running` shows a wall of states, and `STATUS` lies by omission: it says `CrashLoopBackOff` for *both* an app that exits 1 on a bad config **and** an app the kernel OOM-killed. The fixes are opposite (read the log vs. raise the memory limit), and the evidence is in `lastState`, which `oc get pods` doesn't print.

**Goal:** one read-only pass over `oc get pods -A -o json` that answers, in order:

1. Which pods are broken **right now** (CRIT) and which are *recovering but unhealthy* (WARN)?
2. For each one, **why**: OOM (with the limit, so you know what to raise), crash exit code, bad image reference, missing Secret/ConfigMap, node eviction, or a restart storm.
3. What is the **first command** to run per class (HINT lines)?
4. A single `RESULT` line and an exit code a pipeline or an on-call script can gate on.

The fixtures are three clusters: `ocp-prod` (5 CRIT + 3 WARN), `ocp-stage` (WARN only) and `ocp-dev` (clean, with a pod at 1 restart and a pod at 4 restarts that must *not* be flagged).

## 2. The contract (every implementation must follow it)

**Input:** the JSON from `oc get pods -A -o json` (or `kubectl`), `kind: List` or `PodList`, saved to `NAME.pods.json`. Anything else → exit 3.

**Rules** (default `-r 5`). Per pod, then per container status. **The first matching rule wins** (the order is part of the contract):

| # | Condition | Class | Severity | Detail |
|---|---|---|---|---|
| 0 | pod `phase=Failed` and `status.reason=Evicted` | `Evicted` | WARN | `- node=<spec.nodeName>` (no container) |
| – | phase is not `Running`/`Pending` (e.g. `Succeeded` Job pods) | ignored | | |
| 1 | `state.waiting.reason` is `ImagePullBackOff` or `ErrImagePull` | `ImagePullBackOff` | CRIT | `image=<status image>` |
| 2 | waiting `CreateContainerConfigError` / `CreateContainerError` | `ContainerConfigError` | CRIT | `reason=<waiting reason>` |
| 3 | `lastState.terminated.reason` or `state.terminated.reason` is `OOMKilled` | `OOMKilled` | **CRIT if currently down** (waiting or terminated), **WARN if running again** | `restarts=N limit=<limits.memory or none>` |
| 4 | waiting `CrashLoopBackOff` | `CrashLoopBackOff` | CRIT | `restarts=N exit=<lastState exitCode or ?>` |
| 5 | `restartCount >= -r` | `HighRestarts` | WARN | `restarts=N` |

Missing `namespace`/`name`/`node`/`image` print as `?`. **Order:** CRIT before WARN, then `namespace/pod` (byte order), then container. HINT lines follow, one per class present, in byte order. **Exit codes:** `0` OK · `1` WARN · `2` CRIT · `3` usage / unreadable / not a Pod List.

```
podtriage report: ocp-prod
CRIT ImagePullBackOff payments/batch-ledger-58d4f7c6b-p8s2d batch image=registry.example.com/payments/batch:latest
CRIT ContainerConfigError payments/gateway-9b7c5d6f4-h6v9c gateway reason=CreateContainerConfigError
CRIT ImagePullBackOff payments/ledger-7f6d8b9c4-m3n8v ledger image=registry.example.com/payments/ledger:v2.4.1
CRIT CrashLoopBackOff shop/api-5d7b9c4f8-q9w2z api restarts=12 exit=1
CRIT OOMKilled shop/cart-6c8f7d9b5-x2k4p cart restarts=7 limit=256Mi
WARN Evicted batch/etl-nightly-28391-zq7rt - node=node-02
WARN HighRestarts monitoring/node-exporter-4tq7m node-exporter restarts=9
WARN OOMKilled shop/web-85c9d7f6b-t5h7j web restarts=2 limit=512Mi
HINT ...one per class...
RESULT CRIT crit=5 warn=3 pods=12
```

Golden files: [`samples/expected/`](samples/expected/). Inputs: [`samples/*.pods.json`](samples/) (sanitised: `example.com`, `node-NN`, invented namespaces).

## 3. Four implementations: what each tool is *for*

| | Bash + jq | Ansible | Python | Go |
|---|---|---|---|---|
| Shape | one `jq` program emits TSV, `sort` orders, shell counts | Jinja template emits TSV rows, `sort` filter orders, second template renders | dict walking, one tested `classify_container()` | typed structs, `sort.Slice`, table + golden tests |
| Strength | runs on any bastion that has `jq`; reads like the `oc ... \| jq` you type by hand | fits a pipeline: gate with `fail_on_crit`, same file next to patching/onboarding plays | easiest to unit-test rules and extend (`--json`, Prometheus) | one static binary, the language of `client-go` operators |
| Weak spot | rule logic in jq is dense; `jq` is a dependency | Jinja is a poor language for branching logic; slow on large lists | needs a Python on the box | verbose JSON handling for sparse fields |
| Exit codes | native 0/1/2/3 | cannot return 1/2: use `fail_on_crit` or read `RESULT` | native | native |

Ansible uses `ansible.builtin` only, so it runs offline. [`ansible/collect_cluster.yml`](ansible/collect_cluster.yml) shows the live version with `kubernetes.core.k8s_info` (not part of `make test`: needs a cluster and the collection, and is untested in CI).

## 4. Run it

```bash
cd daily-labs/day-003-pod-triage
make test                                   # all four (ansible is skipped if not installed)

bash/podtriage.sh samples/ocp-prod.pods.json; echo "exit=$?"     # 2
python3 python/podtriage.py -r 3 samples/ocp-stage.pods.json     # lower the restart threshold
(cd go && go run . ../samples/ocp-dev.pods.json)                  # 0
(cd ansible && ansible-playbook pod_triage.yml -e pods_file=../samples/ocp-prod.pods.json)   # writes ansible/reports/*.report
```

## 5. Run it for real

**OpenShift training lab / any cluster** (read-only, needs `get pods` cluster-wide):

```bash
oc get pods -A -o json > /tmp/mycluster.pods.json
bash/podtriage.sh /tmp/mycluster.pods.json
```

**GCP VM or laptop with kind** (the AROHAN clusters work too):

```bash
kind create cluster --name triage
kubectl run oom --image=busybox --restart=Always --overrides='{"spec":{"containers":[{"name":"oom","image":"busybox","command":["sh","-c","tail /dev/zero"],"resources":{"limits":{"memory":"20Mi"}}}]}}'
kubectl run badimg --image=registry.example.com/does/not-exist:1
sleep 60; kubectl get pods -A -o json > /tmp/kind.pods.json && bash/podtriage.sh /tmp/kind.pods.json
```

Expect `OOMKilled` (limit=20Mi) and `ImagePullBackOff`. The fixtures were shaped from real output, so the same classes appear.

## 6. Break it (do these yourself, then record what you saw)

1. Set `-r 3` on `ocp-stage`: which new line appears, and why is `db-0` in `ocp-prod` (4 restarts) still clean at the default?
2. Edit `ocp-prod.pods.json`: remove `limits.memory` from `cart`. What does the detail say? Why is `limit=none` more useful than hiding the field?
3. Change `web`'s `state` from `running` to `waiting: CrashLoopBackOff`. Which severity does the OOM line get now, and why is "currently down" the deciding signal and not the restart count?
4. Add a `Succeeded` pod with `restartCount: 99`. Why must it be ignored? (A completed Job pod is not an incident.)
5. Feed it `oc get pod x -o json` (a single `kind: Pod`). It must exit 3 with a clear error, not print `RESULT OK`. Why is a silent OK the worst failure of a health check?
6. Truncate the JSON mid-file (`head -c 500`). Do all four languages exit 3?

## 7. Architect's lens: 1,000 nodes / 10,000 GPUs

- **Don't pull everything.** `get pods -A -o json` on a 5,000-pod cluster is tens of MB and hits the API server's etcd path. Use `--field-selector=status.phase!=Succeeded`, or better: consume the **watch/informer** cache (a Go controller) or `kube-state-metrics` series (`kube_pod_container_status_last_terminated_reason`, `..._waiting_reason`) and compute these classes in Prometheus rules.
- **Pagination.** Large lists need `--chunk-size`; a snapshot taken while pods churn is not atomic, so treat findings as hints and re-check before acting.
- **GPU pods change the OOM story.** `OOMKilled` is *host* memory; GPU memory exhaustion shows up as an app exit code and CUDA error in logs, not `OOMKilled`. Keep a separate class for it (Stage 3).
- **Severity is policy, not truth.** Here "OOM but running again" is WARN. A platform team would add `owner`/priority class so a CRIT in `payments` pages and one in `dev-sandbox` doesn't.
- **Idempotent output + exit codes** make this safe as a pre-change gate (Day 21 builds the upgrade gate on the same pattern).

## 8. Interview talking points

- **"A pod is in CrashLoopBackOff. Walk me through it."** `oc describe pod` (events, `lastState`, exit code) → `oc logs -p` (previous container) → exit code: 1 app error, 137 SIGKILL (OOM or liveness kill), 139 segfault, 143 SIGTERM, 127 command not found → probes, config, dependencies → only then change anything.
- **"OOMKilled vs. CrashLoopBackOff?"** `CrashLoopBackOff` is the kubelet's *back-off state*, not a cause. The cause is in `lastState.terminated.reason`. `OOMKilled` = the container cgroup hit `limits.memory`; compare with `oc adm top pod` and the working set, then raise the limit or fix the leak.
- **"ImagePullBackOff?"** Wrong tag/digest, missing or wrong `imagePullSecret`, registry unreachable or rate-limited, mirror/`ImageContentSourcePolicy` (OpenShift disconnected) not covering the image. `oc describe pod` events say which.
- **"Why `Evicted` pods linger?"** Kubelet evicts under node pressure (memory/disk/PID) and leaves the Failed pod as a record. They don't self-heal: clean them up, fix requests/limits and ephemeral-storage, and look at the node.

## 9. What I'd add for production

- `--json` output and a `--namespace`/`--label` filter; init containers and ephemeral containers; `Pending` + `Unschedulable` (that's Day 16's classifier).
- Add **owner** resolution (`ownerReferences` → Deployment/StatefulSet) so the report says *which workload* to fix, not just the pod hash.
- Pull **events** and the last 20 log lines for each CRIT (`oc logs -p --tail=20`), redacting secrets.
- Run in-cluster as a CronJob with a read-only ServiceAccount and push the `RESULT` line to the alert channel; dedupe by workload.
- Cross-check against `ResourceQuota` and node pressure (Days 18-19) before recommending "raise the limit".

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`podtriage.sh`** yourself in `mine/bash/podtriage.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
mine/bash/test.sh
```

Rules: `set -Eeuo pipefail`, `LC_ALL=C`, an ERR trap, shellcheck-clean. Start with only the `ImagePullBackOff` and `CrashLoopBackOff` rules on `ocp-prod`, then add `OOMKilled` (the limit lookup in jq is the tricky part), `Evicted`, and the exit codes. Commit with a 5-line note: what you got wrong and what you learned.

**Interview round (solve it in 30 min):** you get `ocp-prod.pods.json` and one question: "Which single pod do you look at first, and what do you run?" Defend the order out loud.
