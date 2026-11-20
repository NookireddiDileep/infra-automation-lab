# Day 006: TLS certificate expiry audit (files *and* what the service really serves)

> **One scenario, four tools.** The same job is built end to end in **Bash → Ansible → Python → Go**, and all four must produce a byte-for-byte identical report, which a shared "golden" test checks.
> Track: Linux / RHEL fleet ops · Stage 1 · Focus language: **Bash** · Difficulty: ⭐⭐ · Time: about 2 h to read, run and break; 30–45 min for "Your turn"

---

## 1. The scenario

Saturday 03:10, pager: "registry login failing, `x509: certificate has expired`". The cert was in the renewal calendar, the renewal job *did* run, and `/etc/pki/tls/certs/registry.pem` on disk is valid until 2027. But the service never reloaded, so it is still serving the old certificate from memory. Nobody was watching the thing users actually see.

An expiry audit has to look at **two places**: the certificate **files** (trust anchors, node certs, ingress certs) and the **live endpoint** (what the TLS handshake returns). It must also notice when they disagree.

**Goal:** per host, one inventory of `source, notAfter, subject`, and a report that says what has expired, what expires soon, and which services need a **reload** because the file was renewed but the endpoint wasn't. It must give a pipeline an exit code it can gate on.

Fixtures (clock pinned to `2026-11-20T00:00:00Z`): `node-01` (CRIT: an expired legacy CA, a 9-day endpoint, a 14-day boundary case, plus a reload-needed), `node-02` (OK, including a 31-day boundary), `node-03` (WARN).

## 2. The contract (every implementation must follow it)

**Inventory v1** (tab-separated, one certificate per line; `#` lines and blank lines ignored):

```
# certscan inventory v1
# host=node-01
file:/etc/pki/tls/certs/node-01.pem<TAB>2027-02-10T10:00:00Z<TAB>CN=node-01.example.com
endpoint:node-01.example.com:443<TAB>2026-11-29T12:00:00Z<TAB>CN=node-01.example.com
```

`notAfter` is strict ISO-8601 UTC (`YYYY-MM-DDTHH:MM:SSZ`). `bash/certscan.sh collect` produces this from real PEM files and live endpoints with `openssl`.

**Rules** (`days = floor((notAfter - now) / 86400)`, floor also for negatives; defaults `--warn-days 30`, `--crit-days 14`, `crit < warn` is enforced):

| Finding | Severity |
|---|---|
| `days < 0` → `expired Nd ago` | **CRIT** |
| `0 <= days <= crit` → `expires in Nd` | **CRIT** |
| `crit < days <= warn` → `expires in Nd` | WARN |
| an `endpoint:` serves a certificate that ends **before** the `file:` with the same subject → `reload-needed` | WARN |
| otherwise | counted as `ok` |

**Report:** header, findings in byte order (`LC_ALL=C`, so `CRIT` sorts before `WARN`), then `RESULT <OK|WARN|CRIT> crit=N warn=N ok=N`. **Exit codes:** `0` OK · `1` WARN · `2` CRIT · `3` usage/input error (bad timestamp, missing `# host=`, wrong field count, `crit >= warn`). Golden files: [`samples/expected/`](samples/expected/); bad inputs: [`samples/bad/`](samples/bad/).

```
certscan report: node-01
CRIT endpoint:node-01.example.com:443 expires in 9d (2026-11-29) CN=node-01.example.com
CRIT file:/etc/pki/ca-trust/source/anchors/legacy-ca.pem expired 10d ago (2026-11-10) CN=Legacy Internal CA,O=Example Corp
CRIT file:/etc/pki/tls/certs/registry.pem expires in 14d (2026-12-04) CN=registry.example.com
WARN file:/etc/pki/tls/certs/ingress.pem expires in 30d (2026-12-20) CN=*.apps.example.com
WARN reload-needed CN=node-01.example.com endpoint serves 2026-11-29 but file has 2027-02-10
RESULT CRIT crit=3 warn=2 ok=2
```

## 3. Four implementations: what each tool is *for*

| | Bash `bash/certscan.sh` | Ansible `ansible/` | Python `python/certscan.py` | Go `go/` |
|---|---|---|---|---|
| Best at | one host, plus the **collector** (`openssl x509`, `s_client`) | **fleet**: gate a pipeline on `fail_on_crit`, write reports idempotently | tested logic, **`--json`** for tickets/dashboards | one static binary, **concurrent** `fleet DIR -j N` |
| Time maths | `date -u -d` to epoch, integer arithmetic | `to_datetime` and `.total_seconds()` | `datetime`, `//` (floors) | `time.Parse`, hand-written `floorDiv` |
| Sorting | `LC_ALL=C sort` | `sort(case_sensitive=true)` | `sorted()` | `sort.Strings` |
| Extra | strict mode, ERR trap, live smoke test | template returns JSON, `assert` as the CI gate | 15 pytest cases, boundary table | goroutine worker pool, `-race` clean |
| Test | `bash/test.sh` | `compare_samples.yml` | `pytest` | `go test` |

Run everything with `make test`. CI runs it too ([`.github/workflows/daily-labs.yml`](../../.github/workflows/daily-labs.yml)).

## 4. Run it

```bash
cd daily-labs/day-006-tls-cert-expiry-audit
NOW=2026-11-20T00:00:00Z

# Bash
bash/certscan.sh check --now $NOW samples/node-01.certs; echo "exit=$?"

# Python
python3 python/certscan.py check --now $NOW --json samples/node-03.certs

# Go
cd go && go run . fleet --now $NOW -j 4 ../samples; cd ..
#   HOST     RESULT  CRIT  WARN  OK
#   node-01  CRIT    3     2     2
#   node-02  OK      0     0     3
#   node-03  WARN    0     1     2

# Ansible (pip install ansible-core)
cd ansible
ansible-playbook compare_samples.yml                                   # contract test
ansible-playbook certscan.yml -e cs_now=$NOW -e fail_on_crit=false     # reports/<host>.report
```

## 5. Run it for real

On an OpenShift training lab node, a GCP RHEL VM, or a kind node:

```bash
bash/certscan.sh collect --host "$(hostname -s)" \
  --file /etc/pki/tls/certs/ca-bundle.crt \
  --file /var/lib/kubelet/pki/kubelet-client-current.pem \
  --endpoint api.example.com:6443 > /tmp/$(hostname -s).certs
bash/certscan.sh check /tmp/$(hostname -s).certs; echo "exit=$?"
```

Notes: `ca-bundle.crt` holds many certificates and `openssl x509` reads only the first, so audit individual anchors. On OpenShift, kubelet client/serving certs rotate on their own, so a CRIT there means rotation is **stuck** (look for pending CSRs: `oc get csr`). For the ingress certificate, point `--endpoint` at a route host.

## 6. Break it (do these yourself, then record what you saw)

1. **Truncation:** in `to_epoch` (Bash) or `floorDiv` (Go) use plain integer division on negatives. Which golden case breaks, and why does Python's `//` not have the bug? (`-9d16h` must be "expired 10d ago", not 9.)
2. **Boundary:** change `d <= crit` to `d < crit`. `registry.pem` (exactly 14 days) silently drops from CRIT to WARN. Which tests catch it?
3. **Wrong sort:** run `sort` without `LC_ALL=C` on a machine with `en_US.UTF-8`. Do the lines stay in the same order? Why does Ansible's `sort` need `case_sensitive=true`?
4. **Stale endpoint:** edit `node-02.certs` so the endpoint date is earlier than the file date. Expect a `reload-needed` WARN. Now make the endpoint date *later*. Why is that deliberately not a finding?
5. **ERR trap:** replace the global `RC` with `return "$rc"` in `cmd_check`. What does the ERR trap print, and why does the exit code still look right?
6. **Ansible newlines:** write `'\n'` instead of the `nl` variable in `tasks/check.yml`. How does the report differ from the golden file?

## 7. Architect's lens

- **Check what clients see, not what the disk holds.** The file audit catches forgotten renewals; the endpoint audit catches forgotten *reloads*. The pair (`reload-needed`) is the incident class that actually pages people.
- **Inventory is the hard part.** Finding every certificate (files, secrets, Java keystores, load balancers, appliances) is harder than parsing dates. Scanning ports on a CIDR finds the surprises; configuration management finds the intended ones. Reconcile both.
- **Thresholds are a policy, not a constant.** 30/14 days suits annual certificates. With 90-day ACME or 24-hour SPIFFE certificates, alert on "renewal failed N times" instead of days left.
- **Scale.** Collection is I/O bound and per-host, so it shards trivially (Ansible `forks`, Go workers). Keep the *evaluation* a pure function of the inventory, so it is unit-testable and can run centrally.
- **Trade-off:** pull (central scanner dials every endpoint) sees the real handshake but needs network reach and SNI knowledge; push (agents report files) is simple but blind to what is served. Do both for the critical few.
- **Clock matters.** Everything is relative to `now`; a skewed host mis-reports by days. Pin `--now` in tests, and treat time sync as a prerequisite (the next Stage 1 lab).

## 8. Interview talking points

- **"How do you stop certificate-expiry outages?"** Automate issuance and renewal (ACME, cert-manager, or the OpenShift operators), **and** monitor expiry independently, because renewal fails silently. Alert at 30 days (ticket) and 14 days (page), on the served certificate, not only the file.
- **"The cert on disk is fine but clients see an expired one."** The process never reloaded. Compare `openssl s_client -connect host:443 -servername host | openssl x509 -noout -enddate` with the file; then `systemctl reload` (or restart the pod), and fix the renewal hook so it reloads.
- **"Why does `openssl s_client` need `-servername`?"** SNI. Without it, a virtual-hosting server or ingress returns its default certificate and you audit the wrong one.
- **"What expires inside an OpenShift cluster?"** Kubelet client and serving certs (rotated via CSRs), API server and etcd serving certs, ingress default cert, service-CA signed secrets, and any custom CA you trusted. Stuck CSRs are the usual cause of a node going `NotReady` after a long outage.
- **"Why floor the day count?"** So 1 second late reads "expired 1d ago", never "0d". Truncation toward zero would call an expired certificate "expires in 0d", a different severity story. Same arithmetic, four languages, one golden file.

## 9. What I'd add for production

- Collect SANs, issuer, key size and signature algorithm too (SHA-1/RSA-1024 findings), and check chain completeness and hostname match.
- Export `cert_expiry_seconds{host,source,subject}` as metrics (the Prometheus blackbox exporter does this for endpoints) and alert on the metric; keep this tool as the offline/audit view.
- An allow-list with owner and expiry date for known-bad certificates, so the report stays actionable.
- Scan Kubernetes `Secret`s of type `kubernetes.io/tls` and `Route` certificates (Stage 2 follow-up: certificate expiry inside the cluster).
- A `--fix` mode that only *prints* the reload command per `reload-needed` finding.

## 10. Your turn (focus language: Bash, 30–45 min)

Rewrite **`certscan.sh`** (just the `check` subcommand) yourself in `mine/bash/certscan.sh`, without looking, until this passes:

```bash
mkdir -p mine/bash && cp bash/test.sh mine/bash/
LIVE=0 CERTSCAN=mine/bash/certscan.sh mine/bash/test.sh   # LIVE=0 skips the `collect` smoke test
```

(implement `collect` last, then drop `LIVE=0`). Start with `node-02` (exit 0), then the CRIT boundaries, then `reload-needed`, and handle `floor` for negative numbers last.

### Friday interview round: solve it in 30 minutes

> *"We have 400 RHEL hosts and a wiki page listing certificate expiry dates that nobody updates. Design and sketch how you'd replace it so we never have an expiry outage again. You have 30 minutes; write the core check in the language of your choice and tell me what you'd do about certificates that aren't files."*

Aim for: inventory source, collector, a pure evaluator with thresholds, the file-vs-endpoint comparison, alert routing (ticket at 30d, page at 14d), and the renewal-hook reload.
