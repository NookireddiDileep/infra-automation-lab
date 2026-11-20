# Day 006: run all four implementations against the same contract
.PHONY: test bash python go ansible
test: bash python go ansible

bash:
	bash/test.sh

python:
	cd python && python3 -m pytest -q

go:
	cd go && go vet ./... && go test ./...

# Ansible: syntax, byte-identical golden compare, then a real run twice into a temp dir (2nd run must be changed=0)
ansible:
	@if ! command -v ansible-playbook >/dev/null; then echo "SKIP ansible: ansible-core not installed"; exit 0; fi; \
	cd ansible && ansible-playbook --syntax-check certscan.yml && \
	ansible-playbook compare_samples.yml && \
	out=$$(mktemp -d) && trap 'rm -rf "$$out"' EXIT && \
	ansible-playbook certscan.yml -e out_dir="$$out" -e cs_now=2026-11-20T00:00:00Z -e fail_on_crit=false >/dev/null && \
	ansible-playbook certscan.yml -e out_dir="$$out" -e cs_now=2026-11-20T00:00:00Z -e fail_on_crit=false | grep -q 'changed=0' && \
	cmp "$$out/node-01.report" ../samples/expected/node-01.report && \
	! ansible-playbook certscan.yml -e out_dir="$$out" -e cs_now=2026-11-20T00:00:00Z >/dev/null 2>&1 && \
	echo "ansible: idempotent, byte-identical, and fails the run on CRIT"
