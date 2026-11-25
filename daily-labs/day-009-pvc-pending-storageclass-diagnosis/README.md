# Day 009: PVC Pending / StorageClass binding-mode diagnosis (why is my claim Pending, and is that even a bug?)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Kubernetes / OpenShift Day-2 · Stage 2 · Focus language: **Bash** · Difficulty: ⭐⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Wednesday 10:15. A team says "our database pod is stuck, the PVC is `Pending`". `oc get pvc -A` shows a handful of `Pending` claims, and they are **not all the same problem**:

- One has no pod yet. With `volumeBindingMode: WaitForFirstConsumer` that is **normal**: the volume is created only after the scheduler picks a node, so the claim waits for its first pod.
- One names a StorageClass that does not exist (a typo, or a class that only exists on the other cluster).
- One uses a class whose provisioner is `kubernetes.io/no-provisioner` (local volumes): nothing will ever create a PV, someone must pre-create one.
- One is on an `Immediate` class with a real CSI provisioner and still `Pending`: the provisioner is failing (backend quota, dead controller pod).
- One has a `volume.kubernetes.io/selected-node` annotation: the scheduler **chose a node**, but the provisioner could not create the volume there (zone or topology mismatch).
- One is `Lost`: the PV it was bound to is gone.
- And two traps at the **class** level: **two default StorageClasses**, and a **local class bound `Immediate`** (the volume binds before scheduling and strands the pod on the wrong node).

**Goal:** one pass over a saved `oc get pvc -A -o json` and a saved `oc get storageclass -o json`, answering for every claim: *broken, expected, or fine?* — with the reason and the first thing to look at, and a `RESULT` line with an exit code a pipeline can gate on (for example before a migration or an upgrade: see Day 021).

Fixtures: `ocp-prod` (6 CRIT + 3 WARN + 1 INFO on 10 claims: every class above), `ocp-lab` (no default StorageClass, so a claim without a class is `NOSC`), `ocp-stage` (WARN + INFO only: a local class bound `Immediate` but unused, plus a claim legitimately waiting for its consumer), `ocp-dev` (clean).

## 2. The contract (every implementation must follow it)

**Usage**: `pvcbind PVCS_JSON STORAGECLASSES_JSON`. Both are `kind: List` documents (`items[]`). The report name is the PVC file name without `.pvcs.json`.

- `PVCS_JSON`: items with `kind: PersistentVolumeClaim` are judged, other kinds are ignored. Read: `metadata.namespace`, `metadata.name`, `metadata.annotations["volume.kubernetes.io/selected-node"]`, `spec.storageClassName`, `spec.volumeName`, `status.phase`.
- `STORAGECLASSES_JSON`: items with `kind: StorageClass`. Read: `metadata.name`, `provisioner` (`-` if not a string), `volumeBindingMode` (absent or `null` means `Immediate`), and the annotation `storageclass.kubernetes.io/is-default-class` (a default only when it is the string `"true"`).

Refuse with exit **3** (and print nothing on stdout): unreadable file, not JSON, not a `List`, a claim without non-empty `metadata.namespace` and `metadata.name`, a claim whose `status.phase` is not `Pending|Bound|Lost`, a claim whose `spec.storageClassName` is present but neither a string nor `null`, a `StorageClass` without a non-empty name, or a `volumeBindingMode` other than `Immediate|WaitForFirstConsumer`.

**Default class**: if several classes are marked default, the one with the lowest name (byte order) is used for claims that have no `storageClassName`. **A missing (or `null`) `storageClassName` and an empty string `""` are different things**: missing means "use the default"; `""` means "no dynamic provisioning, bind a pre-created PV only".

**Checks.** Only `Pending` and `Lost` claims produce a finding (a `Bound` claim is never flagged, even if its class vanished later). First match wins, one finding per claim:

| Class | Rule | Sev |
|---|---|---|
| `LOST` | phase `Lost` (detail `volume=NAME`) | CRIT |
| `NOSC` | Pending, `storageClassName` missing and there is no default class | CRIT |
| `NOPV` | Pending and `storageClassName` is `""`, **or** the class's provisioner is `kubernetes.io/no-provisioner` with `Immediate` binding | CRIT |
| `UNKNOWNSC` | Pending and the effective class is not in the class list | CRIT |
| `SELECTEDSTUCK` | Pending, class is `WaitForFirstConsumer` and the `selected-node` annotation is a non-empty string | CRIT |
| `PROVISIONFAILED` | Pending, class is `Immediate` with a dynamic provisioner | CRIT |
| `WAITFORCONSUMER` | Pending, class is `WaitForFirstConsumer`, no `selected-node` | INFO |

Class-level findings, emitted after all claims, per class sorted by name, `MULTIDEFAULT` before `LOCALIMMEDIATE`:

| Class | Rule | Sev |
|---|---|---|
| `MULTIDEFAULT` | more than one default class (one row per default class) | WARN |
| `LOCALIMMEDIATE` | provisioner `kubernetes.io/no-provisioner` and mode is not `WaitForFirstConsumer` | WARN |

**Report** (stdout). Claims are judged in `namespace/name` byte order. Findings are sorted by severity (CRIT, WARN, INFO), then subject (`ns/name` or `sc/NAME`, byte order), then emission order:

```
pvc-binding report: NAME
CRIT payments/ledger-0 UNKNOWNSC class=gold-ssd
CRIT shop/data-1 SELECTEDSTUCK class=gp3-csi node=node-02.example.com provisioner=ebs.csi.aws.com
WARN sc/local-fast LOCALIMMEDIATE provisioner=kubernetes.io/no-provisioner mode=Immediate
INFO shop/data-0 WAITFORCONSUMER class=gp3-csi mode=WaitForFirstConsumer via=default
HINT UNKNOWNSC: ...
RESULT CRIT crit=6 warn=3 info=1 pvcs=10 healthy=3
```

Details: `class=-` prints for a missing or empty class name; ` via=default` is appended when the class came from the default; `provisioner=` and `volume=` print `-` when missing. One `HINT CLASS:` line per class present, in the order of the tables above (claim classes first). `pvcs` counts all claims, `healthy` the claims with no finding. **INFO never changes the verdict.** Exit codes: `0` OK (INFO allowed) · `1` WARN only · `2` any CRIT · `3` input error.

Golden files: [`samples/expected/`](samples/expected/). Inputs: [`samples/*.pvcs.json`](samples/) and [`samples/*.storageclasses.json`](samples/) (sanitised: `node-NN.example.com`, invented namespaces).

## 3. Four implementations: what each tool is *for*

| | Bash + jq | Ansible | Python | Go |
|---|---|---|---|---|
| Shape | one `jq` program: a `name → {mode, prov, default}` map of classes, one `judge` function per claim, class rows appended | one Jinja template builds a JSON list of findings (a `namespace` as accumulator, one macro to append a row), one `sort(attribute='rank,subject,idx')`, a second template renders | pure functions (`parse_classes`, `judge_pvc`, `check`) + `--json` view | typed helpers over decoded JSON; **one goroutine per claim**, each writing only its own result slot |
| Strength | same `oc ... \| jq` you type by hand; runs on any bastion | `collect_cluster.yml` snapshots the live cluster with `kubernetes.core.k8s_info`, so collect and judge live together | easiest to unit-test the decision table (missing vs `""` class, Immediate vs WFFC) | static binary; judging thousands of claims concurrently; the operators' language |
| Weak spot | the `judge` function is a long `if/elif` chain in `jq` | Jinja has no early return: the chain is nested `if`s | needs a Python on the box | verbose around untyped JSON |
| Exit codes | native 0/1/2/3 | cannot return 1/2: `fail_on_crit` or read `RESULT` | native | native |

`--json` exists in Python and Go (the text report is the contract). The Ansible check uses `ansible.builtin` only, so it runs offline; [`collect_cluster.yml`](ansible/collect_cluster.yml) needs a cluster and `kubernetes.core` and is **not** part of `make test`.

## 4. Run it

```bash
cd daily-labs/day-009-pvc-pending-storageclass-diagnosis
make test                                   # all four (ansible is skipped if not installed)

bash/pvcbind.sh samples/ocp-prod.pvcs.json samples/ocp-prod.storageclasses.json; echo "exit=$?"   # 2
python3 python/pvcbind.py --json samples/ocp-stage.pvcs.json samples/ocp-stage.storageclasses.json
(cd go && go run . ../samples/ocp-dev.pvcs.json ../samples/ocp-dev.storageclasses.json)             # 0
(cd ansible && ansible-playbook pvcbind.yml \
   -e pvcs_file=../samples/ocp-prod.pvcs.json -e scs_file=../samples/ocp-prod.storageclasses.json)
```

## 5. Run it for real

**OpenShift training lab / any cluster** (read-only):

```bash
oc get pvc -A -o json > /tmp/mycluster.pvcs.json
oc get storageclass -o json > /tmp/mycluster.storageclasses.json
bash/pvcbind.sh /tmp/mycluster.pvcs.json /tmp/mycluster.storageclasses.json
# the evidence behind a finding
oc describe pvc -n <ns> <name>            # events: "waiting for first consumer", ProvisioningFailed ...
oc get pv                                  # for NOPV: does a matching Available PV exist?
oc get pods -n openshift-cluster-csi-drivers   # for PROVISIONFAILED: is the CSI controller running?
```

The report works from the **spec and status** only; the events in `oc describe pvc` are the proof that confirms it (and a saved file never ages, which is why this check uses no clock).

**GCP VM or laptop with kind** (AROHAN clusters work too): kind ships the `standard` class (`rancher.io/local-path`, `WaitForFirstConsumer`), which is enough to see the INFO case, the `UNKNOWNSC` case and the local-class trap.

```bash
kind create cluster --name pvc
kubectl create ns demo
cat <<'YAML' | kubectl -n demo apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: waits}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}      # default class, no pod: INFO
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: typo}
spec: {storageClassName: gold-ssd, accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}   # UNKNOWNSC
YAML
kubectl get pvc -A -o json > /tmp/kind.pvcs.json && kubectl get sc -o json > /tmp/kind.storageclasses.json
bash/pvcbind.sh /tmp/kind.pvcs.json /tmp/kind.storageclasses.json
```

Then create a pod that mounts `waits` and run it again: the claim becomes `Bound` and the INFO line disappears.

## 6. Break it (do these yourself, then record what you saw)

1. In `ocp-prod.pvcs.json` delete `"storageClassName": ""` from `logging/es-1`. Which finding does it become (it will use the default class), and why is "missing" a very different request from `""`?
2. Remove the `volume.kubernetes.io/selected-node` annotation from `shop/data-1`. It changes from CRIT to INFO. Why is that the same claim, the same class, and a completely different diagnosis?
3. Set `volumeBindingMode` of `local-fast` to `WaitForFirstConsumer`. Which two findings change (the class WARN and the `prom-0` claim), and what is a claim on a local class waiting for now?
4. Remove the default annotation from `gp3-csi`. `MULTIDEFAULT` disappears, and `shop/data-0` (no class, so it takes the default) turns from INFO into CRIT. Which class does it get now, and why is "which default wins" a bad thing to leave to chance?
5. Change a phase to `"Maybe"`. Exit must be 3 with an empty stdout. Why refuse here instead of treating it as healthy?
6. Rename the class of the Bound claim `payments/audit-0` to `ghost`. Nothing is reported. Is that correct? What would break the day somebody deletes the pod and the PV is rebound?

## 7. Architect's lens: 1,000 claims, 50 teams

- **Binding mode is a topology decision, not a detail.** `Immediate` creates the volume *before* the pod is scheduled, so for zonal storage (EBS, PD, local disks) the volume may land in a zone where the pod can never run. `WaitForFirstConsumer` lets the scheduler pick the node first, and the provisioner honours it. On cloud and OpenShift the default classes are WFFC for exactly this reason: make that the platform baseline and flag any `Immediate` + zonal class.
- **Expected is not the same as healthy.** `WAITFORCONSUMER` is INFO because a gate that fails on it would train teams to ignore the gate. The alert that matters is *"Pending for N minutes **and** a pod is waiting for it"* (that is the pod-side `FailedScheduling` from Day 004), which needs both objects and a clock: this lab's snapshot is the shape of that join.
- **One default class, owned by the platform.** Two defaults give "arbitrary" behaviour that changes with creation order. Enforce it with a policy (Kyverno / ValidatingAdmissionPolicy) and treat a missing default as a conscious choice (some multi-tenant clusters want teams to name a class).
- **Static PVs are a process, not a feature.** `no-provisioner` classes mean a human pre-creates PVs; at scale that is an inventory problem (free PVs per node per size). Prefer a local-volume provisioner (LVMS / TopoLVM / local static provisioner) so the cluster creates them.
- **Know what you cannot see.** From claims and classes alone you cannot tell *why* a CSI provisioner fails (backend quota, credentials, throttling). The report routes you to the right place (`oc describe pvc`, the driver pods, the array's own logs); do not pretend to diagnose the backend.
- **Close the loop.** Quotas per class (`<class>.storageclass.storage.k8s.io/requests.storage` in `ResourceQuota`, see Day 005/007), `allowVolumeExpansion` set deliberately, VolumeSnapshotClasses next to each class, and capacity alerts on the backend, so Pending is the exception.

## 8. Interview talking points

- **"A PVC is Pending. Walk me through it."** `oc describe pvc` first: the event tells you which of three worlds you are in. "waiting for first consumer" is normal WFFC, `ProvisioningFailed` is a provisioner/backend problem, "storageclass not found" is a typo. Then: does a pod reference the claim, which node was selected (annotation), is the CSI controller healthy, is there backend quota.
- **"`Immediate` vs `WaitForFirstConsumer`?"** Immediate provisions and binds as soon as the claim is created, ignoring where the pod will run. WFFC delays binding until a pod using the claim is scheduled, so the volume is created in the pod's zone and respects node selectors and affinity. Use WFFC for any topology-constrained storage.
- **"What happens if `storageClassName` is empty vs absent?"** Absent: the `DefaultStorageClass` admission plugin fills in the default class (if there is one). `""`: explicitly no class, so only a PV with no class can bind; dynamic provisioning is off. This is one of the most common reasons a "default storage" claim hangs.
- **"Why is a local-volume StorageClass `Immediate` dangerous?"** The PVC binds to a PV on some node before the pod is scheduled; if that node cannot run the pod (resources, taints), the pod is stuck forever with a "volume node affinity conflict". `no-provisioner` classes should always be WFFC.
- **"Two default StorageClasses?"** Older versions rejected the claim, newer ones pick the most recently created one. Either way it is an accident: keep one default and enforce it.

## 9. What I'd add for production

- Join with pods: a claim is only a problem when a **pod is waiting for it** (`FailedScheduling: ... unbound immediate PersistentVolumeClaims`) for more than N minutes; add the pod name and age to the finding.
- Read `oc get pv` too: report `Released`/`Failed` PVs, orphaned `Available` PVs and PVs whose node affinity points at a node that no longer exists.
- Check `allowVolumeExpansion`, `reclaimPolicy: Delete` on classes holding production data, and per-class quota usage; flag classes with no `VolumeSnapshotClass`.
- Per-driver hints (ODF/Ceph RBD, EBS, vSphere): which controller pod and which log line to read for `ProvisioningFailed`.

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`pvcbind.sh`** yourself in `mine/bash/pvcbind.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
mine/bash/test.sh
```

Start with `ocp-dev` (exit 0), then `ocp-lab` (the missing default), `ocp-stage` (INFO must not change the verdict), then `ocp-prod`. Do the input checks last. Hint: build the `class name → {mode, prov, default}` map first and print it; then write `judge` as one `if/elif` chain in the order of the table, with `first match wins`.

*Friday-style 30-minute prompt:* "A team's StatefulSet is stuck at 2/3 replicas. `data-2` is `Pending` on an `Immediate` class backed by zonal disks. Using only `oc get pvc,pod,sc,pv -o json`, say what you check, in what order, and what you tell the team."
