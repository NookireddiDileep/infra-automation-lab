# Daily Labs Curriculum: from RHEL ops to AI infrastructure architect

**North star:** a top-1% workload-management engineer and **AI infrastructure architect**, someone who can run, debug, automate and *design* large GPU clusters (Kubernetes/OpenShift, HPC, Linux) and prove it in code.

Every lab is solved in **Bash, Ansible, Python and Go**. The **scenario track** climbs the stack toward AI infra. The **language focus** follows [ROADMAP.md](../ROADMAP.md): the focus language is the one I rewrite myself in `mine/`.

Every scenario uses **offline fixtures** (saved `oc`/`kubectl` JSON, `nvidia-smi`/`dcgmi`/`ibstat` output, logs), so it runs anywhere with no cluster or GPU. Each README also has a **"Run it for real"** section for the OpenShift training lab, a GCP VM or the AROHAN kind clusters.

---

## The ladder (scenario tracks)

| Stage | Dates (target) | Scenario track | Focus language | Why it matters for AI infra |
|---|---|---|---|---|
| **1** | 16 Nov – 29 Nov 2026 | **Linux / RHEL fleet ops** | Bash | GPU nodes are Linux boxes first. Most "GPU incidents" are kernel, driver, disk, NTP or network problems. |
| **2** | 30 Nov – 27 Dec 2026 | **Kubernetes / OpenShift Day-2** | Ansible | The control plane for AI workloads. This is also the job-switch target. |
| **3** | 28 Dec 2026 – 31 Jan 2027 | **GPU & HPC operations** | Python | Driver/CUDA drift, XID errors, MIG, NCCL, InfiniBand, DCGM: the actual AI infra ops work. |
| **4** | 1 Feb – 14 Mar 2027 | **AI platform architecture & SRE** | Go | Capacity, scheduling, fair-share, cost, topology, closed-loop remediation: the *architect* layer. |
| **5** | from 15 Mar 2027 | **Mixed / interview mode** | rotate | Harder, multi-step incidents across all layers, timed like system-design and troubleshooting rounds. |

**Weekly rhythm (one lab every day):**
- **Mon–Fri:** a stage scenario (incident, audit or automation)
- **Sat:** an **architecture scenario**. Same four tools, but the README adds a design section: how this works at 1,000 nodes / 10,000 GPUs, the trade-offs, and failure domains.
- **Sun:** an **interview-mode scenario**. Shorter, with a "solve it in 30 min" prompt, plus revision of the week's GOTCHAS and INTERVIEW-BANK.

---

## Scenario backlog (picked in order; can change)

### Stage 1: Linux / RHEL fleet ops
1. ✅ Patch-window drift check (pre/post reboot)
2. ✅ Disk-full triage: top consumers, deleted-but-open files (`/proc/*/fd`), safe log cleanup
3. TLS certificate expiry audit across endpoints and files
4. Time sync drift (chrony offset/stratum) across a fleet. Clock skew breaks distributed training and etcd.
5. systemd flapping services: restart storms from journal
6. Failed SSH / sudo audit from `secure` logs (security ops)
7. LVM/XFS capacity forecast: days-to-full from usage history
8. Kernel parameter & sysctl compliance (GPU-node tuning baseline: hugepages, `vm.swappiness`, NUMA)
9. NFS/remote mount health & stale-handle detection (shared datasets and checkpoints)
10. Journald error-burst analysis per hour/unit
11. RPM package drift between "identical" nodes (golden image compliance)
12. Backup verification report (job logs → missed/failed/oversized)
13. NIC / bonding / MTU consistency check (jumbo frames for storage and RDMA networks)
14. **Sat · architecture:** fleet health-check design for 1,000 nodes (push vs pull, sharding, timeouts)

### Stage 2: Kubernetes / OpenShift Day-2
15. Pod triage report: CrashLoopBackOff, OOMKilled, ImagePullBackOff with reasons
16. Pending pod root-cause classifier (FailedScheduling reason parser; links to AROHAN Lab 03)
17. Namespace onboarding: quota + limitrange + RBAC + NetworkPolicy from one spec
18. ResourceQuota / requests-vs-usage right-sizing report
19. Node pressure & eviction analysis (memory/disk/PID pressure)
20. PVC Pending / StorageClass binding-mode diagnosis
21. OpenShift ClusterOperator & MachineConfigPool health gate (pre-upgrade check)
22. Cluster upgrade pre-flight: deprecated APIs, PDBs blocking drains, unhealthy nodes
23. Certificate expiry inside the cluster (secrets, kubelet, ingress)
24. etcd backup verification & defrag decision (DB size, leader changes)
25. RBAC audit: who can `*` on what (cluster-admin sprawl)
26. Image registry hygiene: tags, digests, unsigned images, old images
27. Drain & cordon orchestrator with PDB awareness (rolling node maintenance)
28. **Sat · architecture:** multi-tenant cluster design (quotas, priority classes, isolation)

### Stage 3: GPU & HPC operations
29. GPU fleet inventory from `nvidia-smi -q` / CSV: model, driver, CUDA, ECC, persistence mode
30. Driver / CUDA / firmware version drift across GPU nodes (and against a compatibility matrix)
31. XID error triage from kernel logs: classify (app vs hardware vs needs-reset) and decide the action
32. Idle / under-utilised GPU detection: cost-waste report from DCGM metrics
33. GPU health gate before scheduling: ECC errors, retired pages, thermals, PCIe link width/speed
34. MIG layout planner: given inference demand, pick MIG profiles per A100/H100
35. GPU fragmentation report (free GPUs per node vs pending pod sizes; links to AROHAN Lab 03)
36. InfiniBand / RDMA link health from `ibstat`/`ibdiagnet`-style output (rate, state, errors)
37. NCCL all-reduce test results parser: bus bandwidth vs expected, find the slow node
38. Slurm vs Kubernetes job accounting: queue wait, GPU-hours by team
39. Model storage throughput check: time to load weights vs SLO (cold-start budget)
40. Inference SLO check: TTFT / tokens-per-sec percentiles from logs (links to AROHAN Lab 02)
41. GPU node cordon-and-drain on hardware fault (automated remediation, dry-run first)
42. **Sat · architecture:** GPU cluster design for 1,000 GPUs (nodes, network rails, storage, failure domains)

### Stage 4: AI platform architecture & SRE
43. GPU capacity planner: demand forecast → nodes to buy, with headroom and N+1
44. Scheduler simulator: bin-pack vs spread on a job trace (utilisation, wait time)
45. Fair-share / DRF calculator for team queues (KAI/Kueue-style quotas)
46. GPU chargeback / showback report per team and project
47. Topology-aware placement checker: same NVLink domain / leaf switch for a gang
48. Gang scheduling admission: all-or-nothing reservation with a timeout
49. Autoscaling decision engine for inference (queue depth / latency → replicas → GPUs)
50. Checkpoint & restart policy: lost GPU-hours vs checkpoint interval (failure-rate model)
51. Error budget & SLO burn-rate alerting for inference endpoints
52. Closed-loop remediation: detect → diagnose → act → verify, with guardrails (AROHAN direction)
53. Multi-cluster placement: where should this training job run (capacity, cost, data locality)?
54. Reconciler pattern: desired-state controller for GPU node labels/taints (Go operator style)
55. **Sat · architecture:** an AI platform reference architecture write-up (the portfolio centrepiece)

---

## The quality bar (non-negotiable)

- **One contract** (input format → output report + exit codes), **golden files**, and all four implementations byte-for-byte identical. CI (`make test`) is green before merge.
- Production habits in every language: strict mode and traps (Bash); idempotency, `serial` and check mode (Ansible); argparse, logging, typed functions and pytest (Python); stdlib-only, `go vet` and table tests (Go).
- README sections: Scenario · Contract · Tool comparison · Run it · **Run it for real** · **Break it** · **Architect's lens** (how it scales) · Interview talking points · Production next steps.
- Public-repo hygiene: `example.com` and `node-NN` only. No real hosts, IPs, customers or tokens.

## How *I* use each day (authenticity rule)

The reference solutions are AI-assisted, and the repo says so openly. **Proof of skill is the `mine/` folder.** Each day I rewrite the **focus-language** version myself, without looking, until it passes the same `make test` contract. Then I commit it as `mine/<lang>/` with a 5-line note: what I got wrong and what I learned.

Interviewers will ask me to explain or extend this code live. My rewrites, my GOTCHAS and my answers aloud are what make that safe.
