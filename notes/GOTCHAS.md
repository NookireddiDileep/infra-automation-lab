# Gotchas  #gotcha

One line per trap: **symptom → cause → fix** (week/day). Reread every Saturday.

| Symptom | Cause | Fix | From |
|---|---|---|---|
| Counter is 0 after `cat f \| while read` | Pipe runs the loop in a subshell | `done < f` | W1 Wed |
| Last line of file skipped | No trailing newline | `while read -r l \|\| [[ -n "$l" ]]` | W1 Wed |
| Script exits right after `((x++))` under `set -e` | `((0))` returns exit status 1 | `x=$((x+1))` | W1 Wed |
| `comm` says "file not in sorted order" / wrong diff | Inputs sorted with different locales | `export LC_ALL=C` before `sort` and `comm` | Daily 001 |
| ERR trap fires after a function `return 2` | Non-zero return trips `set -e`/ERR in the caller | Return status through a global (or call it as `f \|\| rc=$?`) | Daily 001 |
| Go output order changes between runs | Map iteration is random | `sort.Strings` before printing | Daily 001 |
| Ansible `join('\n')` gives one line with a literal `\n` | Ansible's templating keeps backslashes in Jinja string literals as-is | Define `nl: "\n"` in YAML double quotes and use `join(nl)` | Daily 001 |
| Drift check flags a service as "missing" though nothing broke | On-demand (socket/D-Bus activated) units exit when idle | Allow-list transient units, or compare only `enabled` services | Daily 001 |
| `rm` a 4 GB log, `df` doesn't move | A process still holds the deleted inode open; `du` can't see it, `df` still counts it | `find /proc/*/fd -lname '*(deleted)'` / `lsof +L1`, then restart/reload or `truncate -s 0 /proc/PID/fd/N` | Daily 002 |
| `/database` reported as inside `/data` | Plain string-prefix match on paths | Match `p == m \|\| p` starts with `m + "/"` (whole components), longest wins | Daily 002 |
| `cmd \| grep -q x` is false although `x` is printed | `pipefail`: `cmd` exits non-zero (or gets SIGPIPE when `grep -q` quits early) | `out=$(cmd) \|\| true; grep -q x <<<"$out"` | Daily 002 |
| Ansible/Jinja `sort` order differs from `LC_ALL=C sort` | Jinja's `sort` filter is case-insensitive by default | `sort(attribute='p', case_sensitive=true)` | Daily 002 |
| `oc get pods` shows CrashLoopBackOff for an OOM-killed container | STATUS is the kubelet back-off state, not the cause | Read `status.containerStatuses[].lastState.terminated.reason` / `exitCode` (137 = SIGKILL, often OOM) | Daily 003 |
| Pod report flags Job pods as "restarting" | `Succeeded` pods keep their old restartCount | Skip phases other than `Running`/`Pending` (and handle `Failed`+`Evicted` separately) | Daily 003 |
| Same `sort` key gives different order in Bash vs Python/Go | `ns/pod` compared with a separator that sorts above `-`/letters (e.g. `\|`) | Use TAB as the field separator (sorts below every printable char) and `LC_ALL=C` | Daily 003 |
| `from_json` fails in Ansible when nothing matched | `lookup('template')` returns `None` for an empty render | `(lookup(...) \| default('', true)) \| split(nl)` | Daily 003 |
| Ansible `lookup('file')` compare with a golden file never matches | The file lookup strips the trailing newline | Re-append it (`~ nl`) before comparing byte for byte | Daily 003 |
| Pending pod: "Insufficient cpu" but the nodes are idle | The scheduler places by **requests**, not live usage | Compare `requests` with node `Allocated resources` (`oc describe node`); right-size requests | Daily 004 |
| Adding nodes doesn't unstick a Pending pod | Reason is `Selector`, `Taint` or `Storage`, which no scale-up fixes (Cluster Autoscaler only acts on capacity reasons) | Read the dominant reason in `PodScheduled`; fix spec, labels, tolerations or the PVC first | Daily 004 |
| Summing the per-reason node counts exceeds the node count | `Insufficient cpu` and `Insufficient memory` can both count the same node | Use the max across Insufficient clauses; sum only the one-reason-per-node classes | Daily 004 |
| Scheduler message parser breaks on a PVC problem | Some clauses have no leading count (`pod has unbound immediate PersistentVolumeClaims`) and the `preemption: ...` tail follows `". "` | Treat a count-less clause as "all N nodes" and cut at the first `". "` | Daily 004 |
| Ansible `regex_search` with groups returns `None`, not `''` | No match gives `None`; with groups a match gives a list | Test `is not none` before indexing `m[0]`; put the two-group regex in the template, not in a `when` | Daily 004 |
| jq runs a later check twice (duplicate findings) | `(A) as [$x] \| B, C, D`: the destructuring binding scopes over **everything to its right**, so `C` and `D` run once per tuple | Wrap the bound part in its own parentheses: `((A) as [$x] \| B), C, D` | Daily 005 |
| Ansible `selectattr('metadata.namespace', ...)` fails with "has no attribute 'namespace'" | Cluster-scoped objects (Namespace) have no `metadata.namespace`, and dotted attribute chains are strict | Index once in a loop with `md['namespace'] \| default('')`, then `selectattr` on the flat key | Daily 005 |
| Ansible `sort` over `rank\tns\ttext` lines scrambles the emission order inside a namespace | Sorting whole strings also sorts by the trailing text | Keep dicts and sort stably in two passes: `sort(attribute='ns') \| sort(attribute='rank')` | Daily 005 |
| Included tasks see the wrong `item` | An outer `loop:` variable named `item` is shadowed by the inner loop in the included file, and lazy `vars` re-evaluate there | `loop_control: {loop_var: case}` on the outer loop | Daily 005 |
| NetworkPolicy exists but traffic still flows | The CNI doesn't enforce it (kind's default), the policy has `ingress: [{}]` (allow all), or `podSelector` doesn't match the pods | Check the CNI, `oc get netpol -o yaml`, and treat `podSelector: {}` + no ingress rules + `Ingress` in `policyTypes` as the only real deny-all | Daily 005 |
| Expired cert reported as "expires in 0d" | Integer division truncates toward zero, so `-9d16h` becomes `-9` or `0` instead of `-10` | Floor explicitly: Python `//` already floors; in Bash and Go write `floor(-s/86400)` by hand and test a 1-second-late case | Daily 006 |
| `openssl x509 -enddate` on a bundle audits only the first certificate | `x509` reads one PEM block; `ca-bundle.crt` has hundreds | Split first (`csplit`/`awk` on `BEGIN CERTIFICATE`) or `openssl crl2pkcs7 \| openssl pkcs7 -print_certs` | Daily 006 |
| `s_client` returns the wrong (default) certificate | No SNI sent, so the server or ingress answers with its default vhost | Always pass `-servername HOST`; use `</dev/null` and `timeout` so it can't hang | Daily 006 |
| Bash script prints an ERR-trap line but exits with the right code | `return 2` from a function is a non-zero status and fires `ERR` under `set -E` | Keep the status in a global (`RC`), `return 0`, and `exit "$RC"` at the end | Daily 006 |
| Ansible warns "Found variable using reserved name 'now'" | `now` is a reserved variable name in newer ansible-core | Use a prefixed name such as `cs_now` | Daily 006 |
| Ansible task name with `: ` fails YAML parsing | An unquoted scalar can't contain a colon followed by a space (`name: Write (idempotent: ...)`) | Reword or quote the whole name | Daily 006 |
| Deploy rejected with `exceeded quota` while nodes are idle | ResourceQuota and the scheduler both count **requests**, not live usage | `oc describe quota` (used vs hard), then `oc adm top pods` vs requests; lower padded requests or raise the quota, never add nodes | Daily 007 |
| Right-sizing tool says "usage 0" for a busy namespace | No PodMetrics returned (metrics-server down or not installed) and the code treated missing as zero | Treat "no data" as its own finding (`NOMETRICS`), never as 0; a tool that does the opposite will recommend cutting the quota to nothing | Daily 007 |
| `oc get --raw /apis/metrics.k8s.io/v1beta1/pods` fails a `kind == List` check | The raw API returns `kind: PodMetricsList`, not `List` | Normalise first (`jq '.kind = "List"'`) or accept both kinds on purpose | Daily 007 |
| Summing per-container CPU in millicores loses usage | metrics-server reports nanocores (`3200000n`); flooring each container to `m` first drops up to 1m per container | Sum in nanocores, floor to millicores once at the end (and say so in the contract) | Daily 007 |
