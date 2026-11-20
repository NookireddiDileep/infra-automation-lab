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
