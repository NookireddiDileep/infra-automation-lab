# Day 005: Namespace onboarding conformance check (is the tenant really fenced in?)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Kubernetes / OpenShift Day-2 · Stage 2 · Focus language: **Bash** · Difficulty: ⭐⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Thursday 11:00. A new team asks for a namespace. Onboarding is "four YAML files and a Slack message": a **ResourceQuota** so they can't eat the cluster, a **LimitRange** so pods without limits get sane defaults, two **RoleBindings** (team admins, team viewers) and a **default-deny NetworkPolicy** plus an allow for the router. Six months later nobody knows which namespaces still match that spec. One quota was bumped "temporarily", a contractor still holds `cluster-admin` in `ledger`, the deny-all policy got an ingress rule during an incident, and one namespace was never created at all.

Onboarding is a *desired-state* problem, so the useful tool has two halves: apply the spec (Ansible, `apply_onboarding.yml`), and **prove the cluster still matches it** (this lab, read-only, runs in CI or a CronJob).

**Goal:** one pass over a saved `oc get ns,resourcequota,limitrange,rolebinding,networkpolicy -A -o json` plus the onboarding spec, answering per namespace:

1. Is anything **MISSING** (object never applied or deleted)?
2. Has anything **DRIFT**ed from the tier's values (quota, default limits, RBAC subjects/role, deny-all policy)?
3. Is there an **EXTRA** `cluster-admin` binding inside the namespace?
4. Which findings are **CRIT** (security or unbounded) and which are **WARN** (cosmetic or operational), plus a `RESULT` line with an exit code a pipeline can gate on.

Fixtures: `ocp-prod` (5 CRIT + 3 WARN across 6 namespaces: a missing namespace, a leaked `cluster-admin`, a hollowed-out deny-all, an unbounded quota, a wrong RBAC group), `ocp-stage` (WARN only), `ocp-dev` (clean; extra ConfigMaps, an extra `view` binding and a `cluster-admin` binding in a namespace **outside** the spec must not be flagged).

## 2. The contract (every implementation must follow it)

**Inputs:** `SPEC.json` and `NAME.cluster.json` (`kind: List` with `items[]`; other kinds are ignored). The report name is the cluster file name without `.cluster.json`.

```json
{"namespaces": [{"name": "payments", "tier": "medium", "admin_group": "payments-admins", "viewer_group": "payments-viewers"}]}
```

Spec problems (not an object, empty `namespaces`, a missing/empty field, unknown tier), an unreadable file, bad JSON or a snapshot that isn't a `List` → exit 3.

**Tiers** (fixed in the contract):

| tier | `requests.cpu` | `requests.memory` | `pods` | default limit cpu | default limit memory |
|---|---|---|---|---|---|
| small | `4` | `8Gi` | `20` | `500m` | `512Mi` |
| medium | `16` | `32Gi` | `60` | `1` | `1Gi` |
| large | `64` | `128Gi` | `200` | `2` | `2Gi` |

**Quantities** are compared after normalising (cpu → millicores, memory → bytes, pods → integer), so `16` = `16000m` and `32Gi` = `32768Mi`. Valid: cpu `N`, `N.NNN` (≤ 3 decimals) or `Nm`; memory `N` plus optional `Ki|Mi|Gi|Ti`; pods `N`. **Anything else, including a JSON number instead of a string, is invalid and never equal**, so it reports drift instead of silently passing.

**Checks per spec namespace** (a namespace is identified by its `Namespace` object; objects of other namespaces are never looked at). Findings are emitted in this order:

| # | Check | Finding | Sev |
|---|---|---|---|
| 0 | no `Namespace/NAME` | `MISSING Namespace/NAME`, then **stop** for this namespace | CRIT |
| 1 | `ResourceQuota/onboarding-quota` | absent → `MISSING`; per key `requests.cpu`, `requests.memory`, `pods`: key absent → `DRIFT … key want=W have=-` (unbounded); different → `DRIFT … key want=W have=H` | CRIT / absent key CRIT, different WARN |
| 2 | `LimitRange/onboarding-limits` | absent → `MISSING`; first `type: Container` item, `default.cpu` then `default.memory`: absent or different → `DRIFT` | WARN |
| 3 | `RoleBinding/onboarding-admin` (role `admin`, `Group` = `admin_group`) and `onboarding-view` (role `view`, `viewer_group`) | absent → `MISSING`; `roleRef want=R have=X`; no `Group` subject with that name → `subjects want=Group:G have=Kind:name,…` (`-` if none) | CRIT |
| 4 | any `RoleBinding` in the namespace whose `roleRef.name` is `cluster-admin` | `EXTRA RoleBinding/NAME cluster-admin to Kind:name,…` | CRIT |
| 5 | `NetworkPolicy/default-deny-ingress` | absent → `MISSING`; must have empty/absent `podSelector`, no `ingress` rules **and** `Ingress` in `policyTypes`, else `DRIFT … not deny-all` | CRIT |
| 6 | `NetworkPolicy/allow-from-openshift-ingress` | absent → `MISSING` (existence only) | WARN |

**Output order:** CRIT before WARN, then namespace (byte order), then emission order above. `HINT CLASS:` line per class present (`DRIFT`, `EXTRA`, `MISSING`, byte order). **Exit codes:** `0` OK · `1` WARN · `2` CRIT · `3` usage / unreadable / invalid input. `RESULT … namespaces=K clean=J` counts spec namespaces and those with no finding.

```
onboardcheck report: ocp-prod
CRIT checkout MISSING Namespace/checkout
CRIT ledger EXTRA RoleBinding/temp-debug cluster-admin to User:ops-contractor-01,Group:ledger-debuggers
CRIT ledger DRIFT NetworkPolicy/default-deny-ingress not deny-all
CRIT ml-platform DRIFT ResourceQuota/onboarding-quota requests.cpu want=64 have=-
CRIT reports DRIFT RoleBinding/onboarding-admin subjects want=Group:reports-admins have=Group:platform-all-staff
WARN ml-platform DRIFT ResourceQuota/onboarding-quota requests.memory want=128Gi have=256Gi
...
RESULT CRIT crit=5 warn=3 namespaces=6 clean=2
```

Golden files: [`samples/expected/`](samples/expected/). Inputs: [`samples/*.spec.json`](samples/) and [`samples/*.cluster.json`](samples/) (sanitised: `example.com`-style names, invented namespaces and groups).

## 3. Four implementations: what each tool is *for*

| | Bash + jq | Ansible | Python | Go |
|---|---|---|---|---|
| Shape | one `jq` program: `def`s for quantities, a generator per check, TSV out, `sort -s` | one Jinja template builds a JSON list of findings (macros for quantities), two stable `sort` passes, a second template renders | pure functions per check (`qty`, `check_namespace`) + `--json` view | typed helpers over decoded JSON; **one goroutine per namespace** writing to its own result slot |
| Strength | same `oc ... \| jq` you type by hand; runs on any bastion | same tool *applies* the spec (`apply_onboarding.yml`, `kubernetes.core.k8s`), so apply and verify live together | easiest to unit-test rules and quantity edge cases | static binary; checks 1,000 namespaces concurrently; the language of operators |
| Weak spot | quantity math and nested generators get dense (a missing `( )` silently runs a check twice) | Jinja is awkward for logic: macros return strings, not objects | needs a Python on the box | verbose around untyped JSON |
| Exit codes | native 0/1/2/3 | cannot return 1/2: `fail_on_crit` or read `RESULT` | native | native |

`--json` exists in Python only (the text report is the contract). The Ansible check uses `ansible.builtin` only, so it runs offline. Two extra playbooks need a cluster and `kubernetes.core` and are **not** part of `make test`: [`apply_onboarding.yml`](ansible/apply_onboarding.yml) (spec → six objects per namespace) and [`collect_cluster.yml`](ansible/collect_cluster.yml) (live snapshot).

## 4. Run it

```bash
cd daily-labs/day-005-namespace-onboarding-check
make test                                   # all four (ansible is skipped if not installed)

bash/onboardcheck.sh samples/ocp-prod.spec.json samples/ocp-prod.cluster.json; echo "exit=$?"   # 2
python3 python/onboardcheck.py --json samples/ocp-stage.spec.json samples/ocp-stage.cluster.json
(cd go && go run . ../samples/ocp-dev.spec.json ../samples/ocp-dev.cluster.json)                  # 0
(cd ansible && ansible-playbook onboard_check.yml \
   -e spec_file=../samples/ocp-prod.spec.json -e cluster_file=../samples/ocp-prod.cluster.json)
```

## 5. Run it for real

**OpenShift training lab / any cluster** (read-only):

```bash
oc get ns,resourcequota,limitrange,rolebinding,networkpolicy -A -o json > /tmp/mycluster.cluster.json
# write /tmp/mycluster.spec.json with your namespaces, then:
bash/onboardcheck.sh /tmp/mycluster.spec.json /tmp/mycluster.cluster.json
```

**GCP VM or laptop with kind** (AROHAN clusters work too): `kind` ships a CNI that does **not** enforce NetworkPolicy, so the check passes while the fence does nothing: install Calico/Cilium before you trust the result.

```bash
kind create cluster --name onboard
cd ansible && ansible-galaxy collection install kubernetes.core
ansible-playbook apply_onboarding.yml -e spec_file=../samples/ocp-dev.spec.json     # sandbox, demo, ci
cd .. && kubectl get ns,resourcequota,limitrange,rolebinding,networkpolicy -A -o json > /tmp/kind.cluster.json
bash/onboardcheck.sh samples/ocp-dev.spec.json /tmp/kind.cluster.json                 # expect exit 0
kubectl -n demo edit resourcequota onboarding-quota                                   # change pods, re-run: WARN drift
kubectl -n demo create rolebinding temp --clusterrole=cluster-admin --user=someone    # re-run: CRIT EXTRA
```

## 6. Break it (do these yourself, then record what you saw)

1. Change `payments` quota `requests.cpu` from `16000m` to `16.5`. Why does the unit change alone not alert, but this does?
2. Delete `requests.memory` from a quota instead of changing it. Why is a *missing* key CRIT while a *different* value is WARN? (Think `LimitRange`-less pods with no ceiling.)
3. Add `ingress: [{}]` to `default-deny-ingress`. Does the policy still *exist*? Why does an empty ingress rule mean "allow everything"?
4. Bind `cluster-admin` to a `ServiceAccount` in a spec namespace. What does the `EXTRA` line show? What would you do for a legitimate operator ServiceAccount (an allow-list)?
5. Add `"tier": "huge"` to the spec. It must exit 3 with a message, not produce a half report. Why is "bad spec → refuse" different from "bad cluster state → report"?
6. Set a quota value to the JSON number `16` instead of `"16"`. All four must call it drift. Why is "unknown → loud, not skipped" right for a conformance check?

## 7. Architect's lens: 1,000 namespaces, 50 teams

- **Spec in Git, check in the cluster.** The spec is the source of truth (GitOps: Argo CD/Flux apply it); this check is the *independent* verifier, because a controller reports "synced" even when someone added a second RoleBinding next to the managed ones. Drift detection that shares code with the applier shares its blind spots.
- **Prefer the platform to do the fencing.** On OpenShift, a **Project template** (`projectRequestTemplate`) creates quota, limits and a deny-all in every new project, so onboarding can't be forgotten. This check then catches *changes after* creation. Kyverno/Gatekeeper can block the bad edit at admission instead of reporting it later: detect, then prevent.
- **Tiers beat snowflakes.** Three tiers keep the conformance rules finite. Per-team exceptions belong in the spec (a reviewed override), never as hand-edited drift.
- **Scale.** Don't `oc get -A` everything: list per kind with a label selector (`team=...`), or read from an informer cache/kube-state-metrics (`kube_resourcequota`). Namespaces are independent, so the check shards trivially: that's why the Go version fans out one goroutine per namespace.
- **Quotas are not isolation.** `ResourceQuota` bounds *sum*; a LimitRange gives per-container defaults; NetworkPolicy fences traffic; RBAC fences the API. Four layers, four different failure modes. A tenant is fenced only if all four hold (and the CNI enforces policy).
- **Severity design.** Anything that widens blast radius (unbounded quota, `cluster-admin`, open ingress) is CRIT; cosmetic or recoverable (default limits, router allow) is WARN.

## 8. Interview talking points

- **"How do you onboard a tenant on OpenShift?"** Project template or GitOps-applied bundle: Namespace/Project with team labels, ResourceQuota, LimitRange, RoleBindings to **groups** (from the IdP, never individual users), default-deny NetworkPolicy plus explicit allows (router, monitoring), optionally egress rules/EgressFirewall and a ClusterResourceQuota across a team's projects.
- **"What does a ResourceQuota do to a pod with no limits?"** If the quota covers `requests.cpu`/`limits.cpu`, the API **rejects** pods that don't set them. That is why a LimitRange with defaults is the companion object.
- **"Why `view` and `admin` as ClusterRoles bound with a RoleBinding?"** A ClusterRole bound by a RoleBinding grants its rules *only inside that namespace*; the same ClusterRole via a ClusterRoleBinding would be cluster-wide. Common confusion in audits.
- **"NetworkPolicy default-deny doesn't seem to work."** Is the CNI enforcing it (kind's default doesn't)? Is the policy selecting the pods (`podSelector: {}` = all)? Remember policies are additive allow-lists, and an empty `ingress: [{}]` allows everything.

## 9. What I'd add for production

- Run as a CronJob with a read-only ServiceAccount; export findings as metrics (`onboarding_drift{ns,class}`) and page only on CRIT older than N minutes.
- Check actual `status.hard/used` against the quota, labels/annotations (owner, cost centre), EgressFirewall, ClusterResourceQuota, and that RoleBinding groups exist in the IdP.
- An allow-list for legitimate `cluster-admin` subjects, per-namespace spec overrides with an expiry date, and `--fix` that emits the `oc apply` diff instead of applying it.
- Admission policy (Kyverno/Gatekeeper) so the CRIT classes can't be created in the first place.

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`onboardcheck.sh`** yourself in `mine/bash/onboardcheck.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
mine/bash/test.sh
```

Start with `ocp-dev` (exit 0), then the `MISSING` cases, then `DRIFT`, and do quantities last.
