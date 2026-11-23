# Day 007: ResourceQuota right-sizing (is the quota full, or just padded?)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Kubernetes / OpenShift Day-2 · Stage 2 · Focus language: **Bash** · Difficulty: ⭐⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Monday 09:40. Two tickets arrive within ten minutes. Team `payments`: "our deploy is stuck, pods fail with `exceeded quota: team-quota, requested: requests.cpu=500m, used: requests.cpu=16, limited: requests.cpu=16`". Platform capacity review: "the cluster is 92% *reserved* but nodes sit at 35% CPU; where is the slack?"

Both are the same problem seen from two sides. A **ResourceQuota counts requests, not usage**. Teams request generously (copy-pasted `requests.cpu: "2"` everywhere), the quota fills up, deploys get rejected, and meanwhile the nodes are idle. The other direction exists too: pods that *use* more than they request are cheap on paper and first to be throttled or evicted under node pressure.

**Goal:** one pass over a saved `oc get resourcequota -A -o json` and a saved `PodMetrics` list (what `oc adm top pods` reads), answering per quota:

1. Is a quota **FULL** (new pods rejected) or **NEAR** full (a rollout surge will be rejected)?
2. Is `hard` far bigger than the use (**OVERQUOTA**: capacity reserved for nobody)?
3. Are requests far above real usage (**OVERREQUEST**) or below it (**UNDERREQUEST**)?
4. Can we even tell (**NOMETRICS**: no metrics for the namespace; **INVALID**: unparseable quota value)?

…and a `RESULT` line with an exit code a pipeline can gate on.

Fixtures: `ocp-prod` (3 CRIT + 11 WARN across 7 quotas: a full CPU quota, a full pod quota, memory used 130% of what was requested, padded requests, a namespace with no metrics, a quota with `1.5` CPU and `pods: "0"`, and a `count/configmaps` quota that is not judged), `ocp-stage` (WARN only), `ocp-dev` (clean, with every threshold **exactly on the OK side**: 84%, 30%, 120%).

## 2. The contract (every implementation must follow it)

**Usage**: `rightsize QUOTA_JSON METRICS_JSON`. Both are `kind: List` documents (`items[]`); other kinds are ignored. The report name is the quota file name without `.quota.json`.

- `QUOTA_JSON`: `ResourceQuota` items. Only `status.hard` and `status.used` are read (what the API server enforces now, not what `spec` says).
- `METRICS_JSON`: `PodMetrics` items (`oc get --raw /apis/metrics.k8s.io/v1beta1/pods`): `metadata.namespace`, `containers[].usage.{cpu,memory}`.

A file that is unreadable, not JSON, not a `List`, a `ResourceQuota` without name/namespace, a `PodMetrics` without namespace, or a container usage that is not a metrics-server quantity → exit **3** (refuse; never print a half report).

**Quantities.** Quota values: cpu `N` or `Nm` (→ millicores), memory `N` plus optional `Ki|Mi|Gi|Ti` (→ bytes), pods `N`. **Anything else, including a JSON number or `1.5`, is INVALID.** Usage values: cpu `N` plus optional `n|u|m` (nano/micro/milli, plain = cores), memory `N` plus optional `Ki|Mi|Gi`. Per namespace, CPU is summed in **nanocores and floored to millicores once**, memory is summed in bytes.

**Checks.** Per `ResourceQuota` (sorted by namespace, then name), for each key `requests.cpu`, `requests.memory`, `pods` in that order, **only if the key is in both `status.hard` and `status.used`** (other keys such as `count/*` are never judged). `pct = floor(used*100/hard)`, `eff = floor(actual*100/used)`:

| Class | Rule | Sev |
|---|---|---|
| `INVALID` | either value invalid, or `hard` = 0 (then no other check for that key) | WARN |
| `FULL` | `pct >= 100` | CRIT |
| `NEAR` | `pct >= 85` | WARN |
| `OVERQUOTA` | cpu/memory only, `used*4 <= hard` (and not FULL/NEAR) | WARN |
| `OVERREQUEST` | cpu/memory, namespace has metrics, `used > 0`, `actual*100 < used*30` | WARN |
| `UNDERREQUEST` | same, `actual*100 > used*120` | memory **CRIT**, cpu WARN |
| `NOMETRICS` | after the keys: `requests.cpu` used > 0 and the namespace has **no** PodMetrics | WARN |

`FULL`/`NEAR`/`OVERQUOTA` are mutually exclusive per key; the usage check (`OVERREQUEST`/`UNDERREQUEST`) is independent of them and follows it.

**Report** (stdout). Findings are sorted CRIT before WARN, then namespace (byte order), then emission order (quota, key, class):

```
rightsize report: NAME
CRIT ml-platform quota/gpu-quota UNDERREQUEST requests.memory requested=20Gi actual=26624Mi eff=130%
WARN ledger quota/team-quota OVERREQUEST requests.cpu requested=6 actual=420m eff=7%
...
HINT FULL: new pods are rejected (exceeded quota); find oversized requests first, then raise hard
RESULT CRIT crit=3 warn=11 quotas=7 healthy=1
```

`used`, `hard` and `requested` print exactly as in the JSON; `actual` prints as `NNNm` (CPU) or `NNNMi` (memory, floored). One `HINT CLASS:` line per class present, in the table's order. `quotas` counts all `ResourceQuota` items, `healthy` those with no finding. Exit codes: `0` OK · `1` WARN only · `2` any CRIT · `3` input error.

Golden files: [`samples/expected/`](samples/expected/). Inputs: [`samples/*.quota.json`](samples/) and [`samples/*.metrics.json`](samples/) (sanitised: invented namespaces, no real hosts or users).

## 3. Four implementations: what each tool is *for*

| | Bash + jq | Ansible | Python | Go |
|---|---|---|---|---|
| Shape | one `jq` program: `def`s for quantities, one generator per quota, sorted by `[rank, ns, idx]` | one Jinja template builds a JSON list of findings (macros for quantities, namespace objects as accumulators), one `sort(attribute='rank,ns,idx')`, a second template renders | pure functions (`qty`, `namespace_usage`, `quota_findings`) + `--json` view | typed helpers over decoded JSON; **one goroutine per quota** writing to its own result slot |
| Strength | same `oc ... \| jq` you type by hand; runs on any bastion | `collect_cluster.yml` snapshots the live cluster with `kubernetes.core.k8s_info`, so collect and judge live together | easiest to unit-test the threshold edges | static binary; thousands of quotas concurrently; the operators' language |
| Weak spot | a 90-line `jq` program is dense; doubles lose exactness above 2^53 | Jinja has no real functions: macros return strings, so numbers round-trip through text | needs a Python on the box | verbose around untyped JSON |
| Exit codes | native 0/1/2/3 | cannot return 1/2: `fail_on_crit` or read `RESULT` | native | native |

`--json` exists in Python only (the text report is the contract). The Ansible check uses `ansible.builtin` only, so it runs offline; [`collect_cluster.yml`](ansible/collect_cluster.yml) needs a cluster and `kubernetes.core` and is **not** part of `make test`.

## 4. Run it

```bash
cd daily-labs/day-007-quota-rightsizing-report
make test                                   # all four (ansible is skipped if not installed)

bash/rightsize.sh samples/ocp-prod.quota.json samples/ocp-prod.metrics.json; echo "exit=$?"   # 2
python3 python/rightsize.py --json samples/ocp-stage.quota.json samples/ocp-stage.metrics.json
(cd go && go run . ../samples/ocp-dev.quota.json ../samples/ocp-dev.metrics.json)               # 0
(cd ansible && ansible-playbook rightsize.yml \
   -e quota_file=../samples/ocp-prod.quota.json -e metrics_file=../samples/ocp-prod.metrics.json)
```

## 5. Run it for real

**OpenShift training lab / any cluster** (read-only):

```bash
oc get resourcequota -A -o json > /tmp/mycluster.quota.json
oc get --raw /apis/metrics.k8s.io/v1beta1/pods > /tmp/mycluster.metrics.json    
jq '.kind = "List"' /tmp/mycluster.metrics.json > /tmp/m && mv /tmp/m /tmp/mycluster.metrics.json
bash/rightsize.sh /tmp/mycluster.quota.json /tmp/mycluster.metrics.json
```

A raw `PodMetricsList` has `kind: PodMetricsList`, not `List`; that is why the `jq` line above rewrites it (and why the check refuses anything else loudly).

**GCP VM or laptop with kind** (AROHAN clusters work too): kind has no metrics-server, so install one first or you will see only `NOMETRICS`.

```bash
kind create cluster --name rightsize
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl -n kube-system patch deploy metrics-server --type=json -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
kubectl create ns demo && kubectl -n demo create quota team-quota --hard=requests.cpu=1,requests.memory=1Gi,pods=10
kubectl -n demo create deploy web --image=registry.k8s.io/pause:3.9 --replicas=3
kubectl -n demo set resources deploy web --requests=cpu=300m,memory=256Mi     # 900m of 1 CPU: expect NEAR, then OVERREQUEST (pause uses ~0)
```

## 6. Break it (do these yourself, then record what you saw)

1. Raise `payments` `used` memory from `28Gi` to `27Gi` in `ocp-prod.quota.json`. Which class disappears and why is `pct` floored (not rounded) for this check?
2. Change `ledger` actual CPU so `eff` is exactly 30%, then 1 millicore less. Why is the boundary inclusive on the OK side, and what would flapping around the threshold do to an alert?
3. Delete every `PodMetrics` item for `reports`. `UNDERREQUEST` disappears and `NOMETRICS` appears. Why is "no data" a finding and not silence?
4. Set `payments` `pods` hard to the JSON number `60`. All four must say `INVALID`. Why is "unknown → loud" right for a capacity check?
5. Make the metrics container usage `"cpu": "1.5"`. It must exit 3 and print nothing on stdout. Why refuse here but only warn for an invalid quota value?
6. Add a second `ResourceQuota` to `payments` (say `gpu-quota` with `requests.nvidia.com/gpu`). It is not judged. What would you change to judge extended resources, and which threshold would stop making sense?

## 7. Architect's lens: 1,000 namespaces, 50 teams

- **Quota is a reservation, not a measurement.** The scheduler places by *requests*. A cluster can be 92% reserved and 35% used: the cost is paid in unschedulable pods and in cluster-autoscaler adding nodes nobody needs. Right-sizing requests recovers that capacity without buying hardware.
- **Where the numbers should come from at scale.** Not a snapshot of `oc get -A`: use kube-state-metrics (`kube_resourcequota{type="hard|used"}`) and `container_cpu_usage_seconds_total` / `container_memory_working_set_bytes` over **days** (p95, not an instant). A 15-second `PodMetrics` window right after a deploy lies in both directions; this lab's single sample is the shape of the check, not a basis for cutting a team's quota.
- **Automate the recommendation, not the cut.** VPA in `Off`/recommendation mode, or Goldilocks, gives per-workload targets; this check ranks *namespaces* so a human talks to the right team first. Never auto-lower `requests` of a stateful workload.
- **Severity design.** `FULL` blocks deploys right now: CRIT. Memory use above request is CRIT because memory is not compressible (OOM or eviction), CPU above request is WARN because it only throttles. Waste (`OVERQUOTA`, `OVERREQUEST`) is WARN: it costs money, not availability.
- **Chargeback closes the loop.** If teams pay for *requested* resources, padding corrects itself; if capacity is free, no report will change behaviour. That is the bridge to GPU fair-share and showback in Stage 4.
- **Multi-quota and scopes.** Real clusters have `ClusterResourceQuota` (OpenShift) across a team's projects and scoped quotas (`PriorityClass`, `Terminating`). Decide whether a finding belongs to the quota or to the team before you page anyone.

## 8. Interview talking points

- **"Pods are rejected with `exceeded quota`, but the nodes are idle. Why?"** Quota and the scheduler both count **requests**. Check `oc describe quota` (used vs hard), then `oc adm top pods` vs the pods' requests; the fix is lower requests or a bigger quota, never "more nodes".
- **"What happens to a pod with no requests/limits in a quota'd namespace?"** If the quota covers `requests.cpu`/`limits.cpu`, the API server rejects it, unless a **LimitRange** supplies defaults. That is why quota and LimitRange come as a pair (Day 005).
- **"How would you right-size requests safely?"** Measure p95 over at least a week including peaks, set requests near p90 and limits with headroom (or no CPU limit), roll out gradually, watch throttling (`container_cpu_cfs_throttled_periods_total`) and OOMs. VPA in recommendation mode first.
- **"Why is memory-over-request worse than CPU-over-request?"** CPU is compressible (throttled); memory is not. A pod using more than it requested is first in line for eviction under node pressure and for the OOM killer by QoS class (`Burstable` before `Guaranteed`).
- **"Your report says `NOMETRICS` for a busy namespace."** metrics-server down or scraping failing; on OpenShift check `oc -n openshift-monitoring get pods` and `oc adm top nodes`. A right-sizing tool that treats missing metrics as zero usage would tell you to cut a quota to nothing.

## 9. What I'd add for production

- Read kube-state-metrics / Prometheus (p95 over 7–14 days) instead of one `PodMetrics` sample; export `quota_utilisation{ns,resource}` and alert on `FULL` for more than N minutes only.
- Judge `limits.*`, extended resources (`requests.nvidia.com/gpu`), `count/*` objects and scoped quotas; follow `ClusterResourceQuota` across projects.
- Per-namespace overrides (a reviewed, expiring exception list) and a `--fix` that prints the `oc patch resourcequota` diff instead of applying it.
- Join with Day 005 (does the namespace follow its tier?) so "OVERQUOTA" can say "tier `large` is wrong for this team, move to `medium`".

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`rightsize.sh`** yourself in `mine/bash/rightsize.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
mine/bash/test.sh
```

Start with `ocp-dev` (exit 0: thresholds), then `ocp-stage`, then `ocp-prod`. Do quantities and the `INVALID` path last. Hint: sketch the numbers on paper first (`pct`, `eff`), then write the `jq` generators one class at a time and diff after each.
