# Day 008: Node pressure & eviction analysis (who is under pressure, and who got evicted?)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Kubernetes / OpenShift Day-2 · Stage 2 · Focus language: **Bash** · Difficulty: ⭐⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Tuesday 08:50. The on-call hand-over says: "payments pods keep restarting on their own, nobody changed anything". `oc get pods` shows a dozen pods in `Evicted` / `Error` state, and a few that were rescheduled three times overnight. Nothing is `CrashLoopBackOff`, so Day 003 finds nothing.

Evictions are the **kubelet protecting its own node**. When `memory.available`, `nodefs.available`, `imagefs.available` or `pid.available` drops under the eviction threshold, the kubelet sets a pressure condition (`MemoryPressure`, `DiskPressure`, `PIDPressure`), the node lifecycle controller adds a `node.kubernetes.io/*-pressure:NoSchedule` taint, and the kubelet starts killing pods (BestEffort and over-request Burstable first). Hours later the condition is `False` again and the only evidence is a pile of `Evicted` events, which the API server keeps for about an hour by default.

**Goal:** one pass over a saved `oc get nodes -o json` and a saved `oc get events -A -o json`, answering per node:

1. Is the node **NOTREADY** right now (kubelet gone, everything on it will be evicted after the toleration)?
2. Is it **under pressure now** (`MEMORYPRESSURE`, `DISKPRESSURE`, `PIDPRESSURE`), and is the **taint missing** (`NOTAINT`: the scheduler still sends it pods)?
3. Did the kubelet **evict pods here** (`EVICTIONS`), or is it a **storm** (`EVICTIONSTORM`: chronic pressure)? Evictions are counted by resource (`memory`, `ephemeral-storage`, `pids`) so the fix goes to the right team.

…and a `RESULT` line with an exit code a pipeline can gate on (for example before a drain, or before an upgrade: see Day 021).

Fixtures: `ocp-prod` (5 CRIT + 3 WARN on 8 nodes: a dead kubelet, memory pressure with 6 evictions, disk pressure **without** its taint plus 2 evictions, PID pressure, a node that is healthy *now* but evicted one pod, an eviction event for a node that no longer exists, and a non-eviction event), `ocp-stage` (WARN only, with 4 evictions on one node: **one under the storm threshold**), `ocp-dev` (clean).

## 2. The contract (every implementation must follow it)

**Usage**: `nodepressure NODES_JSON EVENTS_JSON`. Both are `kind: List` documents (`items[]`). The report name is the nodes file name without `.nodes.json`.

- `NODES_JSON`: items with `kind: Node` are judged, other kinds are ignored. Read: `metadata.name`, `status.conditions[]` (`type`, `status`, `reason`, `lastTransitionTime`) for the four types `Ready`, `MemoryPressure`, `DiskPressure`, `PIDPressure` (the last condition of a type wins), `spec.taints[].key`.
- `EVENTS_JSON`: only events with `reason == "Evicted"` are read (all others are ignored, even malformed ones). Read: `source.host` (the node that evicted), `count` (default 1), `message`.

Refuse with exit **3** (and print nothing on stdout): unreadable file, not JSON, not a `List`, a `Node` without a non-empty `metadata.name`, a judged condition whose `status` is not `True|False|Unknown`, an `Evicted` event without a non-empty string `source.host`, or an `Evicted` event whose `count` is present but not a number ≥ 1 with no fraction (`0`, `1.5`, `"2"`, `null`, `true` are all refused).

**Resource** of an eviction: the first match of `low on resource: ([A-Za-z0-9-]+)\.` in `message` (the text the kubelet writes), else `unknown`. Per node, counts are summed per resource (`count` of each event) and `total` is the sum.

**Checks.** Per `Node` sorted by name (byte order), in this emission order:

| Class | Rule | Sev |
|---|---|---|
| `NOTREADY` | `Ready` is absent (`status=Missing reason=-`) or its status is not `True` | CRIT |
| `MEMORYPRESSURE` / `DISKPRESSURE` / `PIDPRESSURE` | that condition has status `True` (in this order) | CRIT |
| `NOTAINT` | emitted right after a pressure finding when `spec.taints` has no `node.kubernetes.io/{memory,disk,pid}-pressure` key | WARN |
| `EVICTIONS` | `total` of Evicted events for the node is 1–4 | WARN |
| `EVICTIONSTORM` | `total >= 5` | CRIT |

Pressure conditions that are `Unknown` are **not** reported separately: a dead kubelet turns all four conditions `Unknown`, and `NOTREADY` already says it. Evicted events whose host is not in the node list (a node that was replaced) are ignored.

**Report** (stdout). Findings are sorted CRIT before WARN, then node name (byte order), then emission order:

```
node-pressure report: NAME
CRIT node-03.example.com MEMORYPRESSURE since=2026-11-23T21:58:10Z reason=KubeletHasInsufficientMemory
CRIT node-03.example.com EVICTIONSTORM total=6 ephemeral-storage=1 memory=5
WARN node-04.example.com NOTAINT node.kubernetes.io/disk-pressure
...
HINT MEMORYPRESSURE: memory.available is under the eviction threshold: ...
RESULT CRIT crit=5 warn=3 nodes=8 healthy=3
```

`since` and `reason` print the condition's `lastTransitionTime` and `reason` exactly as in the JSON (`-` if missing or empty). Resources inside a finding are sorted by name. One `HINT CLASS:` line per class present, in the table's order. `nodes` counts all `Node` items, `healthy` those with no finding. Exit codes: `0` OK · `1` WARN only · `2` any CRIT · `3` input error.

Golden files: [`samples/expected/`](samples/expected/). Inputs: [`samples/*.nodes.json`](samples/) and [`samples/*.events.json`](samples/) (sanitised: `node-NN.example.com`, invented namespaces).

## 3. Four implementations: what each tool is *for*

| | Bash + jq | Ansible | Python | Go |
|---|---|---|---|---|
| Shape | one `jq` program: a host→resource→count map from the events, then one generator per node | one Jinja template builds a JSON list of findings (a `namespace` as accumulator, one macro to append a row), one `sort(attribute='rank,node,idx')`, a second template renders | pure functions (`evictions_by_host`, `node_findings`, `check`) + `--json` view | typed helpers over decoded JSON; **one goroutine per node**, each writing only its own result slot |
| Strength | same `oc ... \| jq` you type by hand; runs on any bastion | `collect_cluster.yml` snapshots the live cluster with `kubernetes.core.k8s_info`, so collect and judge live together | easiest to unit-test the threshold edges (4 vs 5 evictions) | static binary; judging thousands of nodes concurrently; the operators' language |
| Weak spot | a 70-line `jq` program is dense | Jinja has no real functions; counters must live in a `namespace` | needs a Python on the box | verbose around untyped JSON |
| Exit codes | native 0/1/2/3 | cannot return 1/2: `fail_on_crit` or read `RESULT` | native | native |

`--json` exists in Python only (the text report is the contract). The Ansible check uses `ansible.builtin` only, so it runs offline; [`collect_cluster.yml`](ansible/collect_cluster.yml) needs a cluster and `kubernetes.core` and is **not** part of `make test`.

## 4. Run it

```bash
cd daily-labs/day-008-node-pressure-eviction-analysis
make test                                   # all four (ansible is skipped if not installed)

bash/nodepressure.sh samples/ocp-prod.nodes.json samples/ocp-prod.events.json; echo "exit=$?"   # 2
python3 python/nodepressure.py --json samples/ocp-stage.nodes.json samples/ocp-stage.events.json
(cd go && go run . ../samples/ocp-dev.nodes.json ../samples/ocp-dev.events.json)                 # 0
(cd ansible && ansible-playbook nodepressure.yml \
   -e nodes_file=../samples/ocp-prod.nodes.json -e events_file=../samples/ocp-prod.events.json)
```

## 5. Run it for real

**OpenShift training lab / any cluster** (read-only):

```bash
oc get nodes -o json > /tmp/mycluster.nodes.json
oc get events -A --field-selector reason=Evicted -o json > /tmp/mycluster.events.json
bash/nodepressure.sh /tmp/mycluster.nodes.json /tmp/mycluster.events.json
# what the kubelet itself thinks (on the node): thresholds and what it reserves
oc debug node/<node> -- chroot /host sh -c 'grep -A8 -E "evictionHard|systemReserved" /etc/kubernetes/kubelet.conf'
```

Events live about an hour by default (`--event-ttl`), so run this **soon after** the incident or ship events to a log store. An empty events file is a valid input, it just means "no evidence".

**GCP VM or laptop with kind** (AROHAN clusters work too): make a node run out of memory on purpose.

```bash
kind create cluster --name pressure
kubectl create ns demo
# a pod with no limit that allocates until the node's memory.available is under the threshold
kubectl -n demo run hog --image=polinux/stress --restart=Never --command -- stress --vm 1 --vm-bytes 2G --vm-hang 0
kubectl get nodes -o json > /tmp/kind.nodes.json
kubectl get events -A --field-selector reason=Evicted -o json > /tmp/kind.events.json
```

kind shares the host's memory and has no real eviction signal for the container, so you may need `--vm-bytes` close to your VM's free RAM, or lower `evictionHard` in a kubeadm patch. On a small GCP VM with a real kubelet the effect is easy to reproduce.

## 6. Break it (do these yourself, then record what you saw)

1. In `ocp-prod.events.json` change the first `node-03` event `count` from `4` to `3`. The total drops from 6 to 5: still CRIT. Now delete the `ephemeral-storage` event too (total 4). What changes, and why is the threshold on the *sum* of `count`, not on the number of events?
2. Remove the `node.kubernetes.io/memory-pressure` taint from `node-03`. Which finding appears, and what would the scheduler do with a new pod?
3. Set `node-06` `MemoryPressure` to `True`. Explain why the report now shows `MEMORYPRESSURE` **and** `NOTAINT` **and** the old eviction line, in that order.
4. Change `"status": "False"` to `"status": "Maybe"` in any condition. Exit must be 3 with an empty stdout. Why refuse here instead of treating it as "healthy"?
5. Empty `ocp-prod.events.json` (`"items": []`). `node-03` is still in `MEMORYPRESSURE`, but the evictions are gone. Why must "no events" never be read as "no evictions happened"?
6. Rename a message so it no longer contains `low on resource:`. Which resource does the report show, and why print `unknown` instead of dropping the event?

## 7. Architect's lens: 1,000 nodes, 50 teams

- **Conditions are a snapshot, evictions are the history.** The condition tells you what is true *now*; a node that flaps around the threshold looks healthy 90% of the time. At scale, alert on `kube_node_status_condition{condition=~".*Pressure",status="true"}` held for N minutes, **and** on the *rate* of `kubelet_evictions` (`kubelet_evictions{eviction_signal=...}`), not on a point-in-time `oc get`.
- **Events are a poor store.** Default TTL is one hour and the API server drops them under load. For fleet analysis ship events to Loki/Elasticsearch or use the kube-state-metrics + node-exporter series; this lab's file is the *shape* of the check.
- **Fix the cause at the owner.** Evictions are decided by usage above request, so the actionable output is "which namespace's pods were the victims, and what were their requests?". This check names the **resource** so the right team is called: memory evictions go to the app owners (requests/limits), `ephemeral-storage` to whoever writes logs or `emptyDir`s, `pids` to whoever forks.
- **Protect the node, not just the pods.** The kubelet needs `systemReserved`/`kubeReserved` and a sane `evictionHard`/`evictionSoft`; on OpenShift they are set through a `KubeletConfig` per MachineConfigPool (`autoSizingReserved` is the modern default). A node with no reservation fails *before* the kubelet can evict, and then you see `NOTREADY`, not `EVICTIONS`.
- **Severity design.** A dead kubelet and a True pressure condition are CRIT: the node is already hurting workloads. A missing taint is WARN but dangerous: it means new pods keep arriving. Evictions on a healthy node are WARN (it recovered), but five or more is a pattern: CRIT.
- **Close the loop.** The remediation is not "evict faster": it is requests that match reality (Day 007), `LimitRange` defaults (Day 005), PriorityClasses so critical pods are evicted last, and cluster autoscaling so pressure resolves by capacity. For GPU nodes, the same shape applies to `nvidia-smi`/DCGM signals in Stage 3.

## 8. Interview talking points

- **"Pods were evicted overnight, but nothing is wrong now. Where do you look?"** `oc get events -A --field-selector reason=Evicted` (soon, events expire), the pod's `status.message` ("The node was low on resource: memory..."), `oc describe node` conditions and `Allocated resources`, then kubelet logs (`oc adm node-logs <node> -u kubelet | grep -i evict`) and Prometheus for `node_memory_MemAvailable_bytes` around the time.
- **"What order does the kubelet evict in?"** Pods whose usage exceeds requests first, then by Priority, then by how far above request they are. In QoS terms: `BestEffort` and over-request `Burstable` before `Guaranteed`. `Guaranteed` pods can still be evicted if system daemons starve the node.
- **"Evicted vs OOMKilled?"** `OOMKilled` is the **kernel** killing a container that hit its cgroup memory limit (or the node ran out): the pod restarts in place. `Evicted` is the **kubelet** killing the whole pod to relieve node pressure: the pod is Failed and a controller creates a new one elsewhere.
- **"Why does `NotReady` evict pods after five minutes?"** The node lifecycle controller adds `unreachable`/`not-ready` taints; the default `tolerationSeconds: 300` that admission injects expires, then the taint manager evicts. Tune per workload, never cluster-wide.
- **"A node is `MemoryPressure=True` but takes new pods."** The pressure taint is missing or the pod tolerates it (daemonsets do). Check `spec.taints` and the pod's tolerations; this report flags it as `NOTAINT`.

## 9. What I'd add for production

- Read Prometheus (condition held ≥ 5 min, `kubelet_evictions` rate, `node_memory_MemAvailable_bytes`/`node_filesystem_avail_bytes` trends) instead of one snapshot, and keep an eviction history beyond the event TTL.
- Name the **victims**: parse `Container X was using 2Gi, request is 512Mi` and rank namespaces by "usage above request at eviction time".
- Judge `PodDisruptionBudget`-blocked drains and cordon state (`spec.unschedulable`), and join with the Day 021/022 pre-upgrade gates.
- Print the `KubeletConfig` diff (reserved CPU/memory, `evictionHard`) per MachineConfigPool instead of only a hint.

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`nodepressure.sh`** yourself in `mine/bash/nodepressure.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
mine/bash/test.sh
```

Start with `ocp-dev` (exit 0), then `ocp-stage` (the 4-vs-5 boundary), then `ocp-prod`. Do the input checks last. Hint: build the `host → resource → count` map first and print it; the per-node `jq` generator is then three small `select`s.

