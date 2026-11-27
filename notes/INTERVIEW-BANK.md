# Interview Bank  #interview

Short answers you can say in 30 seconds. Every Saturday, cover the answers, say them aloud, and tag the weak ones `#revise`.

## Bash: loops and input
- **Why use `while IFS= read -r line` rather than `for line in $(cat file)`?**  
  The `for` version splits on any whitespace and expands globs. `while read` handles one line at a time and keeps it intact.
- **Why are variables lost after `cmd | while read`?**  
  The pipeline runs the loop in a subshell. Use `done < file` or process substitution `done < <(cmd)`.
- **How do you run checks on many hosts in parallel?**  
  Background jobs with `&` plus `wait`, or `xargs -P N`. Cap the concurrency and log results per host.

## Bash: production habits
<!-- fill from Week 3 -->

## Text processing (grep / awk / sed / jq)
<!-- fill from Week 2 -->

## Scenario stories (STAR) — link scripts to real work
<!-- e.g. "During DR drills at work I checked reachability of N hosts by hand; I'd now script it like ping_check.sh, which cuts the check from X min to Y sec." -->

## Daily labs
- **How do you verify a patch window beyond "it pings"?** (Daily 001)  
  Snapshot mounts, listening ports, running services and kernel before and after, then diff them. Anything missing is CRIT and fails the change; new items and full disks are WARN. In Ansible, use `serial` + `max_fail_percentage: 0` so a bad batch stops the rollout.
- **Bash vs Ansible vs Python vs Go for the same job?** (Daily 001)  
  Bash for one host with no dependencies. Ansible for fleet collection and gating. Python for tested logic and JSON. Go for a static binary and concurrent fleet processing.
- **`df` says the filesystem is full but `du` adds up to much less. Why, and how do you fix it without a reboot?** (Daily 002)  
  Deleted-but-open files: the inode stays allocated until the last file descriptor closes, and `du` can't see a file with no name. Find them with `lsof +L1` or `find /proc/*/fd -lname '*(deleted)'`. Restart or reload the owning process, or `truncate -s 0 /proc/PID/fd/N`. Count shared inodes once. Other causes: data hidden under a mount point, ext4 reserved blocks, LVM/btrfs snapshots.
- **A pod shows CrashLoopBackOff. How do you tell an app bug from an OOM kill?** (Daily 003)  
  CrashLoopBackOff is only the kubelet's back-off state. `oc describe pod` / `lastState.terminated` gives the reason and exit code: `OOMKilled` (137) means the container hit `limits.memory`, so compare with `oc adm top pod` and raise the limit or fix the leak; exit 1 is an app error, so read `oc logs -p`. Check probes too: a failing liveness probe also kills with 137.
- **ImagePullBackOff: what do you check, in order?** (Daily 003)  
  `oc describe pod` events: tag/digest typo, missing or wrong `imagePullSecret` in the namespace/ServiceAccount, registry unreachable or rate-limited, and on disconnected OpenShift the mirror configuration (ImageContentSourcePolicy/IDMS) not covering that repository.
- **Why would your triage tool treat "OOMKilled but Running" as WARN, not CRIT?** (Daily 003)  
  The container recovered, so users aren't down now, but the limit is wrong and it will bite again under load. Severity follows *current* availability; the restart history is the early warning.
- **Walk me through a disk-full incident.** (Daily 002)  
  `df -h` and `df -i` (space vs inodes) → `du -xh -d 1 | sort -h` on the hot fs → deleted-open files → old rotated logs and caches → work out whether safe cleanup brings it under the threshold or it needs capacity → fix the root cause (logrotate with a `postrotate` signal, retention, quotas, a separate fs) and alert on time-to-full, not just %.
- **At 1,000 nodes, would you auto-delete files when disks fill?** (Daily 002)  
  Not broadly. Detect with metrics (pull), triage read-only on the hot nodes (push), then allow-listed actions only (truncate known logs of known units, image prune), dry-run first and verify with a second snapshot. Deleting data automatically across a fleet is how you lose a dataset.
- **A pod has been Pending for an hour. How do you find out why?** (Daily 004)  
  `oc describe pod` and read the `PodScheduled` condition: it lists, per reason, how many nodes were rejected (`0/12 nodes are available: 6 Insufficient nvidia.com/gpu, 3 had untolerated taint, ...`). Insufficient X means requests don't fit (check allocatable minus allocated, not usage); taint or selector means fix the spec or labels; unbound PVC means look at the StorageClass and binding mode. Only capacity reasons are fixed by adding nodes.
- **Why is "volume node affinity conflict" different from a nodeSelector problem?** (Daily 004)  
  The pod's own spec is fine; the PV is pinned to a zone/node set the pod can't reach. It usually comes from `Immediate` binding picking a zone before scheduling. Use `WaitForFirstConsumer` so the volume is provisioned where the pod lands.
- **Will the Cluster Autoscaler help a Pending pod?** (Daily 004)  
  Only if the pod is unschedulable for capacity reasons and a node group could fit it. It won't help with a bad `nodeSelector`, a missing toleration, an unbindable PVC or a scheduling gate, so classify first and page on the human-fix classes.
- **How do you onboard a new team onto a shared OpenShift cluster, and how do you know it stayed that way?** (Daily 005)  
  A project template or GitOps bundle creates the namespace with a ResourceQuota, LimitRange, RoleBindings to IdP groups and a default-deny NetworkPolicy plus explicit allows. Then an independent read-only check compares live objects with the spec: MISSING, DRIFT and EXTRA (a stray `cluster-admin`). Unbounded or privilege-widening drift is CRIT, cosmetic drift is WARN. Admission policy (Kyverno/Gatekeeper) can then block the worst edits up front.
- **What does a ResourceQuota do to a pod that sets no limits, and what fixes it?** (Daily 005)  
  When the quota tracks `requests.*` or `limits.*`, the API rejects pods that don't set those values. A LimitRange with default requests/limits fills them in, so the two objects always go together.
- **A default-deny NetworkPolicy is applied but pods still receive traffic. What do you check?** (Daily 005)  
  Whether the CNI enforces NetworkPolicy at all (kind's default does not), whether `podSelector` matches the pods, and whether someone added an ingress rule: `ingress: [{}]` means allow everything. Policies are additive allow-lists, so one permissive policy overrides the deny-all.
- **The certificate file on disk is valid but users see an expired certificate. Why?** (Daily 006)  
  The service is still serving the old certificate from memory: the renewal replaced the file but nothing reloaded the process. Compare `openssl x509 -enddate` on the file with `openssl s_client -connect host:443 -servername host </dev/null | openssl x509 -noout -enddate`, reload, and make the renewal hook reload the service. Audit both file and endpoint.
- **How do you build an expiry check that doesn't produce false alarms or silent gaps?** (Daily 006)  
  Separate collection from evaluation: a collector emits `source, notAfter, subject`, and a pure function applies thresholds (warn 30d, crit 14d) with floored day counts. A failed handshake is an error, not a skipped row, and the clock is injectable so tests are deterministic.
- **Why pass `-servername` to `openssl s_client`?** (Daily 006)  
  SNI. Without it a shared host or ingress returns its default certificate, so you audit the wrong one.
- **Pods are rejected with `exceeded quota` but the nodes are idle. Why, and how do you fix it?** (Daily 007)  
  Quota and the scheduler both count **requests**, not usage. Compare `oc describe quota` (used vs hard) with `oc adm top pods`: teams usually pad requests. Lower the requests (VPA recommendation, p95 over a week) or raise `hard` deliberately; adding nodes does not unblock a full quota.
- **Why is a pod using more memory than it requested worse than the same for CPU?** (Daily 007)  
  CPU is compressible: the pod is throttled. Memory is not: under node pressure a Burstable pod over its request is evicted first, and at its limit it is OOMKilled. So memory under-requesting is CRIT in my report and CPU is WARN.
- **How do you right-size requests without causing an outage?** (Daily 007)  
  Measure p95 over at least a week including peaks, set requests near p90, keep limit headroom, roll out gradually and watch CPU throttling and OOMs. Start with VPA in recommendation mode; never auto-lower stateful workloads.
- **Pods got evicted overnight but the node is fine now. How do you investigate?** (Daily 008)  
  Conditions are a snapshot, so go to the history: `oc get events -A --field-selector reason=Evicted` (they expire in about an hour), the evicted pod's `status.message` ("The node was low on resource: memory"), `oc describe node` for conditions and allocated resources, kubelet logs, and Prometheus for memory/disk series around that time. Name the resource, then the namespaces whose usage was above their requests.
- **Evicted vs OOMKilled?** (Daily 008)  
  OOMKilled is the kernel killing a container that hit its cgroup limit; the pod restarts in place. Evicted is the kubelet removing the whole pod to relieve node pressure (memory, disk, PIDs); the pod ends up Failed and a controller recreates it elsewhere. BestEffort and over-request Burstable pods go first.
- **A node has `MemoryPressure=True` but still gets new pods. Why?** (Daily 008)  
  The `node.kubernetes.io/memory-pressure:NoSchedule` taint is missing, or the pod tolerates it (DaemonSets do). Check `spec.taints` and tolerations, and make "pressure without taint" an explicit finding.
- **A PVC is Pending. How do you tell a bug from normal behaviour?** (Daily 009)  
  `oc describe pvc` events decide: "waiting for first consumer" is a WaitForFirstConsumer class with no pod yet (normal); `ProvisioningFailed` is the provisioner or backend; "storageclass not found" is a typo. Then check the `selected-node` annotation (scheduler chose a node, provisioning failed there: topology), the CSI controller pods, and backend quota.
- **`Immediate` vs `WaitForFirstConsumer`, and why does it matter?** (Daily 009)  
  Immediate binds as soon as the claim exists, ignoring where the pod will run; for zonal or local storage the volume can end up where the pod cannot be scheduled. WFFC waits for the scheduler's node choice, so the volume honours zone, affinity and taints. Platform baseline: WFFC for anything topology-bound.
- **What is the difference between an absent `storageClassName` and an empty string?** (Daily 009)  
  Absent: admission fills in the default class (if any). `""`: no class on purpose, so only a pre-created PV without a class can satisfy it and nothing is provisioned. Confusing the two is a classic reason a claim hangs forever.
- **What do you check before an OpenShift upgrade?** (Daily 010)  
  ClusterVersion (channel, target, Upgradeable, Failing), every ClusterOperator Available and neither Degraded nor Progressing, every MachineConfigPool Updated with no degraded machines and no forgotten pause, nodes Ready, PDBs that would block drains, a fresh etcd backup. Snapshot the state so you can diff after.
- **What does `Upgradeable=False` on a ClusterOperator mean?** (Daily 010)  
  It blocks minor-version updates (for example 4.15 to 4.16), not z-stream ones. Usually an admin acknowledgement is required (removed APIs, credentials mode). Read the condition message, fix or acknowledge, and re-check.
- **A MachineConfigPool shows updated 3/4 and nothing moves. What now?** (Daily 010)  
  Not updating, not paused, not degraded means stuck. Find the node whose desiredConfig differs from currentConfig, read the machine-config-daemon log there and the controller log, and look for a drain blocked by a PDB.
- **Services fail intermittently but nothing is down. How do you rule out clock skew?** (Daily 011)  
  `chronyc tracking` (Leap status, System time, Stratum), `chronyc sources -v` (selected source, reach) and `timedatectl`. Compare hosts with each other, not just with your wall clock. Skew breaks etcd leases, Kerberos (5 min default), token and certificate validity, and distributed-training barriers, so alert on the offset per workload sensitivity.
- **Why do you want at least three or four NTP sources per host?** (Daily 011)  
  To out-vote a falseticker. With two sources chrony sees a disagreement but cannot tell which one is wrong; three give a majority, four still have one after a failure. Point the fleet at 3-4 internal servers that are themselves synced upstream, not the public pool from every node.
- **A host reports `Leap status: Not synchronised`. What do you do?** (Daily 011)  
  Reachability first: UDP 123 through the firewall, DNS for the server names, the Reach column in `chronyc sources` (0 means nothing answered), `journalctl -u chronyd`. Fix the path, and only if the offset is large use `chronyc makestep`, with the running workload in mind (stepping the clock under a database or etcd member is its own risk).
